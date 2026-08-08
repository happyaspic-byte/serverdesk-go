package poller

// EventLog / EventWatcher — 장비 이벤트 이력(poller.py 의 동명 클래스 포트).
//
// 로그 화면의 '라이브 tail' 은 활성 경보 스냅샷이 아니라 **일어난 일의 이력**이어야
// 한다. 스냅샷 방식은 경보가 해소되는 순간 로그에서도 증발했다. 여기서 상태
// 전이·경보 발생/해제를 이벤트로 남기고 /api/devices 응답에 최근분(events[])을
// 싣는다. 재시작해도 jsonl 테일을 복원해 이력이 이어진다.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

// EventLog 는 장비 이벤트의 링 + jsonl 영속 저장소다.
type EventLog struct {
	path string
	keep int
	mu   sync.Mutex
	ring []map[string]any
}

// NewEventLog 는 path 의 jsonl 테일(최대 keep 건)을 복원해 연다.
func NewEventLog(path string, keep int) *EventLog {
	if keep <= 0 {
		keep = 500
	}
	el := &EventLog{path: path, keep: keep, ring: []map[string]any{}}
	var lines [][]byte
	if data, err := os.ReadFile(path); err == nil {
		sc := bufio.NewScanner(bytes.NewReader(data))
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			ln := bytes.TrimSpace(sc.Bytes())
			if len(ln) > 0 {
				lines = append(lines, append([]byte(nil), ln...))
			}
		}
	}
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	for _, ln := range lines {
		var ev map[string]any
		if json.Unmarshal(ln, &ev) == nil {
			el.ring = append(el.ring, ev)
		}
	}
	return el
}

// Len 은 현재 링의 건수다(기동 로그용).
func (el *EventLog) Len() int {
	el.mu.Lock()
	defer el.mu.Unlock()
	return len(el.ring)
}

// eventStamp 는 화면 표기 관행(KST, 사전순 정렬 가능)과 같은 축의 타임스탬프다.
func eventStamp() string {
	return time.Now().UTC().Add(9 * time.Hour).Format("2006-01-02 15:04:05")
}

// Add 는 이벤트 1건을 링과 파일에 남긴다. 파일 쓰기 실패는 조용히 넘긴다 —
// 로그 실패가 폴러를 죽이지 못하게 한다.
func (el *EventLog) Add(host, label, kind, sev, desc string) {
	ev := map[string]any{
		"ts": eventStamp(), "host": host, "label": label,
		"kind": kind, "sev": sev, "desc": desc,
	}
	b, err := json.Marshal(ev)
	el.mu.Lock()
	el.ring = append(el.ring, ev)
	if len(el.ring) > el.keep {
		el.ring = el.ring[len(el.ring)-el.keep:]
	}
	if err == nil {
		if f, ferr := os.OpenFile(el.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); ferr == nil {
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	el.mu.Unlock()
}

// List 는 최신순 최대 limit 건을 돌려준다.
func (el *EventLog) List(limit int) []any {
	el.mu.Lock()
	defer el.mu.Unlock()
	n := len(el.ring)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]any, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, el.ring[n-1-i])
	}
	return out
}

// EventWatcher 는 10초 주기로 장비 뷰(FT+엣지)를 diff 해 이벤트를 EventLog 에
// 남긴다. 첫 스냅은 기준선으로만 쓴다(부팅 때 전 장비 '등록' 스팸 방지).
// 경보 diff 키는 (name, desc, sev) — DEVICE_STATE 는 상태 전이 이벤트가 대신
// 말하므로 제외한다.
type EventWatcher struct {
	ev     *EventLog
	cache  *FleetCache
	states []*ClusterState
	// edgeDevices 는 엣지 장비 스냅샷 공급 함수다(edge.Worker.LatestDevices 에 해당).
	edgeDevices func() []map[string]any
	// display 는 클러스터 key → 표시 라벨 공급 함수다.
	display func(key string) string

	snap   map[string]watchEntry
	primed bool
	t0     time.Time
}

type watchEntry struct {
	status string
	alerts map[[3]string]bool
	label  string
}

// bootGrace 는 기동 유예(초)다 — FT 티어가 순차 채워지며 기존 경보가 전부
// '신규 발생'으로 찍히는 재시작 노이즈를 흡수한다.
const bootGrace = 120.0

var stateLabelKO = map[string]string{"op": "가동", "deg": "저하", "down": "오프라인"}

// NewEventWatcher 는 이벤트 워처를 만든다.
func NewEventWatcher(ev *EventLog, cache *FleetCache, states []*ClusterState,
	edgeDevices func() []map[string]any, display func(key string) string) *EventWatcher {
	return &EventWatcher{
		ev: ev, cache: cache, states: states,
		edgeDevices: edgeDevices, display: display,
		snap: map[string]watchEntry{}, t0: time.Now(),
	}
}

// Start 는 ctx 종료까지 10초 주기로 diff 를 돌린다.
func (w *EventWatcher) Start(ctx context.Context) {
	for ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("error", "events", fmt.Sprintf("이벤트 워처 실패: %v\n%s", r, debug.Stack()))
				}
			}()
			w.round()
		}()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

// devices 는 현재 장비 뷰(FT 변환 + 엣지)를 모은다.
func (w *EventWatcher) devices() []map[string]any {
	var devs []map[string]any
	fleet, _, _ := w.cache.Snapshot()
	if fleet != nil {
		func() {
			defer func() { _ = recover() }()
			disp := map[string]DisplayMeta{}
			for _, st := range w.states {
				disp[st.Key] = DisplayMeta{Label: w.display(st.Key)}
			}
			out := BuildDevices(fleet, disp, 30)
			for _, d := range listVal(out["devices"]) {
				if dm := dictVal(d); dm != nil {
					devs = append(devs, dm)
				}
			}
		}()
	}
	if w.edgeDevices != nil {
		devs = append(devs, w.edgeDevices()...)
	}
	return devs
}

type alertKey [3]string

func (w *EventWatcher) round() {
	cur := map[string]watchEntry{}
	for _, d := range w.devices() {
		host := strVal(d["host"])
		if host == "" {
			host = strVal(d["id"])
		}
		if host == "" {
			continue
		}
		m := dictVal(d["meta"])
		al := map[[3]string]bool{}
		for _, av := range listVal(m["alerts"]) {
			a := dictVal(av)
			if a == nil {
				continue
			}
			if strVal(a["name"]) == "DEVICE_STATE" {
				continue
			}
			sev := strVal(a["sev"])
			if sev == "" {
				sev = strVal(a["severity"])
			}
			if sev == "" {
				sev = "warning"
			}
			if sev != "critical" && sev != "warning" && sev != "info" {
				sev = "warning"
			}
			al[alertKey{strVal(a["name"]), strVal(a["desc"]), sev}] = true
		}
		label := host
		if m != nil && strVal(m["label"]) != "" {
			label = strVal(m["label"])
		}
		cur[host] = watchEntry{status: strVal(d["status"]), alerts: al, label: label}
	}
	if w.primed && time.Since(w.t0).Seconds() > bootGrace {
		for host, c := range cur {
			p, ok := w.snap[host]
			if !ok {
				w.ev.Add(host, c.label, "new", "info", "장비 등록됨")
				continue
			}
			if p.status != c.status {
				sev := "info"
				if c.status == "down" {
					sev = "critical"
				} else if c.status == "deg" {
					sev = "warning"
				}
				w.ev.Add(host, c.label, "state", sev,
					fmt.Sprintf("상태 %s → %s", stateLabel(p.status), stateLabel(c.status)))
			}
			for _, k := range sortedAlertDiff(c.alerts, p.alerts) {
				desc := k[1]
				if desc == "" {
					desc = k[0]
				}
				w.ev.Add(host, c.label, "alert", k[2], desc)
			}
			for _, k := range sortedAlertDiff(p.alerts, c.alerts) {
				desc := k[1]
				if desc == "" {
					desc = k[0]
				}
				w.ev.Add(host, c.label, "clear", "info", "해제: "+desc)
			}
		}
		for host, p := range w.snap {
			if _, ok := cur[host]; !ok {
				w.ev.Add(host, p.label, "gone", "info", "장비 제거됨")
			}
		}
	}
	w.snap = cur
	w.primed = true
}

func stateLabel(s string) string {
	if v, ok := stateLabelKO[s]; ok {
		return v
	}
	return s
}

// sortedAlertDiff 는 a 에만 있는 경보 키를 정렬해 돌려준다(Python sorted(set 차)).
func sortedAlertDiff(a, b map[[3]string]bool) []alertKey {
	var out []alertKey
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		for x := 0; x < 3; x++ {
			if out[i][x] != out[j][x] {
				return out[i][x] < out[j][x]
			}
		}
		return false
	})
	return out
}
