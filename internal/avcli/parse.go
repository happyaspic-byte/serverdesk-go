package avcli

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// 이 파일의 구조체는 avcli_parse.py 가 반환하는 dict 와 필드명이 1:1 이다
// (JSON 태그 = Python dict 키). Python 의 None 에 해당하는 값은 포인터로 두어
// JSON null 로 직렬화되게 했다.

// NameID 는 {name, id} 쌍 인덱스 항목이다.
type NameID struct {
	Name *string `json:"name"`
	ID   *string `json:"id"`
}

// Vulnerability 는 node-info 의 CPU 취약점 패치 플래그다.
type Vulnerability struct {
	MeltdownPatch  *bool `json:"meltdown_patch"`
	SpectreV2Patch *bool `json:"spectre_v2_patch"`
}

// NodeInfo — node-info 의 물리 노드.
// 주의: <memory> 는 물리 설치 용량이라 /proc/meminfo MemTotal 과 다르고,
// virtual-machines 는 "구동중"이 아니라 "이 노드 배치(placement)"다
// (ztC Edge 실측에서 stopped VM 도 실려 나온다).
type NodeInfo struct {
	Name                     *string       `json:"name"`
	ID                       *string       `json:"id"`
	State                    string        `json:"state"`
	SubState                 *string       `json:"sub_state"`
	StandingState            *string       `json:"standing_state"`
	Mode                     *string       `json:"mode"`
	Primary                  bool          `json:"primary"`
	Manufacturer             *string       `json:"manufacturer"`
	Model                    *string       `json:"model"`
	MaintenanceAllowed       *bool         `json:"maintenance_allowed"`
	MaintenanceGuestShutdown *bool         `json:"maintenance_guest_shutdown"`
	Cpus                     *int64        `json:"cpus"` // 소켓 수(코어 아님)
	MemoryBytes              *int64        `json:"memory_bytes"`
	MemoryRaw                *string       `json:"memory_raw"`
	Vulnerability            Vulnerability `json:"vulnerability"`
	VMPlacements             []NameID      `json:"vm_placements"`
	IP                       *string       `json:"ip"`
	Gateway                  *string       `json:"gateway"`
	DNS                      []string      `json:"dns"`
	Healthy                  bool          `json:"healthy"`
}

// ParseNodeInfo 는 avance/node[] 를 파싱한다. 이름순 정렬 — Edge 는 node1 이
// 먼저 나오는 등 요소 순서가 불안정하므로 인덱스 접근은 금지다.
func ParseNodeInfo(root *Element) []NodeInfo {
	nodes := []NodeInfo{}
	if root == nil {
		return nodes
	}
	for _, n := range root.FindAll("node") {
		memB, memRaw := sizePair(n, "memory")
		ln := n.Find("local-networks/local-network")
		node := NodeInfo{
			Name:                     getText(n, "name"),
			ID:                       getText(n, "id"),
			State:                    getLowerDef(n, "state", "unknown"),
			SubState:                 getLower(n, "sub-state"),
			StandingState:            getLower(n, "standing-state"),
			Mode:                     getLower(n, "mode"),
			Primary:                  boolOr(getBool(n, "primary"), false),
			Manufacturer:             getText(n, "manufacturer"),
			Model:                    getText(n, "model"),
			MaintenanceAllowed:       getBool(n, "maintenance-allowed"),
			MaintenanceGuestShutdown: getBool(n, "maintenance-guest-shutdown"),
			Cpus:                     getInt(n, "cpus"),
			MemoryBytes:              memB,
			MemoryRaw:                memRaw,
			Vulnerability: Vulnerability{
				MeltdownPatch:  getBool(n, "vulnerability/meltdown-patch-enabled"),
				SpectreV2Patch: getBool(n, "vulnerability/spectre-v2-patch-enabled"),
			},
			VMPlacements: []NameID{},
			DNS:          []string{},
		}
		for _, v := range n.FindAll("virtual-machines/virtual-machine") {
			node.VMPlacements = append(node.VMPlacements,
				NameID{Name: getText(v, "name"), ID: getText(v, "id")})
		}
		if ln != nil {
			node.IP = getText(ln, "ip-address")
			node.Gateway = getText(ln, "gateway-address")
			node.DNS = getTexts(ln, "dns/address")
		}
		node.Healthy = node.State == "running" &&
			(node.StandingState == nil || *node.StandingState == "normal") &&
			(node.Mode == nil || *node.Mode == "normal")
		nodes = append(nodes, node)
	}
	slices.SortStableFunc(nodes, func(a, b NodeInfo) int {
		return strings.Compare(strVal(a.Name), strVal(b.Name))
	})
	return nodes
}

// DetectPlatform — 플랫폼 판별 계약: Stratus/ztC Edge → "ztcedge", 그 외 → "everrun".
// ztcedge 에서만 LED-info 가 동작한다(everRun 은 서버측 NPE 라 호출 금지).
func DetectPlatform(nodes []NodeInfo) string {
	for _, n := range nodes {
		if strings.ToLower(strings.TrimSpace(strVal(n.Manufacturer))) == "stratus" &&
			strings.ToLower(strings.TrimSpace(strVal(n.Model))) == "ztc edge" {
			return "ztcedge"
		}
	}
	return "everrun"
}

// UnitResources — unit-info 의 클러스터 단위 잔여 용량.
// 노드 물리량(node-info)과 다르다 — 하이퍼바이저 예약분을 뺀 공식 소스.
type UnitResources struct {
	TotalVcpus       *int64   `json:"total_vcpus"`
	UsedVcpus        *float64 `json:"used_vcpus"`
	TotalMemoryBytes *int64   `json:"total_memory_bytes"`
	TotalMemoryRaw   *string  `json:"total_memory_raw"`
	UsedMemoryBytes  *int64   `json:"used_memory_bytes"`
	UsedMemoryRaw    *string  `json:"used_memory_raw"`
	VcpuPct          *float64 `json:"vcpu_pct,omitempty"`
	MemoryPct        *float64 `json:"memory_pct,omitempty"`
}

// UnitVM 은 unit-info 하위 VM 인덱스 항목이다.
type UnitVM struct {
	Name   *string `json:"name"`
	ID     *string `json:"id"`
	HaMode *string `json:"ha_mode"`
}

// UnitInfo — unit-info 의 클러스터 루트 객체.
// Syncing=true 면 미러 재동기화 중이라 FT 정상 판정에서 제외해야 한다.
type UnitInfo struct {
	Name            *string         `json:"name"`
	ID              *string         `json:"id"`
	Version         *string         `json:"version"`
	UUID            *string         `json:"uuid"`
	Configured      *bool           `json:"configured"`
	Syncing         bool            `json:"syncing"`
	Address         *string         `json:"address"`
	Netmask         *string         `json:"netmask"`
	Ntp             []string        `json:"ntp"`
	Resources       UnitResources   `json:"resources"`
	SharedNetworks  []SharedNetwork `json:"shared_networks"`
	StorageGroups   []NameID        `json:"storage_groups"`
	VirtualMachines []UnitVM        `json:"virtual_machines"`
}

func round1(x float64) float64 { return math.RoundToEven(x*10) / 10 }

// ParseUnitInfo — root 가 nil 이면 nil(Python 의 {} 에 해당).
func ParseUnitInfo(root *Element) *UnitInfo {
	if root == nil {
		return nil
	}
	res := root.Find("resources")
	totMem, totMemRaw := sizePair(res, "total-memory")
	usedMem, usedMemRaw := sizePair(res, "used-memory")
	u := &UnitInfo{
		Name:       getText(root, "name"),
		ID:         getText(root, "id"),
		Version:    getText(root, "version"),
		UUID:       getText(root, "uuid"),
		Configured: getBool(root, "configured"),
		Syncing:    boolOr(getBool(root, "syncing"), false),
		Address:    getText(root, "address"),
		Netmask:    getText(root, "netmask"),
		Ntp:        getTexts(root, "ntp/address"),
		Resources: UnitResources{
			TotalVcpus:       getInt(res, "total-vcpus"),
			UsedVcpus:        getFloat(res, "used-vcpus"),
			TotalMemoryBytes: totMem,
			TotalMemoryRaw:   totMemRaw,
			UsedMemoryBytes:  usedMem,
			UsedMemoryRaw:    usedMemRaw,
		},
		SharedNetworks:  []SharedNetwork{},
		StorageGroups:   []NameID{},
		VirtualMachines: []UnitVM{},
	}
	for _, s := range root.FindAll("shared-networks/shared-network") {
		u.SharedNetworks = append(u.SharedNetworks, parseSharedNetwork(s))
	}
	for _, g := range root.FindAll("storage-groups/storage-group") {
		u.StorageGroups = append(u.StorageGroups,
			NameID{Name: getText(g, "name"), ID: getText(g, "id")})
	}
	for _, v := range root.FindAll("virtual-machines/virtual-machine") {
		u.VirtualMachines = append(u.VirtualMachines, UnitVM{
			Name:   getText(v, "name"),
			ID:     getText(v, "id"),
			HaMode: getLower(v, "fault-tolerant"),
		})
	}
	r := &u.Resources
	if r.TotalVcpus != nil && *r.TotalVcpus != 0 && r.UsedVcpus != nil {
		pct := round1(*r.UsedVcpus / float64(*r.TotalVcpus) * 100)
		r.VcpuPct = &pct
	}
	if r.TotalMemoryBytes != nil && *r.TotalMemoryBytes != 0 && r.UsedMemoryBytes != nil {
		pct := round1(float64(*r.UsedMemoryBytes) / float64(*r.TotalMemoryBytes) * 100)
		r.MemoryPct = &pct
	}
	return u
}

// SharedNetwork — network-info / unit-info 의 공유 네트워크.
// Role: a-link(노드 간 동기화 전용, 최소 2개 필요) | business | private | management.
type SharedNetwork struct {
	Name          *string `json:"name"`
	ID            *string `json:"id"`
	FaultTolerant *string `json:"fault_tolerant"`
	Role          *string `json:"role"`
	BandwidthBps  *int64  `json:"bandwidth_bps"`
	BandwidthRaw  *string `json:"bandwidth_raw"`
	Mtu           *int64  `json:"mtu"`
}

func parseSharedNetwork(s *Element) SharedNetwork {
	return SharedNetwork{
		Name:          getText(s, "name"),
		ID:            getText(s, "id"),
		FaultTolerant: getLower(s, "fault-tolerant"),
		Role:          getLower(s, "role"),
		BandwidthBps:  ParseBandwidth(textStr(s, "bandwidth")),
		BandwidthRaw:  getText(s, "bandwidth"),
		Mtu:           getInt(s, "mtu"),
	}
}

// ParseNetworkInfo 는 avance/shared-network[] 를 파싱한다.
func ParseNetworkInfo(root *Element) []SharedNetwork {
	out := []SharedNetwork{}
	if root == nil {
		return out
	}
	for _, s := range root.FindAll("shared-network") {
		out = append(out, parseSharedNetwork(s))
	}
	return out
}

// VMInterface — interface 의 net0/net1 은 node0/node1 측 vNIC 이다.
// 한쪽만 ENABLED 면 심플렉스. shared-network 는 정식 id 가 아니라 이름 문자열이다.
type VMInterface struct {
	SharedNetwork *string `json:"shared_network"`
	MAC           *string `json:"mac"`
	Net0Status    *string `json:"net0_status"`
	Net1Status    *string `json:"net1_status"`
	Redundant     bool    `json:"redundant"`
}

// DiskImage — 볼륨의 노드별 미러 조각. EnableStatus 가 하나라도 ENABLED 가
// 아니면 미러 깨짐이다(대문자 ENABLED/DISABLED 표기에 주의).
type DiskImage struct {
	Name         *string `json:"name"`
	ID           *string `json:"id"`
	Enabled      bool    `json:"enabled"`
	EnableStatus *string `json:"enable_status"`
	NodeName     *string `json:"node_name"`
	NodeID       *string `json:"node_id"`
}

// VMVolume — 볼륨 키 계약: 이름은 유일하지 않다(root/swap/diagdata 중복)라 id 를 키로.
// cdrom 은 name/id/size 가 아예 없고 Mirrored 는 null 이다.
type VMVolume struct {
	Name            *string     `json:"name"`
	ID              *string     `json:"id"`
	Device          *string     `json:"device"`
	DeviceID        *string     `json:"device_id"` // vbd:oNNN
	IsCdrom         bool        `json:"is_cdrom"`
	SizeBytes       *int64      `json:"size_bytes"`
	SizeRaw         *string     `json:"size_raw"`
	SectorSizeBytes *int64      `json:"sector_size_bytes"`
	SectorSizeRaw   *string     `json:"sector_size_raw"`
	DiskImages      []DiskImage `json:"disk_images"`
	Mirrored        *bool       `json:"mirrored"`
}

func parseVMVolume(v *Element) VMVolume {
	sizeB, sizeRaw := sizePair(v, "size")
	secB, secRaw := sizePair(v, "sector-size")
	vol := VMVolume{
		Name:            getText(v, "name"),
		ID:              getText(v, "id"),
		Device:          getText(v, "device"),
		DeviceID:        getText(v, "device-id"),
		SizeBytes:       sizeB,
		SizeRaw:         sizeRaw,
		SectorSizeBytes: secB,
		SectorSizeRaw:   secRaw,
		DiskImages:      []DiskImage{},
	}
	for _, di := range v.FindAll("disk-images/disk-image") {
		vol.DiskImages = append(vol.DiskImages, DiskImage{
			Name:         getText(di, "name"),
			ID:           getText(di, "id"),
			Enabled:      strings.ToUpper(textStr(di, "enable-status")) == "ENABLED",
			EnableStatus: getText(di, "enable-status"),
			NodeName:     getText(di, "node/name"),
			NodeID:       getText(di, "node/id"),
		})
	}
	vol.IsCdrom = vol.Device != nil && strings.Contains(strings.ToLower(*vol.Device), "cdrom")
	if !vol.IsCdrom {
		m := len(vol.DiskImages) == 2
		for _, i := range vol.DiskImages {
			if !i.Enabled {
				m = false
			}
		}
		vol.Mirrored = &m
	}
	return vol
}

// ALink — a-links 의 자식은 엘리먼트 태그명이 곧 네트워크 이름인 동적 태그다
// (everRun 은 priv0/net_82, ztC Edge 는 A1/A2). 고정 XPath 사용 불가.
type ALink struct {
	Network      string  `json:"network"`
	Role         *string `json:"role"`
	BandwidthBps *int64  `json:"bandwidth_bps"`
	BandwidthRaw *string `json:"bandwidth_raw"`
}

// VMInstance — local-virtual-machine. VM↔노드 배치는 node-info 가 아니라
// 여기를 봐야 한다. 이것만 <ID> 대문자 태그다(나머지는 전부 소문자 id).
type VMInstance struct {
	Name            *string `json:"name"`
	ID              *string `json:"id"`
	Enabled         bool    `json:"enabled"`
	EnableStatus    *string `json:"enable_status"`
	ConfigVhostNet  *bool   `json:"config_vhost_net"`
	DisableVhostNet *bool   `json:"disable_vhost_net"`
	MtbfStatus      *string `json:"mtbf_status"`
	UUID            *string `json:"uuid"`
	NodeName        *string `json:"node_name"`
	NodeID          *string `json:"node_id"`
}

// VMInfo — vm-info. 토폴로지 간선의 대부분이 여기 들어있다.
type VMInfo struct {
	Name             *string       `json:"name"`
	InternalName     *string       `json:"internal_name"`
	ID               *string       `json:"id"`
	UUID             *string       `json:"uuid"`
	Cpus             *int64        `json:"cpus"`
	BootType         *string       `json:"boot_type"`
	MemoryBytes      *int64        `json:"memory_bytes"`
	MemoryRaw        *string       `json:"memory_raw"`
	OsType           *string       `json:"os_type"`
	State            string        `json:"state"`
	StandingState    *string       `json:"standing_state"`
	HaMode           *string       `json:"ha_mode"` // ft | ha
	VhostNetDisabled *bool         `json:"vhost_net_disabled"`
	LiveDumpState    *string       `json:"live_dump_state"`
	Interfaces       []VMInterface `json:"interfaces"`
	Volumes          []VMVolume    `json:"volumes"`
	ALinks           []ALink       `json:"a_links"`
	Instances        []VMInstance  `json:"instances"`
	Nodes            []string      `json:"nodes"`
	Redundancy       string        `json:"redundancy"`
	DiskMirrored     bool          `json:"disk_mirrored"`
	NicRedundant     bool          `json:"nic_redundant"`
	ImageContainers  []string      `json:"image_containers,omitempty"` // JoinImageContainers 가 채움
}

// ParseVMInfo 는 avance/virtual-machine[] 을 파싱한다(이름순 정렬).
func ParseVMInfo(root *Element) []VMInfo {
	vms := []VMInfo{}
	if root == nil {
		return vms
	}
	for _, v := range root.FindAll("virtual-machine") {
		memB, memRaw := sizePair(v, "memory")
		vm := VMInfo{
			Name:             getText(v, "name"),
			InternalName:     getText(v, "internal-name"),
			ID:               getText(v, "id"),
			UUID:             getText(v, "uuid"),
			Cpus:             getInt(v, "cpus"),
			BootType:         getText(v, "boot-type"),
			MemoryBytes:      memB,
			MemoryRaw:        memRaw,
			OsType:           getText(v, "type"),
			State:            getLowerDef(v, "state", "unknown"),
			StandingState:    getLower(v, "standing-state"),
			HaMode:           getLower(v, "fault-tolerant"),
			VhostNetDisabled: getBool(v, "vhost-net-disabled"),
			LiveDumpState:    getLower(v, "live-dump-state"),
			Interfaces:       []VMInterface{},
			Volumes:          []VMVolume{},
			ALinks:           []ALink{},
			Instances:        []VMInstance{},
			Nodes:            []string{},
		}
		for _, i := range v.FindAll("interfaces/interface") {
			n0 := strings.ToUpper(textStr(i, "net0-status"))
			n1 := strings.ToUpper(textStr(i, "net1-status"))
			iface := VMInterface{
				SharedNetwork: getText(i, "shared-network"),
				MAC:           getText(i, "MAC"), // 태그 대문자 예외
				Redundant:     n0 == "ENABLED" && n1 == "ENABLED",
			}
			if n0 != "" {
				iface.Net0Status = &n0
			}
			if n1 != "" {
				iface.Net1Status = &n1
			}
			vm.Interfaces = append(vm.Interfaces, iface)
		}
		for _, lv := range v.FindAll("local-virtual-machines/local-virtual-machine") {
			vm.Instances = append(vm.Instances, VMInstance{
				Name:            getText(lv, "name"),
				ID:              getText(lv, "ID"), // 태그 대문자 예외
				Enabled:         strings.ToUpper(textStr(lv, "enable-status")) == "ENABLED",
				EnableStatus:    getText(lv, "enable-status"),
				ConfigVhostNet:  getBool(lv, "config-vhost-net"),
				DisableVhostNet: getBool(lv, "disable-vhost-net"),
				MtbfStatus:      getLower(lv, "mtbf/status"),
				UUID:            getText(lv, "uuid"),
				NodeName:        getText(lv, "node/name"),
				NodeID:          getText(lv, "node/id"),
			})
		}
		for _, x := range v.FindAll("volumes/volume") {
			vm.Volumes = append(vm.Volumes, parseVMVolume(x))
		}
		if al := v.Find("a-links"); al != nil {
			for _, ch := range al.Children {
				vm.ALinks = append(vm.ALinks, ALink{
					Network:      ch.Tag,
					Role:         getLower(ch, "role"),
					BandwidthBps: ParseBandwidth(textStr(ch, "bandwidth")),
					BandwidthRaw: getText(ch, "bandwidth"),
				})
			}
		}
		seen := map[string]bool{}
		for _, inst := range vm.Instances {
			if inst.NodeName != nil && !seen[*inst.NodeName] {
				seen[*inst.NodeName] = true
				vm.Nodes = append(vm.Nodes, *inst.NodeName)
			}
		}
		slices.Sort(vm.Nodes)
		vm.Redundancy = vmRedundancy(&vm)
		dataVols := 0
		mirrored := true
		for _, x := range vm.Volumes {
			if x.IsCdrom {
				continue
			}
			dataVols++
			if x.Mirrored == nil || !*x.Mirrored {
				mirrored = false
			}
		}
		vm.DiskMirrored = dataVols > 0 && mirrored
		vm.NicRedundant = len(vm.Interfaces) > 0
		for _, x := range vm.Interfaces {
			if !x.Redundant {
				vm.NicRedundant = false
			}
		}
		vms = append(vms, vm)
	}
	slices.SortStableFunc(vms, func(a, b VMInfo) int {
		return strings.Compare(strVal(a.Name), strVal(b.Name))
	})
	return vms
}

// vmRedundancy — FT/HA 판정 계약.
// 정상 이중화 = local-virtual-machine 2개 + 모두 ENABLED + mtbf/status == normal.
// 한쪽 DISABLED 또는 인스턴스 1개면 simplex.
func vmRedundancy(vm *VMInfo) string {
	inst := vm.Instances
	if len(inst) == 0 {
		return "unknown"
	}
	enabled := 0
	mtbfOK := true
	for _, i := range inst {
		if i.Enabled {
			enabled++
		}
		if i.MtbfStatus != nil && *i.MtbfStatus != "normal" {
			mtbfOK = false
		}
	}
	if len(inst) >= 2 && enabled == len(inst) && mtbfOK {
		return "redundant"
	}
	if enabled >= 1 {
		return "simplex"
	}
	return "down"
}

// StorageDisk — storage-info-v2 --disks 의 논리 디스크(노드마다 1개).
// NodeName 은 이름 문자열뿐이다(정식 id 없음).
type StorageDisk struct {
	Name          *string `json:"name"`
	ID            *string `json:"id"`
	SizeBytes     *int64  `json:"size_bytes"`
	SizeRaw       *string `json:"size_raw"`
	UsedBytes     *int64  `json:"used_bytes"`
	UsedRaw       *string `json:"used_raw"`
	StandingState *string `json:"standing_state"`
	NodeName      *string `json:"node_name"`
}

// StorageVolume — v2 --volumes 의 볼륨에는 id 가 없다 → volume-info 로 별도 조인.
type StorageVolume struct {
	Name            *string `json:"name"`
	ID              *string `json:"id"`
	SizeBytes       *int64  `json:"size_bytes"`
	SizeRaw         *string `json:"size_raw"`
	SectorSizeBytes *int64  `json:"sector_size_bytes"`
}

// StorageGroup — storage-info(구버전)와 storage-info-v2(--disks --volumes)를
// 같은 구조로 흡수한다. 구버전만 <sector-size> 를 주고 v2 는 논리/물리로 나뉘어
// 있어, sector-size 가 없으면 논리 섹터로 폴백한다.
type StorageGroup struct {
	Name                    *string         `json:"name"`
	ID                      *string         `json:"id"`
	Description             *string         `json:"description"`
	SizeBytes               *int64          `json:"size_bytes"`
	SizeRaw                 *string         `json:"size_raw"`
	UsedBytes               *int64          `json:"used_bytes"`
	UsedRaw                 *string         `json:"used_raw"`
	SectorSizeBytes         *int64          `json:"sector_size_bytes"`
	SectorSizeRaw           *string         `json:"sector_size_raw"`
	LogicalSectorSizeBytes  *int64          `json:"logical_sector_size_bytes"`
	PhysicalSectorSizeBytes *int64          `json:"physical_sector_size_bytes"`
	DiskType                *string         `json:"disk_type"` // 512n | 512e | 4kn
	Disks                   []StorageDisk   `json:"disks"`
	Volumes                 []StorageVolume `json:"volumes"`
	UsedPct                 *float64        `json:"used_pct,omitempty"`
	FreeBytes               *int64          `json:"free_bytes,omitempty"`
}

// ParseStorageInfo 는 storage-info / storage-info-v2 응답을 파싱한다.
func ParseStorageInfo(root *Element) []StorageGroup {
	groups := []StorageGroup{}
	if root == nil {
		return groups
	}
	for _, g := range root.FindAll("storage-group") {
		sizeB, sizeRaw := sizePair(g, "size")
		usedB, usedRaw := sizePair(g, "size-used")
		secB, secRaw := sizePair(g, "sector-size")
		lsecB, lsecRaw := sizePair(g, "logical-sector-size")
		psecB, _ := sizePair(g, "physical-sector-size")
		if secB == nil {
			secB, secRaw = lsecB, lsecRaw
		}
		grp := StorageGroup{
			Name:                    getText(g, "name"),
			ID:                      getText(g, "id"),
			Description:             getText(g, "description"),
			SizeBytes:               sizeB,
			SizeRaw:                 sizeRaw,
			UsedBytes:               usedB,
			UsedRaw:                 usedRaw,
			SectorSizeBytes:         secB,
			SectorSizeRaw:           secRaw,
			LogicalSectorSizeBytes:  lsecB,
			PhysicalSectorSizeBytes: psecB,
			DiskType:                getText(g, "disk-type"),
			Disks:                   []StorageDisk{},
			Volumes:                 []StorageVolume{},
		}
		for _, d := range g.FindAll("disks/disk") {
			db, draw := sizePair(d, "size")
			ub, uraw := sizePair(d, "used-size")
			grp.Disks = append(grp.Disks, StorageDisk{
				Name:          getText(d, "name"),
				ID:            getText(d, "id"),
				SizeBytes:     db,
				SizeRaw:       draw,
				UsedBytes:     ub,
				UsedRaw:       uraw,
				StandingState: getLower(d, "standing-state"),
				NodeName:      getText(d, "node"),
			})
		}
		for _, v := range g.FindAll("volumes/volume") {
			vb, vraw := sizePair(v, "size")
			vsec, _ := sizePair(v, "volume-sector-size")
			grp.Volumes = append(grp.Volumes, StorageVolume{
				Name:            getText(v, "name"),
				ID:              getText(v, "id"),
				SizeBytes:       vb,
				SizeRaw:         vraw,
				SectorSizeBytes: vsec,
			})
		}
		if sizeB != nil && *sizeB != 0 && usedB != nil {
			pct := round1(float64(*usedB) / float64(*sizeB) * 100)
			grp.UsedPct = &pct
			free := *sizeB - *usedB
			grp.FreeBytes = &free
		}
		groups = append(groups, grp)
	}
	return groups
}

// Volume — volume-info. 이름이 중복되므로 반드시 id(volume:oNNN)를 키로 쓴다.
// ISO 볼륨에는 sector-size 가 없다.
type Volume struct {
	Name             *string `json:"name"`
	ID               *string `json:"id"`
	SizeBytes        *int64  `json:"size_bytes"`
	SizeRaw          *string `json:"size_raw"`
	SectorSizeBytes  *int64  `json:"sector_size_bytes"`
	Bootable         *bool   `json:"bootable"`
	StorageGroupName *string `json:"storage_group_name"`
	StorageGroupID   *string `json:"storage_group_id"`
}

// ParseVolumeInfo 는 avance/volume[] 을 파싱한다.
func ParseVolumeInfo(root *Element) []Volume {
	out := []Volume{}
	if root == nil {
		return out
	}
	for _, v := range root.FindAll("volume") {
		sizeB, sizeRaw := sizePair(v, "size")
		secB, _ := sizePair(v, "sector-size")
		out = append(out, Volume{
			Name:             getText(v, "name"),
			ID:               getText(v, "id"),
			SizeBytes:        sizeB,
			SizeRaw:          sizeRaw,
			SectorSizeBytes:  secB,
			Bootable:         getBool(v, "bootable"),
			StorageGroupName: getText(v, "storage-group/name"),
			StorageGroupID:   getText(v, "storage-group/id"),
		})
	}
	return out
}

// ImageContainer — 실사용 용량은 image-container 의 size/size-used 만이 실측치다.
// hasFileSystem/isLocal 은 camelCase 태그 예외. isLocal=true 는 노드 로컬(비미러).
type ImageContainer struct {
	Name             *string  `json:"name"`
	ID               *string  `json:"id"`
	HasFilesystem    *bool    `json:"has_filesystem"`
	IsLocal          *bool    `json:"is_local"`
	SizeBytes        *int64   `json:"size_bytes"`
	SizeRaw          *string  `json:"size_raw"`
	UsedBytes        *int64   `json:"used_bytes"`
	UsedRaw          *string  `json:"used_raw"`
	StorageGroupName *string  `json:"storage_group_name"`
	StorageGroupID   *string  `json:"storage_group_id"`
	UsedPct          *float64 `json:"used_pct,omitempty"`
	VmID             *string  `json:"vm_id,omitempty"`   // JoinImageContainers 가 채움
	VmName           *string  `json:"vm_name,omitempty"` // JoinImageContainers 가 채움
}

// ParseImageContainerInfo 는 avance/image-container[] 를 파싱한다.
func ParseImageContainerInfo(root *Element) []ImageContainer {
	out := []ImageContainer{}
	if root == nil {
		return out
	}
	for _, c := range root.FindAll("image-container") {
		sizeB, sizeRaw := sizePair(c, "size")
		usedB, usedRaw := sizePair(c, "size-used")
		item := ImageContainer{
			Name:             getText(c, "name"),
			ID:               getText(c, "id"),
			HasFilesystem:    getBool(c, "hasFileSystem"),
			IsLocal:          getBool(c, "isLocal"),
			SizeBytes:        sizeB,
			SizeRaw:          sizeRaw,
			UsedBytes:        usedB,
			UsedRaw:          usedRaw,
			StorageGroupName: getText(c, "storage-group/name"),
			StorageGroupID:   getText(c, "storage-group/id"),
		}
		if sizeB != nil && *sizeB != 0 && usedB != nil {
			pct := round1(float64(*usedB) / float64(*sizeB) * 100)
			item.UsedPct = &pct
		}
		out = append(out, item)
	}
	return out
}

// JoinImageContainers — image-container 는 id 링크가 없어 이름 접두어 매칭이
// 유일한 조인 방법이다(스키마상 유일하게 id 링크가 끊긴 지점).
//
// 컨테이너 이름 규칙: <vm internal-name>_<볼륨역할>_<uuid>. internal-name 은
// 점 등이 제거되어 변형될 수 있으므로(예: 26.04 → 2604) 가장 긴 접두어가 일치하는
// VM 에 붙인다. 매칭 실패(orphan 컨테이너) 시 명시적 로그를 남기고,
// 다른 VM의 UUID와 충돌 시 불일치 매칭을 방어한다.
func JoinImageContainers(vms []*VMInfo, containers []*ImageContainer) {
	if len(vms) == 0 || len(containers) == 0 {
		return
	}
	type key struct {
		prefix string
		vm     *VMInfo
	}
	keys := []key{}
	for _, vm := range vms {
		if vm == nil {
			continue
		}
		if vm.InternalName != nil {
			keys = append(keys, key{*vm.InternalName, vm})
		}
		if vm.Name != nil {
			keys = append(keys, key{*vm.Name, vm})
		}
	}
	slices.SortStableFunc(keys, func(a, b key) int { return len(b.prefix) - len(a.prefix) })

	// 다른 VM 의 UUID 집합 구성 (UUID 불일치 오매칭 방어용)
	vmByUUID := make(map[string]*VMInfo)
	for _, vm := range vms {
		if vm != nil && vm.UUID != nil && *vm.UUID != "" {
			norm := strings.ToLower(strings.ReplaceAll(*vm.UUID, "-", ""))
			if len(norm) >= 8 {
				vmByUUID[norm] = vm
			}
		}
	}

	for _, c := range containers {
		if c == nil {
			continue
		}
		cname := strVal(c.Name)
		matched := false
		for _, k := range keys {
			if strings.HasPrefix(cname, k.prefix+"_") || cname == k.prefix {
				// UUID 불일치 방어: 컨테이너 이름에 포함된 UUID 토큰이 다른 VM의 UUID와 명백히 일치하고 현재 매칭 대상과 다르면 건너뜀
				parts := strings.Split(cname, "_")
				if len(parts) >= 2 {
					token := strings.ToLower(strings.ReplaceAll(parts[len(parts)-1], "-", ""))
					if len(token) >= 8 {
						conflict := false
						for otherUUID, otherVM := range vmByUUID {
							if otherVM != k.vm && (strings.HasPrefix(otherUUID, token) || strings.HasPrefix(token, otherUUID)) {
								conflict = true
								break
							}
						}
						if conflict {
							continue
						}
					}
				}
				c.VmID = k.vm.ID
				c.VmName = k.vm.Name
				k.vm.ImageContainers = append(k.vm.ImageContainers, strVal(c.ID))
				matched = true
				break
			}
		}
		if !matched {
			Logf("warn", "avcli", fmt.Sprintf("orphan image-container: name=%q id=%q", cname, strVal(c.ID)))
		}
	}
}

// Alert — alert-info. <time> 은 TZ 표기가 없는 노드 로컬시각이다.
// TimeEpoch 은 tz 오프셋을 모르는 시점의 잠정값(=로컬시각을 UTC 로 간주)이고,
// 폴러가 노드 오프셋을 받아오면 ApplyAlertTimezone 으로 보정한다.
// id 만 "alert:11728" 처럼 o 접두어가 없다.
type Alert struct {
	Name            *string `json:"name"`
	ID              *string `json:"id"`
	Time            *string `json:"time"`
	TimeEpoch       *int64  `json:"time_epoch"`
	TimeEpochNaive  *int64  `json:"time_epoch_naive"`
	TzOffsetSecs    int64   `json:"tz_offset_secs"`
	Description     *string `json:"description"`
	SeverityRaw     *string `json:"severity_raw"`
	SeverityNumeric *string `json:"severity_numeric"`
	Severity        string  `json:"severity"`
	AgeSecs         *int64  `json:"age_secs,omitempty"`
}

// severity 숫자(0/1/2)의 의미가 문서로 확정되지 않았으므로 문자열 분류를 병행한다.
var (
	critRe = regexp.MustCompile(
		`(?i)offline|unreachable|failed|failure|fault|lost|cannot|expired|broken|\bdown\b` +
			`|simplex|split[- ]?brain|not\s+redundant|quorum`)
	warnRe = regexp.MustCompile(
		`(?i)warning|no link|disconnect|maintenance|\bsync|pressure|capacity|temporary` +
			`|degrade|reboot|unexpected|too\s?few|not\s+enabled|not\s+configured`)
)

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ClassifyAlert — severity 숫자 + 키워드 병행 분류 → critical | warning | info.
//
// severity 숫자(0/1/2)의 의미가 문서로 확정되지 않았다(0이 가장 심각한 것으로
// "관측"되나 샘플이 적다). 실측에서 "Node Maintenance" 같은 정상 운영 이벤트도
// severity=0 으로 나와, 숫자를 그대로 믿으면 거의 모든 알림이 critical 이 된다.
// => 키워드 분류를 1차 기준으로 삼고, 숫자는 "info 로 떨어진 것을 warning 으로
// 끌어올리는" 보조 신호로만 쓴다(미탐 방지). 숫자만으로 critical 을 만들지 않는다.
func ClassifyAlert(name, description, severity string) string {
	text := name + " " + description
	byText := "info"
	if critRe.MatchString(text) {
		byText = "critical"
	} else if warnRe.MatchString(text) {
		byText = "warning"
	}
	byNum := ""
	if isDigits(strings.TrimSpace(severity)) {
		switch strings.TrimSpace(severity) {
		case "0":
			byNum = "critical"
		case "1":
			byNum = "warning"
		case "2":
			byNum = "info"
		}
	}
	if byText == "info" && (byNum == "critical" || byNum == "warning") {
		return "warning"
	}
	return byText
}

func numericSeverity(sev *string) *string {
	if sev == nil {
		return nil
	}
	var m map[string]string = map[string]string{"0": "critical", "1": "warning", "2": "info"}
	if v, ok := m[strings.TrimSpace(*sev)]; ok {
		return &v
	}
	return nil
}

// ParseAlertInfo 는 avance/alert[] 를 파싱한다(시각 내림차순 정렬).
func ParseAlertInfo(root *Element) []Alert {
	out := []Alert{}
	if root == nil {
		return out
	}
	for _, a := range root.FindAll("alert") {
		name := getText(a, "name")
		desc := getText(a, "description")
		sev := getText(a, "severity")
		tstr := getText(a, "time")
		var naive *int64
		if tstr != nil {
			naive = ParseJavaDate(*tstr)
		}
		out = append(out, Alert{
			Name:            name,
			ID:              getText(a, "id"),
			Time:            tstr,
			TimeEpoch:       naive,
			TimeEpochNaive:  naive,
			TzOffsetSecs:    0,
			Description:     desc,
			SeverityRaw:     sev,
			SeverityNumeric: numericSeverity(sev),
			Severity:        ClassifyAlert(strVal(name), strVal(desc), strVal(sev)),
		})
	}
	slices.SortStableFunc(out, func(a, b Alert) int {
		x, y := int64Val(a.TimeEpoch), int64Val(b.TimeEpoch)
		switch {
		case x < y:
			return 1
		case x > y:
			return -1
		}
		return 0
	})
	return out
}

// ApplyAlertTimezone — 알림 시각을 노드 TZ 기준으로 UTC epoch 보정하고 AgeSecs 를 채운다.
//
// avcli 의 <time> 은 "2026-07-14 16:02:24" 처럼 TZ 표기가 없는 **노드 로컬시각**이다.
// 실장비(Asia/Seoul) 검증: 노드 uptime 으로 역산한 재부팅 시각과 "Node rebooted
// unexpectedly" 알림 시각이 오프셋 보정 후 138초 이내로 일치했다(보정 전 +9시간 오차).
// 오프셋을 모르면 0 을 넘겨라 — 잠정값(naive)이 유지된다. now 가 제로값이면 현재 시각.
func ApplyAlertTimezone(alerts []Alert, tzOffsetSecs int64, now time.Time) []Alert {
	if len(alerts) == 0 {
		return alerts
	}
	if now.IsZero() {
		now = time.Now()
	}
	for i := range alerts {
		a := &alerts[i]
		if a.TimeEpochNaive == nil {
			a.AgeSecs = nil
			continue
		}
		a.TzOffsetSecs = tzOffsetSecs
		ep := *a.TimeEpochNaive - tzOffsetSecs
		a.TimeEpoch = &ep
		age := now.Unix() - ep
		a.AgeSecs = &age
	}
	return alerts
}

// LicenseInfo — license-info. Expires 를 먼저 읽고 true 일 때만 ExpireDate 를
// 읽는다(ztC Edge 는 expires=false 라 expire-date 요소 자체가 없다).
type LicenseInfo struct {
	Name          *string `json:"name"`
	ID            *string `json:"id"`
	Type          *string `json:"type"`
	Edition       *string `json:"edition"`
	InstallDate   *string `json:"install_date"`
	InstallEpoch  *int64  `json:"install_epoch"`
	AllowFeatures *bool   `json:"allow_features"`
	Activated     *bool   `json:"activated"`
	Expires       bool    `json:"expires"`
	Installed     *bool   `json:"installed"`
	ExpireDate    *string `json:"expire_date"`
	ExpireEpoch   *int64  `json:"expire_epoch"`
	DaysLeft      *int64  `json:"days_left"`
}

// ParseLicenseInfo 는 avance/license 를 파싱한다(root/license 가 없으면 nil).
func ParseLicenseInfo(root *Element) *LicenseInfo {
	return parseLicenseInfo(root, time.Now())
}

func parseLicenseInfo(root *Element, now time.Time) *LicenseInfo {
	if root == nil {
		return nil
	}
	lic := root.Find("license")
	if lic == nil {
		return nil
	}
	out := &LicenseInfo{
		Name:          getText(lic, "name"),
		ID:            getText(lic, "id"),
		Type:          getText(lic, "type"),
		Edition:       getText(lic, "edition"),
		InstallDate:   getText(lic, "install-date"),
		AllowFeatures: getBool(lic, "allow-features"),
		Activated:     getBool(lic, "activated"),
		Expires:       boolOr(getBool(lic, "expires"), false),
		Installed:     getBool(lic, "installed"),
	}
	if out.InstallDate != nil {
		out.InstallEpoch = ParseJavaDate(*out.InstallDate)
	}
	if out.Expires {
		out.ExpireDate = getText(lic, "expire-date")
		if out.ExpireDate != nil {
			out.ExpireEpoch = ParseJavaDate(*out.ExpireDate)
		}
		if out.ExpireEpoch != nil && *out.ExpireEpoch != 0 {
			// Python 의 (expire - now) // 86400 와 같은 바닥 나눗셈.
			dl := int64(math.Floor(float64(*out.ExpireEpoch-now.Unix()) / 86400))
			out.DaysLeft = &dl
		}
	}
	return out
}

// LEDEntry — LED-info(ztC Edge 전용). everRun 에서는 서버측 NPE 로 항상
// 실패하므로 everRun 에는 호출 금지.
type LEDEntry struct {
	Node *string `json:"node"`
	Led  *string `json:"led"`
}

// ParseLEDInfo 는 LED-info 응답을 파싱한다. <node><name/><LED/></node> 형태가
// 기본이고, 스키마가 다르면(<node0>flashing</node0> 같은 동적 태그) 최상위 자식을
// 그대로 노출한다.
func ParseLEDInfo(root *Element) []LEDEntry {
	out := []LEDEntry{}
	if root == nil {
		return out
	}
	for _, n := range root.findDescendants("node") {
		led := getLower(n, "LED")
		if led == nil {
			led = getLower(n, "led")
		}
		out = append(out, LEDEntry{Node: getText(n, "name"), Led: led})
	}
	if len(out) > 0 {
		return out
	}
	for _, ch := range root.Children {
		v := strings.ToLower(strings.TrimSpace(ch.Text))
		var lp *string
		if v != "" {
			lp = &v
		}
		tag := ch.Tag
		out = append(out, LEDEntry{Node: &tag, Led: lp})
	}
	return out
}

// 스토리지 사용률 임계값. health 요약과 토폴로지 그래프가 같은 값을 써야
// /api/fleet 과 /api/topology 의 상태가 어긋나지 않는다.
const (
	SGWarnPct = 85.0
	SGCritPct = 95.0
)

// AlertRecentSecs 는 "최근 알림" 판정 구간(초). AlertCountsRecent 집계에만 쓴다.
const AlertRecentSecs = int64(24 * 3600)

// SeverityCounts — 알림 건수 집계.
type SeverityCounts struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// HealthSummary — 대시보드 상단에 쓸 단일 요약. Reasons 에 근거를 같이 담는다.
type HealthSummary struct {
	Level              string         `json:"level"`
	AuthoritativeLevel string         `json:"authoritative_level"`
	AlertLevel         string         `json:"alert_level"`
	Reasons            []string       `json:"reasons"`
	AlertCounts        SeverityCounts `json:"alert_counts"`
	AlertCountsRecent  SeverityCounts `json:"alert_counts_recent"`
}

// SummarizeClusterHealth 는 클러스터 건강도를 한 단계로 요약한다.
//
// alert-info 는 해소 여부 플래그가 없는 **이벤트 로그**다. 실측에서 state=running
// 인 node0 에 "Node Unreachable"(6일 전) 이 남아 있었다. 알림을 권위로 취급하면
// 정상 클러스터가 영구히 critical 로 보이므로, 권위 상태(node/vm/storage/unit/
// license 의 현재값)가 ok 인 동안에는 알림이 warning 까지만 올리도록 제한한다.
func SummarizeClusterHealth(unit *UnitInfo, nodes []NodeInfo, vms []VMInfo,
	storageGroups []StorageGroup, alerts []Alert, lic *LicenseInfo) HealthSummary {
	reasons := []string{}
	level := "ok"
	rank := map[string]int{"ok": 0, "warning": 1, "critical": 2}
	bump := func(newLevel, why string) {
		if rank[newLevel] > rank[level] {
			level = newLevel
		}
		reasons = append(reasons, why)
	}

	for _, n := range nodes {
		if n.State != "running" {
			bump("critical", "노드 "+strVal(n.Name)+" state="+n.State)
		} else if n.Mode != nil && *n.Mode != "normal" {
			bump("warning", "노드 "+strVal(n.Name)+" mode="+*n.Mode)
		} else if n.StandingState != nil && *n.StandingState != "normal" {
			bump("warning", "노드 "+strVal(n.Name)+" standing="+*n.StandingState)
		}
	}
	if len(nodes) > 0 && len(nodes) < 2 {
		bump("warning", "노드가 1대만 조회됨")
	}

	for _, v := range vms {
		if v.State == "running" && v.Redundancy == "simplex" {
			bump("warning", "VM "+strVal(v.Name)+" 이중화 상실(simplex)")
		} else if v.Redundancy == "down" {
			bump("critical", "VM "+strVal(v.Name)+" 인스턴스 전부 비활성")
		}
		if v.State == "running" && !v.DiskMirrored {
			bump("warning", "VM "+strVal(v.Name)+" 디스크 미러 비정상")
		}
	}

	if unit != nil && unit.Syncing {
		bump("warning", "유닛 동기화(syncing) 진행 중")
	}

	for _, g := range storageGroups {
		if g.UsedPct == nil {
			continue
		}
		pct := *g.UsedPct
		if pct >= SGCritPct {
			bump("critical", fmt.Sprintf("스토리지그룹 %s 사용률 %.1f%%", strVal(g.Name), pct))
		} else if pct >= SGWarnPct {
			bump("warning", fmt.Sprintf("스토리지그룹 %s 사용률 %.1f%%", strVal(g.Name), pct))
		}
	}

	if lic != nil {
		if lic.DaysLeft != nil && *lic.DaysLeft < 0 {
			bump("critical", "라이선스 만료됨")
		} else if lic.DaysLeft != nil && *lic.DaysLeft <= 30 {
			bump("warning", fmt.Sprintf("라이선스 %d일 남음", *lic.DaysLeft))
		}
		if lic.Activated != nil && !*lic.Activated {
			bump("warning", "라이선스 미활성")
		}
	}

	// 여기까지가 "권위 상태". 이하 알림 오버레이.
	authoritative := level
	counts := SeverityCounts{}
	recent := SeverityCounts{}
	addSev := func(c *SeverityCounts, sev string) {
		switch sev {
		case "critical":
			c.Critical++
		case "warning":
			c.Warning++
		default:
			c.Info++
		}
	}
	for _, a := range alerts {
		addSev(&counts, a.Severity)
		if a.AgeSecs != nil && *a.AgeSecs >= 0 && *a.AgeSecs <= AlertRecentSecs {
			addSev(&recent, a.Severity)
		}
	}

	alertLevel := "ok"
	if counts.Critical > 0 {
		alertLevel = "critical"
	} else if counts.Warning > 0 {
		alertLevel = "warning"
	}

	if counts.Critical > 0 {
		if authoritative == "ok" {
			bump("warning", fmt.Sprintf("critical 알림 %d건(권위 상태는 정상 — 해소된 과거 알림일 수 있음)", counts.Critical))
		} else {
			bump("critical", fmt.Sprintf("critical 알림 %d건", counts.Critical))
		}
	} else if counts.Warning > 0 {
		bump("warning", fmt.Sprintf("warning 알림 %d건", counts.Warning))
	}

	if len(reasons) > 20 {
		reasons = reasons[:20]
	}
	return HealthSummary{
		Level:              level,
		AuthoritativeLevel: authoritative,
		AlertLevel:         alertLevel,
		Reasons:            reasons,
		AlertCounts:        counts,
		AlertCountsRecent:  recent,
	}
}
