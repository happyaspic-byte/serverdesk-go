package poller

// 뷰 빌더 — fleet / topology (poller.py 의 build_cluster_view / build_topology 포트).
//
// 출력 JSON 스키마는 Python 폴러와 1:1 계약이다(프런트·외부 감시 도구가 소비).
// 그래서 이 파일의 맵 조립은 키 하나하나가 Python 의 dict 조립과 대응한다.

import (
	"fmt"
	"time"

	"serverdesk/internal/avcli"
	"serverdesk/internal/sshmetrics"
	"serverdesk/internal/topology"
)

// BuildClusterViews 는 클러스터 상태 스냅샷에서
//  1. /api/fleet 의 cluster 뷰(map — Python build_cluster_view 와 같은 키)
//  2. topology.BuildFullTopology 입력용 타입 뷰
//
// 를 함께 만든다. 같은 스냅샷에서 두 표현을 만들어야 두 엔드포인트가 어긋나지 않는다.
func BuildClusterViews(st *ClusterState, now time.Time) (map[string]any, topology.ClusterView) {
	snap := st.snapshot()
	nowF := float64(now.UnixNano()) / 1e9

	// 노드에서 받아온 TZ 오프셋으로 알림 시각을 UTC 로 보정한다. primary 노드를 우선
	// 쓰고, 없으면 아무 노드나(클러스터 내 노드는 같은 TZ 를 쓴다).
	var tzOff *int64
	tzName := ""
	var primIPs, allIPs []string
	for _, n := range snap.nodes {
		if n.IP == nil {
			continue
		}
		if n.Primary {
			primIPs = append(primIPs, *n.IP)
		}
		allIPs = append(allIPs, *n.IP)
	}
	for _, ip := range append(primIPs, allIPs...) {
		osm := snap.nodeOS[ip]
		if osm == nil {
			continue
		}
		if v, ok := numVal(osm["tz_offset_secs"]); ok {
			off := int64(v)
			tzOff = &off
			tzName = strVal(osm["tz_name"])
			break
		}
	}
	if tzOff == nil && st.Cfg.TzOffsetSecs != nil {
		tzOff = st.Cfg.TzOffsetSecs // 설정 폭백(SSH 불가 환경)
	}
	if tzOff != nil {
		avcli.ApplyAlertTimezone(snap.alerts, *tzOff, now)
	}

	ledByNode := map[string]any{}
	for _, e := range snap.led {
		if e.Node != nil {
			if e.Led != nil {
				ledByNode[*e.Node] = *e.Led
			} else {
				ledByNode[*e.Node] = nil
			}
		}
	}

	mergedNodes := make([]any, 0, len(snap.nodes))
	typedNodes := make([]topology.NodeView, 0, len(snap.nodes))
	spineByNode := map[string]*sshmetrics.Spine{}
	for _, n := range snap.nodes {
		nm := toJSONMap(n)
		ip := strVal(nm["ip"])
		osm := snap.nodeOS[ip]
		if osm == nil {
			osm = map[string]any{}
		}
		if sp := snap.nodeSpine[ip]; sp != nil {
			if name := strVal(nm["name"]); name != "" {
				spineByNode[name] = sp
			}
		}
		nm["os"] = osm
		nm["cpu_pct"] = osm["cpu_pct"]
		nm["mem_pct"] = osm["mem_pct"]
		nm["uptime_secs"] = osm["uptime_secs"]
		nm["temp_max_c"] = osm["temp_max_c"]
		sshOK := boolVal(osm["reachable"])
		snmpInfo := dictVal(osm["snmp"])
		snmpOK := boolVal(snmpInfo["reachable"])
		reachable := sshOK || snmpOK
		nm["reachable"] = reachable
		nm["ssh_reachable"] = sshOK
		nm["snmp_reachable"] = snmpOK
		// 프런트가 '멈춘 값'을 구분할 수 있게 최상위에 신선도를 노출한다.
		var metricsSource any
		if sshOK || (snmpOK && osm["source"] == "snmp") {
			metricsSource = osm["source"]
		}
		if !reachable {
			metricsSource = nil
		}
		nm["metrics_source"] = metricsSource
		var osAge any
		if ts, ok := numVal(osm["ts"]); ok && metricsSource != nil {
			osAge = round1(nowF - ts)
		}
		nm["os_age_secs"] = osAge
		var staleSince any
		if ss, ok := numVal(osm["stale_since"]); ok {
			staleSince = int64(ss)
		}
		nm["os_stale_since"] = staleSince
		if name := strVal(nm["name"]); name != "" {
			nm["led"] = ledByNode[name]
		} else {
			nm["led"] = nil
		}
		if ip != "" {
			nm["history"] = map[string]any{
				"cpu": st.RingFor(ip, "cpu").Series(),
				"mem": st.RingFor(ip, "mem").Series(),
			}
		} else {
			nm["history"] = map[string]any{"cpu": []any{}, "mem": []any{}}
		}
		mergedNodes = append(mergedNodes, nm)
		typedNodes = append(typedNodes, typedNodeView(n, osm, nm))
	}

	health := avcli.SummarizeClusterHealth(snap.unit, snap.nodes, snap.vms,
		snap.sgroups, snap.alerts, snap.license)

	// Collection ages must describe the same locked snapshot as the device data.
	// Reading ClusterState again here could observe Mark("fast") after snapshot()
	// and falsely make a pre-success view look notification-ready.
	fastAge := snapshotTierAge(snap, "fast", nowF)
	slowAge := snapshotTierAge(snap, "slow", nowF)
	// fast 티어가 주기의 3배 넘게 갱신되지 않으면 stale.
	fastIV := float64(st.Cfg.Intervals.Fast)
	stale := fastAge == nil || *fastAge > fastIV*3

	// 데이터가 아예 없는 클러스터를 "ok" 로 보고하면 안 된다(관리 IP 도달 불가 등).
	if len(mergedNodes) == 0 {
		health.Level = "unknown"
		health.Reasons = append([]string{"클러스터에서 노드 정보를 가져오지 못함(수집 실패)"}, health.Reasons...)
	} else if stale && health.Level == "ok" {
		health.Level = "warning"
		health.Reasons = append([]string{"수집이 지연되어 데이터가 오래됨(stale)"}, health.Reasons...)
	}

	// NIC<->네트워크 확정 매핑. 노드 spine 설정(SSH)에서 읽고, 설정 파일에 명시가
	// 있으면 그쪽이 우선이다.
	nicMap := BuildNICNetworkMap(spineByNode, snap.networks, st.Cfg.NicNetworkMap)

	unit := toJSONMap(snap.unit)
	lic := toJSONMap(snap.license)
	platform := st.GetPlatform()
	if platform == "" {
		platform = "unknown"
	}
	name := st.DisplayName()
	if name == "" {
		name = st.Key
	}

	errs := map[string]any{}
	for k, v := range snap.tierErr {
		if v != "" {
			errs[k] = v
		}
	}
	lastOK := map[string]any{}
	for k, v := range snap.tierTS {
		lastOK[k] = int64(v)
	}

	view := map[string]any{
		"key":              st.Key,
		"name":             name,
		"mgmt_ip":          st.Cfg.MgmtIP,
		"platform":         platform,
		"version":          unit["version"],
		"uuid":             unit["uuid"],
		"tz_offset_secs":   ptrInt64AsAny(tzOff),
		"tz_name":          tzNameOrNil(tzName),
		"nic_network_map":  nicMap,
		"stale":            stale,
		"health":           toJSONMap(health),
		"unit":             unit,
		"nodes":            mergedNodes,
		"vms":              toJSONAny(snap.vms),
		"networks":         toJSONAny(snap.networks),
		"storage_groups":   toJSONAny(snap.sgroups),
		"volumes":          toJSONAny(snap.volumes),
		"image_containers": toJSONAny(snap.containers),
		"alerts":           toJSONAny(snap.alerts),
		"traps":            TrapView(st.TrapsSnapshot(), tzOff, st.trapViewMax),
		"license":          lic,
		"collection": map[string]any{
			"fast_age_secs":   fastAge,
			"slow_age_secs":   slowAge,
			"static_age_secs": snapshotTierAge(snap, "static", nowF),
			"errors":          errs,
			"last_success":    lastOK,
		},
	}

	typed := topology.ClusterView{
		Key:             st.Key,
		Platform:        platform,
		Unit:            typedUnitView(snap.unit),
		Nodes:           typedNodes,
		Networks:        typedNetworkViews(snap.networks),
		StorageGroups:   typedStorageGroupViews(snap.sgroups),
		Volumes:         typedVolumeViews(snap.volumes),
		ImageContainers: typedContainerViews(snap.containers),
		VMs:             typedVMViews(snap.vms),
		Alerts:          typedAlertViews(snap.alerts),
		License:         typedLicenseView(snap.license),
		NICNetworkMap:   TypedNICNetworkMap(nicMap),
	}
	return view, typed
}

func snapshotTierAge(s snapshot, tier string, now float64) *float64 {
	ts, ok := s.tierTS[tier]
	if !ok {
		return nil
	}
	age := round1(now - ts)
	return &age
}

// --- 타입 뷰 변환기(topology 패키지 입력용) ----------------------------------
// 어댑터 계약(topology/adapter.go)이 문자열 라벨을 기대하는 필드만 변환하고,
// 나머지는 avcli 정규화 값을 그대로 옮긴다.

func strp(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func typedNodeView(n avcli.NodeInfo, osm map[string]any, merged map[string]any) topology.NodeView {
	placements := make([]topology.VMPlacementRef, 0, len(n.VMPlacements))
	for _, p := range n.VMPlacements {
		placements = append(placements, topology.VMPlacementRef{Name: strp(p.Name)})
	}
	primary := n.Primary
	nv := topology.NodeView{
		Name:          strp(n.Name),
		ID:            strp(n.ID),
		State:         n.State,
		SubState:      strp(n.SubState),
		StandingState: strp(n.StandingState),
		Mode:          strp(n.Mode),
		Primary:       &primary,
		Manufacturer:  strp(n.Manufacturer),
		Model:         strp(n.Model),
		MemoryRaw:     strp(n.MemoryRaw),
		IP:            strp(n.IP),
		Gateway:       strp(n.Gateway),
		DNS:           append([]string{}, n.DNS...),
		VMPlacements:  placements,
		OS:            osToNodeOSView(osm),
	}
	if n.Cpus != nil {
		nv.CPUs = fmt.Sprintf("%d", *n.Cpus)
	}
	if v, ok := numVal(merged["cpu_pct"]); ok {
		nv.CPUPct = &v
	}
	if v, ok := numVal(merged["mem_pct"]); ok {
		nv.MemPct = &v
	}
	if v, ok := numVal(merged["uptime_secs"]); ok {
		nv.UptimeSecs = &v
	}
	if v, ok := numVal(merged["temp_max_c"]); ok {
		nv.TempMaxC = &v
	}
	return nv
}

// osToNodeOSView 는 nodeOS 맵을 topology.NodeOSView 계약으로 변환한다.
// 맵의 하위 값(links/net/temps)은 이미 sshmetrics 의 JSON 태그 모양이라
// 서브트리 라운드트립으로 옮긴다(JSON null 은 문자열 필드에 무해하다).
func osToNodeOSView(osm map[string]any) *topology.NodeOSView {
	if len(osm) == 0 {
		return nil
	}
	out := &topology.NodeOSView{Source: strVal(osm["source"])}
	if l, ok := osm["links"].([]any); ok && len(l) > 0 {
		var links []topology.LinkView
		if err := jsonRoundTrip(l, &links); err == nil {
			out.Links = links
		}
	}
	if l, ok := osm["net"].([]any); ok && len(l) > 0 {
		var nets []topology.NetDevView
		if err := jsonRoundTrip(l, &nets); err == nil {
			out.Net = nets
		}
	}
	if l, ok := osm["temps"].([]any); ok && len(l) > 0 {
		var temps []topology.TempView
		if err := jsonRoundTrip(l, &temps); err == nil {
			out.Temps = temps
		}
	}
	return out
}

func typedUnitView(u *avcli.UnitInfo) topology.UnitView {
	if u == nil {
		return topology.UnitView{Resources: topology.ResourcesView{}}
	}
	rv := topology.ResourcesView{}
	if u.Resources.TotalVcpus != nil {
		rv.TotalVCPUs = fmt.Sprintf("%d", *u.Resources.TotalVcpus)
	}
	if u.Resources.UsedVcpus != nil {
		rv.UsedVCPUs = fmt.Sprintf("%v", *u.Resources.UsedVcpus)
	}
	rv.TotalMemoryRaw = strp(u.Resources.TotalMemoryRaw)
	rv.UsedMemoryRaw = strp(u.Resources.UsedMemoryRaw)
	syncing := u.Syncing
	return topology.UnitView{
		Name:       strp(u.Name),
		ID:         strp(u.ID),
		Version:    strp(u.Version),
		UUID:       strp(u.UUID),
		Address:    strp(u.Address),
		Netmask:    strp(u.Netmask),
		Configured: u.Configured,
		Syncing:    &syncing,
		Resources:  rv,
	}
}

func typedNetworkViews(nets []avcli.SharedNetwork) []topology.NetworkView {
	out := make([]topology.NetworkView, 0, len(nets))
	for _, n := range nets {
		out = append(out, topology.NetworkView{
			Name:          strp(n.Name),
			ID:            strp(n.ID),
			FaultTolerant: strp(n.FaultTolerant),
			Role:          strp(n.Role),
			BandwidthRaw:  strp(n.BandwidthRaw),
			MTU:           n.Mtu,
		})
	}
	return out
}

func typedStorageGroupViews(sgs []avcli.StorageGroup) []topology.StorageGroupView {
	out := make([]topology.StorageGroupView, 0, len(sgs))
	for _, g := range sgs {
		disks := make([]topology.DiskView, 0, len(g.Disks))
		for _, d := range g.Disks {
			disks = append(disks, topology.DiskView{
				Name:          strp(d.Name),
				ID:            strp(d.ID),
				SizeBytes:     d.SizeBytes,
				UsedBytes:     d.UsedBytes,
				StandingState: strp(d.StandingState),
				NodeName:      strp(d.NodeName),
			})
		}
		out = append(out, topology.StorageGroupView{
			Name:                    strp(g.Name),
			ID:                      strp(g.ID),
			SizeBytes:               g.SizeBytes,
			UsedBytes:               g.UsedBytes,
			LogicalSectorSizeBytes:  g.LogicalSectorSizeBytes,
			PhysicalSectorSizeBytes: g.PhysicalSectorSizeBytes,
			DiskType:                strp(g.DiskType),
			Disks:                   disks,
		})
	}
	return out
}

func typedVolumeViews(vols []avcli.Volume) []topology.VolumeView {
	out := make([]topology.VolumeView, 0, len(vols))
	for _, v := range vols {
		out = append(out, topology.VolumeView{
			Name:             strp(v.Name),
			ID:               strp(v.ID),
			SizeRaw:          strp(v.SizeRaw),
			SectorSizeBytes:  v.SectorSizeBytes,
			Bootable:         v.Bootable,
			StorageGroupName: strp(v.StorageGroupName),
			StorageGroupID:   strp(v.StorageGroupID),
		})
	}
	return out
}

func typedContainerViews(cs []avcli.ImageContainer) []topology.ImageContainerView {
	out := make([]topology.ImageContainerView, 0, len(cs))
	for _, c := range cs {
		out = append(out, topology.ImageContainerView{
			Name:             strp(c.Name),
			ID:               strp(c.ID),
			SizeRaw:          strp(c.SizeRaw),
			UsedRaw:          strp(c.UsedRaw),
			IsLocal:          c.IsLocal,
			HasFilesystem:    c.HasFilesystem,
			StorageGroupName: strp(c.StorageGroupName),
			StorageGroupID:   strp(c.StorageGroupID),
		})
	}
	return out
}

func typedVMViews(vms []avcli.VMInfo) []topology.VMView {
	out := make([]topology.VMView, 0, len(vms))
	for _, vm := range vms {
		volViews := make([]topology.VMVolumeView, 0, len(vm.Volumes))
		for _, v := range vm.Volumes {
			imgs := make([]topology.VMDiskImageView, 0, len(v.DiskImages))
			for _, di := range v.DiskImages {
				imgs = append(imgs, topology.VMDiskImageView{
					Name:         strp(di.Name),
					ID:           strp(di.ID),
					EnableStatus: strp(di.EnableStatus),
					NodeName:     strp(di.NodeName),
					NodeID:       strp(di.NodeID),
				})
			}
			isCdrom := v.IsCdrom
			volViews = append(volViews, topology.VMVolumeView{
				Name:          strp(v.Name),
				ID:            strp(v.ID),
				SizeRaw:       strp(v.SizeRaw),
				SectorSizeRaw: strp(v.SectorSizeRaw),
				Device:        strp(v.Device),
				DeviceID:      strp(v.DeviceID),
				IsCdrom:       &isCdrom,
				DiskImages:    imgs,
			})
		}
		ifaces := make([]topology.VMInterfaceInput, 0, len(vm.Interfaces))
		for _, i := range vm.Interfaces {
			ifaces = append(ifaces, topology.VMInterfaceInput{
				SharedNetwork: strp(i.SharedNetwork),
				MAC:           strp(i.MAC),
				Net0Status:    strp(i.Net0Status),
				Net1Status:    strp(i.Net1Status),
			})
		}
		alinks := make([]topology.VMALinkView, 0, len(vm.ALinks))
		for _, a := range vm.ALinks {
			alinks = append(alinks, topology.VMALinkView{
				Network:      a.Network,
				Role:         strp(a.Role),
				BandwidthRaw: strp(a.BandwidthRaw),
			})
		}
		insts := make([]topology.VMInstanceView, 0, len(vm.Instances))
		for _, in := range vm.Instances {
			insts = append(insts, topology.VMInstanceView{
				Name:           strp(in.Name),
				ID:             strp(in.ID),
				EnableStatus:   strp(in.EnableStatus),
				ConfigVhostNet: in.ConfigVhostNet,
				MTBFStatus:     strp(in.MtbfStatus),
				UUID:           strp(in.UUID),
				NodeName:       strp(in.NodeName),
				NodeID:         strp(in.NodeID),
			})
		}
		vv := topology.VMView{
			Name:          strp(vm.Name),
			InternalName:  strp(vm.InternalName),
			ID:            strp(vm.ID),
			UUID:          strp(vm.UUID),
			BootType:      strp(vm.BootType),
			MemoryRaw:     strp(vm.MemoryRaw),
			OSType:        strp(vm.OsType),
			State:         vm.State,
			StandingState: strp(vm.StandingState),
			HAMode:        strp(vm.HaMode),
			Interfaces:    ifaces,
			Volumes:       volViews,
			ALinks:        alinks,
			Instances:     insts,
		}
		if vm.Cpus != nil {
			vv.CPUs = fmt.Sprintf("%d", *vm.Cpus)
		}
		out = append(out, vv)
	}
	return out
}

func typedAlertViews(alerts []avcli.Alert) []topology.AlertView {
	out := make([]topology.AlertView, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, topology.AlertView{
			Name:        strp(a.Name),
			ID:          strp(a.ID),
			Time:        strp(a.Time),
			Description: strp(a.Description),
			// ClassifyAlert 는 avcli 원문 숫자를 기대한다
			SeverityRaw: strp(a.SeverityRaw),
		})
	}
	return out
}

func typedLicenseView(lic *avcli.LicenseInfo) *topology.LicenseInput {
	if lic == nil {
		return nil
	}
	var expires *bool
	exp := lic.Expires
	expires = &exp
	return &topology.LicenseInput{
		Name:        strp(lic.Name),
		ID:          strp(lic.ID),
		Type:        strp(lic.Type),
		Edition:     strp(lic.Edition),
		InstallDate: strp(lic.InstallDate),
		ExpireDate:  lic.ExpireDate,
		Expires:     expires,
		Activated:   lic.Activated,
	}
}

func ptrInt64AsAny(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func tzNameOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}
