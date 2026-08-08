// Package config 는 everrun-poller 의 config.local.json 스키마를 그대로 읽고
// 원자적 RMW 저장·비밀 마스킹·배포 점검을 제공한다.
// (poller.py 의 load_config / DEFAULTS / _persist_* / check_* 계열의 Go 포트)
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Intervals 는 폴링 티어 주기(초)다.
// avcli 1콜이 실측 3.7~4.8초라 fast(node+alert 2콜)는 60초, slow(6~7콜)는 300초가
// 겹치지 않는 최소치다. license 같은 static 은 하루 1회면 충분하다.
type Intervals struct {
	Fast   int `json:"fast"`
	Slow   int `json:"slow"`
	Static int `json:"static"`
	OS     int `json:"os"`
	SNMP   int `json:"snmp"`
}

// TrapConfig 는 SNMP 트랩 수신기 설정이다.
// 기본 포트가 10162 인 이유: udp/162 는 1024 미만 특권 포트라 non-root 폴러가 못 여는
// 호스트가 있다(ip_unprivileged_port_start=1024). 권한이 있으면 162 로 둔다.
// Community 가 nil 이면 모든 community 를 허용한다(poller.py 계약).
type TrapConfig struct {
	Enabled   bool    `json:"enabled"`
	Bind      string  `json:"bind"`
	Port      int     `json:"port"`
	Community *string `json:"community"`
	Persist   string  `json:"persist"`  // runtime_dir 기준 상대경로
	Ring      int     `json:"ring"`     // 파일 링버퍼 크기(전 클러스터 공통)
	ViewMax   int     `json:"view_max"` // meta.traps[] 로 노출할 클러스터별 최근 건수
	MibDir    string  `json:"mib_dir"`  // OID→이름 매핑용 MIB 디렉터리
}

// NodeConfig 는 FT 클러스터의 물리 노드 SSH 접속 정보다.
// 관리 API(POST /api/clusters)로 추가된 항목은 root_user 대신 ssh_user 키로
// 기록되므로 Unmarshal 시 두 키를 모두 받아 RootUser 로 통일한다.
type NodeConfig struct {
	Name         string `json:"name"`
	IP           string `json:"ip"`
	RootUser     string `json:"root_user,omitempty"`
	RootPassword string `json:"root_password,omitempty"`
}

// UnmarshalJSON 은 root_user / ssh_user 두 키를 모두 허용한다.
func (n *NodeConfig) UnmarshalJSON(data []byte) error {
	type alias NodeConfig
	var aux struct {
		alias
		SSHUser string `json:"ssh_user"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*n = NodeConfig(aux.alias)
	if n.RootUser == "" {
		n.RootUser = aux.SSHUser
	}
	return nil
}

// ClusterConfig 는 everRun / ztC Edge FT 클러스터 하나의 설정이다.
// TzOffsetSecs 가 nil 이면 alert-info 의 naive 로컬시각 보정을 노드 SSH 자동 판별에
// 맡긴다(poller.py 의 tz_offset_secs=null 계약). SSH 를 못 쓰는 환경에서는
// KST=32400 처럼 명시한다.
type ClusterConfig struct {
	Key              string                       `json:"key"`
	Name             string                       `json:"name,omitempty"`
	MgmtIP           string                       `json:"mgmt_ip"`
	AdminUser        string                       `json:"admin_user,omitempty"`
	AdminPassword    string                       `json:"admin_password,omitempty"`
	NodeRootPassword string                       `json:"node_root_password,omitempty"`
	SNMPCommunity    string                       `json:"snmp_community,omitempty"`
	SNMPEnabled      bool                         `json:"snmp_enabled"`
	Platform         string                       `json:"platform,omitempty"` // 생략 시 node-info manufacturer/model 로 자동 판별
	TzOffsetSecs     *int64                       `json:"tz_offset_secs"`
	Site             string                       `json:"site,omitempty"`
	Company          string                       `json:"company,omitempty"`
	Factory          string                       `json:"factory,omitempty"`
	NicNetworkMap    map[string]map[string]string `json:"nic_network_map,omitempty"` // node -> if -> shared-network
	Intervals        Intervals                    `json:"intervals"`                 // 로드 시 최상위 값이 병합된 확정값
	HistoryPoints    int                          `json:"history_points,omitempty"`
	SSHTimeout       int                          `json:"ssh_timeout,omitempty"`
	Nodes            []NodeConfig                 `json:"nodes,omitempty"`

	// present / intervalsRaw 는 파일에 명시된 키만 기록해 Load 가 상속 여부를
	// 판별하는 데 쓴다. snmp_enabled=false 를 명시한 클러스터와 아예 생략한
	// 클러스터를 구분하기 위해 필요하다(제로값 구분 불가 문제).
	present      map[string]bool
	intervalsRaw json.RawMessage
}

// UnmarshalJSON 은 명시된 키 집합을 함께 기록한다(상속 판별용).
func (c *ClusterConfig) UnmarshalJSON(data []byte) error {
	type alias ClusterConfig
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*c = ClusterConfig(a)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	c.present = make(map[string]bool, len(raw))
	for k := range raw {
		c.present[k] = true
	}
	c.intervalsRaw = raw["intervals"]
	return nil
}

// EdgeDevice 는 비-FT 엣지 장비 설정이다. Kind(printer|nas|plc|proxmox|server)가
// 판별자이며, 관리 API 바디의 "type" 키도 별칭으로 받는다. 모르는 키(_note 등)는
// encoding/json 기본 동작으로 무시된다 — 원본 보존은 Store 의 RawMessage RMW 가
// 담당하므로 이 구조체는 읽기 전용 뷰다.
type EdgeDevice struct {
	Key  string `json:"key"`
	Kind string `json:"kind"` // printer | nas | plc | proxmox | server

	// 표시 메타(수집 동작에 영향 없음, /api/devices 그룹핑 전용)
	Name     string `json:"name,omitempty"`
	Company  string `json:"company,omitempty"`
	Factory  string `json:"factory,omitempty"`
	Site     string `json:"site,omitempty"`
	AssetTag string `json:"asset_tag,omitempty"`
	FloorPos string `json:"floor_pos,omitempty"`
	Vendor   string `json:"vendor,omitempty"`

	// 공통 대상
	IP        string `json:"ip,omitempty"`
	Community string `json:"community,omitempty"` // printer/nas/server 의 SNMP

	// printer: 웹 관리자 계정(선택)
	WebUser     string `json:"web_user,omitempty"`
	WebPassword string `json:"web_password,omitempty"`

	// nas: 보조 NIC
	ExtraIPs []string `json:"extra_ips,omitempty"`

	// plc: 옴론 FINS/UDP (읽기전용)
	FinsPort    int `json:"fins_port,omitempty"`
	FinsSrcNode int `json:"fins_src_node,omitempty"`

	// proxmox: PVE HTTPS API (읽기전용)
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`

	// server: Redfish BMC — community(OS SNMP) 와 둘 중 하나 이상 필요
	BmcIP       string `json:"bmc_ip,omitempty"`
	BmcUser     string `json:"bmc_user,omitempty"`
	BmcPassword string `json:"bmc_password,omitempty"`
}

// UnmarshalJSON 은 kind / type 두 판별 키를 모두 허용한다.
func (d *EdgeDevice) UnmarshalJSON(data []byte) error {
	type alias EdgeDevice
	var aux struct {
		alias
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*d = EdgeDevice(aux.alias)
	if d.Kind == "" {
		d.Kind = aux.Type
	}
	return nil
}

// Config 는 config.local.json 의 최상위 구조다. Load 가 poller.py 의 DEFAULTS 와
// 같은 기본값을 채우고 클러스터 상속까지 확정한 뒤 반환한다.
// 이 구조체를 통째로 재직렬화해 저장하면 기본값·파생값이 파일에 박제되므로,
// 저장은 반드시 Store(원본 RawMessage RMW)를 통해야 한다.
// Thresholds 는 사용률 경보 임계값(%)이다. 파일에 없으면 78/90 으로 채운다.
// 부분 지정(한쪽만)은 오설정 방지로 에러다.
type Thresholds struct {
	Warn float64 `json:"warn"`
	Crit float64 `json:"crit"`
}

type Config struct {
	Listen             string          `json:"listen"`
	LogLevel           string          `json:"log_level"`
	AvcliBin           string          `json:"avcli_bin"`
	AvcliTimeout       int             `json:"avcli_timeout"`  // 초, 기본 90 — avcli 1콜 실측 15~40초의 상한
	HistoryPoints      int             `json:"history_points"` // 기본 120
	CacheRefresh       int             `json:"cache_refresh"`  // 초, 기본 5
	RuntimeDir         string          `json:"runtime_dir"`
	SSHTimeout         int             `json:"ssh_timeout"` // 초, 기본 20
	SNMPEnabled        bool            `json:"snmp_enabled"`
	SNMPCommunity      string          `json:"snmp_community,omitempty"`
	SimDevices         int             `json:"sim_devices"` // 0 이면 시뮬 끔(실장비만)
	SimSeed            int64           `json:"sim_seed"`
	Intervals          Intervals       `json:"intervals"`
	Thresholds         Thresholds      `json:"thresholds"` // warn/crit 사용률 임계값 %, 기본 78/90
	Trap               TrapConfig      `json:"-"`                    // 스키마는 평면 trap_* 키(및 관용적으로 중첩 trap 객체) — Load 가 수동 해석
	CORSAllowedOrigins []string        `json:"cors_allowed_origins"` // 비어 있으면 ACAO 미부여(드라이브바이 인벤토리 유출 방지)
	HTTPTimeout        int             `json:"http_timeout"`         // 초, 기본 30 — 유휴/slowloris 차단
	Clusters           []ClusterConfig `json:"clusters"`
	EdgeDevices        []EdgeDevice    `json:"edge_devices"`

	// Path 는 로드한 파일 경로다. Store 를 같은 파일에 대고 열 때 쓴다.
	Path string `json:"-"`
}

// trapOverlay 는 평면 trap_* 키와 중첩 trap 객체를 같은 우선순위 규칙으로
// 합치기 위한 포인터 중간형이다(명시된 키만 덮어쓰기 위해 전부 포인터).
type trapOverlay struct {
	Enabled   *bool   `json:"enabled"`
	Bind      *string `json:"bind"`
	Port      *int    `json:"port"`
	Community *string `json:"community"`
	Persist   *string `json:"persist"`
	Ring      *int    `json:"ring"`
	ViewMax   *int    `json:"view_max"`
	MibDir    *string `json:"mib_dir"`
}

func (o *trapOverlay) apply(t *TrapConfig) {
	if o.Enabled != nil {
		t.Enabled = *o.Enabled
	}
	if o.Bind != nil {
		t.Bind = *o.Bind
	}
	if o.Port != nil {
		t.Port = *o.Port
	}
	if o.Community != nil {
		t.Community = o.Community
	}
	if o.Persist != nil {
		t.Persist = *o.Persist
	}
	if o.Ring != nil {
		t.Ring = *o.Ring
	}
	if o.ViewMax != nil {
		t.ViewMax = *o.ViewMax
	}
	if o.MibDir != nil {
		t.MibDir = *o.MibDir
	}
}

// mergeIntervals 는 base 위에 raw JSON 객체에 명시된 키만 덮어쓴다.
// poller.py 의 `merged = dict(DEFAULTS["intervals"]); merged.update(...)` 와 동일.
func mergeIntervals(base Intervals, raw json.RawMessage) Intervals {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return base
	}
	out := base
	put := func(key string, dst *int) {
		if v, ok := m[key]; ok {
			_ = json.Unmarshal(v, dst)
		}
	}
	put("fast", &out.Fast)
	put("slow", &out.Slow)
	put("static", &out.Static)
	put("os", &out.OS)
	put("snmp", &out.SNMP)
	return out
}

// Parse 는 config JSON 바이트를 해석하고 poller.py load_config 와 같은
// 기본값 채우기·큟스터 상속·자격증명 마스킹 등록까지 수행한다.
// clusters 가 비었거나 key/mgmt_ip 가 없으면 에러(poller.py 는 SystemExit).
func Parse(data []byte) (*Config, error) {
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config JSON 파싱 실패: %w", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("config JSON 파싱 실패: %w", err)
	}

	// --- 최상위 기본값(poller.py DEFAULTS 와 동일) ---
	if _, ok := top["listen"]; !ok {
		c.Listen = "0.0.0.0:9891"
	}
	if _, ok := top["log_level"]; !ok {
		c.LogLevel = "info"
	}
	if _, ok := top["avcli_bin"]; !ok {
		c.AvcliBin = "avcli"
	}
	if _, ok := top["avcli_timeout"]; !ok {
		c.AvcliTimeout = 90
	}
	if _, ok := top["history_points"]; !ok {
		c.HistoryPoints = 120
	}
	if _, ok := top["cache_refresh"]; !ok {
		c.CacheRefresh = 5
	}
	if _, ok := top["runtime_dir"]; !ok {
		c.RuntimeDir = "~/.everrun-poller"
	}
	if _, ok := top["ssh_timeout"]; !ok {
		c.SSHTimeout = 20
	}
	if _, ok := top["snmp_enabled"]; !ok {
		c.SNMPEnabled = true
	}
	if _, ok := top["sim_devices"]; !ok {
		c.SimDevices = 50
	}
	if _, ok := top["thresholds"]; !ok {
		c.Thresholds = Thresholds{Warn: 78, Crit: 90}
	} else if !(c.Thresholds.Warn > 0 && c.Thresholds.Warn < c.Thresholds.Crit && c.Thresholds.Crit <= 100) {
		return nil, fmt.Errorf("thresholds: 0 < warn < crit <= 100 이어야 합니다")
	}
	if _, ok := top["sim_seed"]; !ok {
		c.SimSeed = 20260720
	}
	if _, ok := top["http_timeout"]; !ok {
		c.HTTPTimeout = 30
	}
	if _, ok := top["cors_allowed_origins"]; !ok {
		c.CORSAllowedOrigins = []string{}
	}

	// --- intervals: 기본값 위에 파일에 명시된 키만 병합 ---
	base := Intervals{Fast: 60, Slow: 300, Static: 86400, OS: 10, SNMP: 60}
	if raw, ok := top["intervals"]; ok {
		base = mergeIntervals(base, raw)
	}
	c.Intervals = base

	// --- trap: 기본값 ← 평면 trap_* 키 ← 중첩 trap 객체 순으로 병합 ---
	c.Trap = TrapConfig{
		Enabled: true, Bind: "0.0.0.0", Port: 10162,
		Persist: "traps.jsonl", Ring: 500, ViewMax: 50, MibDir: "docs/mibs",
	}
	var flat trapOverlay
	if raw, ok := top["trap_enabled"]; ok {
		var v bool
		if json.Unmarshal(raw, &v) == nil {
			flat.Enabled = &v
		}
	}
	if raw, ok := top["trap_bind"]; ok {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			flat.Bind = &v
		}
	}
	if raw, ok := top["trap_port"]; ok {
		var v int
		if json.Unmarshal(raw, &v) == nil {
			flat.Port = &v
		}
	}
	if raw, ok := top["trap_community"]; ok {
		var v *string // 명시적 null 도 "전부 허용" 으로 기본값과 같다
		if json.Unmarshal(raw, &v) == nil {
			flat.Community = v
		}
	}
	if raw, ok := top["trap_persist"]; ok {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			flat.Persist = &v
		}
	}
	if raw, ok := top["trap_ring"]; ok {
		var v int
		if json.Unmarshal(raw, &v) == nil {
			flat.Ring = &v
		}
	}
	if raw, ok := top["trap_view_max"]; ok {
		var v int
		if json.Unmarshal(raw, &v) == nil {
			flat.ViewMax = &v
		}
	}
	if raw, ok := top["trap_mib_dir"]; ok {
		var v string
		if json.Unmarshal(raw, &v) == nil {
			flat.MibDir = &v
		}
	}
	flat.apply(&c.Trap)
	if raw, ok := top["trap"]; ok {
		var nested trapOverlay
		if err := json.Unmarshal(raw, &nested); err == nil {
			nested.apply(&c.Trap)
		}
	}

	// --- 클러스터 검증 + 최상위 값 상속(poller.py load_config 와 동일 규칙) ---
	if len(c.Clusters) == 0 {
		return nil, errors.New("config error: clusters 가 비어 있습니다")
	}
	for i := range c.Clusters {
		cl := &c.Clusters[i]
		if cl.Key == "" || cl.MgmtIP == "" {
			return nil, errors.New("config error: 각 cluster 는 key, mgmt_ip 가 필요합니다")
		}
		if cl.AdminUser == "" {
			cl.AdminUser = "admin"
		}
		if !cl.present["history_points"] {
			cl.HistoryPoints = c.HistoryPoints
		}
		if !cl.present["snmp_community"] {
			cl.SNMPCommunity = c.SNMPCommunity
			if cl.SNMPCommunity == "" {
				cl.SNMPCommunity = "public"
			}
		}
		if !cl.present["snmp_enabled"] {
			cl.SNMPEnabled = c.SNMPEnabled
		}
		if !cl.present["ssh_timeout"] {
			cl.SSHTimeout = c.SSHTimeout
		}
		iv := c.Intervals
		if cl.present["intervals"] && len(cl.intervalsRaw) > 0 {
			iv = mergeIntervals(iv, cl.intervalsRaw)
		}
		cl.Intervals = iv

		// 자격증명은 설정 파일에서만 읽는다(코드 하드코딩 금지). 전부 마스킹 대상.
		RegisterSecret(cl.AdminPassword)
		RegisterSecret(cl.NodeRootPassword)
		for _, n := range cl.Nodes {
			RegisterSecret(n.RootPassword)
		}
	}

	// 엣지 자격증명도 로드 시점에 등록한다. poller.py 는 API 추가 경로에서만
	// 등록해 재시작 후 파일에서 읽은 값은 마스킹이 빠지는 구멍이 있었다 — 여기서 닫는다.
	for _, d := range c.EdgeDevices {
		RegisterSecret(d.WebPassword)
		RegisterSecret(d.Password)
		RegisterSecret(d.BmcPassword)
	}
	// 사설 trap community 도 유출 방지 대상(poller.py start_trap_receiver 와 동일 판단).
	if c.Trap.Community != nil {
		RegisterSecret(*c.Trap.Community)
	}
	return &c, nil
}

// Load 는 path 의 JSON 파일을 읽어 Parse 하고, 파일 경로를 Config.Path 에 기록한다.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config 읽기 실패: %w", err)
	}
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	c.Path = path
	return c, nil
}
