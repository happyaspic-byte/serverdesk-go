package webfront

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// stateFile 은 공유 상태 JSON 파일 하나의 읽기/갱신을 직렬화한다.
//
// 배경: 폴러는 읽기 전용 원천이라 경보 해제 API 가 없다. 그래서 확인(ack)·점검(maint)·
// 메모(notes)·에스컬레이션 클레임(escal) 같은 콘솔 상태를 이 정적 서버가 파일로 들고
// 있는다(사이트 범위 안). 장비 설정이 아니라 콘솔 상태라 AllowWrites 와는 분리돼 있다.
type stateFile struct {
	path     string
	maxBytes int64 // PUT 바디 상한
	maxKeys  int   // 저장 키 상한
	mu       sync.Mutex
}

// flock 호출 자체는 flock_unix.go / flock_windows.go 에 있다(Windows 는 mutex 만).
// lock 은 프로세스 내 mutex 와 프로세스 간 flock 을 함께 잡는다(cron 판 _state_file_lock
// 하드닝). mutex 만으로는 같은 상태 파일을 여는 두 프로세스가 동시에 읽기-수정-쓰기를
// 할 때 마지막 rename 이 앞선 운영자의 변경을 지운다. 별도 .lock inode 에 flock 을 걸어
// 프로세스 경계를 넘겨 보호하고, 기존 원자적 rename 과 함께 부분 파일도 방지한다.
func (sf *stateFile) lock() func() {
	sf.mu.Lock()
	fd, err := os.OpenFile(sf.path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return sf.mu.Unlock // 락 파일을 못 만들어도 프로세스 내 직렬화는 유지
	}
	if !lockFile(fd) {
		fd.Close()
		return sf.mu.Unlock
	}
	return func() {
		unlockFile(fd)
		fd.Close()
		sf.mu.Unlock()
	}
}

// read treats only a genuinely absent state file as empty. Permission, I/O,
// corruption, and schema errors must remain visible so a later delta cannot
// silently replace operator acknowledgements with a partial new map.
func (sf *stateFile) read() (map[string]any, error) {
	unlock := sf.lock()
	defer unlock()
	return sf.readLocked()
}

func (sf *stateFile) readLocked() (map[string]any, error) {
	data, err := os.ReadFile(sf.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read operator state: %w", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, fmt.Errorf("decode operator state: %w", err)
	}
	if obj == nil {
		return nil, errors.New("decode operator state: top level must be an object")
	}
	return obj, nil
}

// update 는 락 안에서 읽기-병합-원자적 쓰기(tmp + rename)를 한 번에 한다.
// fn 이 돌려준 맵이 그대로 저장되며, 저장된 맵을 호출부에도 돌려준다.
func (sf *stateFile) update(fn func(cur map[string]any) map[string]any) (map[string]any, error) {
	unlock := sf.lock()
	defer unlock()
	cur, err := sf.readLocked()
	if err != nil {
		return nil, err
	}
	merged := fn(cur)
	if merged == nil {
		merged = map[string]any{}
	}
	dir := filepath.Dir(sf.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(sf.path)+"-*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	// ACK, maintenance and hand-off notes are authenticated operator data.
	// Keep them private from creation time instead of relying on a later chmod.
	if err := tmp.Chmod(0o600); err != nil {
		return nil, err
	}
	if _, err := tmp.Write(marshalJSON(merged)); err != nil {
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := replaceOperatorStateFile(tmpPath, sf.path); err != nil { // 같은 디렉터리 교체 — 부분 기록 방지
		return nil, err
	}
	committed = true
	return merged, nil
}

// deltaEndpoint 는 ack/maint/notes 델타 병합 PUT 의 엔드포인트별 차이를 묶은 것이다.
type deltaEndpoint struct {
	sf       *stateFile
	name     string // 오류 메시지용 ("ack"|"maint"|"notes")
	keyLimit int    // 키 정규화 상한(ack 300, maint/notes 120)
	clean    func(d map[string]any, cap int) map[string]any
}

func (s *Server) ackEndpoint() deltaEndpoint {
	return deltaEndpoint{s.ack, "ack", 300, cleanAck}
}

func (s *Server) maintEndpoint() deltaEndpoint {
	return deltaEndpoint{s.maint, "maint", 120, cleanMaint}
}

func (s *Server) notesEndpoint() deltaEndpoint {
	return deltaEndpoint{s.notes, "notes", 120, cleanNotes}
}

// handleStateGet은 GET /ack·/maint·/notes 공통이다. 상위 로그인 미들웨어가 접근을 인증한다.
func (s *Server) handleStateGet(w http.ResponseWriter, sf *stateFile) {
	state, err := sf.read()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, state)
}

// handleDeltaPut 은 PUT /ack·/maint·/notes 공통 핸들러다 — **델타 병합**이 기본이다.
//
// 왜 전체 교체가 아닌가(실측 버그): 운영자 A 가 경보1을 확인해 서버가 {a1} 이 된 뒤,
// 그보다 먼저 화면을 연 B 가 자기 맵 {a2} 를 통째로 PUT 하면 a1 이 흔적 없이 사라졌다.
// NOC 처럼 2명 이상이 같은 콘솔을 보는 환경에서 이건 "확인했는데 경보가 되살아난다"로
// 나타난다. → 클라이언트는 '무엇을 켰고 무엇을 껐는지'만 보내고, 병합은 락 안에서 서버가 한다.
//
// 본문 형식:
//
//	{"set": {키: 값}, "del": [키...]}   델타 병합(권장)
//	{"replace": {키: 값}}               전체 교체(전체 해제 등 의도적 초기화)
//	{키: 값}                            구형 호환 — 전체 교체로 취급
func (s *Server) handleDeltaPut(w http.ResponseWriter, r *http.Request, ep deltaEndpoint) {
	if !CheckSameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin "+ep.name+" write rejected")
		return
	}
	raw, ok := readCappedBody(w, r, ep.sf.maxBytes, ep.name+" state too large")
	if !ok {
		return
	}
	var body any
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = "{}"
	}
	if err := json.Unmarshal([]byte(trimmed), &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	obj, isObj := body.(map[string]any)
	if ep.name == "ack" {
		if !isObj {
			writeJSONError(w, http.StatusBadRequest, "ack state must be an object")
			return
		}
		if len(obj) == 0 {
			// 빈 바디/빈 객체: 과거엔 구형 전체-교체 분기로 떨어져 확인 상태가 통째로
			// 지워졌다. set/del 델타나 'replace' 명시가 없으면 의도를 알 수 없으므로 거부.
			writeJSONError(w, http.StatusBadRequest, "empty ack body: send set/del delta or explicit replace")
			return
		}
	} else if !isObj || len(obj) == 0 {
		writeJSONError(w, http.StatusBadRequest, "empty "+ep.name+" body: send set/del delta or explicit replace")
		return
	}
	_, hasSet := obj["set"]
	_, hasDel := obj["del"]
	isDelta := hasSet || hasDel
	if rep, has := obj["replace"]; has {
		if _, isMap := rep.(map[string]any); !isMap {
			// {'replace': 'x'}·{'replace': null} 은 구형 전체-교체 분기로 떨어져 기존
			// 상태가 통째로 지워지는데 200 을 돌려주던 구멍. 명시 거부한다.
			writeJSONError(w, http.StatusBadRequest, ep.name+" replace must be an object")
			return
		}
	}
	cap := ep.sf.maxKeys
	merged, err := ep.sf.update(func(cur map[string]any) map[string]any {
		if rep, has := obj["replace"]; has {
			if rm, isMap := rep.(map[string]any); isMap {
				return ep.clean(rm, cap)
			}
		}
		if isDelta {
			add, _ := obj["set"].(map[string]any)
			rm, _ := obj["del"].([]any)
			for k, v := range ep.clean(add, cap) {
				cur[k] = v
			}
			for i, k := range rm {
				if i >= cap { // del 목록 자체도 상한으로 자른다(Python rm[:MAX] 해당)
					break
				}
				popStateKey(cur, pyStr(k), ep.keyLimit)
			}
			// 병합 결과에 상한 재적용 — cur(최대 cap) + add(최대 cap) 이면 cap 의 두 배까지
			// 불어나, 바디 캡 안에서 PUT 를 반복할 때마다 파일이 무한 성장한다.
			return ep.clean(cur, cap)
		}
		return ep.clean(obj, cap) // 구형 호환: 전체 교체
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, ep.name+" write failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": len(merged), "merged": isDelta})
}

// cleanAck accepts both the legacy ISO timestamp string and the structured
// {ts,by,reason} form. Unknown shapes and entries without a timestamp are
// discarded rather than persisted as attacker-controlled arbitrary objects.
// Values remain JSON data; rendering clients must continue to use textContent.
func cleanAck(d map[string]any, cap int) map[string]any {
	out := make(map[string]any, len(d))
	for k, v := range d {
		// 키도 잘라야 한다 — 값만 묶고 키를 무제한으로 두면 바디 캡 안에서 거대 키가
		// 그대로 저장돼 파일이 부푼다(실측 100KB 키 수용).
		switch typed := v.(type) {
		case string:
			if ts := truncateRunes(typed, 40); ts != "" {
				out[stateKey(k, 300)] = ts
			}
		case map[string]any:
			ts, _ := typed["ts"].(string)
			ts = truncateRunes(ts, 40)
			if ts == "" {
				continue
			}
			by, _ := typed["by"].(string)
			reason, _ := typed["reason"].(string)
			out[stateKey(k, 300)] = map[string]any{
				"ts":     ts,
				"by":     truncateRunes(by, 80),
				"reason": truncateRunes(reason, 500),
			}
		}
	}
	return keepNewest(out, func(v any) string {
		if structured, ok := v.(map[string]any); ok {
			return pyStr(structured["ts"])
		}
		return pyStr(v)
	}, cap)
}

// cleanMaint — 값은 {until, note, by, ts} 창 객체만 받는다. until 없는 항목은 창이
// 아니므로 버린다. 상한 초과 시 '가장 최근 쓰기'(ts 내림차순)를 남긴다 — 삽입순 절단은
// 포화 상태의 새 쓰기를 200 OK 와 함께 조용히 버린다.
func cleanMaint(d map[string]any, cap int) map[string]any {
	out := make(map[string]any, len(d))
	for k, v := range d {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		until := truncateRunes(pyStr(m["until"]), 40)
		if until == "" {
			continue // until 없는 항목은 창이 아니다 — 버린다
		}
		out[stateKey(k, 120)] = map[string]any{
			"until": until,
			"note":  truncateRunes(pyStr(m["note"]), 200),
			"by":    truncateRunes(pyStr(m["by"]), 40),
			"ts":    truncateRunes(pyStr(m["ts"]), 40),
		}
	}
	return keepNewest(out, func(v any) string {
		m, _ := v.(map[string]any)
		return pyStr(m["ts"])
	}, cap)
}

// cleanNotes — 값은 {text, ts, by} 메모 객체만 받는다. 빈 메모는 저장하지 않는다
// (삭제는 del 로). 상한 정책은 maint 와 같다.
func cleanNotes(d map[string]any, cap int) map[string]any {
	out := make(map[string]any, len(d))
	for k, v := range d {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		text := truncateRunes(pyStr(m["text"]), 1000)
		if strings.TrimSpace(text) == "" {
			continue // 빈 메모는 저장하지 않는다(삭제는 del 로)
		}
		out[stateKey(k, 120)] = map[string]any{
			"text": text,
			"ts":   truncateRunes(pyStr(m["ts"]), 40),
			"by":   truncateRunes(pyStr(m["by"]), 40),
		}
	}
	return keepNewest(out, func(v any) string {
		m, _ := v.(map[string]any)
		return pyStr(m["ts"])
	}, cap)
}

// kv 는 정렬용 키-값 쌍이다.
type kv struct {
	k, v string
}

// keepNewest 는 len(m) > cap 일 때 값(ISO 시각 문자열)이 가장 최근인 cap 개만 남긴다.
func keepNewest(m map[string]any, valOf func(v any) string, cap int) map[string]any {
	if len(m) <= cap {
		return m
	}
	items := make([]kv, 0, len(m))
	for k, v := range m {
		items = append(items, kv{k, valOf(v)})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].v > items[j].v })
	out := make(map[string]any, cap)
	for _, it := range items[:cap] {
		out[it.k] = m[it.k]
	}
	return out
}

// stateKey 는 cron 판 _state_key 포트: 길이 상한을 지키되, 같은 prefix 를 가진 긴 키가
// 단순 절단으로 충돌하지 않게 sha256 suffix 를 붙여 정규화한다. 해시 suffix 는 저장·삭제
// 양쪽에서 재현 가능하고, 전체 길이는 기존 상한을 지킨다.
func stateKey(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return truncateRunes(s, limit-17) + "~" + hex.EncodeToString(sum[:])[:16]
}

// popStateKey 는 cron 판 _pop_state_key 포트: 새 해시 키와 구버전 prefix-절단 키를
// 함께 제거한다(구버전 파일에서 마이그레이션된 키도 del 이 먹히게).
func popStateKey(m map[string]any, raw string, limit int) {
	delete(m, stateKey(raw, limit))
	if utf8.RuneCountInString(raw) > limit {
		delete(m, truncateRunes(raw, limit))
	}
}

// truncateRunes 는 Python 의 str[:n](문자 단위 절단)에 해당한다. 바이트로 자르면
// 한글 키/메모가 UTF-8 중간에서 끊긴다.
func truncateRunes(s string, n int) string {
	if n < 0 {
		return ""
	}
	if len(s) <= n { // 바이트 ≤ n 이면 문자도 ≤ n
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// pyStr 은 Python str(v) 에 해당하는 최소 변환이다. 정상 클라이언트는 문자열만 보내다.
// 문자열이 아닌 값은 JSON 으로 직렬화해 저장한다(임의 구조 저장 방지 취지 유지).
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return string(marshalJSON(t))
	}
}

// ── 에스컬레이션 클레임(/escal) ─────────────────────────────────────────────
// 에스컬레이션은 '누가 보내는가'가 문제다 — 콘솔을 여러 개 띄워 두면 각자 critical 을
// 보고 같은 웹훅을 쏜다. 클레임 맵을 락 안에서 add-if-absent 로 갱신하고, 이긴 키만
// 돌려줘 한 콘솔만 보내게 한다.
//
// 클레임 TTL: 서버는 "작은 공유 상태" 철학상 원래 시계 해석을 하지 않지만(만료 청소는
// 클라이언트 소임 — maint 가 그렇다), escal 은 외부 발송 트리거라 성격이 다르다:
// 만료가 없으면 누구든 critical 키를 미리 계산해 선점(claim poisoning)해 정상 콘솔의
// 에스컬레이션을 영구 무력화할 수 있다. 그래서 클레임은 서버 시각으로 스탬프하고
// TTL 이 지나면 자연 만료(재클레임 가능)시킨다. 만료는 클레임 시각 기준의 슬라이딩
// 윈도우다 — 타인의 PUT 접촉으로는 갱신되지 않아야 선점이 영구화되지 않는다.
// ExportUIState 는 콘솔 공유 상태(백업용)다. 서버 알림 설정과 webhook secret은
// config·managed credential store에서 별도로 백업하므로 이 결과에 포함되지 않는다.
func (s *Server) ExportUIStateWithError() (map[string]any, error) {
	out := make(map[string]any, 4)
	for name, sf := range map[string]*stateFile{
		"ack": s.ack, "maint": s.maint, "notes": s.notes, "escal": s.escal,
	} {
		state, err := sf.read()
		if err != nil {
			return nil, fmt.Errorf("%s state: %w", name, err)
		}
		out[name] = state
	}
	return out, nil
}

// ExportUIState is kept for callers that cannot surface an error. New
// security-sensitive paths must use ExportUIStateWithError.
func (s *Server) ExportUIState() map[string]any {
	state, _ := s.ExportUIStateWithError()
	return state
}

// ImportUIState 는 ExportUIState 의 4키만 받아 교체한다(알 수 없는 키 무시).
func (s *Server) ImportUIState(m map[string]any) error {
	targets := map[string]*stateFile{"ack": s.ack, "maint": s.maint, "notes": s.notes, "escal": s.escal}
	for k, sf := range targets {
		v, ok := m[k]
		if !ok {
			continue
		}
		obj, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("ui.%s 가 객체가 아닙니다", k)
		}
		if _, err := sf.update(func(cur map[string]any) map[string]any { return obj }); err != nil {
			return err
		}
	}
	return nil
}

const escalClaimTTL = 6 * time.Hour

// escalClaimExpired 는 서버 스탬프(UTC ISO) 클레임이 now 기준 TTL 을 넘겼는가.
// 파싱 불가 값은 만료로 본다 — 클레임은 재클레임으로 회복되는 값이라 fail-open 이 낫다
// (fail-close 로 남기면 그 키가 곧 영구 선점이 된다).
func escalClaimExpired(iso string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return true
	}
	return now.Sub(t) > escalClaimTTL
}

// handleEscalGet — GET 도 만료 클레임은 걸러서 돌려준다: 운영자가 보는 상태와 PUT 판단이
// 갈리면 안 된다. 파일은 쓰지 않고 응답만 거른다(청소는 다음 PUT 이 한다).
func (s *Server) handleEscalGet(w http.ResponseWriter) {
	cur, err := s.escal.read()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now()
	live := make(map[string]any, len(cur))
	for k, v := range cur {
		if !escalClaimExpired(pyStr(v), now) {
			live[k] = v
		}
	}
	writeJSON(w, http.StatusOK, live)
}

// handleEscalPut — 클레임 등록 {"set": {키: ...}}. **add-if-absent**: 이미 있는 키는
// 덮지 않고, 새로 들어간 키만 added 로 돌려준다. 호출자는 added 에 든 것만 웹훅을 쏜다 —
// 콘솔이 몇 개가 열리든 같은 경보의 에스컬레이션은 1회만 나간다(락 안 원자 갱신).
// 클레임 값은 클라이언트가 본낸 ISO 대신 서버 시각 UTC ISO 로 스탬프한다 — 클라이언트
// 시계 어긋남과 무관하게 TTL 을 적용하기 위해서다. 만료된 클레임은 다시 클레임할 수 있다.
func (s *Server) handleEscalPut(w http.ResponseWriter, r *http.Request) {
	if !CheckSameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin escal write rejected")
		return
	}
	raw, ok := readCappedBody(w, r, maxEscalBytes, "escal state too large")
	if !ok {
		return
	}
	var body any
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = "{}"
	}
	if err := json.Unmarshal([]byte(trimmed), &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	obj, isObj := body.(map[string]any)
	add, isMap := obj["set"].(map[string]any)
	if !isObj || !isMap {
		writeJSONError(w, http.StatusBadRequest, "escal body must be {set: {...}}")
		return
	}
	added := []string{}
	merged, err := s.escal.update(func(cur map[string]any) map[string]any {
		// 만료 클레임 청소 — TTL 지난 것은 재클레임 대상이므로 먼저 걷어낸다.
		now := time.Now()
		for k, v := range cur {
			if escalClaimExpired(pyStr(v), now) {
				delete(cur, k)
			}
		}
		// Python datetime.now(timezone.utc).isoformat() 과 같은 모양(+00:00 suffix)으로
		// 찍는다 — 구형 Python 의 fromisoformat 이 'Z' 를 못 파싱하는 혼합 배포를 배려.
		stamp := now.UTC().Format("2006-01-02T15:04:05.999999-07:00")
		for k := range add {
			key := stateKey(k, 300)
			if _, exists := cur[key]; exists {
				continue
			}
			cur[key] = stamp // 서버 시각 스탬프 — TTL 계산 기준(클라이언트 시계 불신)
			added = append(added, key)
		}
		// 상한 — 오래된 클레임부터 버린다(값이 ISO 시각이라 내림차순 앞쪽이 가장 최근).
		return keepNewest(cur, func(v any) string { return pyStr(v) }, maxEscalKeys)
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "escal write failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "count": len(merged)})
}
