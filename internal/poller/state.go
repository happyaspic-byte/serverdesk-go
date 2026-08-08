// Package poller 는 everrun-poller(poller.py)의 수집 코어 Go 포트다.
//
// 클러스터별 상태 저장소(ClusterState) + 3티어 avcli 스케줄러 + OS 메트릭(SSH) +
// SNMP 폭백 워커 + fleet 캐시를 제공한다. 수집 실패 시에도 마지막 성공 스냅샷을
// stale 표식과 함께 유지하는 것이 핵심 계약이다(빈 응답 금지).
package poller

import (
	"sync"
	"time"

	"serverdesk/internal/avcli"
	"serverdesk/internal/config"
	"serverdesk/internal/sshmetrics"
)

// Logf 는 호스트 로거 연결 훅이다(level 은 "debug"/"info"/"warn"/"error").
// 기본 no-op. main 이 config.Mask 를 적용한 로거를 꽂는다.
var Logf = func(level, cluster, msg string) {}

func logf(level, cluster, msg string) { Logf(level, cluster, msg) }

// Ring 은 스파크라인용 시계열 링버퍼다(poller.py 의 Ring).
// nil 값은 적립하지 않는다 — 첫 SSH 샘플처럼 델타 기준이 없어 cpu_pct 가
// null 인 포인트가 그래프에 0 으로 찍히는 것을 막기 위함이다.
type Ring struct {
	size int
	d    []ringPoint
	mu   sync.Mutex
}

type ringPoint struct {
	t int64
	v any
}

// NewRing 은 최대 size 포인트를 유지하는 링을 만든다.
func NewRing(size int) *Ring {
	if size <= 0 {
		size = 120
	}
	return &Ring{size: size}
}

// Push 는 (ts, value) 를 적립한다. value 가 nil 이면 무시한다(Python 동일).
func (r *Ring) Push(ts int64, value any) {
	if value == nil {
		return
	}
	r.mu.Lock()
	r.d = append(r.d, ringPoint{ts, value})
	if len(r.d) > r.size {
		r.d = append([]ringPoint(nil), r.d[len(r.d)-r.size:]...)
	}
	r.mu.Unlock()
}

// Series 는 [{"t":ts,"v":value}, ...] 형태로 복사해 돌려준다(오래된 것부터).
func (r *Ring) Series() []any {
	r.mu.Lock()
	out := make([]any, 0, len(r.d))
	for _, p := range r.d {
		out = append(out, map[string]any{"t": p.t, "v": p.v})
	}
	r.mu.Unlock()
	return out
}

// NodeTarget 은 OS 메트릭/SNMP 워커의 노드 대상 하나다
// (poller.py ClusterState.node_targets 의 원소).
type NodeTarget struct {
	IP       string
	Name     string
	User     string
	Password string
}

// ClusterState 는 클러스터 1개의 마지막 성공 수집 상태다(poller.py 의 ClusterState).
// 수집 실패 시에도 이전 값을 유지하고, 뷰 빌더가 stale 플래그를 붙인다.
//
// nodeOS 는 Python 의 동적 dict 계약을 그대로 살리기 위해 map[string]any 로 둔다:
// SSH 가 끊기면 파생 메트릭만 버리고 식별 필드(tz/platform/last_ssh_ts)를 남기는 등
// 키 집합이 수집원 상태에 따라 달라지기 때문이다.
type ClusterState struct {
	Key string
	Cfg *config.ClusterConfig

	mu sync.Mutex

	nodes      []avcli.NodeInfo
	vms        []avcli.VMInfo
	networks   []avcli.SharedNetwork
	sgroups    []avcli.StorageGroup
	volumes    []avcli.Volume
	containers []avcli.ImageContainer
	alerts     []avcli.Alert
	unit       *avcli.UnitInfo
	license    *avcli.LicenseInfo
	led        []avcli.LEDEntry

	Platform string // cfg.platform 또는 node-info 로 자동 판별(detect_platform)

	tierTS  map[string]float64 // tier -> 마지막 성공 epoch
	tierErr map[string]string  // tier -> 마지막 오류(없으면 빈 문자열 키 자체를 지운다)

	nodeOS    map[string]map[string]any    // node ip -> os metrics dict
	nodeSpine map[string]*sshmetrics.Spine // node ip -> spine 설정(메트릭과 수명이 다르다)
	history   map[string]map[string]*Ring  // node ip -> {"cpu","mem"} 링
	traps     []map[string]any             // 최신이 앞(Prepend). 최대 trapViewMax 건

	trapViewMax int
}

// NewClusterState 는 cfg 로 클러스터 상태를 만든다.
func NewClusterState(cfg *config.ClusterConfig, trapViewMax int) *ClusterState {
	if trapViewMax <= 0 {
		trapViewMax = 50
	}
	return &ClusterState{
		Key:         cfg.Key,
		Cfg:         cfg,
		Platform:    cfg.Platform,
		tierTS:      map[string]float64{},
		tierErr:     map[string]string{},
		nodeOS:      map[string]map[string]any{},
		nodeSpine:   map[string]*sshmetrics.Spine{},
		history:     map[string]map[string]*Ring{},
		traps:       []map[string]any{},
		trapViewMax: trapViewMax,
	}
}

// snapshot 은 뷰 빌드가 쓸 상태 사본을 잠금 하에 만든다.
// 슬라이스는 헤더만 복사한다(원소는 불변 취급 — 워커가 통째로 교체한다).
// alerts 는 ApplyAlertTimezone 이 원소를 고치므로 깊은 복사한다.
type snapshot struct {
	nodes      []avcli.NodeInfo
	vms        []avcli.VMInfo
	networks   []avcli.SharedNetwork
	sgroups    []avcli.StorageGroup
	volumes    []avcli.Volume
	containers []avcli.ImageContainer
	alerts     []avcli.Alert
	unit       *avcli.UnitInfo
	license    *avcli.LicenseInfo
	led        []avcli.LEDEntry
	nodeOS     map[string]map[string]any
	nodeSpine  map[string]*sshmetrics.Spine
	tierTS     map[string]float64
	tierErr    map[string]string
}

func (st *ClusterState) snapshot() snapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := snapshot{
		nodes:      append([]avcli.NodeInfo(nil), st.nodes...),
		vms:        append([]avcli.VMInfo(nil), st.vms...),
		networks:   append([]avcli.SharedNetwork(nil), st.networks...),
		sgroups:    append([]avcli.StorageGroup(nil), st.sgroups...),
		volumes:    append([]avcli.Volume(nil), st.volumes...),
		containers: append([]avcli.ImageContainer(nil), st.containers...),
		alerts:     append([]avcli.Alert(nil), st.alerts...),
		unit:       st.unit,
		license:    st.license,
		led:        append([]avcli.LEDEntry(nil), st.led...),
		nodeOS:     make(map[string]map[string]any, len(st.nodeOS)),
		nodeSpine:  make(map[string]*sshmetrics.Spine, len(st.nodeSpine)),
		tierTS:     make(map[string]float64, len(st.tierTS)),
		tierErr:    make(map[string]string, len(st.tierErr)),
	}
	for k, v := range st.nodeOS {
		cp := make(map[string]any, len(v))
		for kk, vv := range v {
			cp[kk] = vv
		}
		s.nodeOS[k] = cp
	}
	for k, v := range st.nodeSpine {
		s.nodeSpine[k] = v
	}
	for k, v := range st.tierTS {
		s.tierTS[k] = v
	}
	for k, v := range st.tierErr {
		s.tierErr[k] = v
	}
	return s
}

// --- 워커가 쓰는 setter 들 ------------------------------------------------

func (st *ClusterState) setNodes(v []avcli.NodeInfo) {
	st.mu.Lock()
	st.nodes = v
	st.mu.Unlock()
}

func (st *ClusterState) setVMs(v []avcli.VMInfo) {
	st.mu.Lock()
	st.vms = v
	st.mu.Unlock()
}

func (st *ClusterState) setNetworks(v []avcli.SharedNetwork) {
	st.mu.Lock()
	st.networks = v
	st.mu.Unlock()
}

func (st *ClusterState) setStorageGroups(v []avcli.StorageGroup) {
	st.mu.Lock()
	st.sgroups = v
	st.mu.Unlock()
}

func (st *ClusterState) setVolumes(v []avcli.Volume) {
	st.mu.Lock()
	st.volumes = v
	st.mu.Unlock()
}

func (st *ClusterState) setContainers(v []avcli.ImageContainer) {
	st.mu.Lock()
	st.containers = v
	st.mu.Unlock()
}

func (st *ClusterState) setAlerts(v []avcli.Alert) {
	st.mu.Lock()
	st.alerts = v
	st.mu.Unlock()
}

func (st *ClusterState) setUnit(v *avcli.UnitInfo) {
	st.mu.Lock()
	st.unit = v
	st.mu.Unlock()
}

func (st *ClusterState) setLicense(v *avcli.LicenseInfo) {
	st.mu.Lock()
	st.license = v
	st.mu.Unlock()
}

func (st *ClusterState) setLED(v []avcli.LEDEntry) {
	st.mu.Lock()
	st.led = v
	st.mu.Unlock()
}

// joinImageContainers 는 vms 와 image_containers 를 이름 접두어로 조인한다.
// (poller.py tier_slow 의 join 단계) avcli.JoinImageContainers 는 포인터 슬라이스를
// 요구하므로 상태 슬라이스 원소의 주소를 넘긴다 — 잠금 하에서 원位 갱신.
func (st *ClusterState) joinImageContainers() {
	st.mu.Lock()
	vms := make([]*avcli.VMInfo, 0, len(st.vms))
	for i := range st.vms {
		vms = append(vms, &st.vms[i])
	}
	cs := make([]*avcli.ImageContainer, 0, len(st.containers))
	for i := range st.containers {
		cs = append(cs, &st.containers[i])
	}
	avcli.JoinImageContainers(vms, cs)
	st.mu.Unlock()
}

// setPlatform 은 자동 판별 결과를 한 번만 기록한다(cfg 명시값이 있으면 유지).
func (st *ClusterState) setPlatform(p string) {
	st.mu.Lock()
	if st.Platform == "" {
		st.Platform = p
	}
	st.mu.Unlock()
}

// GetPlatform 은 현재 플랫폼을 읽는다.
func (st *ClusterState) GetPlatform() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.Platform
}

// Mark 는 티어 수집 결과를 기록한다(poller.py mark). err 가 빈 문자열이면 성공.
func (st *ClusterState) Mark(tier, err string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if err != "" {
		st.tierErr[tier] = err
		return
	}
	st.tierTS[tier] = nowFloat()
	delete(st.tierErr, tier)
}

// Age 는 티어의 마지막 성공 시점부터의 경과 초다. 한 번도 성공하지 못하면 nil.
func (st *ClusterState) Age(tier string) *float64 {
	st.mu.Lock()
	ts, ok := st.tierTS[tier]
	st.mu.Unlock()
	if !ok {
		return nil
	}
	v := round1(nowFloat() - ts)
	return &v
}

// TierErr 는 티어의 마지막 오류 문자열이다(없으면 "").
func (st *ClusterState) TierErr(tier string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.tierErr[tier]
}

// NodeCounts 는 /api/health 용 (노드 수, VM 수) 다.
func (st *ClusterState) NodeCounts() (nodes, vms int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return len(st.nodes), len(st.vms)
}

// OSReachable 은 /api/health 의 os_metrics 맵(ip -> reachable)이다.
func (st *ClusterState) OSReachable() map[string]bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make(map[string]bool, len(st.nodeOS))
	for ip, v := range st.nodeOS {
		out[ip] = boolVal(v["reachable"])
	}
	return out
}

// RingFor 는 노드의 cpu/mem 링을 돌려준다(없으면 만든다 — Python ring() 동일).
func (st *ClusterState) RingFor(ip, kind string) *Ring {
	st.mu.Lock()
	defer st.mu.Unlock()
	h := st.history[ip]
	if h == nil {
		h = map[string]*Ring{}
		st.history[ip] = h
	}
	r := h[kind]
	if r == nil {
		r = NewRing(st.Cfg.HistoryPoints)
		h[kind] = r
	}
	return r
}

// AddTrap 은 수신·정규화된 트랩 1건을 클러스터 뷰 버퍼 맨 앞에 넣는다
// (Python 의 traps.appendleft + maxlen).
func (st *ClusterState) AddTrap(trap map[string]any) {
	st.mu.Lock()
	st.traps = append([]map[string]any{trap}, st.traps...)
	if len(st.traps) > st.trapViewMax {
		st.traps = st.traps[:st.trapViewMax]
	}
	st.mu.Unlock()
}

// TrapsSnapshot 은 최신순 트랩 버퍼의 사본이다.
func (st *ClusterState) TrapsSnapshot() []map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]map[string]any(nil), st.traps...)
}

// NodeTargets 는 설정 노드와 avcli 발견 노드 IP 의 합집합이다
// (poller.py node_targets). EAC(avcli)가 죽은 상황이야말로 SNMP 폭백이 필요한
// 때인데 발견 결과만 볼 수는 없으므로 설정 노드가 먼저 들어간다.
func (st *ClusterState) NodeTargets() []NodeTarget {
	out := map[string]*NodeTarget{}
	var order []string
	for _, n := range st.Cfg.Nodes {
		if n.IP == "" {
			continue
		}
		out[n.IP] = &NodeTarget{IP: n.IP, Name: n.Name, Password: n.RootPassword, User: n.RootUser}
		order = append(order, n.IP)
	}
	defaultPW := st.Cfg.NodeRootPassword
	st.mu.Lock()
	for _, n := range st.nodes {
		if n.IP == nil || *n.IP == "" {
			continue
		}
		ip := *n.IP
		e := out[ip]
		if e == nil {
			e = &NodeTarget{IP: ip, Password: defaultPW, User: "root"}
			out[ip] = e
			order = append(order, ip)
		}
		if e.Name == "" && n.Name != nil {
			e.Name = *n.Name
		}
		if e.Password == "" {
			e.Password = defaultPW
		}
	}
	st.mu.Unlock()
	targets := make([]NodeTarget, 0, len(order))
	for _, ip := range order {
		targets = append(targets, *out[ip])
	}
	return targets
}

// --- nodeOS 갱신(OS/SNMP 워커 전용, Python 의 잠금 블록 포트) --------------

// failNodeOS 는 SSH 실패 시 파생 메트릭을 버리고 식별 필드만 남긴다
// (poller.py OsMetricsWorker.collect 의 실패 경로).
// 남겨두면 SSH 가 끊긴 뒤에도 몇 시간 전 값이 '현재값'인 척 노출되고,
// SNMP 폭백도 "값이 이미 있으니 덮지 않는다" 조건에 막혀 죽는다.
func (st *ClusterState) failNodeOS(ip, name string) {
	keep := []string{"ip", "name", "source", "snmp", "tz_offset_secs", "tz_name",
		"os_platform", "last_ssh_ts"}
	st.mu.Lock()
	defer st.mu.Unlock()
	prev := st.nodeOS[ip]
	if prev == nil {
		prev = map[string]any{}
	}
	nw := map[string]any{}
	for _, k := range keep {
		if v, ok := prev[k]; ok {
			nw[k] = v
		}
	}
	nw["ip"] = ip
	if name != "" {
		nw["name"] = name
	} else if v, ok := prev["name"]; ok {
		nw["name"] = v
	}
	nw["reachable"] = false
	nw["source"] = "ssh"
	if v, ok := prev["stale_since"]; ok && v != nil {
		nw["stale_since"] = v
	} else {
		nw["stale_since"] = nowFloat()
	}
	// SNMP 폭백이 채워 둔 값(source=='snmp')까지 지우면 os(10s) 주기가
	// snmp(60s) 주기보다 빨라 60초 중 ~50초 동안 cpu/mem/uptime 이 null 로
	// 깜빡인다. SSH 실패가 바로 폭백이 필요한 순간이므로 SNMP 제공 값은 보존한다.
	if prev["source"] == "snmp" {
		nw["source"] = "snmp"
		for _, k := range []string{"cpu_pct", "mem_pct", "uptime_secs", "ts"} {
			if v, ok := prev[k]; ok && v != nil {
				nw[k] = v
			}
		}
	}
	if boolVal(prev["reachable"]) {
		if ts, ok := prev["ts"]; ok && ts != nil {
			nw["last_ssh_ts"] = ts
		}
	}
	st.nodeOS[ip] = nw
}

// setNodeOS 는 SSH 성공 결과를 커밋한다. SNMP 워커가 써 둔 2차 신호("snmp" 키)는
// 다른 수집원의 결과라 보존한다 — 통째로 갈아끼우면 SSH 가 살아있는 동안
// SNMP 생존 신호가 영영 보이지 않는다.
func (st *ClusterState) setNodeOS(ip string, m map[string]any, spine *sshmetrics.Spine) {
	st.mu.Lock()
	if spine != nil {
		// spine 은 메트릭이 아니라 설정이라 nodeOS 와 수명이 다르다.
		st.nodeSpine[ip] = spine
	}
	if prev, ok := st.nodeOS[ip]; ok {
		if ps, has := prev["snmp"]; has {
			m["snmp"] = ps
		}
	}
	st.nodeOS[ip] = m
	st.mu.Unlock()
}

// snmpNodeOS 는 SNMP 폭백 결과를 반영한다(poller.py SnmpWorker 의 잠금 블록).
// SSH 가 실패한 노드는 SNMP 값으로 덮어쓴다 — '엔트리가 비었을 때만' 조건을
// 걸면 SSH 성공 이력이 있는 노드에 옛 값이 남아 폭백이 영원히 발동하지 않는다
// (실측: cpu 42.0 이 몇 시간 고정).
func (st *ClusterState) snmpNodeOS(ip, name string, info map[string]any) {
	st.mu.Lock()
	defer st.mu.Unlock()
	entry := st.nodeOS[ip]
	if entry == nil {
		entry = map[string]any{"ip": ip, "name": name, "reachable": false, "source": "snmp"}
		st.nodeOS[ip] = entry
	}
	entry["snmp"] = info
	if !boolVal(entry["reachable"]) && boolVal(info["reachable"]) {
		filled := false
		for _, k := range []string{"cpu_pct", "mem_pct", "uptime_secs"} {
			if v, ok := info[k]; ok && v != nil {
				entry[k] = v
				filled = true
			} else {
				delete(entry, k)
			}
		}
		if filled {
			entry["source"] = "snmp"
			entry["ts"] = float64(time.Now().Unix())
		}
	}
}

// nowFloat 는 Python time.time() 에 해당하는 초 단위 float 시각이다.
func nowFloat() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

// PatchDisplayMeta 는 PUT /api/clusters 의 표시 메타 변경을 실행 중 설정에 반영한다.
// 뷰 빌더가 Cfg.Name 을 읽는 경로와 동기화한다(문자열 필드라도 동시 읽기/쓰기는
// 데이터 경합이라 잠금으로 직렬화한다).
func (st *ClusterState) PatchDisplayMeta(fields map[string]string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, v := range fields {
		switch k {
		case "label":
			st.Cfg.Name = v
		case "company":
			st.Cfg.Company = v
		case "factory":
			st.Cfg.Factory = v
		case "site":
			st.Cfg.Site = v
		}
	}
}

// DisplayName 은 표시 이름을 읽는다(PatchDisplayMeta 와 같은 잠금).
func (st *ClusterState) DisplayName() string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.Cfg.Name
}

func round1(x float64) float64 {
	// Python round(x, 1) 은 round-half-even 이다.
	return roundHalfEven(x*10) / 10
}

// roundHalfEven 은 Python 의 banker's rounding 이다.
func roundHalfEven(x float64) float64 {
	return mathRoundToEven(x)
}

func boolVal(v any) bool {
	b, _ := v.(bool)
	return b
}
