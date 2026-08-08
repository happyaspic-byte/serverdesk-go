package sshmetrics

// 이 파일의 struct 들은 Python 폴리 serverdesk(everrun-poller) 의 os-metrics dict 를
// 그대로 미러링한다. JSON 키 이름·null 의미까지 기존 프런트/DB 계약이므로 바꾸지 않는다.
//
// Python dict 는 "섹션이 없으면 키 자체가 없고, 계산 불가면 None" 이라서
// Go 에서는 포인터 + omitempty 로 옮긴다: nil 포인터 = 키 없음/None.
// 0 이나 false 가 유효한 값인 필드(예: mem_pct=0, recently_booted=false)를
// "없음" 과 구분하려고 값 타입을 쓰지 않는다.

// Metrics 는 노드 1회 수집 결과(파싱 + 직전 샘플과의 델타)다.
// SSH 가 실패하면 Collect 가 error 를 반환하고 Metrics 를 만들지 않는다 —
// 몇 시간 전 값이 '현재값'인 척 노출되는 사고를 막기 위한 Python 과 같은 규약이다.
type Metrics struct {
	// TS 는 노드에서 잰 시각(date +%s). T= 행이 없거나 0 이면 폴 시각으로 대체한다.
	// 델타의 dt 도 노드 시각끼리 계산하므로 폴-노드 간 시계 오차와 무관하다.
	TS int64 `json:"ts"`
	// RawSections 는 수신된 @섹션 이름 목록(정렬됨). 수집 누락 디버깅용이다.
	RawSections []string `json:"raw_sections"`
	// IP 는 Collect 의 host 인자다. 호출자가 결과를 노드별로 키잉할 때 쓴다.
	IP string `json:"ip"`

	// CPUPct 는 /proc/stat jiffies 폴 간 델타로 계산한 사용률이다(순간값 아님).
	// 첫 샘플은 델타 기준이 없어 반드시 null 이다 — 0% 로 보고하면 안 된다.
	CPUPct *float64 `json:"cpu_pct"`

	Ctxt         *int64 `json:"ctxt,omitempty"`
	ProcsRunning *int64 `json:"procs_running,omitempty"`
	ProcsBlocked *int64 `json:"procs_blocked,omitempty"`

	// Load 는 [1분, 5분, 15분] 로드다. /proc/loadavg 첫 행만 쓴다(Python 동일).
	Load []float64 `json:"load,omitempty"`

	UptimeSecs *int64   `json:"uptime_secs,omitempty"`
	UptimeDays *float64 `json:"uptime_days,omitempty"`

	MemTotalBytes     *int64   `json:"mem_total_bytes,omitempty"`
	MemAvailableBytes *int64   `json:"mem_available_bytes,omitempty"`
	MemUsedBytes      *int64   `json:"mem_used_bytes,omitempty"`
	MemPct            *float64 `json:"mem_pct,omitempty"`
	SwapTotalBytes    *int64   `json:"swap_total_bytes,omitempty"`
	SwapUsedBytes     *int64   `json:"swap_used_bytes,omitempty"`
	SwapPct           *float64 `json:"swap_pct,omitempty"`
	// DirtyBytes 는 쓰기 지연량이다. 디스크 정체(백업 창 등) 진단에 쓴다.
	DirtyBytes *int64 `json:"dirty_bytes,omitempty"`

	// Filesystems 은 tmpfs/devtmpfs/squashfs/iso9660/udf 를 제외한 df 결과다.
	// loop 마운트 ISO 는 항상 100% 라 넣으면 디스크 부족 오탐이 난다(원격에서 제외됨).
	Filesystems []Disk `json:"filesystems,omitempty"`
	FSMaxPct    *int64 `json:"fs_max_pct,omitempty"`

	// Net/DiskIO 는 누적 카운터가 아니라 "폴 간 증분" 이다. 첫 샘플에는 없다 —
	// 누적 drop 이 이미 수천만 건인 노드에서 절대값을 보여주면 매번 경보가 된다.
	Net               []NetRate `json:"net,omitempty"`
	InterconnectDrops *int64    `json:"interconnect_drops,omitempty"`
	DiskIO            []DiskIO  `json:"diskio,omitempty"`

	// Spine 은 NIC↔shared-network 의 확정 매핑(설정 파일)이다. 메트릭과 수명이
	// 다르다(SSH 가 끊겨도 설정은 유효)는 점에 주의 — 호출자가 별도 보관할 수 있다.
	Spine *Spine `json:"spine,omitempty"`

	Temps    []Temp   `json:"temps,omitempty"`
	TempMaxC *float64 `json:"temp_max_c,omitempty"`
	Links    []Link   `json:"links,omitempty"`

	MDStat     []string `json:"mdstat,omitempty"`
	MDDegraded *bool    `json:"md_degraded,omitempty"`
	DRBD       []string `json:"drbd,omitempty"`
	// DRBDUpToDate 는 모든 리소스가 UpToDate/UpToDate 일 때만 true 다.
	DRBDUpToDate *bool `json:"drbd_uptodate,omitempty"`

	// TZOffsetSecs/TZName 은 노드 타임존이다. avcli 의 alert 시각은 TZ 없는 노드
	// 로컬시각이라 UTC 환산에 이 오프셋이 꼭 필요하다(없으면 epoch 이 9시간 어긋난다).
	TZOffsetSecs *int64 `json:"tz_offset_secs,omitempty"`
	TZName       string `json:"tz_name,omitempty"`

	RunningDomains []string `json:"running_domains,omitempty"`
	// OSPlatform 은 "ztc" 또는 "everrun" (/etc/opt/ft/is_ztc 존재 여부).
	OSPlatform string `json:"os_platform,omitempty"`

	// RebootedAt/RebootAgoSecs 는 uptime 이 뒤로 감긴 게 감지된 뒤 24시간 동안만
	// 채워진다(오래된 리부트를 영구히 띄우지 않으려고).
	RebootedAt    *int64 `json:"rebooted_at,omitempty"`
	RebootAgoSecs *int64 `json:"reboot_ago_secs,omitempty"`
	// RecentlyBooted 는 uptime<1h 여부다. false 도 유효값이라 포인터다.
	RecentlyBooted *bool `json:"recently_booted,omitempty"`
}

// Disk 는 df -PB1 한 행이다(1바이트 블록).
type Disk struct {
	Device     string `json:"device"`
	Mount      string `json:"mount"`
	SizeBytes  int64  `json:"size_bytes"`
	UsedBytes  int64  `json:"used_bytes"`
	AvailBytes int64  `json:"avail_bytes"`
	// UsedPct 는 df 의 % 컬럼이다. 파싱 실패 시 nil(0% 와 구분).
	UsedPct *int64 `json:"used_pct"`
}

// NetRate 는 NIC 하나의 폴 간 증분이다. 절대 누적값은 넣지 않는다.
type NetRate struct {
	Name     string `json:"name"`
	GuestTap bool   `json:"guest_tap"`
	// Interconnect 는 A-Link(노드 간 사설 interconnect) 여부다. 이름이 아니라
	// spine 확정 role 로 판정한다 — everRun 의 A-Link 네트워크 이름은 임의라
	// (실장비: priv0, net_82) 이름 규칙만 쓰면 net_82 를 나르는 p38p2 를 놓쳐
	// 인터커넥트 드롭을 과소 보고한다.
	Interconnect bool `json:"interconnect"`
	// InterconnectEvidence 는 판정 근거다: "spine-config" | "name-heuristic".
	InterconnectEvidence string `json:"interconnect_evidence"`

	// bps 는 rate() 가 먼저 소수 1자리로 반올림된 뒤 ×8 된 값이다(Python 동일).
	// 카운터 리셋(랩/재부팅)이면 rate 는 nil → 0 으로 둔다(음수 속도 방지).
	RxBps *float64 `json:"rx_bps,omitempty"`
	TxBps *float64 `json:"tx_bps,omitempty"`

	RxDropDelta *int64 `json:"rx_drop_delta,omitempty"`
	TxDropDelta *int64 `json:"tx_drop_delta,omitempty"`
	RxErrDelta  *int64 `json:"rx_err_delta,omitempty"`
	TxErrDelta  *int64 `json:"tx_err_delta,omitempty"`
}

// DiskIO 는 디스크 하나의 폴 간 증분이다.
type DiskIO struct {
	Name string `json:"name"`
	// ReadBps/WriteBps 는 섹터 증분 ×512 다.
	ReadBps  *float64 `json:"read_bps,omitempty"`
	WriteBps *float64 `json:"write_bps,omitempty"`
	// BusyPct 는 io_ms 증분/(dt×1000)×100 을 0..100 으로 클램프한 값이다.
	BusyPct *float64 `json:"busy_pct,omitempty"`
}

// Temp 는 hwmon 온도 하나다. 원격은 밀리도(milli-℃)를 주고 여기서 ℃로 환산한다.
type Temp struct {
	Chip    string  `json:"chip"`
	Label   string  `json:"label"`
	Celsius float64 `json:"celsius"`
}

// Link 는 @link 의 파이프 고정 5필드다: 이름|operstate|speed|phy/virt|mtu.
// 공백 구분이면 speed 가 비었을 때(캐리어 없는 NIC/브리지/본드에서 흔함) 필드가
// 밀려 phy/virt 마커가 speed 자리로 들어가 물리 판별이 조용히 틀어진다 — 그래서
// 마커가 없으면 이름으로 추측하지 않고 Physical 을 nil 로 둔다.
type Link struct {
	Name  string  `json:"name"`
	State *string `json:"state"`
	// SpeedMbps 는 커널이 캐리어 없는 NIC 에 주는 -1 을 nil 로 정규화한다
	// ("-1 Mbps" 가 프런트에 찍히는 걸 막기 위해).
	SpeedMbps *int64 `json:"speed_mbps"`
	MTU       *int64 `json:"mtu"`
	Up        bool   `json:"up"`
	// Physical 은 /sys/class/net/<if>/device 존재 여부다(실제 PCI 디바이스만 존재).
	// 미상이면 nil — 브리지를 physical=true 로 오분류했던 구 스크립트 버그 방지.
	Physical *bool `json:"physical"`
	GuestTap bool  `json:"guest_tap"`
}

// SpineNetwork 는 /etc/opt/ft/spine/networks/<키>/ 아래 shared-network 정의다.
type SpineNetwork struct {
	Name string `json:"name"`
	// Role 은 "a-link" | "business" 다(ALINK/A-LINK/PRIVATE→a-link,
	// BUSINESS/MANAGEMENT→business). 미지의 role 은 소문자 그대로 통과시킨다.
	Role    *string `json:"role"`
	Ordinal *int64  `json:"ordinal"`
	MTU     *int64  `json:"mtu"`
	Bridge  *string `json:"bridge"`
}

// Spine 은 NIC↔shared-network 의 확정 매핑이다. avcli 에는 이 매핑을 주는 명령이
// 없어 /etc/opt/ft/spine 파일이 유일한 확정 소스다.
type Spine struct {
	// SelfUUID 는 /etc/opt/ft/node-config-uuid 이다. 노드 디렉터리는 클러스터
	// 전체 노드 것을 담고 있어 로컬 노드를 이 값으로 골라낸다.
	SelfUUID *string `json:"self_uuid"`
	// Networks 는 이름 정렬된 shared-network 목록이다(uuid 디렉터리와 name
	// 디렉터리에 중복 저장된 정의를 name 으로 통합한 결과).
	Networks []SpineNetwork `json:"networks"`
	// NICNetworks/NICRoles 는 물리 NIC 이름 → 네트워크 이름/role 이다.
	// parent_uuid 가 알려진 네트워크를 가리키지 않으면 값이 nil 이다.
	NICNetworks map[string]*string `json:"nic_networks"`
	NICRoles    map[string]*string `json:"nic_roles"`
}

// --- 델타 계산용 원시 누적값(뷰에는 노출하지 않는다) ---

// rawSample 은 다음 폴의 델타 기준이다. Python 의 cur_sample 에 해당한다.
// JSON 으로 나가면 안 되므로 전부 비공개다.
type rawSample struct {
	ts         int64
	cpuJiffies []int64
	netRaw     map[string]nicCounters
	diskRaw    map[string]diskCounters
}

// nicCounters 는 /proc/net/dev 의 누적 카운터다.
type nicCounters struct {
	rxBytes, rxPackets, rxErrs, rxDrop int64
	txBytes, txPackets, txErrs, txDrop int64
	guestTap                           bool
	interconnect                       bool
	interconnectEvidence               string
}

// diskCounters 는 /proc/diskstats 의 누적 카운터다.
type diskCounters struct {
	reads, readSectors   int64
	writes, writeSectors int64
	ioMS                 int64
}
