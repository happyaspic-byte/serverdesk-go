package poller

// 엣지 워커 매니저 — 엣지 장비 목록의 실행 중 변경(핫애드/제거/메타 패치)을 담당한다.
//
// edge.Worker 는 장비 목록이 생성 시 고정(비공개 필드)이라, 목록 변경은 워커를
// 새로 만들어 교체하는 방식으로 구현한다. 첫 라운드는 Start 즉시 돌기 때문에
// 스냅샷 공백은 수 초에 그친다. 대가로 장비별 48포인트 히스토리가 리셋된다 —
// 메타 편집·추가·제거는 드문 운영 작업이라 실익이 더 크다(Python 은 워커 내부
// 목록을 직접 고쳐 히스토리가 유지됐다 — Go 포트의 알려진 차이).

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"

	"serverdesk/internal/edge"
)

// EdgeCollectorStatus is a point-in-time worker liveness snapshot.
type EdgeCollectorStatus struct {
	Configured  int
	Observed    int
	LastRoundAt time.Time
	LastError   string
}

// EdgeManager 는 실행 중인 edge.Worker 와 그 대상 목록을 소유한다.
//
// 워커의 수명 컨텍스트는 NewEdgeManager 에 받은 루트 ctx 뿐이다 —
// Add/Remove/PatchMeta 에 HTTP 요청 ctx 를 넘기면 요청 종료와 함께
// 재생성된 워커까지 죽어 스냅샷이 그 상태로 얼어붙는다(실제 관측된 장애).
type EdgeManager struct {
	mu      sync.Mutex
	root    context.Context // 워커 수명 전용 루트(절대 요청 ctx 금지)
	devices []edge.DeviceConfig
	worker  *edge.Worker
	cancel  context.CancelFunc
	logf    func(level, comp, msg string)
	// lastGood 는 마지막 정상 스냅샷이다. 워커 교체(Add/Remove/PatchMeta) 직후
	// 새 워커의 첫 라운드가 끝나기 전까지 Latest 가 빈 목록을 반환하면
	// /api/devices 에서 엣지 장비 전체가 몇 초간 사라졌다 돌아오는 깜빡임이
	// 생긴다(실제 보고된 결함). lastGoodAt 으로 2라운드까지만 폴섭한다.
	lastGood   []map[string]any
	lastGoodAt time.Time
}

// NewEdgeManager 는 초기 장비 목록으로 워커를 만들어 ctx 에서 시작한다.
// 장비가 0 대면 워커를 만들지 않는다(빈 라운드 방지).
// ctx 는 프로세스 수명의 루트여야 한다 — 이후 모든 워커 재생성도 이 ctx 를 쓴다.
func NewEdgeManager(ctx context.Context, devices []edge.DeviceConfig, logf func(level, comp, msg string)) *EdgeManager {
	m := &EdgeManager{root: ctx, logf: logf}
	m.devices = append([]edge.DeviceConfig(nil), devices...)
	if len(m.devices) > 0 {
		m.spawnLocked()
	}
	return m
}

// spawnLocked 은 현재 목록으로 새 워커를 만든다. 호출 전 mu 를 쥐고 있어야 한다.
func (m *EdgeManager) spawnLocked() {
	w := edge.NewWorker(append([]edge.DeviceConfig(nil), m.devices...))
	if m.logf != nil {
		w.Logf = m.logf
	}
	wctx, cancel := context.WithCancel(m.root)
	m.worker = w
	m.cancel = cancel
	go w.Start(wctx)
}

// respawnLocked 은 워커를 교체한다(목록 변경 반영).
func (m *EdgeManager) respawnLocked() {
	if m.cancel != nil {
		m.cancel()
	}
	m.worker = nil
	m.cancel = nil
	if len(m.devices) > 0 {
		m.spawnLocked()
	}
}

// Devices 는 현재 대상 설정 목록의 사본이다.
func (m *EdgeManager) Devices() []edge.DeviceConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]edge.DeviceConfig(nil), m.devices...)
}

// Latest 는 현재 목록에 있는 장비만 남긴 최신 스냅샷이다.
// 제거 직후 이전 워커의 스냅샷이 잠시 남는 경합을 API 출력 단에서 걸러낸다
// (Python 의 '최대 1라운드 재등장' 경합을 여기서 결정적으로 닫는다).
//
// 워커 교체 직후 새 워커의 첫 라운드가 비어 있으면 마지막 정상 스냅샷으로
// 버틴다(최대 2라운드=130초). 이 폴섭이 없으면 장비 추가/삭제/수정 때마다
// 엣지 장비가 화면에서 몇 초간 사라졌다 돌아온다.
// 최상위 맵은 요청별 사본이다(가용성 주입용). 중첩 값은 읽기 전용이다.
func (m *EdgeManager) Latest() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()

	keys := map[string]bool{}
	for _, d := range m.devices {
		keys[d.Key] = true
	}
	var latest []map[string]any
	if m.worker != nil {
		latest = m.worker.LatestDevices()
	}
	if len(latest) > 0 {
		m.lastGood, m.lastGoodAt = latest, time.Now()
	} else if len(m.lastGood) > 0 && time.Since(m.lastGoodAt) < 130*time.Second {
		latest = m.lastGood
	} else {
		return nil
	}

	out := []map[string]any{}
	for _, d := range latest {
		id, _ := d["id"].(string)
		if keys[id] {
			out = append(out, maps.Clone(d))
		}
	}
	return out
}

// CollectorStatus reports worker liveness without exposing device identities.
func (m *EdgeManager) CollectorStatus() EdgeCollectorStatus {
	m.mu.Lock()
	worker := m.worker
	configured := len(m.devices)
	m.mu.Unlock()

	status := EdgeCollectorStatus{Configured: configured}
	if worker == nil {
		return status
	}
	status.Observed = len(worker.LatestDevices())
	status.LastRoundAt, status.LastError = worker.CollectionStatus()
	return status
}

// Add 는 장비를 핫애드한다(다음 라운드부터 폴링 — 첫 라운드는 즉시 시작).
func (m *EdgeManager) Add(d edge.DeviceConfig) {
	m.mu.Lock()
	m.devices = append(m.devices, d)
	m.respawnLocked()
	m.mu.Unlock()
}

// Remove 는 장비를 제거한다.
func (m *EdgeManager) Remove(key string) {
	m.mu.Lock()
	out := m.devices[:0]
	for _, d := range m.devices {
		if d.Key != key {
			out = append(out, d)
		}
	}
	m.devices = append([]edge.DeviceConfig(nil), out...)
	retained := make([]map[string]any, 0, len(m.lastGood))
	for _, d := range m.lastGood {
		if id, _ := d["id"].(string); id != key {
			retained = append(retained, d)
		}
	}
	m.lastGood = retained
	m.respawnLocked()
	m.mu.Unlock()
}

// PatchMeta 는 표시 메타 변경을 즉시 반영한다. 목록의 설정을 고치고 워커를
// 교체하며, 교체 완료 전까지는 마지막 스냅샷의 사본을 패치해 다음 GET 부터
// 새 값이 보이게 한다. 이미 발행된 스냅샷과 워커의 맵은 변경하지 않는다.
//
// config 키 → 엣지 meta 키(카멜) 매핑 — floor_pos 만 이름이 갈린다(floorPos).
func (m *EdgeManager) PatchMeta(key string, fields map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var updated edge.DeviceConfig
	changed := false
	for i := range m.devices {
		if m.devices[i].Key != key {
			continue
		}
		d := &m.devices[i]
		before := edgeDisplayMeta(*d)
		for k, v := range fields {
			switch k {
			case "label":
				d.Name = v
			case "company":
				d.Company = v
			case "factory":
				d.Factory = v
			case "site":
				d.Site = v
			case "asset_tag":
				d.AssetTag = v
			case "floor_pos":
				d.FloorPos = v
			case "vendor":
				d.Vendor = v
			}
		}
		updated = *d
		changed = changed || before != edgeDisplayMeta(updated)
	}
	// Equivalent recognized fields (including empty overrides that resolve to
	// the same effective defaults) change nothing: no config write, no cache
	// refresh, no respawn. Real edits still restart the worker below.
	if !changed {
		return
	}
	// Latest 와 같은 잠금 안에서 폴섭을 교체해 이전 워커의 응답이
	// 방금 편집한 스냅샷을 덮어쓰지 못하게 한다.
	if m.worker != nil {
		if latest := m.worker.LatestDevices(); len(latest) > 0 {
			m.lastGood, m.lastGoodAt = latest, time.Now()
		}
	}
	values := edgeDisplayMeta(updated)
	patched := append([]map[string]any(nil), m.lastGood...)
	for i, d := range patched {
		if id, _ := d["id"].(string); id != key {
			continue
		}
		meta, _ := d["meta"].(map[string]any)
		if meta == nil {
			continue
		}
		d = maps.Clone(d)
		meta = maps.Clone(meta)
		d["meta"] = meta
		patched[i] = d
		for k := range fields {
			switch k {
			case "label":
				meta["label"] = values.label
			case "company":
				meta["company"] = values.company
			case "factory":
				meta["factory"] = values.factory
			case "site":
				d["site"] = values.site
			case "asset_tag":
				meta["assetTag"] = values.assetTag
			case "floor_pos":
				meta["floorPos"] = values.floorPos
			case "vendor":
				meta["vendor"] = projectedVendor(updated, d, values.vendor)
			}
		}
	}
	m.lastGood = patched
	m.respawnLocked()
}

// edgeDisplayMeta follows edge.baseDevice's configured defaults. Both the
// no-op check and immediate fallback patch use the same effective values.
type edgeMetaValues struct {
	label, company, factory, site, assetTag, floorPos, vendor string
}

func edgeDisplayMeta(d edge.DeviceConfig) edgeMetaValues {
	values := edgeMetaValues{d.Name, d.Company, d.Factory, d.Site, d.AssetTag, d.FloorPos, d.Vendor}
	if values.label == "" {
		values.label = d.Key
	}
	if values.company == "" {
		values.company = "루비컴"
	}
	if values.factory == "" {
		parts := strings.Split(d.IP, ".")
		if len(parts) > 3 {
			parts = parts[:3]
		}
		values.factory = strings.Join(parts, ".") + ".0/24"
	}
	if values.site == "" {
		values.site = d.IP
	}
	return values
}

// projectedVendor keeps the vendor shown by the retained snapshot's producer
// path while the configured value is empty. NAS, printer, and PLC hardcode a
// constant on success and emit the raw configured value when down; proxmox
// substitutes "Proxmox" for an empty configured vendor; server uses the
// configured value unless Redfish reports a maker. A configured override
// projects directly on every path, and the first replacement round replaces
// the projection with the real producer output.
func projectedVendor(dev edge.DeviceConfig, retained map[string]any, configured string) string {
	if configured != "" {
		return configured
	}
	status, _ := retained["status"].(string)
	if status == "down" {
		return ""
	}
	kind := dev.Kind
	if kind == "" {
		kind = dev.Type
	}
	switch kind {
	case "nas":
		return "Synology"
	case "printer":
		return "Samsung Electronics"
	case "plc":
		return "OMRON"
	case "proxmox":
		return "Proxmox"
	}
	return ""
}

// Stop 은 워커를 멈춘다(그레이스풀 셧다운 경로).
func (m *EdgeManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}
