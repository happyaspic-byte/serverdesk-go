package topology

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// avcli XML -> 정규화 ClusterInput (참조 구현 / 테스트용 내부 헬퍼)
// ---------------------------------------------------------------------------

// xelem 은 임의의 avcli XML 을 담는 최소 트리다.
// encoding/xml 의 구조체 태그로는 a-links 처럼 동적 태그명을 받을 수 없어
// 범용 트리로 파싱한다.
type xelem struct {
	Name     string
	Text     string
	Children []*xelem
}

// parseXMLDoc 은 XML 문자열을 파싱한다. 빈 응답/파싱 실패는 nil 로 흡수한다
// (removable-disk-info 는 0바이트로 온다).
func parseXMLDoc(s string) *xelem {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	dec := xml.NewDecoder(strings.NewReader(s))
	var stack []*xelem
	var root *xelem
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil
		}
		switch t := tok.(type) {
		case xml.StartElement:
			el := &xelem{Name: t.Name.Local}
			if len(stack) > 0 {
				top := stack[len(stack)-1]
				top.Children = append(top.Children, el)
			} else if root == nil {
				root = el
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	return root
}

// find 는 "a/b/c" 경로의 첫 번째 자식 요소를 찾는다 (ElementTree find 의 축약판).
func (e *xelem) find(path string) *xelem {
	if e == nil {
		return nil
	}
	cur := e
	for _, part := range strings.Split(path, "/") {
		var next *xelem
		for _, ch := range cur.Children {
			if ch.Name == part {
				next = ch
				break
			}
		}
		if next == nil {
			return nil
		}
		cur = next
	}
	return cur
}

// findAll 은 "a/b" 경로의 모든 자식 요소를 찾는다 (ElementTree findall 의 축약판).
// nil 수신자(없는 local-networks 등)는 빈 결과로 흡수한다.
func (e *xelem) findAll(path string) []*xelem {
	if e == nil {
		return nil
	}
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		var out []*xelem
		for _, ch := range e.Children {
			if ch.Name == parts[0] {
				out = append(out, ch)
			}
		}
		return out
	}
	var out []*xelem
	for _, ch := range e.Children {
		if ch.Name == parts[0] {
			out = append(out, ch.findAll(strings.Join(parts[1:], "/"))...)
		}
	}
	return out
}

// xtxt 는 경로의 텍스트를 공백 trim 해서 돌려준다. 요소가 없거나 비어 있으면 "".
func xtxt(el *xelem, path string) string {
	if el == nil {
		return ""
	}
	f := el.find(path)
	if f == nil {
		return ""
	}
	return strings.TrimSpace(f.Text)
}

// NormalizeOptions 는 NormalizeFromXML 의 선택 인자다.
type NormalizeOptions struct {
	Platform      string                    // 비우면 노드 manufacturer/model 로 추정
	Site          *SiteRef                  // fleet 조립 시 사이트 계층
	NodeMetrics   map[string]*NodeOSMetrics // SSH/collectd 보강
	NICNetworkMap NICNetworkMap             // 노드 spine 설정 확정 매핑
}

// NormalizeFromXML 은 avcli XML 문자열 맵을 ClusterInput 으로 정규화한다.
//
// xmls 는 {"node-info": "<xml>", "vm-info": ..., "unit-info": ..., ...} 형태.
// 빈 응답/파싱 실패는 내부에서 흡수한다(0바이트로 오는 케이스 대비).
func NormalizeFromXML(clusterID string, xmls map[string]string, opts *NormalizeOptions) ClusterInput {
	var platform string
	var site *SiteRef
	var nodeMetrics map[string]*NodeOSMetrics
	var nicMap NICNetworkMap
	if opts != nil {
		platform = opts.Platform
		site = opts.Site
		nodeMetrics = opts.NodeMetrics
		nicMap = opts.NICNetworkMap
	}
	out := ClusterInput{
		ClusterID:     clusterID,
		Platform:      platform,
		Site:          site,
		NodeMetrics:   nodeMetrics,
		NICNetworkMap: nicMap,
	}

	// unit-info
	if r := parseXMLDoc(xmls["unit-info"]); r != nil {
		res := r.find("resources")
		out.Unit = UnitInput{
			Name:        xtxt(r, "name"),
			ID:          xtxt(r, "id"),
			Version:     xtxt(r, "version"),
			UUID:        xtxt(r, "uuid"),
			Configured:  ParseBool(xtxt(r, "configured"), nil),
			Syncing:     parseBoolDef(xtxt(r, "syncing"), false),
			Address:     xtxt(r, "address"),
			Netmask:     xtxt(r, "netmask"),
			TotalVCPUs:  xtxt(res, "total-vcpus"),
			UsedVCPUs:   xtxt(res, "used-vcpus"),
			TotalMemory: xtxt(res, "total-memory"),
			UsedMemory:  xtxt(res, "used-memory"),
		}
	}

	// node-info
	placement := map[string][]string{} // vm name -> [node name]
	if r := parseXMLDoc(xmls["node-info"]); r != nil {
		for _, nd := range r.findAll("node") {
			ln := nd.find("local-networks/local-network")
			name := xtxt(nd, "name")
			var dns []string
			for _, a := range ln.findAll("dns/address") {
				dns = append(dns, a.Text) // 원본은 strip 하지 않는다
			}
			ip, gw := "", ""
			if ln != nil {
				ip = xtxt(ln, "ip-address")
				gw = xtxt(ln, "gateway-address")
			}
			out.Nodes = append(out.Nodes, NodeInput{
				Name:          name,
				ID:            xtxt(nd, "id"),
				State:         xtxt(nd, "state"),
				SubState:      xtxt(nd, "sub-state"),
				StandingState: xtxt(nd, "standing-state"),
				Mode:          xtxt(nd, "mode"),
				Primary:       parseBoolDef(xtxt(nd, "primary"), false),
				Manufacturer:  xtxt(nd, "manufacturer"),
				Model:         xtxt(nd, "model"),
				CPUs:          xtxt(nd, "cpus"),
				Memory:        xtxt(nd, "memory"),
				IPAddress:     ip,
				Gateway:       gw,
				DNS:           dns,
			})
			for _, v := range nd.findAll("virtual-machines/virtual-machine") {
				vn := xtxt(v, "name")
				placement[vn] = append(placement[vn], name)
			}
		}
		if out.Platform == "" && len(out.Nodes) > 0 {
			// 플랫폼 추정: Stratus/ztC Edge 조합만 ztcedge, 나머지는 everrun
			if out.Nodes[0].Manufacturer == "Stratus" && out.Nodes[0].Model == "ztC Edge" {
				out.Platform = "ztcedge"
			} else {
				out.Platform = "everrun"
			}
		}
	}

	// network-info
	if r := parseXMLDoc(xmls["network-info"]); r != nil {
		for _, net := range r.findAll("shared-network") {
			out.Networks = append(out.Networks, NetworkInput{
				Name:          xtxt(net, "name"),
				ID:            xtxt(net, "id"),
				FaultTolerant: xtxt(net, "fault-tolerant"),
				Role:          xtxt(net, "role"),
				Bandwidth:     xtxt(net, "bandwidth"),
				MTU:           parseInt64OrNil(xtxt(net, "mtu")),
			})
		}
	}

	// storage-info-v2 --disks --volumes (없으면 구형 storage-info)
	r := parseXMLDoc(xmls["storage-info-v2"])
	if r == nil {
		r = parseXMLDoc(xmls["storage-info"])
	}
	if r != nil {
		for _, sg := range r.findAll("storage-group") {
			var disks []DiskInput
			for _, d := range sg.findAll("disks/disk") {
				disks = append(disks, DiskInput{
					Name:          xtxt(d, "name"),
					ID:            xtxt(d, "id"),
					SizeBytes:     ParseSize(xtxt(d, "size")),
					UsedBytes:     ParseSize(xtxt(d, "used-size")),
					StandingState: xtxt(d, "standing-state"),
					Node:          xtxt(d, "node"),
				})
			}
			lss := xtxt(sg, "logical-sector-size")
			if lss == "" {
				lss = xtxt(sg, "sector-size")
			}
			out.StorageGroups = append(out.StorageGroups, StorageGroupInput{
				Name:               xtxt(sg, "name"),
				ID:                 xtxt(sg, "id"),
				SizeBytes:          ParseSize(xtxt(sg, "size")),
				UsedBytes:          ParseSize(xtxt(sg, "size-used")),
				LogicalSectorSize:  strOrNil(lss),
				PhysicalSectorSize: strOrNil(xtxt(sg, "physical-sector-size")),
				DiskType:           xtxt(sg, "disk-type"),
				Disks:              disks,
			})
		}
	}

	// volume-info
	if r := parseXMLDoc(xmls["volume-info"]); r != nil {
		for _, v := range r.findAll("volume") {
			out.Volumes = append(out.Volumes, VolumeInfoInput{
				Name:         xtxt(v, "name"),
				ID:           xtxt(v, "id"),
				Size:         xtxt(v, "size"),
				SectorSize:   strOrNil(xtxt(v, "sector-size")),
				Bootable:     ParseBool(xtxt(v, "bootable"), nil),
				StorageGroup: xsgRef(v.find("storage-group")),
			})
		}
	}

	// image-container-info
	if r := parseXMLDoc(xmls["image-container-info"]); r != nil {
		for _, ic := range r.findAll("image-container") {
			out.ImageContainers = append(out.ImageContainers, ImageContainerInput{
				Name:          xtxt(ic, "name"),
				ID:            xtxt(ic, "id"),
				Size:          xtxt(ic, "size"),
				SizeUsed:      xtxt(ic, "size-used"),
				IsLocal:       ParseBool(xtxt(ic, "isLocal"), nil),
				HasFilesystem: ParseBool(xtxt(ic, "hasFileSystem"), nil),
				StorageGroup:  xsgRef(ic.find("storage-group")),
			})
		}
	}

	// vm-info
	if r := parseXMLDoc(xmls["vm-info"]); r != nil {
		for _, vm := range r.findAll("virtual-machine") {
			name := xtxt(vm, "name")
			var interfaces []VMInterfaceInput
			for _, itf := range vm.findAll("interfaces/interface") {
				interfaces = append(interfaces, VMInterfaceInput{
					SharedNetwork: xtxt(itf, "shared-network"),
					MAC:           xtxt(itf, "MAC"),
					Net0Status:    xtxt(itf, "net0-status"),
					Net1Status:    xtxt(itf, "net1-status"),
				})
			}
			var volumes []VMVolumeInput
			for _, vol := range vm.findAll("volumes/volume") {
				var imgs []DiskImageInput
				for _, im := range vol.findAll("disk-images/disk-image") {
					imgs = append(imgs, DiskImageInput{
						Name:         xtxt(im, "name"),
						ID:           xtxt(im, "id"),
						EnableStatus: xtxt(im, "enable-status"),
						Node:         xtxt(im, "node/name"),
						NodeID:       xtxt(im, "node/id"),
					})
				}
				volumes = append(volumes, VMVolumeInput{
					Name:       xtxt(vol, "name"),
					ID:         xtxt(vol, "id"),
					Size:       xtxt(vol, "size"),
					SectorSize: xtxt(vol, "sector-size"),
					Device:     xtxt(vol, "device"),
					DeviceID:   xtxt(vol, "device-id"),
					DiskImages: imgs,
				})
			}
			// a-links: 태그명이 곧 네트워크 이름인 동적 태그
			var alinks []VMALinkInput
			if al := vm.find("a-links"); al != nil {
				for _, child := range al.Children {
					alinks = append(alinks, VMALinkInput{
						Network:   child.Name,
						Role:      xtxt(child, "role"),
						Bandwidth: xtxt(child, "bandwidth"),
					})
				}
			}
			var insts []VMInstanceInput
			for _, lv := range vm.findAll("local-virtual-machines/local-virtual-machine") {
				insts = append(insts, VMInstanceInput{
					Name:           xtxt(lv, "name"),
					ID:             xtxt(lv, "ID"), // 대문자 ID 주의
					EnableStatus:   xtxt(lv, "enable-status"),
					ConfigVhostNet: ParseBool(xtxt(lv, "config-vhost-net"), nil),
					MTBF:           xtxt(lv, "mtbf/status"),
					UUID:           xtxt(lv, "uuid"),
					Node:           xtxt(lv, "node/name"),
					NodeID:         xtxt(lv, "node/id"),
				})
			}
			out.VMs = append(out.VMs, VMInput{
				Name:           name,
				InternalName:   xtxt(vm, "internal-name"),
				ID:             xtxt(vm, "id"),
				UUID:           xtxt(vm, "uuid"),
				CPUs:           xtxt(vm, "cpus"),
				BootType:       xtxt(vm, "boot-type"),
				Memory:         xtxt(vm, "memory"),
				Type:           xtxt(vm, "type"),
				State:          xtxt(vm, "state"),
				StandingState:  xtxt(vm, "standing-state"),
				FaultTolerant:  xtxt(vm, "fault-tolerant"),
				Interfaces:     interfaces,
				Volumes:        volumes,
				ALinks:         alinks,
				Instances:      insts,
				PlacementNodes: placement[name],
			})
		}
	}

	// alert-info
	if r := parseXMLDoc(xmls["alert-info"]); r != nil {
		for _, a := range r.findAll("alert") {
			out.Alerts = append(out.Alerts, AlertInput{
				Name:        xtxt(a, "name"),
				ID:          xtxt(a, "id"),
				Time:        xtxt(a, "time"),
				Description: xtxt(a, "description"),
				Severity:    xtxt(a, "severity"),
			})
		}
	}

	// license-info
	if r := parseXMLDoc(xmls["license-info"]); r != nil {
		if lic := r.find("license"); lic != nil {
			expires := parseBoolDef(xtxt(lic, "expires"), false)
			out.License = &LicenseInput{
				Name:        xtxt(lic, "name"),
				ID:          xtxt(lic, "id"),
				Type:        xtxt(lic, "type"),
				Edition:     xtxt(lic, "edition"),
				InstallDate: xtxt(lic, "install-date"),
				// expires=false 면 expire-date 요소 자체가 없다 (Edge)
				ExpireDate: ptrIfExpires(xtxt(lic, "expire-date"), expires),
				Expires:    &expires,
				Activated:  ParseBool(xtxt(lic, "activated"), nil),
			}
		}
	}

	return out
}

// parseBoolDef 는 실패 시 def 를 돌려주는 ParseBool 래퍼다.
func parseBoolDef(s string, def bool) bool {
	v := ParseBool(s, &def)
	return *v
}

// parseInt64OrNil 은 정수 문자열을 *int64 로 바꾼다. 파싱 불가/빈 값이면 nil.
func parseInt64OrNil(s string) *int64 {
	if s == "" {
		return nil
	}
	var v int64
	for _, ch := range strings.TrimSpace(s) {
		if ch < '0' || ch > '9' {
			return nil
		}
		v = v*10 + int64(ch-'0')
	}
	return &v
}

// xsgRef 는 storage-group 요소를 SGRef 로 바꾼다. 요소가 없으면 nil.
func xsgRef(sg *xelem) *SGRef {
	if sg == nil {
		return nil
	}
	return &SGRef{Name: xtxt(sg, "name"), ID: xtxt(sg, "id")}
}

// ptrIfExpires 는 expires=true 일 때만 만료일 문자열을 돌려준다.
func ptrIfExpires(s string, expires bool) *string {
	if !expires || s == "" {
		return nil
	}
	return &s
}

// ---------------------------------------------------------------------------
// 샘플 XML 디렉터리 로더 (단독 검증용)
// ---------------------------------------------------------------------------

// cmdFiles 는 명령 -> 후보 파일명이다. --disks --volumes 로 받은 응답을 우선 사용한다.
var cmdFiles = []struct {
	cmd        string
	candidates []string
}{
	{"unit-info", []string{"unit-info"}},
	{"node-info", []string{"node-info"}},
	{"vm-info", []string{"vm-info"}},
	{"network-info", []string{"network-info"}},
	{"storage-info-v2", []string{"storage-info-v2-full", "storage-info-v2"}},
	{"storage-info", []string{"storage-info"}},
	{"volume-info", []string{"volume-info"}},
	{"image-container-info", []string{"image-container-info"}},
	{"alert-info", []string{"alert-info"}},
	{"license-info", []string{"license-info"}},
}

// LoadXMLDir 은 <dir>/<prefix><command>.xml 들을 읽어 xmls 맵으로 반환한다.
// 없거나 0바이트인 파일은 건너끰다.
func LoadXMLDir(dir, prefix string) map[string]string {
	out := map[string]string{}
	for _, cf := range cmdFiles {
		for _, cand := range cf.candidates {
			fp := filepath.Join(dir, prefix+cand+".xml")
			st, err := os.Stat(fp)
			if err != nil || st.Size() == 0 {
				continue
			}
			data, err := os.ReadFile(fp)
			if err != nil {
				continue
			}
			out[cf.cmd] = string(data)
			break
		}
	}
	return out
}
