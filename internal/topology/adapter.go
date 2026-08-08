package topology

// ---------------------------------------------------------------------------
// 폴러 fleet 뷰 -> ClusterInput 어댑터 (topology_adapter.py 이식)
// ---------------------------------------------------------------------------
// 폴러 쪽 정규화(snake_case + *_bytes/*_raw)와 이 패키지의 입력 계약(avcli 원문에
// 가까운 문자열) 사이의 키 이름 차이를 여기서 흡수한다. 폴러 쪽 정규화를 되돌리는
// 게 아니라 입력 계약이 기대하는 이름으로 **덧붙여** 준다.

// ClusterView 는 폴러 build_cluster_view 가 만드는 클러스터 뷰다.
type ClusterView struct {
	Key             string               `json:"key"`
	Platform        string               `json:"platform"`
	Unit            UnitView             `json:"unit"`
	Nodes           []NodeView           `json:"nodes"`
	Networks        []NetworkView        `json:"networks"`
	StorageGroups   []StorageGroupView   `json:"storage_groups"`
	Volumes         []VolumeView         `json:"volumes"`
	ImageContainers []ImageContainerView `json:"image_containers"`
	VMs             []VMView             `json:"vms"`
	Alerts          []AlertView          `json:"alerts"`
	License         *LicenseInput        `json:"license"`
	NICNetworkMap   NICNetworkMap        `json:"nic_network_map"`
}

// UnitView 는 폴러 뷰의 unit 이다. 리소스 합계는 resources 아래에 있다.
type UnitView struct {
	Name       string        `json:"name"`
	ID         string        `json:"id"`
	Version    string        `json:"version"`
	UUID       string        `json:"uuid"`
	Address    string        `json:"address"`
	Netmask    string        `json:"netmask"`
	Configured *bool         `json:"configured"`
	Syncing    *bool         `json:"syncing"`
	Resources  ResourcesView `json:"resources"`
}

// ResourcesView 는 unit 의 합계 리소스다. 메모리는 원문 라벨(*_raw)을 넘긴다 —
// 입력 계약이 ParseSize 로 문자열을 먹기 때문이다.
type ResourcesView struct {
	TotalVCPUs     string `json:"total_vcpus"`
	UsedVCPUs      string `json:"used_vcpus"`
	TotalMemoryRaw string `json:"total_memory_raw"`
	UsedMemoryRaw  string `json:"used_memory_raw"`
}

// NodeView 는 폴러 뷰의 노드다. OS 실측(os)과 인덱싱된 VM 배치를 함께 가진다.
type NodeView struct {
	Name          string           `json:"name"`
	ID            string           `json:"id"`
	State         string           `json:"state"`
	SubState      string           `json:"sub_state"`
	StandingState string           `json:"standing_state"`
	Mode          string           `json:"mode"`
	Primary       *bool            `json:"primary"`
	Manufacturer  string           `json:"manufacturer"`
	Model         string           `json:"model"`
	CPUs          string           `json:"cpus"`
	MemoryRaw     string           `json:"memory_raw"` // 문자열 라벨 ("15.95 GiB")
	IP            string           `json:"ip"`
	Gateway       string           `json:"gateway"`
	DNS           []string         `json:"dns"`
	CPUPct        *float64         `json:"cpu_pct"`
	MemPct        *float64         `json:"mem_pct"`
	UptimeSecs    *float64         `json:"uptime_secs"`
	TempMaxC      *float64         `json:"temp_max_c"`
	VMPlacements  []VMPlacementRef `json:"vm_placements"`
	OS            *NodeOSView      `json:"os"`
}

// VMPlacementRef 는 노드에 배치된 VM 이름 하나다 (역인덱싱 원료).
type VMPlacementRef struct {
	Name string `json:"name"`
}

// NodeOSView 는 노드의 /proc + /sys 수집분이다.
type NodeOSView struct {
	Links  []LinkView   `json:"links"` // @link (operstate/speed)
	Net    []NetDevView `json:"net"`   // @net (에러·드롭 델타) — 이름으로 links 와 합친다
	Temps  []TempView   `json:"temps"`
	Source string       `json:"source"`
}

// LinkView 는 @link 섹션의 인터페이스 하나다.
type LinkView struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	SpeedMbps *int64 `json:"speed_mbps"`
	MTU       *int64 `json:"mtu"`
	Physical  *bool  `json:"physical"`
}

// NetDevView 는 @net 섹션의 인터페이스 카운터 델타다.
type NetDevView struct {
	Name        string `json:"name"`
	RxErrDelta  *int64 `json:"rx_err_delta"`
	TxErrDelta  *int64 `json:"tx_err_delta"`
	RxDropDelta *int64 `json:"rx_drop_delta"`
	TxDropDelta *int64 `json:"tx_drop_delta"`
}

// TempView 는 온도 센서 하나다. 맵 키는 "chip/label" (chip 없으면 label).
type TempView struct {
	Chip    string   `json:"chip"`
	Label   string   `json:"label"`
	Celsius *float64 `json:"celsius"`
}

// NetworkView 는 폴러 뷰의 shared-network 다.
type NetworkView struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	FaultTolerant string `json:"fault_tolerant"`
	Role          string `json:"role"`
	BandwidthRaw  string `json:"bandwidth_raw"`
	MTU           *int64 `json:"mtu"`
}

// StorageGroupView 는 폴러 뷰의 스토리지 그룹이다 (이미 bytes 로 정규화됨).
type StorageGroupView struct {
	Name                    string     `json:"name"`
	ID                      string     `json:"id"`
	SizeBytes               *int64     `json:"size_bytes"`
	UsedBytes               *int64     `json:"used_bytes"`
	LogicalSectorSizeBytes  *int64     `json:"logical_sector_size_bytes"`
	PhysicalSectorSizeBytes *int64     `json:"physical_sector_size_bytes"`
	DiskType                string     `json:"disk_type"`
	Disks                   []DiskView `json:"disks"`
}

// DiskView 는 폴러 뷰의 논리 디스크다.
type DiskView struct {
	Name          string `json:"name"`
	ID            string `json:"id"`
	SizeBytes     *int64 `json:"size_bytes"`
	UsedBytes     *int64 `json:"used_bytes"`
	StandingState string `json:"standing_state"`
	NodeName      string `json:"node_name"`
}

// VolumeView 는 폴러 뷰의 독립 볼륨이다 (volume-info 기반).
type VolumeView struct {
	Name             string `json:"name"`
	ID               string `json:"id"`
	SizeRaw          string `json:"size_raw"`
	SectorSizeBytes  *int64 `json:"sector_size_bytes"`
	Bootable         *bool  `json:"bootable"`
	StorageGroupName string `json:"storage_group_name"`
	StorageGroupID   string `json:"storage_group_id"`
}

// ImageContainerView 는 폴러 뷰의 이미지 컨테이너다.
type ImageContainerView struct {
	Name             string `json:"name"`
	ID               string `json:"id"`
	SizeRaw          string `json:"size_raw"`
	UsedRaw          string `json:"used_raw"`
	IsLocal          *bool  `json:"is_local"`
	HasFilesystem    *bool  `json:"has_filesystem"`
	StorageGroupName string `json:"storage_group_name"`
	StorageGroupID   string `json:"storage_group_id"`
}

// VMView 는 폴러 뷰의 VM 이다.
type VMView struct {
	Name          string             `json:"name"`
	InternalName  string             `json:"internal_name"`
	ID            string             `json:"id"`
	UUID          string             `json:"uuid"`
	CPUs          string             `json:"cpus"`
	BootType      string             `json:"boot_type"`
	MemoryRaw     string             `json:"memory_raw"`
	OSType        string             `json:"os_type"`
	State         string             `json:"state"`
	StandingState string             `json:"standing_state"`
	HAMode        string             `json:"ha_mode"`    // "ft" | "ha"
	Interfaces    []VMInterfaceInput `json:"interfaces"` // 키 이름 동일
	Volumes       []VMVolumeView     `json:"volumes"`
	ALinks        []VMALinkView      `json:"a_links"`
	Instances     []VMInstanceView   `json:"instances"`
}

// VMVolumeView 는 폴러 뷰의 VM 소속 볼륨이다.
type VMVolumeView struct {
	Name          string            `json:"name"`
	ID            string            `json:"id"`
	SizeRaw       string            `json:"size_raw"`
	SectorSizeRaw string            `json:"sector_size_raw"`
	Device        string            `json:"device"`
	DeviceID      string            `json:"device_id"`
	IsCdrom       *bool             `json:"is_cdrom"`
	DiskImages    []VMDiskImageView `json:"disk_images"`
}

// VMDiskImageView 는 폴러 뷰의 디스크 이미지다.
type VMDiskImageView struct {
	Name         string `json:"name"`
	ID           string `json:"id"`
	EnableStatus string `json:"enable_status"`
	NodeName     string `json:"node_name"`
	NodeID       string `json:"node_id"`
}

// VMALinkView 는 폴러 뷰의 VM a-link 점유다.
type VMALinkView struct {
	Network      string `json:"network"`
	Role         string `json:"role"`
	BandwidthRaw string `json:"bandwidth_raw"`
}

// VMInstanceView 는 폴러 뷰의 로컬 VM 인스턴스다.
type VMInstanceView struct {
	Name           string `json:"name"`
	ID             string `json:"id"`
	EnableStatus   string `json:"enable_status"`
	ConfigVhostNet *bool  `json:"config_vhost_net"`
	MTBFStatus     string `json:"mtbf_status"`
	UUID           string `json:"uuid"`
	NodeName       string `json:"node_name"`
	NodeID         string `json:"node_id"`
}

// AlertView 는 폴러 뷰의 알림이다. SeverityRaw 는 avcli 원문 숫자 문자열이다.
type AlertView struct {
	Name        string `json:"name"`
	ID          string `json:"id"`
	Time        string `json:"time"`
	Description string `json:"description"`
	SeverityRaw string `json:"severity_raw"`
}

// FleetInput 은 BuildFullTopology 의 입력이다. 폴러의 fleet 뷰 그대로에
// 클러스터별 사이트/Spine 매핑을 얹은 것이다.
type FleetInput struct {
	Clusters   []ClusterView            `json:"clusters"`
	Sites      map[string]*SiteRef      `json:"sites"`    // cluster key -> 사이트
	NICMaps    map[string]NICNetworkMap `json:"nic_maps"` // cluster key -> spine 매핑 (뷰의 것보다 우선)
	FleetLabel string                   `json:"fleet_label"`
}

// BuildFullTopology 는 폴러 fleet 뷰를 받아 어댑트 후 토폴로지 그래프를 만든다.
// 클러스터가 1대이고 사이트가 없으면 단일 클러스터 그래프(roots=클러스터 id),
// 그 외에는 fleet 루트로 통합한다.
func BuildFullTopology(fleet FleetInput) *FullTopology {
	clusters := AdaptFleet(fleet.Clusters, fleet.Sites, fleet.NICMaps)
	if len(clusters) == 1 && clusters[0].Site == nil {
		return BuildClusterTopology(clusters[0])
	}
	return BuildFleetTopology(clusters, fleet.FleetLabel)
}

// AdaptFleet 은 fleet 뷰 전체를 BuildFleetTopology 입력 슬라이스로 변환한다.
func AdaptFleet(views []ClusterView, sites map[string]*SiteRef, nicMaps map[string]NICNetworkMap) []ClusterInput {
	out := make([]ClusterInput, 0, len(views))
	for _, v := range views {
		out = append(out, AdaptCluster(v, sites[v.Key], nicMaps[v.Key]))
	}
	return out
}

// AdaptCluster 는 폴러 cluster 뷰 1개를 BuildClusterTopology 입력으로 변환한다.
func AdaptCluster(view ClusterView, site *SiteRef, nicNetworkMap NICNetworkMap) ClusterInput {
	nicMap := nicNetworkMap
	if len(nicMap) == 0 {
		nicMap = view.NICNetworkMap // 명시 인자가 비어 있으면 뷰의 것을 쓴다
	}
	return ClusterInput{
		ClusterID:       view.Key,
		Platform:        view.Platform,
		Site:            site,
		Unit:            adaptUnit(view),
		Nodes:           adaptNodes(view),
		Networks:        adaptNetworks(view),
		StorageGroups:   adaptStorageGroups(view),
		Volumes:         adaptVolumes(view),
		ImageContainers: adaptImageContainers(view),
		VMs:             adaptVMs(view),
		Alerts:          adaptAlerts(view),
		License:         view.License,
		NodeMetrics:     adaptNodeMetrics(view),
		NICNetworkMap:   nicMap,
	}
}

// adaptUnit 은 unit 의 resources 합계를 계약 필드로 펼친다.
// 메모리는 원문 라벨을 준다(입력 계약이 ParseSize 로 문자열을 먹는다).
func adaptUnit(view ClusterView) UnitInput {
	u := view.Unit
	return UnitInput{
		Name:        u.Name,
		ID:          u.ID,
		Version:     u.Version,
		UUID:        u.UUID,
		Address:     u.Address,
		Netmask:     u.Netmask,
		Configured:  u.Configured,
		Syncing:     u.Syncing != nil && *u.Syncing,
		TotalVCPUs:  u.Resources.TotalVCPUs,
		UsedVCPUs:   u.Resources.UsedVCPUs,
		TotalMemory: u.Resources.TotalMemoryRaw,
		UsedMemory:  u.Resources.UsedMemoryRaw,
	}
}

func adaptNodes(view ClusterView) []NodeInput {
	out := make([]NodeInput, 0, len(view.Nodes))
	for _, n := range view.Nodes {
		out = append(out, NodeInput{
			Name:          n.Name,
			ID:            n.ID,
			State:         n.State,
			SubState:      n.SubState,
			StandingState: n.StandingState,
			Mode:          n.Mode,
			Primary:       n.Primary != nil && *n.Primary,
			Manufacturer:  n.Manufacturer,
			Model:         n.Model,
			CPUs:          n.CPUs,
			Memory:        n.MemoryRaw,
			IPAddress:     n.IP,
			Gateway:       n.Gateway,
			DNS:           n.DNS,
		})
	}
	return out
}

func adaptNetworks(view ClusterView) []NetworkInput {
	out := make([]NetworkInput, 0, len(view.Networks))
	for _, n := range view.Networks {
		out = append(out, NetworkInput{
			Name:          n.Name,
			ID:            n.ID,
			FaultTolerant: n.FaultTolerant,
			Role:          n.Role,
			Bandwidth:     n.BandwidthRaw,
			MTU:           n.MTU,
		})
	}
	return out
}

func adaptStorageGroups(view ClusterView) []StorageGroupInput {
	out := make([]StorageGroupInput, 0, len(view.StorageGroups))
	for _, g := range view.StorageGroups {
		var disks []DiskInput
		for _, d := range g.Disks {
			disks = append(disks, DiskInput{
				Name:          d.Name,
				ID:            d.ID,
				SizeBytes:     d.SizeBytes,
				UsedBytes:     d.UsedBytes,
				StandingState: d.StandingState,
				Node:          d.NodeName,
			})
		}
		out = append(out, StorageGroupInput{
			Name:               g.Name,
			ID:                 g.ID,
			SizeBytes:          g.SizeBytes,
			UsedBytes:          g.UsedBytes,
			LogicalSectorSize:  intPtrToAny(g.LogicalSectorSizeBytes),
			PhysicalSectorSize: intPtrToAny(g.PhysicalSectorSizeBytes),
			DiskType:           g.DiskType,
			Disks:              disks,
		})
	}
	return out
}

// sgRef 는 이름/id 쌍을 SGRef 로 만든다. 둘 다 없으면 nil.
func sgRef(name, id string) *SGRef {
	if name == "" && id == "" {
		return nil
	}
	return &SGRef{Name: name, ID: id}
}

func adaptVolumes(view ClusterView) []VolumeInfoInput {
	out := make([]VolumeInfoInput, 0, len(view.Volumes))
	for _, v := range view.Volumes {
		out = append(out, VolumeInfoInput{
			Name:         v.Name,
			ID:           v.ID,
			Size:         v.SizeRaw,
			SectorSize:   intPtrToAny(v.SectorSizeBytes),
			Bootable:     v.Bootable,
			StorageGroup: sgRef(v.StorageGroupName, v.StorageGroupID),
		})
	}
	return out
}

func adaptImageContainers(view ClusterView) []ImageContainerInput {
	out := make([]ImageContainerInput, 0, len(view.ImageContainers))
	for _, c := range view.ImageContainers {
		out = append(out, ImageContainerInput{
			Name:          c.Name,
			ID:            c.ID,
			Size:          c.SizeRaw,
			SizeUsed:      c.UsedRaw,
			IsLocal:       c.IsLocal,
			HasFilesystem: c.HasFilesystem,
			StorageGroup:  sgRef(c.StorageGroupName, c.StorageGroupID),
		})
	}
	return out
}

// adaptVMs 는 node.vm_placements 를 역인덱싱해 VM 의 placement_nodes 를 채운다.
func adaptVMs(view ClusterView) []VMInput {
	placements := map[string][]string{}
	var placementOrder []string
	for _, n := range view.Nodes {
		for _, p := range n.VMPlacements {
			if _, ok := placements[p.Name]; !ok {
				placementOrder = append(placementOrder, p.Name)
			}
			placements[p.Name] = append(placements[p.Name], n.Name)
		}
	}

	out := make([]VMInput, 0, len(view.VMs))
	for _, vm := range view.VMs {
		var vols []VMVolumeInput
		for _, v := range vm.Volumes {
			var imgs []DiskImageInput
			for _, di := range v.DiskImages {
				imgs = append(imgs, DiskImageInput{
					Name:         di.Name,
					ID:           di.ID,
					EnableStatus: di.EnableStatus,
					Node:         di.NodeName,
					NodeID:       di.NodeID,
				})
			}
			vols = append(vols, VMVolumeInput{
				Name:       v.Name,
				ID:         v.ID,
				Size:       v.SizeRaw,
				SectorSize: v.SectorSizeRaw,
				Device:     v.Device,
				DeviceID:   v.DeviceID,
				DiskImages: imgs,
			})
		}
		var alinks []VMALinkInput
		for _, a := range vm.ALinks {
			alinks = append(alinks, VMALinkInput{
				Network:   a.Network,
				Role:      a.Role,
				Bandwidth: a.BandwidthRaw,
			})
		}
		var insts []VMInstanceInput
		for _, i := range vm.Instances {
			insts = append(insts, VMInstanceInput{
				Name:           i.Name,
				ID:             i.ID,
				EnableStatus:   i.EnableStatus,
				ConfigVhostNet: i.ConfigVhostNet,
				MTBF:           i.MTBFStatus,
				UUID:           i.UUID,
				Node:           i.NodeName,
				NodeID:         i.NodeID,
			})
		}
		placement := placements[vm.Name]
		if placement == nil {
			placement = []string{}
		}
		out = append(out, VMInput{
			Name:           vm.Name,
			InternalName:   vm.InternalName,
			ID:             vm.ID,
			UUID:           vm.UUID,
			CPUs:           vm.CPUs,
			BootType:       vm.BootType,
			Memory:         vm.MemoryRaw,
			Type:           vm.OSType,
			State:          vm.State,
			StandingState:  vm.StandingState,
			FaultTolerant:  vm.HAMode,
			PlacementNodes: placement,
			Interfaces:     vm.Interfaces,
			Volumes:        vols,
			ALinks:         alinks,
			Instances:      insts,
		})
	}
	return out
}

func adaptAlerts(view ClusterView) []AlertInput {
	out := make([]AlertInput, 0, len(view.Alerts))
	for _, a := range view.Alerts {
		out = append(out, AlertInput{
			Name:        a.Name,
			ID:          a.ID,
			Time:        a.Time,
			Description: a.Description,
			// ClassifyAlert 는 avcli 원문 숫자를 기대한다
			Severity: a.SeverityRaw,
		})
	}
	return out
}

// adaptNodeMetrics 는 node.os(/proc 수집)를 NodeOSMetrics 계약으로 변환한다.
// 링크는 @link(operstate/speed)와 @net(에러·드롭 델타)이 별도 섹션이라 이름으로 합친다.
func adaptNodeMetrics(view ClusterView) map[string]*NodeOSMetrics {
	out := map[string]*NodeOSMetrics{}
	for _, n := range view.Nodes {
		osm := n.OS
		if osm == nil {
			continue
		}
		netByName := map[string]NetDevView{}
		for _, x := range osm.Net {
			netByName[x.Name] = x
		}
		var links []LinkInput
		for _, l := range osm.Links {
			nx, hasNx := netByName[l.Name]
			var rxErr, txErr, drops *int64
			if hasNx {
				rxErr = nx.RxErrDelta
				txErr = nx.TxErrDelta
				// 델타가 없으면 0 으로 보고 합산한다 (원본 `or 0` 동작)
				d := derefI64(nx.RxDropDelta) + derefI64(nx.TxDropDelta)
				drops = &d
			}
			links = append(links, LinkInput{
				Name:       l.Name,
				OperState:  l.State,
				Speed:      l.SpeedMbps,
				MTU:        l.MTU, // 역할 판별 보조(a-link 9000 / business 1500)
				Physical:   l.Physical,
				RxErrors:   rxErr,
				TxErrors:   txErr,
				DropsDelta: drops,
			})
		}
		temps := map[string]float64{}
		for _, t := range osm.Temps {
			key := t.Label
			if t.Chip != "" {
				key = t.Chip + "/" + t.Label
			}
			if key != "" && t.Celsius != nil {
				temps[key] = *t.Celsius
			}
		}
		out[n.Name] = &NodeOSMetrics{
			Links:    links,
			CPUPct:   n.CPUPct,
			MemPct:   n.MemPct,
			UptimeS:  n.UptimeSecs,
			TempsC:   temps,
			TempMaxC: n.TempMaxC,
			Source:   osm.Source,
		}
	}
	return out
}

func derefI64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
