package poller

// SNMP 트랩 수신 배선 (poller.py start_trap_receiver 포트).
//
// - src IP → ClusterState 라우팅 맵은 설정(mgmt_ip + 노드 IP)에서 만든다.
// - traps.jsonl(전 클러스터 공통 링버퍼)을 읽어 재시작 전 트랩을 각 클러스터 뷰에
//   재분배한다(유실 방지).
// - 트랩은 이벤트라 sink 는 헬스가 아니라 meta.traps[] 버퍼에만 넣는다.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"serverdesk/internal/snmp"
)

// TrapRuntime 은 트랩 수신기 + 영속 저장소 묶음이다.
type TrapRuntime struct {
	Receiver *snmp.TrapReceiver
	Store    *snmp.TrapStore
}

// TrapToRaw 는 디코더 산출물(snmp.Trap)을 Python 원시 트랩 dict 모양으로 변환한다.
// ClusterState.traps 와 _trap_view 가 이 모양을 기대한다(trap_oid 키에 주의).
func TrapToRaw(t snmp.Trap) map[string]any {
	vbs := make([]any, 0, len(t.Varbinds))
	for _, v := range t.Varbinds {
		vbs = append(vbs, map[string]any{
			"oid": v.OID, "name": v.Name, "kind": v.Kind,
			"value": v.Value, "display": v.Display,
		})
	}
	return map[string]any{
		"ts": t.Ts, "src": t.Src, "community": t.Community,
		"version": t.Version, "pdu": t.PDU, "trap_oid": t.OID,
		"name": t.Name, "sev": t.Sev, "desc": t.Desc,
		"varbinds": vbs,
	}
}

// StartTrapReceiver 는 트랩 수신기를 구성·기동한다. 실패(바인드 거부 등)는 경고 후
// nil 반환 — 트랩 수신 실패가 폴러 본체를 죽이지 않는다(Python 과 같은 판단).
//
// persist 가 상대경로면 runtimeDir 기준이다. community 가 빈 문자열이면 모든
// community 를 허용한다(Python 의 None 계약).
func StartTrapReceiver(ctx context.Context, enabled bool, bind string, port int,
	community string, persist string, ring int, mibDir string, runtimeDir string,
	states []*ClusterState) *TrapRuntime {
	if !enabled {
		logf("info", "-", "트랩 수신 비활성(설정)")
		return nil
	}

	// 라우팅 맵: src IP → ClusterState (mgmt IP + 설정 노드 IP)
	ipToState := map[string]*ClusterState{}
	for _, st := range states {
		if st.Cfg.MgmtIP != "" {
			ipToState[st.Cfg.MgmtIP] = st
		}
		for _, n := range st.Cfg.Nodes {
			if n.IP != "" {
				ipToState[n.IP] = st
			}
		}
	}
	byKey := map[string]*ClusterState{}
	for _, st := range states {
		byKey[st.Key] = st
	}
	router := func(srcIP string) *ClusterState {
		if st, ok := ipToState[srcIP]; ok {
			return st
		}
		// 설정에 없던 노드 IP 를 avcli 가 발견했을 수 있다 — 라이브 대상도 확인.
		for _, s := range states {
			for _, t := range s.NodeTargets() {
				if t.IP == srcIP {
					return s
				}
			}
		}
		return nil
	}

	// 영속 저장 + 재시작 재분배
	if persist == "" {
		persist = "traps.jsonl"
	}
	if !filepath.IsAbs(persist) {
		persist = filepath.Join(runtimeDir, persist)
	}
	store := snmp.NewTrapStore(persist, ring)
	// Go TrapStore 버퍼도 채운다 — 다음 Add 가 파일 전체를 재작성하므로 버퍼가
	// 비어 있으면 이전 이력이 날아간다.
	store.Load()
	restored := restoreTraps(persist, byKey, router)
	if restored > 0 {
		logf("info", "-", fmt.Sprintf("영속 트랩 %d건 복원(%s)", restored, persist))
	}

	rx := snmp.NewTrapReceiver(bind, port, community, mibDir, store, func(t snmp.Trap) {
		st := router(t.Src)
		if st == nil {
			return // 미등록 발신 — 폐기(Python dropped_unrouted)
		}
		st.AddTrap(TrapToRaw(t))
	})
	if err := rx.Start(ctx); err != nil {
		logf("error", "-", fmt.Sprintf("트랩 포트 %d 바인드 실패: %v. 수신기 비활성.", port, err))
		return nil
	}
	logf("info", "-", fmt.Sprintf("트랩 수신 시작 udp://%s:%d", bind, port))
	return &TrapRuntime{Receiver: rx, Store: store}
}

// restoreTraps 는 traps.jsonl 을 직접 읽어 각 클러스터 뷰에 재분배한다.
// 오래된 것부터 읽어 Prepend 하므로 최신이 앞에 온다.
//
// 두 포맷을 모두 읽는다: Python 원본(trap_oid + _route)과 Go TrapStore 재작성본
// (oid, _route 없음). _route 가 없으면 src IP 로 라우팅한다(Python 폭백과 동일).
func restoreTraps(path string, byKey map[string]*ClusterState, router func(string) *ClusterState) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	restored := 0
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		ln := bytes.TrimSpace(sc.Bytes())
		if len(ln) == 0 {
			continue
		}
		var rec map[string]any
		if json.Unmarshal(ln, &rec) != nil {
			continue
		}
		var st *ClusterState
		if key := strVal(rec["_route"]); key != "" {
			st = byKey[key]
		}
		if st == nil {
			if src := strVal(rec["src"]); src != "" {
				st = router(src)
			}
		}
		if st == nil {
			continue
		}
		// Go 재작성본의 "oid" 를 Python 키 "trap_oid" 로 맞춰 뷰가 읽게 한다.
		if strVal(rec["trap_oid"]) == "" {
			if oid := strVal(rec["oid"]); oid != "" {
				rec["trap_oid"] = oid
			}
		}
		delete(rec, "_route")
		st.AddTrap(rec)
		restored++
	}
	return restored
}

// ResolveMibDir 는 trap_mib_dir 를 절대경로로 해석한다. 상대경로면 실행 파일
// 디렉터리 기준으로 먼저 보고, 없으면 작업 디렉터리 기준을 쓴다
// (Python 은 poller.py 디렉터리 기준이었다).
func ResolveMibDir(dir string) string {
	dir = ExpandUser(dir)
	if dir == "" || filepath.IsAbs(dir) {
		return dir
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), dir)
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand
		}
	}
	return dir
}

// ExpandUser 는 선두 ~ 를 홈 디렉터리로 펼친다(Python os.path.expanduser).
func ExpandUser(p string) string {
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
		return p
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
