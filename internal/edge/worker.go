package edge

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"serverdesk/internal/snmp"
)

// devHist — 장비별 cpu/mem 히스토리(최대 histLen 포인트).
type devHist struct {
	cpu []int64
	mem []int64
}

// Worker — 엣지 디바이스 폴싱 워커.
//
// Python EdgeWorker 와 같은 생존성 원칙(P1): 한 장비의 패닉/오류가 라운드나
// 워커를 죽이지 않는다 — 장비 단위로 격리하고, 실패한 장비는 down 골격으로 낸다.
// 60초 라운드, 5라운드마다 정적 정보 재조회, 48포인트 cpu/mem 히스토리.
type Worker struct {
	// SNMPGet — SNMP GET 함수. nil 이면 serverdesk/internal/snmp.Get 사용.
	// 테스트에서 가짜 구현 주입용으로 노출한다.
	SNMPGet SNMPGetFunc
	// Logf — (level, comp, msg) 로거. nil 이면 침묵.
	Logf func(level, comp, msg string)

	devices []DeviceConfig

	sws *http.Client
	pve *http.Client
	rf  *http.Client

	mu     sync.RWMutex
	latest []map[string]any
	static map[string]any
	hist   map[string]*devHist
	round  int
}

// NewWorker — 설정 목록으로 워커 생성.
// kind 가 알려진 종류(printer/nas/plc/proxmox/server)가 아니거나 ip/key 가
// 없는 항목은 걸러낸다 — 폴 수 없는 항목이 매 라운드 down 으로 도배되는 것을 막기 위함.
func NewWorker(devices []DeviceConfig) *Worker {
	w := &Worker{
		SNMPGet: nil,
		sws:     insecureClient(5 * time.Second),
		pve:     insecureClient(pveHTTPTimeout),
		rf:      insecureClient(redfishTimeout),
		latest:  []map[string]any{},
		static:  map[string]any{},
		hist:    map[string]*devHist{},
	}
	for _, d := range devices {
		switch d.kind() {
		case "printer", "nas", "plc", "proxmox", "server":
		default:
			continue
		}
		if d.IP == "" || d.Key == "" {
			continue
		}
		w.devices = append(w.devices, d)
	}
	return w
}

// Start — ctx 가 끝날 때까지 60초 라운드 루프. 첫 라운드는 즉시 돌린다.
func (w *Worker) Start(ctx context.Context) {
	for {
		w.safeRound(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(fastSec):
		}
	}
}

// LatestDevices — 마지막 라운드의 플랫 device dict 목록(serverdesk/device@1).
// 매 라운드 새 맵으로 통째 교첸하므로 반환 슬라이스를 읽기 전용으로 써도 안전하다.
func (w *Worker) LatestDevices() []map[string]any {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]map[string]any, len(w.latest))
	copy(out, w.latest)
	return out
}

// safeRound — 라운드 수준 격리: 라운드 전체 패닉도 워커를 죽이지 않는다.
func (w *Worker) safeRound(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			w.logf("ERROR", "edge", fmt.Sprintf("round failed: %v", r))
		}
	}()
	w.pollRound(ctx)
}

func (w *Worker) pollRound(ctx context.Context) {
	now := float64(time.Now().UnixNano()) / 1e9
	snmpGet := w.SNMPGet
	if snmpGet == nil {
		snmpGet = snmp.Get
	}
	pc := &pollCtx{
		ctx: ctx, snmp: snmpGet, now: now,
		refresh: w.round%staticEvery == 0,
		sws:     w.sws, pve: w.pve, rf: w.rf,
	}
	out := make([]map[string]any, 0, len(w.devices))
	for _, dev := range w.devices {
		out = append(out, w.pollOne(pc, dev))
	}
	w.mu.Lock()
	w.latest = out
	w.mu.Unlock()
	w.round++
}

// pollOne — 장비 1대 폴 + 히스토리 갱신. 패닉 시 down 골격으로 대쳸다.
// (패닉이면 static 캐시는 갱신하지 않는다 — Python 과 동일.)
func (w *Worker) pollOne(pc *pollCtx, dev DeviceConfig) (out map[string]any) {
	key := dev.Key
	defer func() {
		if r := recover(); r != nil {
			w.logf("ERROR", "edge", fmt.Sprintf("%s poll failed: %v", key, r))
			out = downBase(dev, pc.now)
		}
	}()
	var d map[string]any
	switch dev.kind() {
	case "printer":
		st, _ := w.static[key].(*printerStatic)
		d, st = w.pollPrinter(pc, dev, st)
		w.static[key] = st
	case "nas":
		st, _ := w.static[key].(*nasStatic)
		d, st = w.pollNAS(pc, dev, st)
		w.static[key] = st
	case "plc":
		st, _ := w.static[key].(*plcStatic)
		d, st = w.pollPLC(pc, dev, st)
		w.static[key] = st
	case "proxmox":
		st, _ := w.static[key].(*pveStatic)
		d, st = w.pollProxmox(pc, dev, st)
		w.static[key] = st
	default: // "server"
		st, _ := w.static[key].(*srvStatic)
		d, st = w.pollServer(pc, dev, st)
		w.static[key] = st
	}
	h := w.hist[key]
	if h == nil {
		h = &devHist{}
		w.hist[key] = h
	}
	if !d["cpuNA"].(bool) {
		h.cpu = appendCap(h.cpu, d["cpu0"].(int64), histLen)
	}
	if !d["memNA"].(bool) {
		h.mem = appendCap(h.mem, d["mem0"].(int64), histLen)
	}
	d["histCpu"] = append([]int64{}, h.cpu...)
	d["histMem"] = append([]int64{}, h.mem...)
	return d
}

// appendCap — 최대 n개 유지 슬라이딩.
func appendCap(s []int64, v int64, n int) []int64 {
	s = append(s, v)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

func (w *Worker) logf(level, comp, msg string) {
	if w.Logf != nil {
		w.Logf(level, comp, msg)
	}
}
