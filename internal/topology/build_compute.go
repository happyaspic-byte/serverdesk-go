package topology

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// 컴퓨트 계층 빌더 (노드 / VM / 로컬 인스턴스 / 배치)
// ---------------------------------------------------------------------------

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
