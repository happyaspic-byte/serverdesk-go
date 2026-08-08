package httpapi

// 설정 백업/복구 — config.local.json(Store)과 콘솔 공유 상태(webfront stateFile)를
// 하나의 JSON 문서로 내보내기/복구한다.
//
// 원칙:
//   - 비밀 필드(필드명에 password|passwd|secret|token|community|api_key|private_key
//     포함, 대소문자 무시)는 내보내기에서 빈 문자열로 마스킹한다 — config 는 평문 파일
//     이라(secrets.go 는 마스킹 레지스트리일 뿐 암호화가 아님) 원본 export 는
//     자격증명 유출이다.
//   - 복구 시 빈 비밀 필드는 기존 파일의 값을 이어받는다(머지) — 그래야 export→import
//     왕복이 무손실이다.
//   - 장비·수집 정의의 라이브 반영은 하지 않는다(admin.go 상단의 기존 원칙 — 수집 대상
//     정의는 파일+재시작 경로만). 바뀐 섹션은 restart_required 로 알린다.
//     thresholds 만 예외로 라이브 반영(putThresholds 와 동일 경로).

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"serverdesk/internal/poller"
)

// backupSchema 는 export 문서의 스키마 식별자다.
const backupSchema = "serverdesk-config/1"

// secretKeyRe 는 마스킹 대상 필드명 패턴이다(대소문자 무시, 부분문자열).
var secretKeyRe = regexp.MustCompile(`(?i)password|passwd|secret|token|community|api_key|private_key`)

// liveOKSections 은 import 시 라이브 반영되는(재시작 불필요) 최상위 섹션이다.
var liveOKSections = map[string]bool{"thresholds": true}

// redactSecrets 는 맵/배열을 재귀 순회하며 비밀 패턴 키의 문자열 값을 "" 로 지운다.
func redactSecrets(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if secretKeyRe.MatchString(k) {
				if _, isStr := val.(string); isStr {
					out[k] = ""
					continue
				}
			}
			out[k] = redactSecrets(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactSecrets(val)
		}
		return out
	default:
		return v
	}
}

// mergeSecrets 는 신 문서(newV)의 빈 비밀 필드를 구 문서(oldV)의 같은 경로 값으로 채운다.
// 배열은 "key" 필드가 있으면 그 값으로, 없으면 인덱스로 짝맞춘다.
func mergeSecrets(newV, oldV any) {
	switch nt := newV.(type) {
	case map[string]any:
		ot, _ := oldV.(map[string]any)
		for k, val := range nt {
			if secretKeyRe.MatchString(k) {
				if sv, isStr := val.(string); isStr && sv == "" && ot != nil {
					if ov, ok := ot[k].(string); ok && ov != "" {
						nt[k] = ov
					}
				}
				continue
			}
			if ot != nil {
				mergeSecrets(val, ot[k])
			}
		}
	case []any:
		ot, _ := oldV.([]any)
		// 짝맞춤은 key, 보조로 name 필드 — 둘 다 없는 요소만 인덱스 폴리다.
		// key/name 이 있는데 구 문서에 없는 신규 항목을 인덱스로 짝맞추면
		// 남의 자격증명을 물려받는 사고가 나므로 그 경우는 머지하지 않는다.
		oldByKey := map[string]any{}
		for _, ov := range ot {
			if om, ok := ov.(map[string]any); ok {
				if key, ok := om["key"].(string); ok {
					oldByKey["k:"+key] = om
				}
				if nm, ok := om["name"].(string); ok {
					oldByKey["n:"+nm] = om
				}
			}
		}
		for i, nv := range nt {
			if nm, ok := nv.(map[string]any); ok {
				if key, ok2 := nm["key"].(string); ok2 {
					if oldByKey["k:"+key] != nil {
						mergeSecrets(nv, oldByKey["k:"+key])
					}
					continue
				}
				if nm2, ok2 := nm["name"].(string); ok2 {
					if oldByKey["n:"+nm2] != nil {
						mergeSecrets(nv, oldByKey["n:"+nm2])
					}
					continue
				}
			}
			if i < len(ot) {
				mergeSecrets(nv, ot[i])
			}
		}
	}
}

// rawToAny 는 RawMessage 문서를 map[string]any 로 푼다.
func rawToAny(doc map[string]json.RawMessage) (map[string]any, error) {
	out := make(map[string]any, len(doc))
	for k, raw := range doc {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("섹션 %s 파싱 실패: %w", k, err)
		}
		out[k] = v
	}
	return out, nil
}

// anyToRaw 는 map[string]any 를 RawMessage 문서로 굽는다.
func anyToRaw(m map[string]any) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(m))
	for k, v := range m {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("섹션 %s 직렬화 실패: %w", k, err)
		}
		out[k] = raw
	}
	return out, nil
}

// validateConfigDoc 은 import 문서의 config 섹션 구조를 검사한다.
func validateConfigDoc(cfg map[string]any) error {
	for _, sec := range []string{"clusters", "edge_devices"} {
		v, ok := cfg[sec]
		if !ok {
			continue
		}
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s 가 배열이 아닙니다", sec)
		}
		for i, e := range arr {
			m, ok := e.(map[string]any)
			if !ok {
				return fmt.Errorf("%s[%d] 가 객체가 아닙니다", sec, i)
			}
			if key, ok := m["key"].(string); !ok || key == "" {
				return fmt.Errorf("%s[%d] 에 key 가 없습니다", sec, i)
			}
		}
	}
	return nil
}

// handleConfigExport 는 GET /api/admin/config/export — 마스킹된 설정 + 콘솔 상태 백업.
// 자격증명 경로라 읽기지만 writeGate 와 같은 게이트를 적용한다.
func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if !s.writeGate(w, r) {
		return
	}
	doc, err := s.Store.ReadDoc()
	if err != nil {
		s.send(w, r, 500, map[string]any{"error": err.Error()})
		return
	}
	plain, err := rawToAny(doc)
	if err != nil {
		s.send(w, r, 500, map[string]any{"error": err.Error()})
		return
	}
	out := map[string]any{
		"schema":      backupSchema,
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"config":      redactSecrets(plain),
		"ui":          s.Gate.ExportUIState(),
	}
	w.Header().Set("Content-Disposition", `attachment; filename="serverdesk-backup-`+time.Now().Format("20060102")+`.json"`)
	s.send(w, r, 200, out)
}

// handleConfigImport 는 POST /api/admin/config/import — 검증 → 비밀 머지 → 파일 교체.
// 장비·수집 섹션은 재시작 전까지 라이브 반영되지 않는다(restart_required 로 알림).
func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	if !s.writeGate(w, r) {
		return
	}
	body, err := readCapped(r, 1024*1024)
	if err != nil || len(body) == 0 {
		s.send(w, r, 400, map[string]any{"error": "JSON 본문이 필요합니다(최대 1MB)"})
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		s.send(w, r, 400, map[string]any{"error": "JSON 파싱 실패: " + err.Error()})
		return
	}
	if doc["schema"] != backupSchema {
		s.send(w, r, 400, map[string]any{"error": "schema 가 " + backupSchema + " 이(가) 아닙니다"})
		return
	}
	newCfg, ok := doc["config"].(map[string]any)
	if !ok {
		s.send(w, r, 400, map[string]any{"error": "config 섹션이 없습니다"})
		return
	}
	if err := validateConfigDoc(newCfg); err != nil {
		s.send(w, r, 400, map[string]any{"error": err.Error()})
		return
	}
	// thresholds 가 오면 import 자체를 막기보다 값 검증만 선행한다(파일은 쓰되 라이브 반영 생략은 모순).
	if th, ok := newCfg["thresholds"].(map[string]any); ok {
		warn, wok := th["warn"].(float64)
		crit, cok := th["crit"].(float64)
		if !wok || !cok || !(warn > 0 && warn < crit && crit <= 100) {
			s.send(w, r, 400, map[string]any{"error": "thresholds: 0 < warn < crit <= 100 이어야 합니다"})
			return
		}
	}

	oldDoc, err := s.Store.ReadDoc()
	if err != nil {
		s.send(w, r, 500, map[string]any{"error": err.Error()})
		return
	}
	oldPlain, err := rawToAny(oldDoc)
	if err != nil {
		s.send(w, r, 500, map[string]any{"error": err.Error()})
		return
	}
	mergeSecrets(newCfg, oldPlain)

	// restart_required 계산 — 바뀐 최상위 섹션 중 라이브 반영 불가능한 것들.
	// 비교는 RawMessage 바이트가 아니라 값 기준이다 — export→import 왕복은
	// 직렬화 형태(키 순서)만 달라진 동일 내용을 '변경'으로 오인했다.
	changed := []string{}
	newRaw, err := anyToRaw(newCfg)
	if err != nil {
		s.send(w, r, 500, map[string]any{"error": err.Error()})
		return
	}
	keys := map[string]bool{}
	for k := range oldPlain {
		keys[k] = true
	}
	for k := range newCfg {
		keys[k] = true
	}
	for k := range keys {
		if liveOKSections[k] {
			continue
		}
		if !reflect.DeepEqual(oldPlain[k], newCfg[k]) {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)

	if err := s.Store.ReplaceDoc(newRaw); err != nil {
		s.send(w, r, 500, map[string]any{"error": "설정 저장 실패: " + err.Error()})
		return
	}
	// thresholds 는 라이브 반영(putThresholds 와 동일 경로).
	if th, ok := newCfg["thresholds"].(map[string]any); ok {
		warn, _ := th["warn"].(float64)
		crit, _ := th["crit"].(float64)
		poller.SetThresholds(warn, crit)
	}
	uiApplied := false
	if ui, ok := doc["ui"].(map[string]any); ok {
		if err := s.Gate.ImportUIState(ui); err != nil {
			s.send(w, r, 400, map[string]any{"error": "콘솔 상태 복구 실패: " + err.Error()})
			return
		}
		uiApplied = true
	}
	msg := "복구했습니다"
	if len(changed) > 0 {
		msg += " — 재시작 후 적용: " + strings.Join(changed, ", ")
	}
	s.send(w, r, 200, map[string]any{"ok": true, "restart_required": changed, "ui_applied": uiApplied, "message": msg})
}

// handleAvailabilityCSV 는 GET /api/availability.csv — 30일 실측 가용성 내보내기.
// 읽기지만 운영 데이터라 writeGate 와 같은 게이트를 적용한다.
func (s *Server) handleAvailabilityCSV(w http.ResponseWriter, r *http.Request) {
	if !s.writeGate(w, r) {
		return
	}
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	_ = cw.Write([]string{"date", "device", "availability_pct", "observed_sec"})
	if s.Avail != nil {
		for _, row := range s.Avail.CSVSnapshot() {
			_ = cw.Write([]string{row.Day, row.Device,
				strconv.FormatFloat(row.Avail, 'f', 3, 64),
				strconv.FormatFloat(row.ObservedSec, 'f', 0, 64)})
		}
	}
	cw.Flush()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="availability-`+time.Now().Format("20060102")+`.csv"`)
	w.WriteHeader(200)
	_, _ = w.Write(buf.Bytes())
}
