package topology

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// 상태 판정 규칙 (R1~R4, R6~R10 의 개별 객체 판정부)
// ---------------------------------------------------------------------------

// nodeStatus 는 물리 노드의 권위 상태를 판정한다 (R6/R7/R8 의 노드 측).
func nodeStatus(nd NodeInput) (string, []string) {
	state := strings.ToLower(nd.State)
	standing := strings.ToLower(nd.StandingState)
	mode := strings.ToLower(nd.Mode)
	var reasons []string
	st := "ok"
	if state != "running" {
		st = "critical"
		s := state
		if s == "" {
			s = "?"
		}
		reasons = append(reasons, fmt.Sprintf("노드 state=%s (running 아님)", s))
	}
	if mode != "" && mode != "normal" {
		st = StatusMax(st, "warning")
		reasons = append(reasons, fmt.Sprintf("노드 mode=%s (유지보수 등 비정상 모드)", mode))
	}
	if standing != "" && standing != "normal" {
		st = StatusMax(st, "degraded")
		reasons = append(reasons, fmt.Sprintf("노드 standing-state=%s", standing))
	}
	if state == "" {
		st = StatusMax(st, "unknown")
	}
	return st, reasons
}

// vmRedundancy 는 VM 이중화를 판정한다 (R1/R2).
// 반환: (redundancy_state, status, reasons)
func vmRedundancy(vm VMInput) (string, string, []string) {
	insts := vm.Instances
	var enabled, badMTBF []VMInstanceInput
	for _, i := range insts {
		if strings.ToUpper(i.EnableStatus) == "ENABLED" {
			enabled = append(enabled, i)
		}
		mtbf := i.MTBF
		if mtbf == "" {
			mtbf = "normal"
		}
		if strings.ToLower(mtbf) != "normal" {
			badMTBF = append(badMTBF, i)
		}
	}
	var reasons []string
	var rstate, st string
	switch {
	case len(insts) >= 2 && len(enabled) >= 2 && len(badMTBF) == 0:
		rstate, st = "protected", "ok"
	case len(enabled) == 1:
		rstate, st = "simplex", "degraded"
		reasons = append(reasons, "로컬 VM 인스턴스 중 1개만 ENABLED — 이중화 상실(심플렉스)")
	case len(enabled) == 0:
		rstate, st = "unprotected", "critical"
		reasons = append(reasons, "ENABLED 로컬 VM 인스턴스 없음")
	default:
		rstate, st = "protected", "ok"
	}
	if len(badMTBF) > 0 {
		st = StatusMax(st, "degraded")
		reasons = append(reasons, fmt.Sprintf("mtbf 상태 비정상 인스턴스 %d개", len(badMTBF)))
	}
	return rstate, st, reasons
}

// vmStatus 는 VM 의 실행 상태를 판정한다.
func vmStatus(vm VMInput) (string, []string) {
	state := strings.ToLower(vm.State)
	standing := strings.ToLower(vm.StandingState)
	var reasons []string
	st := "ok"
	if state == "stopped" || state == "shutoff" || state == "shut off" {
		st = "warning"
		reasons = append(reasons, fmt.Sprintf("VM 정지 상태(state=%s)", state))
	} else if state != "" && state != "running" {
		st = "degraded"
		reasons = append(reasons, fmt.Sprintf("VM state=%s", state))
	}
	if standing != "" && standing != "normal" {
		st = StatusMax(st, "degraded")
		reasons = append(reasons, fmt.Sprintf("VM standing-state=%s", standing))
	}
	return st, reasons
}

// volumeMirrorStatus 는 볼륨 미러 상태를 판정한다 (R3/R4).
// 반환: (mirror_state, status, reasons)
func volumeMirrorStatus(vol VMVolumeInput, syncing bool) (string, string, []string) {
	imgs := vol.DiskImages
	var enabled []DiskImageInput
	for _, i := range imgs {
		if strings.ToUpper(i.EnableStatus) == "ENABLED" {
			enabled = append(enabled, i)
		}
	}
	var reasons []string
	switch {
	case len(imgs) == 0:
		return "unknown", "unknown", []string{"디스크 이미지 정보 없음"}
	case len(enabled) >= 2 && !syncing:
		return "mirrored", "ok", reasons
	case len(enabled) >= 2 && syncing:
		return "syncing", "warning", []string{"유닛 동기화 진행 중(unit syncing=true)"}
	case len(enabled) == 1:
		return "simplex", "degraded", []string{"디스크 이미지 1개만 ENABLED — 미러 이중화 상실"}
	}
	return "broken", "critical", []string{"ENABLED 디스크 이미지 없음"}
}

// sgStatus 는 스토리지 그룹 상태를 판정한다 (R10).
// 반환: (status, reasons, usedPct). 용량 정보가 없으면 디스크 점검 없이 즉시 unknown.
func sgStatus(sg StorageGroupInput) (string, []string, *float64) {
	u := Pct(sg.UsedBytes, sg.SizeBytes)
	var reasons []string
	if u == nil {
		return "unknown", []string{"스토리지 그룹 용량 정보 없음"}, nil
	}
	st := "ok"
	if *u >= sgCritPct {
		st = "critical"
		reasons = append(reasons, fmt.Sprintf("스토리지 그룹 사용률 %.1f%% (>=%d%%)", *u, sgCritPct))
	} else if *u >= sgWarnPct {
		st = "warning"
		reasons = append(reasons, fmt.Sprintf("스토리지 그룹 사용률 %.1f%% (>=%d%%)", *u, sgWarnPct))
	}
	for _, d := range sg.Disks {
		ss := strings.ToLower(d.StandingState)
		if ss != "" && ss != "normal" {
			st = StatusMax(st, "degraded")
			reasons = append(reasons, fmt.Sprintf("논리디스크 %s(%s) standing-state=%s", d.Name, d.Node, ss))
		}
	}
	return st, reasons, u
}

// networkStatus 는 shared-network 상태를 판정한다.
func networkStatus(net NetworkInput) (string, []string) {
	ft := strings.ToLower(net.FaultTolerant)
	if ft != "" && ft != "ft" && ft != "ha" {
		return "degraded", []string{fmt.Sprintf("shared-network fault-tolerant=%s", ft)}
	}
	return "ok", nil
}

// ---------------------------------------------------------------------------
// 메인 빌더 — 클러스터 1대
// ---------------------------------------------------------------------------

// clusterBuild 는 클러스터 1대를 그래프에 싣는 동안 쓰는 조회 맵 모음이다.
type clusterBuild struct {
	g             *graph
	c             *ClusterInput
	cid           string
	clusterGID    string
	syncing       bool
	nodeByName    map[string]string
	netByName     map[string]string
	netRoleByName map[string]string
	sgByRawID     map[string]string
	volGIDByRawID map[string]string
	volNameIndex  map[string][]string
	volNameOrder  []string // volNameIndex 삽입 순서 (이름 접두 매칭의 결정성용)
	nicKeys       []nicKeyGID
}

// nicKeyGID 는 (노드이름, 인터페이스이름) -> NIC 그래프 id 매핑 하나다.
// 알림 대상 해석 시 인터페이스 이름으로 첫 번째 NIC 을 찾는 데 쓴다.
type nicKeyGID struct {
	node, ifname, gid string
}

// BuildClusterTopology 는 정규화된 클러스터 1대를 토폴로지 그래프로 만든다.
// roots 는 클러스터 노드 하나다.
func BuildClusterTopology(c ClusterInput) *FullTopology {
	g := newGraph()
	buildClusterInto(g, &c, nil)
	finalizeGraph(g)
	return emitGraph(g, []ClusterInput{c})
}

// BuildFleetTopology 는 여러 클러스터를 fleet 루트 하나로 통합한다.
// ClusterInput.Site 가 있으면 사이트 계층(level 1)이 생성된다.
// fleetLabel 이 빈 문자열이면 "전체 플릿" 을 쓴다.
func BuildFleetTopology(clusters []ClusterInput, fleetLabel string) *FullTopology {
	if fleetLabel == "" {
		fleetLabel = "전체 플릿"
	}
	g := newGraph()
	fleetID := "fleet:root"
	g.addNode(fleetID, nodeInit{
		Type:  "fleet",
		Label: ptrOrNil(fleetLabel),
		Level: levels["fleet"],
		Meta:  omap{{"cluster_count", len(clusters)}},
	})

	siteSeen := map[string]bool{}
	for i := range clusters {
		c := &clusters[i]
		parent := fleetID
		if c.Site != nil {
			sid := c.Site.ID
			if sid == "" {
				sid = "site:default"
			}
			if !siteSeen[sid] {
				label := c.Site.Label
				if label == "" {
					label = sid
				}
				g.addNode(sid, nodeInit{
					Type:   "site",
					Label:  ptrOrNil(label),
					Level:  levels["site"],
					Parent: &fleetID,
					Meta:   omap{},
				})
				g.addEdge(fleetID, sid, "contains", "ok")
				siteSeen[sid] = true
			}
			parent = sid
		}
		buildClusterInto(g, c, &parent)
	}

	finalizeGraph(g)
	return emitGraph(g, clusters)
}

// buildClusterInto 는 정규화 클러스터 1대를 기존 그래프에 이어 붙인다.
// parentID 가 nil 이면 루트로 둔다(단독 빌드).
func buildClusterInto(g *graph, c *ClusterInput, parentID *string) {
	cid := c.ClusterID
	if cid == "" {
		cid = c.Unit.Address
	}
	if cid == "" {
		cid = "cluster"
	}
	platform := c.Platform
	if platform == "" {
		platform = "everrun"
	}
	syncing := c.Unit.Syncing

	b := &clusterBuild{
		g:             g,
		c:             c,
		cid:           cid,
		syncing:       syncing,
		nodeByName:    map[string]string{},
		netByName:     map[string]string{},
		netRoleByName: map[string]string{},
		sgByRawID:     map[string]string{},
		volGIDByRawID: map[string]string{},
		volNameIndex:  map[string][]string{},
	}

	b.buildUnit(parentID, platform)
	b.buildNodes()
	b.buildNetworks()
	b.buildStorageGroups()
	b.buildNICs()
	b.buildVMs()
	b.buildStandaloneVolumes()
	b.buildImageContainers()
	b.applyAlerts()
}

// buildUnit 은 클러스터 노드(level 2)를 만든다 (R4 의 클러스터 측).
func (b *clusterBuild) buildUnit(parentID *string, platform string) {
	c := b.c
	unitRawID := c.Unit.ID
	if unitRawID == "" {
		unitRawID = "supernova:o0"
	}
	b.clusterGID = gid(b.cid, unitRawID)
	totalMem := ParseSize(c.Unit.TotalMemory)
	usedMem := ParseSize(c.Unit.UsedMemory)
	label := c.Unit.Name
	if label == "" {
		label = b.cid
	}
	var license any
	if c.License != nil {
		license = c.License
	}
	b.g.addNode(b.clusterGID, nodeInit{
		Type:    "cluster",
		Label:   ptrOrNil(label),
		Status:  "ok",
		Level:   levels["cluster"],
		Parent:  parentID,
		Cluster: ptrOrNil(b.cid),
		Meta: omap{
			{"cluster_id", b.cid},
			{"platform", platform},
			{"version", strOrNil(c.Unit.Version)},
			{"uuid", strOrNil(c.Unit.UUID)},
			{"address", strOrNil(c.Unit.Address)},
			{"netmask", strOrNil(c.Unit.Netmask)},
			{"configured", boolOrNil(c.Unit.Configured)},
			{"syncing", b.syncing},
			{"total_vcpus", strOrNil(c.Unit.TotalVCPUs)},
			{"used_vcpus", strOrNil(c.Unit.UsedVCPUs)},
			{"total_memory_bytes", intPtrToAny(totalMem)},
			{"used_memory_bytes", intPtrToAny(usedMem)},
			{"memory_used_pct", floatPtrToAny(Pct(usedMem, totalMem))},
			{"license", license},
			{"raw_id", strOrNil(c.Unit.ID)},
		},
	})
	if parentID != nil {
		b.g.addEdge(*parentID, b.clusterGID, "contains", "ok")
	}
	if b.syncing {
		cn := b.g.get(b.clusterGID)
		cn.Reasons = append(cn.Reasons, "유닛 동기화(syncing) 진행 중")
		cn.Status = StatusMax(cn.Status, "warning")
	}
}

// sortedNodes 는 노드를 이름순으로 정렬한 사본이다.
// 정렬 순서가 곧 lane_index 와 노드 생성 순서가 된다.
func (b *clusterBuild) sortedNodes() []NodeInput {
	out := make([]NodeInput, len(b.c.Nodes))
	copy(out, b.c.Nodes)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// buildNodes 는 물리 노드(level 3)와 FT 페어 엣지를 만든다.
func (b *clusterBuild) buildNodes() {
	nodesSorted := b.sortedNodes()
	for idx, nd := range nodesSorted {
		ngid := gid(b.cid, nd.ID)
		st, reasons := nodeStatus(nd)
		lane := nd.Name
		if lane == "" {
			lane = fmt.Sprintf("node%d", idx)
		}
		var metrics *NodeOSMetrics
		if b.c.NodeMetrics != nil {
			metrics = b.c.NodeMetrics[nd.Name]
		}
		n := b.g.addNode(ngid, nodeInit{
			Type:    "node",
			Label:   ptrOrNil(nd.Name),
			Status:  st,
			Level:   levels["node"],
			Parent:  &b.clusterGID,
			Lane:    lane,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(nd.ID)},
				{"primary", nd.Primary},
				{"state", strOrNil(nd.State)},
				{"sub_state", strOrNil(nd.SubState)},
				{"standing_state", strOrNil(nd.StandingState)},
				{"mode", strOrNil(nd.Mode)},
				{"maintenance", nd.Mode != "" && strings.ToLower(nd.Mode) != "normal"},
				{"manufacturer", strOrNil(nd.Manufacturer)},
				{"model", strOrNil(nd.Model)},
				{"sockets", strOrNil(nd.CPUs)},
				{"memory_bytes", intPtrToAny(ParseSize(nd.Memory))},
				{"memory_label", strOrNil(nd.Memory)},
				{"ip_address", strOrNil(nd.IPAddress)},
				{"gateway", strOrNil(nd.Gateway)},
				{"dns", stringSliceOrEmpty(nd.DNS)},
				{"lane_index", idx},
				// SSH/collectd 로 보강되는 실시간 메트릭 (없으면 nil)
				{"cpu_pct", metricsCPUPct(metrics)},
				{"mem_pct", metricsMemPct(metrics)},
				{"uptime_s", metricsUptime(metrics)},
				{"temps_c", metricsTemps(metrics)},
				{"metrics_source", metricsSource(metrics)},
			},
		})
		n.Reasons = append(n.Reasons, reasons...)
		b.g.addEdge(b.clusterGID, ngid, "contains", "ok")
		b.nodeByName[nd.Name] = ngid
	}

	// FT 페어 엣지 (노드가 정확히 2개일 때만 의미가 있다)
	var ordered []string
	for _, nd := range nodesSorted {
		if ngid, ok := b.nodeByName[nd.Name]; ok {
			ordered = append(ordered, ngid)
		}
	}
	if len(ordered) == 2 {
		pairStatus := StatusMax(b.g.get(ordered[0]).Status, b.g.get(ordered[1]).Status)
		est := "ok"
		if pairStatus != "ok" {
			est = "degraded"
		}
		b.g.addEdge(ordered[0], ordered[1], "ft-pair", est,
			kv{"bidirectional", true}, kv{"syncing", b.syncing})
	}
}

// buildNetworks 는 공유 네트워크(level 5)를 만든다.
func (b *clusterBuild) buildNetworks() {
	for _, net := range b.c.Networks {
		ngid := gid(b.cid, net.ID)
		st, reasons := networkStatus(net)
		n := b.g.addNode(ngid, nodeInit{
			Type:    "sharednetwork",
			Label:   ptrOrNil(net.Name),
			Status:  st,
			Level:   levels["sharednetwork"],
			Parent:  &b.clusterGID,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(net.ID)},
				{"role", strOrNil(net.Role)},
				{"fault_tolerant", strOrNil(net.FaultTolerant)},
				{"bandwidth_label", strOrNil(net.Bandwidth)},
				{"bandwidth_bps", intPtrToAny(ParseBandwidth(net.Bandwidth))},
				{"mtu", intPtrToAny(net.MTU)},
				{"is_interconnect", strings.ToLower(net.Role) == "a-link"},
			},
		})
		n.Reasons = append(n.Reasons, reasons...)
		b.g.addEdge(b.clusterGID, ngid, "contains", "ok")
		b.netByName[net.Name] = ngid
		b.netRoleByName[net.Name] = strings.ToLower(net.Role)
	}
}

// buildStorageGroups 는 스토리지 그룹(level 5)과 노드별 논리 디스크(level 4)를 만든다 (R10).
func (b *clusterBuild) buildStorageGroups() {
	for _, sg := range b.c.StorageGroups {
		sgid := gid(b.cid, sg.ID)
		st, reasons, upct := sgStatus(sg)
		n := b.g.addNode(sgid, nodeInit{
			Type:    "storagegroup",
			Label:   ptrOrNil(sg.Name),
			Status:  st,
			Level:   levels["storagegroup"],
			Parent:  &b.clusterGID,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(sg.ID)},
				{"size_bytes", intPtrToAny(sg.SizeBytes)},
				{"used_bytes", intPtrToAny(sg.UsedBytes)},
				{"size_label", strPtrToAny(HumanSize(sg.SizeBytes))},
				{"used_label", strPtrToAny(HumanSize(sg.UsedBytes))},
				{"used_pct", floatPtrToAny(upct)},
				{"disk_type", strOrNil(sg.DiskType)},
				{"logical_sector_size", sg.LogicalSectorSize},
				{"physical_sector_size", sg.PhysicalSectorSize},
			},
		})
		n.Reasons = append(n.Reasons, reasons...)
		b.g.addEdge(b.clusterGID, sgid, "contains", "ok")
		b.sgByRawID[sg.ID] = sgid

		for _, d := range sg.Disks {
			dgid := gid(b.cid, d.ID)
			hostGID, hasHost := b.nodeByName[d.Node]
			dss := strings.ToLower(d.StandingState)
			dst := "ok"
			if dss != "" && dss != "normal" {
				dst = "degraded"
			}
			parent := &b.clusterGID
			if hasHost {
				parent = &hostGID
			}
			lane := d.Node
			if lane == "" {
				lane = LaneShared
			}
			dn := b.g.addNode(dgid, nodeInit{
				Type:    "disk",
				Label:   ptrOrNil(fmt.Sprintf("%s (%s)", d.Name, d.Node)),
				Status:  dst,
				Level:   levels["disk"],
				Parent:  parent,
				Lane:    lane,
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"raw_id", strOrNil(d.ID)},
					{"node", strOrNil(d.Node)},
					{"size_bytes", intPtrToAny(d.SizeBytes)},
					{"used_bytes", intPtrToAny(d.UsedBytes)},
					{"size_label", strPtrToAny(HumanSize(d.SizeBytes))},
					{"used_pct", floatPtrToAny(Pct(d.UsedBytes, d.SizeBytes))},
					{"standing_state", strOrNil(d.StandingState)},
				},
			})
			if dss != "" && dss != "normal" {
				dn.Reasons = append(dn.Reasons, fmt.Sprintf("논리디스크 standing-state=%s", dss))
			}
			if hasHost {
				b.g.addEdge(hostGID, dgid, "contains", "ok")
			}
			b.g.addEdge(dgid, sgid, "member-of", dst)
		}
	}
}

// buildNICs 는 물리 NIC(level 4)와 uplink 엣지를 만든다 (R9/R9b).
//
// NIC -> shared-network 업링크 근거 우선순위:
//  1. 확정 매핑(노드 spine 설정/설정파일)
//  2. 알림 문자열
//  3. 이름·MTU 휴리스틱
//  4. 개수 일치 짝짓기
//
// 노드 순회는 이름 정렬 순이다(원본은 node_metrics 삽입 순; 밴드 정렬 키가
// 유일하므로 레이아웃 결과는 동일하다).
func (b *clusterBuild) buildNICs() {
	explicit := b.c.NICNetworkMap
	alertNICMap := DeriveNICMapFromAlerts(b.c.Alerts)
	for _, nd := range b.sortedNodes() {
		nodeName := nd.Name
		hostGID, hasHost := b.nodeByName[nodeName]
		if !hasHost {
			continue
		}
		metrics := b.c.NodeMetrics[nodeName]
		if metrics == nil {
			continue
		}
		var physLinks []LinkInput
		for _, l := range metrics.Links {
			kind, _ := GuessNICRole(l.Name)
			if kind == nicKindPhysical {
				physLinks = append(physLinks, l)
			}
		}
		ordinal := ordinalNICMap(physLinks, b.c.Networks)
		for _, link := range metrics.Links {
			ifname := link.Name
			kind, role := GuessNICRole(ifname)
			if kind != nicKindPhysical {
				continue // 브리지 / 게스트 tap / 루프백은 물리 토폴로지에서 제외
			}
			ngid := gid(b.cid, fmt.Sprintf("nic:%s:%s", nodeName, ifname))
			oper := strings.ToLower(link.OperState)

			// 확정 소스(노드 spine 설정/설정파일/알림)가 이 NIC 을 '알고 있는가'.
			// Network 이 nil 이어도 키가 있으면 "소속 네트워크 없음" 이 확정된 것이다.
			nodeMap := explicit[nodeName]
			rawMap, mappingKnown := nodeMap[ifname]
			targetNet := ""
			var evidence string
			var conf *float64
			if mappingKnown {
				if rawMap.Network != nil {
					targetNet = *rawMap.Network
				}
				evidence = rawMap.Evidence
				if evidence == "" {
					evidence = "config"
				}
				conf = rawMap.Confidence
				if conf == nil {
					conf = f64(1.0)
				}
			}
			if targetNet == "" {
				if an, ok := alertNICMap[ifname]; ok && an != "" {
					targetNet = an
					evidence, conf = "alert-text", f64(0.8)
					mappingKnown = true
				}
			}
			if targetNet == "" {
				if hn := heuristicNetForNIC(ifname, role, b.c.Networks, link.MTU); hn != "" {
					targetNet = hn
					evidence, conf = "heuristic", f64(0.4)
				}
			}
			if targetNet == "" {
				if on, ok := ordinal[ifname]; ok {
					targetNet = on
					evidence, conf = "ordinal-guess", f64(0.25)
				}
			}
			_, netExists := b.netByName[targetNet]
			attached := targetNet != "" && netExists
			if !attached {
				targetNet = ""
				if !mappingKnown {
					evidence, conf = "", nil
				}
			}
			// 매핑을 확정 근거로 얻지 못했으면 '미사용 예비 포트' 라고 단정하면 안 된다.
			// 확정 근거 없이 not attached 를 미사용으로 읽으면, 업무 트래픽을 나르는
			// ibiz0 가 끊겨도 R9b(status=ok, 초록)로 분류돼 장애 탐지를 적극적으로 억누른다.
			mappingUnknown := !attached && !mappingKnown

			// R9: 다운된 포트라도 어떤 shared-network 에도 속하지 않으면 '미사용 포트'다.
			//     (실장비 Edge 에 케이블 미연결 ibiz3~5 가 상시 down 으로 존재)
			//     단 '소속 없음' 이 확정된 경우에만. 매핑 자체가 미상이면 unknown.
			var nst string
			switch oper {
			case "up":
				nst = "ok"
			case "down":
				switch {
				case attached:
					nst = "critical"
				case mappingUnknown:
					nst = "unknown"
				default:
					nst = "ok"
				}
			default:
				nst = "unknown"
			}
			unused := oper == "down" && !attached && mappingKnown
			var attachedNet, attachEvidence, mapEvidence any
			isInterconnect := role == "a-link"
			if attached {
				attachedNet = targetNet
				attachEvidence = evidence
				isInterconnect = b.netRoleByName[targetNet] == "a-link"
			}
			if mappingKnown {
				mapEvidence = evidence
			}
			nn := b.g.addNode(ngid, nodeInit{
				Type:    "nic",
				Label:   ptrOrNil(fmt.Sprintf("%s:%s", nodeName, ifname)),
				Status:  nst,
				Level:   levels["nic"],
				Parent:  &hostGID,
				Lane:    nodeName,
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"node", nodeName},
					{"ifname", ifname},
					{"operstate", strOrNil(link.OperState)},
					{"speed_mbps", intPtrToAny(link.Speed)},
					{"nic_kind", kind},
					{"role_guess", strOrNil(role)},
					{"mtu", intPtrToAny(link.MTU)},
					{"is_interconnect", isInterconnect},
					{"attached_network", attachedNet},
					{"attachment_evidence", attachEvidence},
					{"mapping_evidence", mapEvidence},
					{"mapping_unknown", mappingUnknown},
					// 확정 소스가 '소속 없음' 이라고 말해준 포트만 미사용으로 인정한다.
					{"unused", unused},
					{"rx_errors", intPtrToAny(link.RxErrors)},
					{"tx_errors", intPtrToAny(link.TxErrors)},
					{"drops_delta", intPtrToAny(link.DropsDelta)},
					{"source", "ssh:@link"},
				},
			})
			switch {
			case nst == "critical":
				nn.Reasons = append(nn.Reasons,
					fmt.Sprintf("물리 NIC operstate=%s (공유 네트워크 %s 소속)", oper, targetNet))
			case unused:
				nn.Reasons = append(nn.Reasons, "미사용 포트(링크 다운, 소속 공유 네트워크 없음 — 확정)")
			case mappingUnknown:
				op := oper
				if op == "" {
					op = "unknown"
				}
				nn.Reasons = append(nn.Reasons,
					fmt.Sprintf("NIC<->공유네트워크 매핑을 확정하지 못함(operstate=%s) — "+
						"미사용 예비 포트인지 장애인지 판정 불가", op))
			}
			b.g.addEdge(hostGID, ngid, "contains", "ok")
			b.nicKeys = append(b.nicKeys, nicKeyGID{nodeName, ifname, ngid})

			if attached {
				b.g.addEdge(ngid, b.netByName[targetNet], "uplink", nst,
					kv{"evidence", evidence}, kv{"confidence", *conf})
			}
		}
	}
}

// buildVMs 는 VM(level 6) / 로컬 인스턴스(level 7) / 볼륨(level 7) /
// 디스크 이미지(level 8)와 관련 엣지를 만든다 (R1/R2/R3).
func (b *clusterBuild) buildVMs() {
	for _, vm := range b.c.VMs {
		vgid := gid(b.cid, vm.ID)
		vst, vreasons := vmStatus(vm)
		rstate, rst, rreasons := vmRedundancy(vm)
		vst = StatusMax(vst, rst)

		placement := vm.PlacementNodes
		if len(placement) == 0 {
			for _, inst := range vm.Instances {
				if strings.ToUpper(inst.EnableStatus) == "ENABLED" {
					placement = append(placement, inst.Node)
				}
			}
		}
		activeNode := ""
		if len(placement) > 0 {
			activeNode = placement[0]
		}
		lane := activeNode
		if lane == "" {
			lane = LaneShared
		}
		parent := b.clusterGID
		if activeNode != "" {
			if pg, ok := b.nodeByName[activeNode]; ok {
				parent = pg
			}
		}
		protection := strings.ToLower(vm.FaultTolerant)
		var protectionLabel any
		if vm.FaultTolerant == "" {
			protectionLabel = nil // 원본: fault-tolerant 없으면 None
		} else if l, ok := map[string]string{"ft": "내결함성(FT)", "ha": "고가용성(HA)"}[protection]; ok {
			protectionLabel = l
		} else {
			protectionLabel = vm.FaultTolerant // 원문을 그대로 둔다
		}
		n := b.g.addNode(vgid, nodeInit{
			Type:    "vm",
			Label:   ptrOrNil(vm.Name),
			Status:  vst,
			Level:   levels["vm"],
			Parent:  &parent,
			Lane:    lane,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(vm.ID)},
				{"uuid", strOrNil(vm.UUID)},
				{"internal_name", strOrNil(vm.InternalName)},
				{"state", strOrNil(vm.State)},
				{"standing_state", strOrNil(vm.StandingState)},
				{"protection", protection}, // 원본: 항상 문자열 (없으면 "")
				{"protection_label", protectionLabel},
				{"redundancy_state", rstate},
				{"vcpus", strOrNil(vm.CPUs)},
				{"memory_bytes", intPtrToAny(ParseSize(vm.Memory))},
				{"memory_label", strOrNil(vm.Memory)},
				{"os_type", strOrNil(vm.Type)},
				{"boot_type", strOrNil(vm.BootType)},
				{"placement_nodes", stringSliceOrEmpty(placement)},
				{"a_links", vmALinksToJSON(vm.ALinks)},
				{"instance_count", len(vm.Instances)},
			},
		})
		n.Reasons = append(n.Reasons, append(vreasons, rreasons...)...)

		b.buildVMPlacements(vm, vgid, placement)
		b.buildVMInstances(vm, vgid)
		b.buildVMNICs(vm, vgid, n)
		b.buildVMVolumes(vm, vgid, n)
	}
}

// buildVMPlacements 는 노드 -> VM 배치 엣지를 만든다.
// FT 는 lockstep, HA 는 active/standby 역할을 단다.
func (b *clusterBuild) buildVMPlacements(vm VMInput, vgid string, placement []string) {
	isFT := strings.ToLower(vm.FaultTolerant) == "ft"
	for _, inst := range vm.Instances {
		hostGID, ok := b.nodeByName[inst.Node]
		if !ok {
			continue
		}
		enabled := strings.ToUpper(inst.EnableStatus) == "ENABLED"
		role := "standby"
		if isFT {
			role = "lockstep"
		} else if containsString(placement, inst.Node) {
			role = "active"
		}
		est := "ok"
		if !enabled {
			est = "degraded"
		}
		b.g.addEdge(hostGID, vgid, "placement", est,
			kv{"role", role}, kv{"enabled", enabled}, kv{"mtbf", strOrNil(inst.MTBF)})
	}
}

// buildVMInstances 는 로컬 VM 인스턴스 노드와 instance-of / resides-on 엣지를 만든다.
func (b *clusterBuild) buildVMInstances(vm VMInput, vgid string) {
	for _, inst := range vm.Instances {
		igid := gid(b.cid, inst.ID)
		enabled := strings.ToUpper(inst.EnableStatus) == "ENABLED"
		mtbf := inst.MTBF
		if mtbf == "" {
			mtbf = "normal"
		}
		ist := "ok"
		if !enabled || strings.ToLower(mtbf) != "normal" {
			ist = "degraded"
		}
		inode := b.g.addNode(igid, nodeInit{
			Type:    "localvm",
			Label:   ptrOrNil(fmt.Sprintf("%s@%s", vm.Name, inst.Node)),
			Status:  ist,
			Level:   levels["localvm"],
			Parent:  &vgid,
			Lane:    orShared(inst.Node),
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(inst.ID)},
				{"node", strOrNil(inst.Node)},
				{"uuid", strOrNil(inst.UUID)},
				{"enable_status", strOrNil(inst.EnableStatus)},
				{"mtbf", strOrNil(inst.MTBF)},
				{"vhost_net", boolOrNil(inst.ConfigVhostNet)},
			},
		})
		if !enabled {
			inode.Reasons = append(inode.Reasons, "로컬 VM 인스턴스 DISABLED")
		}
		b.g.addEdge(vgid, igid, "instance-of", ist)
		if hostGID, ok := b.nodeByName[inst.Node]; ok {
			b.g.addEdge(igid, hostGID, "resides-on", ist, kv{"span", true})
		}
	}
}

// buildVMNICs 는 vNIC -> shared-network 엣지를 만든다.
// 이중화가 깨지면 VM 권위 상태도 함께 올린다(원본과 동일하게 status 를 직접 갱신).
func (b *clusterBuild) buildVMNICs(vm VMInput, vgid string, vmNode *Node) {
	for i, itf := range vm.Interfaces {
		netGID, ok := b.netByName[itf.SharedNetwork]
		if !ok {
			continue
		}
		n0 := strings.ToUpper(itf.Net0Status) == "ENABLED"
		n1 := strings.ToUpper(itf.Net1Status) == "ENABLED"
		var est, rstate string
		switch {
		case n0 && n1:
			est, rstate = "ok", "redundant"
		case n0 || n1:
			est, rstate = "degraded", "simplex"
		default:
			est, rstate = "critical", "down"
		}
		b.g.addEdge(vgid, netGID, "vnic", est,
			kv{"span", true},
			kv{"mac", strOrNil(itf.MAC)},
			kv{"index", i},
			kv{"net0_status", strOrNil(itf.Net0Status)},
			kv{"net1_status", strOrNil(itf.Net1Status)},
			kv{"redundancy", rstate},
		)
		if est != "ok" {
			vmNode.Reasons = append(vmNode.Reasons,
				fmt.Sprintf("vNIC(%s) 이중화 상태=%s", itf.SharedNetwork, rstate))
			vmNode.Status = StatusMax(vmNode.Status, est)
		}
	}
}

// buildVMVolumes 는 VM 소속 볼륨과 디스크 이미지(미러 조각)를 만든다 (R3/R4).
// id 없는 볼륨(cdrom 등)은 그래프 노드로 만들지 않고 VM 메타에만 남긴다.
func (b *clusterBuild) buildVMVolumes(vm VMInput, vgid string, vmNode *Node) {
	for _, vol := range vm.Volumes {
		if vol.ID == "" {
			vmNode.Meta.appendToList("removable_devices", omap{
				{"device", strOrNil(vol.Device)},
				{"device_id", strOrNil(vol.DeviceID)},
			})
			continue
		}
		volgid := gid(b.cid, vol.ID)
		mstate, mst, mreasons := volumeMirrorStatus(vol, b.syncing)
		vn := b.g.addNode(volgid, nodeInit{
			Type:    "volume",
			Label:   ptrOrNil(vol.Name),
			Status:  mst,
			Level:   levels["volume"],
			Parent:  &vgid,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(vol.ID)},
				{"device", strOrNil(vol.Device)},
				{"device_id", strOrNil(vol.DeviceID)},
				{"size_bytes", intPtrToAny(ParseSize(vol.Size))},
				{"size_label", strOrNil(vol.Size)},
				{"sector_size", strOrNil(vol.SectorSize)},
				{"mirror_state", mstate},
				{"bootable", nil}, // vm-info 쪽 볼륨에는 bootable 이 없다. volume-info 로 보강
				{"attached_to_vm", strOrNil(vm.Name)},
			},
		})
		vn.Reasons = append(vn.Reasons, mreasons...)
		b.registerVolume(vol.Name, vol.ID, volgid)
		b.g.addEdge(vgid, volgid, "attaches", mst,
			kv{"device", strOrNil(vol.Device)}, kv{"device_id", strOrNil(vol.DeviceID)})

		for _, img := range vol.DiskImages {
			igid := gid(b.cid, img.ID)
			en := strings.ToUpper(img.EnableStatus) == "ENABLED"
			ist := "ok"
			if !en {
				ist = "degraded"
			}
			imn := b.g.addNode(igid, nodeInit{
				Type:    "diskimage",
				Label:   ptrOrNil(fmt.Sprintf("%s@%s", vol.Name, img.Node)),
				Status:  ist,
				Level:   levels["diskimage"],
				Parent:  &volgid,
				Lane:    orShared(img.Node),
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"raw_id", strOrNil(img.ID)},
					{"node", strOrNil(img.Node)},
					{"enable_status", strOrNil(img.EnableStatus)},
					{"internal_name", strOrNil(img.Name)},
				},
			})
			if !en {
				imn.Reasons = append(imn.Reasons, "디스크 이미지 DISABLED — 미러 조각 오프라인")
			}
			mst2 := ist
			if en && b.syncing {
				mst2 = "warning"
			}
			syncState := "offline"
			if b.syncing {
				syncState = "syncing"
			} else if en {
				syncState = "in-sync"
			}
			b.g.addEdge(volgid, igid, "mirror", mst2,
				kv{"sync_state", syncState}, kv{"node", strOrNil(img.Node)})
			if hostGID, ok := b.nodeByName[img.Node]; ok {
				b.g.addEdge(igid, hostGID, "resides-on", ist, kv{"span", true})
			}
		}
	}
}

// registerVolume 은 볼륨 id/이름 색인을 삽입 순서와 함께 기록한다.
func (b *clusterBuild) registerVolume(name, rawID, volgid string) {
	b.volGIDByRawID[rawID] = volgid
	if _, ok := b.volNameIndex[name]; !ok {
		b.volNameOrder = append(b.volNameOrder, name)
	}
	b.volNameIndex[name] = append(b.volNameIndex[name], volgid)
}

// buildStandaloneVolumes 는 독립 볼륨(시스템 root/swap/diagdata 등)을 싣고,
// VM 소속 볼륨에는 volume-info 의 bootable/storage-group 을 보강한다.
func (b *clusterBuild) buildStandaloneVolumes() {
	for _, vol := range b.c.Volumes {
		raw := vol.ID
		volgid, exists := b.volGIDByRawID[raw]
		sgGID := ""
		if vol.StorageGroup != nil {
			sgGID = b.sgByRawID[vol.StorageGroup.ID]
		}
		if !exists {
			// VM 에 붙지 않은 시스템 볼륨: 스토리지 그룹 밑에 매단다
			volgid = gid(b.cid, raw)
			parent := b.clusterGID
			if sgGID != "" {
				parent = sgGID
			}
			b.g.addNode(volgid, nodeInit{
				Type:    "volume",
				Label:   ptrOrNil(vol.Name),
				Status:  "ok",
				Level:   levels["volume"],
				Parent:  &parent,
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"raw_id", strOrNil(raw)},
					{"size_bytes", intPtrToAny(ParseSize(vol.Size))},
					{"size_label", strOrNil(vol.Size)},
					{"bootable", boolOrNil(vol.Bootable)},
					{"system_volume", true},
					{"mirror_state", "unknown"},
					{"attached_to_vm", nil},
				},
			})
			b.registerVolume(vol.Name, raw, volgid)
		} else {
			// vm-info 쪽 볼륨에는 bootable/storage-group 이 없다. volume-info 로 보강.
			gv := b.g.get(volgid)
			if v, _ := gv.Meta.get("bootable"); v == nil {
				gv.Meta.set("bootable", boolOrNil(vol.Bootable))
			}
			var sgName any
			if vol.StorageGroup != nil {
				sgName = strOrNil(vol.StorageGroup.Name)
			}
			gv.Meta.set("storage_group", sgName)
		}
		if sgGID != "" {
			b.g.addEdge(volgid, sgGID, "stored-on", "ok", kv{"span", true})
		}
	}
}

// buildImageContainers 는 이미지 컨테이너(level 9, 실사용량)를 만든다.
// 볼륨 연결은 id 참조가 없어 이름 접두 매칭이 유일한 수단이다(조사 계약 참조).
func (b *clusterBuild) buildImageContainers() {
	for _, ic := range b.c.ImageContainers {
		icgid := gid(b.cid, ic.ID)
		sgGID := ""
		if ic.StorageGroup != nil {
			sgGID = b.sgByRawID[ic.StorageGroup.ID]
		}
		sizeB := ParseSize(ic.Size)
		usedB := ParseSize(ic.SizeUsed)
		parent := b.clusterGID
		if sgGID != "" {
			parent = sgGID
		}
		b.g.addNode(icgid, nodeInit{
			Type:    "imagecontainer",
			Label:   ptrOrNil(ic.Name),
			Status:  "ok",
			Level:   levels["imagecontainer"],
			Parent:  &parent,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(ic.ID)},
				{"size_bytes", intPtrToAny(sizeB)},
				{"used_bytes", intPtrToAny(usedB)},
				{"used_pct", floatPtrToAny(Pct(usedB, sizeB))},
				{"is_local", boolOrNil(ic.IsLocal)},
				{"has_filesystem", boolOrNil(ic.HasFilesystem)},
			},
		})
		target := b.matchContainer(ic.Name)
		if target != "" {
			b.g.addEdge(icgid, target, "backs", "ok",
				kv{"evidence", "name-prefix"}, kv{"confidence", 0.6}, kv{"span", true})
		} else if sgGID != "" {
			b.g.addEdge(icgid, sgGID, "stored-on", "ok")
		}
	}
}

// matchContainer 는 volNameIndex 삽입 순서대로 이름 접두 매칭한다.
func (b *clusterBuild) matchContainer(containerName string) string {
	if containerName == "" {
		return ""
	}
	cn := normName(containerName)
	best := ""
	bestLen := 0
	for _, vname := range b.volNameOrder {
		gids := b.volNameIndex[vname]
		if vname == "" || len(gids) != 1 {
			continue // 동명 볼륨이 여러 개면 조인 불가
		}
		vn := normName(vname)
		if vn == "" {
			continue
		}
		if cn == vn || strings.HasPrefix(cn, vn+"_") {
			if len(vn) > bestLen {
				best, bestLen = gids[0], len(vn)
			}
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// 알림 오버레이 (R11)
// ---------------------------------------------------------------------------

// applyAlerts 는 알림을 분류해 대상 노드에 누적한다.
// 권위 상태(status)는 건드리지 않고 alert_status 채널에만 쌓는다 (§5.3).
func (b *clusterBuild) applyAlerts() {
	g := b.g
	for _, a := range b.c.Alerts {
		sev, why := ClassifyAlert(a)
		targets := ExtractAlertTargets(a)
		var resolved []string
		for _, t := range targets {
			tgid := ""
			switch t.Type {
			case "node":
				tgid = b.nodeByName[t.Name]
			case "sharednetwork":
				tgid = b.netByName[t.Name]
			case "vm":
				tgid = b.findByLabel("vm", t.Name)
			case "volume":
				if gids := b.volNameIndex[t.Name]; len(gids) > 0 {
					tgid = gids[0]
				}
			case "nic":
				for _, k := range b.nicKeys {
					if k.ifname == t.Name {
						tgid = k.gid
						break
					}
				}
			case "quorum":
				tgid = b.ensureQuorum(t.Name)
			}
			if tgid != "" {
				resolved = append(resolved, tgid)
			}
		}
		targetEvidence := "alert-text"
		if len(targets) == 0 {
			targetEvidence = "cluster-fallback"
		}
		if len(resolved) == 0 {
			resolved = []string{b.clusterGID}
		}

		rec := &AlertRecord{
			ID:             ptrOrNil(a.ID),
			Name:           ptrOrNil(a.Name),
			Description:    ptrOrNil(a.Description),
			Time:           ptrOrNil(a.Time),
			RawSeverity:    ptrOrNil(a.Severity),
			Severity:       sev,
			ClassifiedBy:   why,
			Targets:        resolved,
			TargetEvidence: targetEvidence,
		}
		g.alertRecords = append(g.alertRecords, rec)
		for _, rgid := range resolved {
			gn := g.get(rgid)
			if gn == nil {
				continue
			}
			gn.Alerts = append(gn.Alerts, rec.ID)
			if sev == "critical" || sev == "degraded" || sev == "warning" {
				// 권위 상태(status)는 건드리지 않는다. 알림은 별도 채널에 누적한다.
				gn.AlertStatus = ptrOrNil(StatusMax(derefStr(gn.AlertStatus), sev))
				desc := a.Description
				if desc == "" {
					desc = a.Name
				}
				gn.Reasons = append(gn.Reasons, "알림: "+desc)
			}
		}
	}
}

// findByLabel 은 타입+라벨로 노드 id 를 찾는다 (알림 대상 해석용).
func (b *clusterBuild) findByLabel(typ, label string) string {
	for _, n := range b.g.nodes {
		if n.Type == typ && n.Label != nil && *n.Label == label {
			return n.ID
		}
	}
	return ""
}

// ensureQuorum 은 쿼럼 노드를 없으면 만든다.
// avcli 에 쿼럼 조회 명령이 없어 알림에서만 발견된다.
func (b *clusterBuild) ensureQuorum(addr string) string {
	qgid := gid(b.cid, "quorum:"+addr)
	if !b.g.has(qgid) {
		b.g.addNode(qgid, nodeInit{
			Type:    "quorum",
			Label:   ptrOrNil("쿼럼 " + addr),
			Status:  "unknown",
			Level:   levels["quorum"],
			Parent:  &b.clusterGID,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"address", addr},
				{"source", "alert-text"},
				{"note", "avcli 에 쿼럼 조회 명령이 없어 알림에서만 발견됨"},
			},
		})
		b.g.addEdge(b.clusterGID, qgid, "quorum", "unknown", kv{"evidence", "alert-text"})
	}
	return qgid
}

// ---------------------------------------------------------------------------
// nil 가능 값 헬퍼 (빌더 전용)
// ---------------------------------------------------------------------------

func intPtrToAny(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func floatPtrToAny(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func strPtrToAny(v *string) any {
	if v == nil {
		return nil
	}
	return *v
}

func stringSliceOrEmpty(s []string) any {
	if s == nil {
		return []string{}
	}
	return s
}

func metricsCPUPct(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return floatPtrToAny(m.CPUPct)
}

func metricsMemPct(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return floatPtrToAny(m.MemPct)
}

func metricsUptime(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return floatPtrToAny(m.UptimeS)
}

func metricsTemps(m *NodeOSMetrics) any {
	if m == nil || m.TempsC == nil {
		return map[string]float64{}
	}
	return m.TempsC
}

func metricsSource(m *NodeOSMetrics) any {
	if m == nil {
		return nil
	}
	return strOrNil(m.Source)
}

func vmALinksToJSON(links []VMALinkInput) any {
	if links == nil {
		return []any{}
	}
	out := make([]any, 0, len(links))
	for _, a := range links {
		out = append(out, omap{
			{"network", strOrNil(a.Network)},
			{"role", strOrNil(a.Role)},
			{"bandwidth", strOrNil(a.Bandwidth)},
		})
	}
	return out
}

func containsString(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func orShared(lane string) string {
	if lane == "" {
		return LaneShared
	}
	return lane
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
