package topology

// ---------------------------------------------------------------------------
// 입력 계약 (정규화된 클러스터 데이터)
// ---------------------------------------------------------------------------
// everrun-poller docs/topology-model.md §2 의 입력 계약을 Go 타입으로 옮긴 것.
// 모든 슬라이스/맵/포인터는 nil 이어도 된다 — 누락된 계층은 그래프에서 비워진다.

// ClusterInput 은 BuildClusterTopology 가 받는 클러스터 1대분의 정규화 입력이다.
// ClusterID 는 그래프 id 접두어가 되므로 클러스터마다 유일해야 한다
// (avcli id 시퀀스는 클러스터마다 겹친다).
type ClusterInput struct {
	ClusterID       string                    `json:"cluster_id"`
	Platform        string                    `json:"platform"` // "everrun" | "ztcedge" (빈 값이면 everrun 취급)
	Site            *SiteRef                  `json:"site"`
	Unit            UnitInput                 `json:"unit"`
	Nodes           []NodeInput               `json:"nodes"`
	Networks        []NetworkInput            `json:"networks"`
	StorageGroups   []StorageGroupInput       `json:"storage_groups"`
	Volumes         []VolumeInfoInput         `json:"volumes"`          // volume-info 독립 볼륨 (시스템 볼륨 포함)
	ImageContainers []ImageContainerInput     `json:"image_containers"` // 실사용량
	VMs             []VMInput                 `json:"vms"`
	Alerts          []AlertInput              `json:"alerts"`
	License         *LicenseInput             `json:"license"`
	NodeMetrics     map[string]*NodeOSMetrics `json:"node_metrics"`    // SSH/collectd 보강 (노드 이름 -> 메트릭)
	NICNetworkMap   NICNetworkMap             `json:"nic_network_map"` // 노드 spine 설정에서 읽은 확정 매핑 (최우선)
}

// SiteRef 는 클러스터가 속한 사이트다. fleet 조립 시 level 1 계층을 만든다.
type SiteRef struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// UnitInput 은 avcli unit-info 이다. 메모리는 "13.95 GiB" 같은 원문 라벨
// 문자열 계약이다 (ParseSize 로 내부에서 변환한다).
type UnitInput struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Version     string `json:"version"`
	UUID        string `json:"uuid"`
	Address     string `json:"address"`
	Netmask     string `json:"netmask"`
	Configured  *bool  `json:"configured"`
	Syncing     bool   `json:"syncing"`
	TotalVCPUs  string `json:"total_vcpus"`
	UsedVCPUs   string `json:"used_vcpus"`
	TotalMemory string `json:"total_memory"`
	UsedMemory  string `json:"used_memory"`
}

// NodeInput 은 avcli node-info 의 물리 노드다.
type NodeInput struct {
	Name          string   `json:"name"`
	ID            string   `json:"id"`
	State         string   `json:"state"`
	SubState      string   `json:"sub_state"`
	StandingState string   `json:"standing_state"`
	Mode          string   `json:"mode"`
	Primary       bool     `json:"primary"`
	Manufacturer  string   `json:"manufacturer"`
	Model         string   `json:"model"`
	CPUs          string   `json:"cpus"`
	Memory        string   `json:"memory"` // 원문 라벨 ("15.95 GiB")
	IPAddress     string   `json:"ip_address"`
	Gateway       string   `json:"gateway"`
	DNS           []string `json:"dns"`
}

// NetworkInput 은 avcli network-info 의 shared-network 다.
type NetworkInput struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	FaultTolerant string `json:"fault_tolerant"`
	Role          string `json:"role"` // a-link | business | private | management
	Bandwidth     string `json:"bandwidth"`
	MTU           *int64 `json:"mtu"`
}

// StorageGroupInput 은 avcli storage-info-v2 의 스토리지 그룹이다.
// size/used 는 이 계약에서만 미리 bytes 로 변환해서 넣는다.
type StorageGroupInput struct {
	Name               string      `json:"name"`
	ID                 string      `json:"id"`
	SizeBytes          *int64      `json:"size_bytes"`
	UsedBytes          *int64      `json:"used_bytes"`
	LogicalSectorSize  any         `json:"logical_sector_size"`  // 원본은 입력 경로에 따라 str/int 혼용
	PhysicalSectorSize any         `json:"physical_sector_size"` // 동일
	DiskType           string      `json:"disk_type"`
	Disks              []DiskInput `json:"disks"`
}

// DiskInput 은 노드별 논리 디스크(disk:oNNN)다. 물리 디스크가 아니다.
type DiskInput struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	SizeBytes     *int64 `json:"size_bytes"`
	UsedBytes     *int64 `json:"used_bytes"`
	StandingState string `json:"standing_state"`
	Node          string `json:"node"` // 노드 이름 문자열 (조인 키)
}

// SGRef 는 스토리지 그룹 참조다. id 로 조인해야 한다(이름은 중복될 수 있다).
type SGRef struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// VolumeInfoInput 은 avcli volume-info 의 독립 볼륨이다.
// VM 에 붙지 않은 시스템 볼륨(root/swap/diagdata 등)도 여기로 들어온다.
type VolumeInfoInput struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	Size         string `json:"size"` // 원문 라벨
	SectorSize   any    `json:"sector_size"`
	Bootable     *bool  `json:"bootable"`
	StorageGroup *SGRef `json:"storage_group"`
}

// ImageContainerInput 은 avcli image-container-info 의 이미지 컨테이너다(실사용량).
type ImageContainerInput struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	Size          string `json:"size"`
	SizeUsed      string `json:"size_used"`
	IsLocal       *bool  `json:"is_local"`
	HasFilesystem *bool  `json:"has_filesystem"`
	StorageGroup  *SGRef `json:"storage_group"`
}

// VMInput 은 avcli vm-info 이다. 그래프 간선 대부분이 여기서 나온다.
type VMInput struct {
	Name           string             `json:"name"`
	InternalName   string             `json:"internal_name"`
	ID             string             `json:"id"`
	UUID           string             `json:"uuid"`
	CPUs           string             `json:"cpus"`
	BootType       string             `json:"boot_type"`
	Memory         string             `json:"memory"` // 원문 라벨
	Type           string             `json:"type"`   // OS 종류
	State          string             `json:"state"`
	StandingState  string             `json:"standing_state"`
	FaultTolerant  string             `json:"fault_tolerant"` // "ft" | "ha"
	PlacementNodes []string           `json:"placement_nodes"`
	Interfaces     []VMInterfaceInput `json:"interfaces"`
	Volumes        []VMVolumeInput    `json:"volumes"`
	ALinks         []VMALinkInput     `json:"a_links"`
	Instances      []VMInstanceInput  `json:"instances"`
}

// VMInterfaceInput 은 VM 의 가상 NIC 이다. net0/net1 은 양쪽 노드 경로 상태다.
type VMInterfaceInput struct {
	SharedNetwork string `json:"shared_network"`
	MAC           string `json:"mac"`
	Net0Status    string `json:"net0_status"`
	Net1Status    string `json:"net1_status"`
}

// VMVolumeInput 은 VM 에 붙은 가상 볼륨이다. ID 가 없는 것(cdrom 등)은
// 그래프 노드를 만들지 않고 VM 메타의 removable_devices 로만 남는다.
type VMVolumeInput struct {
	Name       string           `json:"name"`
	ID         string           `json:"id"`
	Size       string           `json:"size"`
	SectorSize string           `json:"sector_size"`
	Device     string           `json:"device"`    // vda 등
	DeviceID   string           `json:"device_id"` // vbd:oNNN
	DiskImages []DiskImageInput `json:"disk_images"`
}

// DiskImageInput 은 볼륨의 노드별 미러 조각이다.
type DiskImageInput struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	EnableStatus string `json:"enable_status"`
	Node         string `json:"node"`
	NodeID       string `json:"node_id"`
}

// VMALinkInput 은 VM 의 a-link 점유다. XML 에서는 태그명이 곧 네트워크 이름인
// 동적 태그로 온다.
type VMALinkInput struct {
	Network   string `json:"network"`
	Role      string `json:"role"`
	Bandwidth string `json:"bandwidth"`
}

// VMInstanceInput 은 VM 의 노드별 로컬 인스턴스(local-virtual-machine)다.
type VMInstanceInput struct {
	Name           string `json:"name"`
	ID             string `json:"id"`
	EnableStatus   string `json:"enable_status"`
	ConfigVhostNet *bool  `json:"config_vhost_net"`
	MTBF           string `json:"mtbf"`
	UUID           string `json:"uuid"`
	Node           string `json:"node"`
	NodeID         string `json:"node_id"`
}

// AlertInput 은 avcli alert-info 의 알림 원문이다.
// Severity 는 avcli 원문 숫자 문자열("0"/"1"/"2")을 기대하지만,
// 분류는 키워드가 우선이다(숫자만으로는 신뢰 불가).
type AlertInput struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Time        string `json:"time"`
	Description string `json:"description"`
	Severity    string `json:"severity"`
}

// LicenseInput 은 avcli license-info 다. expire_date 는 expires=false 면
// 요소 자체가 없어 nil 로 온다(ztC Edge).
type LicenseInput struct {
	Name        string  `json:"name"`
	ID          string  `json:"id"`
	Type        string  `json:"type"`
	Edition     string  `json:"edition"`
	InstallDate string  `json:"install_date"`
	ExpireDate  *string `json:"expire_date"`
	Expires     *bool   `json:"expires"`
	Activated   *bool   `json:"activated"`
}

// NodeOSMetrics 는 SSH/collectd 로 보강하는 노드 실시간 메트릭이다.
// 없으면 nil 로 두고, 해당 필드는 그래프에서 null 로 나온다.
type NodeOSMetrics struct {
	Links    []LinkInput        `json:"links"`
	CPUPct   *float64           `json:"cpu_pct"`
	MemPct   *float64           `json:"mem_pct"`
	UptimeS  *float64           `json:"uptime_s"`
	TempsC   map[string]float64 `json:"temps_c"`
	TempMaxC *float64           `json:"temp_max_c"` // 모델은 사용하지 않지만 어댑터 계약 유지용
	Source   string             `json:"source"`
}

// LinkInput 은 /sys/class/net 기반 물리 링크 정보다.
type LinkInput struct {
	Name       string `json:"name"`
	OperState  string `json:"operstate"`
	Speed      *int64 `json:"speed"`    // Mbps
	MTU        *int64 `json:"mtu"`      // 역할 판별 보조(a-link 9000 / business 1500)
	Physical   *bool  `json:"physical"` // 참고용. 판정은 이름 휴리스틱이 한다
	RxErrors   *int64 `json:"rx_errors"`
	TxErrors   *int64 `json:"tx_errors"`
	DropsDelta *int64 `json:"drops_delta"`
}

// NICNetworkMap 은 노드 이름 -> 인터페이스 이름 -> 확정 매핑 이다.
// 폴러가 노드의 /etc/opt/ft/spine 설정을 SSH 로 읽어 채운다.
// 맵에 키가 있으면 "확정"이다: Network 가 nil 이면 '소속 네트워크 없음이 확정된
// 예비 포트', 키 자체가 없으면 '미상'이며, 다운돼도 unused 로 단정하지 않는다.
type NICNetworkMap map[string]map[string]NICMapping

// NICMapping 은 물리 NIC -> shared-network 확정 매핑 하나다.
type NICMapping struct {
	Network    *string  `json:"network"` // nil = 소속 네트워크 없음 확정
	Evidence   string   `json:"evidence"`
	Confidence *float64 `json:"confidence"`
}
