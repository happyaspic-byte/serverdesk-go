package topology

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 테스트 픽스처
// ---------------------------------------------------------------------------

// healthyVM 은 FT 이중화가 정상인 VM 1대분 입력이다.
func healthyVM() VMInput {
	return VMInput{
		Name:           "vm1",
		ID:             "vm:o100",
		State:          "running",
		FaultTolerant:  "ft",
		PlacementNodes: []string{"node0"},
		Interfaces: []VMInterfaceInput{{
			SharedNetwork: "network0", MAC: "00:e0:09:00:00:01",
			Net0Status: "ENABLED", Net1Status: "ENABLED",
		}},
		Instances: []VMInstanceInput{
			{Name: "vm1-node0", ID: "localvirtualmachine:o101", EnableStatus: "ENABLED", Node: "node0", MTBF: "normal"},
			{Name: "vm1-node1", ID: "localvirtualmachine:o102", EnableStatus: "ENABLED", Node: "node1", MTBF: "normal"},
		},
		Volumes: []VMVolumeInput{{
			Name: "vm1_boot", ID: "volume:o110", Size: "50.00 GiB",
			Device: "vda", DeviceID: "vbd:o111",
			DiskImages: []DiskImageInput{
				{Name: "img0", ID: "diskimage:o112", EnableStatus: "ENABLED", Node: "node0"},
				{Name: "img1", ID: "diskimage:o113", EnableStatus: "ENABLED", Node: "node1"},
			},
		}},
	}
}

// baseCluster 는 2노드 FT 페어 + 네트워크 1 + 스토리지그룹 1 + 건강한 VM 1 의
// 최소 정상 클러스터다. 규칙 테스트는 여기서 한 군데씩만 망가뜨린다.
func baseCluster() ClusterInput {
	return ClusterInput{
		ClusterID: "c1",
		Platform:  "everrun",
		Unit:      UnitInput{Name: "unit1", ID: "supernova:o0", Version: "8.1.0.2-19"},
		Nodes: []NodeInput{
			{Name: "node0", ID: "host:o1", State: "running", StandingState: "normal", Mode: "normal", Primary: true},
			{Name: "node1", ID: "host:o2", State: "running", StandingState: "normal", Mode: "normal"},
		},
		Networks: []NetworkInput{
			{Name: "network0", ID: "sharednetwork:o10", Role: "business", FaultTolerant: "ft", Bandwidth: "1 Gb/s"},
		},
		StorageGroups: []StorageGroupInput{{
			Name: "sg0", ID: "storagegroup:o20",
			SizeBytes: i64(100 << 30), UsedBytes: i64(50 << 30),
		}},
		VMs: []VMInput{healthyVM()},
	}
}

func mustNode(t *testing.T, topo *FullTopology, id string) *Node {
	t.Helper()
	n := topo.FindNode(id)
	if n == nil {
		t.Fatalf("node %s not found", id)
	}
	return n
}

func mustMeta(t *testing.T, n *Node, key string) any {
	t.Helper()
	v, ok := n.Meta.get(key)
	if !ok {
		t.Fatalf("node %s meta %s missing", n.ID, key)
	}
	return v
}

func edgeAttr(t *testing.T, e *Edge, key string) any {
	t.Helper()
	v, ok := e.Attrs.get(key)
	if !ok {
		t.Fatalf("edge %s attr %s missing", e.ID, key)
	}
	return v
}

func hasReasonContaining(n *Node, sub string) bool {
	for _, r := range n.Reasons {
		if strings.Contains(r, sub) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 기저(baseline) — 정상 클러스터
// ---------------------------------------------------------------------------

func TestHealthyBaseline(t *testing.T) {
	topo := BuildClusterTopology(baseCluster())

	if len(topo.Roots) != 1 || topo.Roots[0] != "c1/supernova:o0" {
		t.Fatalf("roots = %v", topo.Roots)
	}
	cl := mustNode(t, topo, "c1/supernova:o0")
	if cl.RollupStatus != "ok" {
		t.Errorf("cluster rollup = %q, reasons=%v", cl.RollupStatus, cl.Reasons)
	}
	if topo.Summary.WorstStatus != "ok" {
		t.Errorf("worst = %q", topo.Summary.WorstStatus)
	}
	// FT 페어 엣지 (노드 정확히 2대)
	fp := topo.FindEdge("ft-pair|c1/host:o1|c1/host:o2")
	if fp == nil {
		t.Fatal("ft-pair edge missing")
	}
	if edgeAttr(t, fp, "bidirectional") != true || edgeAttr(t, fp, "syncing") != false {
		t.Errorf("ft-pair attrs = %v", fp.Attrs)
	}
	// 레이아웃: node0 는 level3 밴드의 왼쪽(-95), y=390 (모델 문서 §9.3 과 동일)
	n0 := mustNode(t, topo, "c1/host:o1")
	if n0.Layout == nil || n0.Layout.X != -95 || n0.Layout.Y != 390 || n0.Layout.LaneOffset != -1.0 {
		t.Errorf("node0 layout = %+v", n0.Layout)
	}
	// vm 은 active 노드 아래, localvm/diskimage 롤업 포함
	vm := mustNode(t, topo, "c1/vm:o100")
	if vm.Parent == nil || *vm.Parent != "c1/host:o1" {
		t.Errorf("vm parent = %v", vm.Parent)
	}
	if mustMeta(t, vm, "redundancy_state") != "protected" {
		t.Errorf("redundancy_state = %v", mustMeta(t, vm, "redundancy_state"))
	}
	if vm.ChildrenCount != 3 || vm.DescendantCount != 5 {
		t.Errorf("vm children=%d desc=%d, want 3/5", vm.ChildrenCount, vm.DescendantCount)
	}
}

// ---------------------------------------------------------------------------
// R1/R2 — VM 이중화
// ---------------------------------------------------------------------------

func TestR1SimplexVM(t *testing.T) {
	c := baseCluster()
	c.VMs[0].Instances[1].EnableStatus = "DISABLED"
	topo := BuildClusterTopology(c)

	vm := mustNode(t, topo, "c1/vm:o100")
	if mustMeta(t, vm, "redundancy_state") != "simplex" {
		t.Errorf("redundancy_state = %v, want simplex", mustMeta(t, vm, "redundancy_state"))
	}
	if vm.Status != "degraded" {
		t.Errorf("vm status = %q, want degraded", vm.Status)
	}
	lv := mustNode(t, topo, "c1/localvirtualmachine:o102")
	if lv.Status != "degraded" {
		t.Errorf("localvm status = %q, want degraded", lv.Status)
	}
	if e := topo.FindEdge("instance-of|c1/vm:o100|c1/localvirtualmachine:o102"); e == nil || e.Status != "degraded" {
		t.Errorf("instance-of edge = %+v", e)
	}
}

func TestR2UnprotectedVM(t *testing.T) {
	c := baseCluster()
	c.VMs[0].Instances[0].EnableStatus = "DISABLED"
	c.VMs[0].Instances[1].EnableStatus = "DISABLED"
	topo := BuildClusterTopology(c)

	vm := mustNode(t, topo, "c1/vm:o100")
	if mustMeta(t, vm, "redundancy_state") != "unprotected" {
		t.Errorf("redundancy_state = %v, want unprotected", mustMeta(t, vm, "redundancy_state"))
	}
	if vm.Status != "critical" {
		t.Errorf("vm status = %q, want critical", vm.Status)
	}
}

// ---------------------------------------------------------------------------
// R3/R4 — 볼륨 미러 / 유닛 동기화
// ---------------------------------------------------------------------------

func TestR3SimplexVolume(t *testing.T) {
	c := baseCluster()
	c.VMs[0].Volumes[0].DiskImages[1].EnableStatus = "DISABLED"
	topo := BuildClusterTopology(c)

	vol := mustNode(t, topo, "c1/volume:o110")
	if mustMeta(t, vol, "mirror_state") != "simplex" {
		t.Errorf("mirror_state = %v, want simplex", mustMeta(t, vol, "mirror_state"))
	}
	if vol.Status != "degraded" {
		t.Errorf("volume status = %q, want degraded", vol.Status)
	}
	img := mustNode(t, topo, "c1/diskimage:o113")
	if img.Status != "degraded" {
		t.Errorf("diskimage status = %q, want degraded", img.Status)
	}
}

func TestR4UnitSyncing(t *testing.T) {
	c := baseCluster()
	c.Unit.Syncing = true
	topo := BuildClusterTopology(c)

	cl := mustNode(t, topo, "c1/supernova:o0")
	if cl.Status != "warning" {
		t.Errorf("cluster status = %q, want warning", cl.Status)
	}
	if !hasReasonContaining(cl, "동기화") {
		t.Errorf("cluster reasons = %v", cl.Reasons)
	}
	for _, e := range topo.Edges {
		if e.Kind != "mirror" {
			continue
		}
		if edgeAttr(t, e, "sync_state") != "syncing" {
			t.Errorf("mirror %s sync_state = %v", e.ID, edgeAttr(t, e, "sync_state"))
		}
		if e.Status != "warning" {
			t.Errorf("mirror %s status = %q, want warning", e.ID, e.Status)
		}
	}
	vol := mustNode(t, topo, "c1/volume:o110")
	if mustMeta(t, vol, "mirror_state") != "syncing" {
		t.Errorf("mirror_state = %v, want syncing", mustMeta(t, vol, "mirror_state"))
	}
}

func TestR5VolumeRollsUpToVM(t *testing.T) {
	c := baseCluster()
	c.VMs[0].Volumes[0].DiskImages[1].EnableStatus = "DISABLED" // 볼륨만 심플렉스
	topo := BuildClusterTopology(c)

	vm := mustNode(t, topo, "c1/vm:o100")
	if vm.Status != "ok" {
		t.Fatalf("vm status = %q, want ok (볼륨은 자식이라 status 는 안 건드림)", vm.Status)
	}
	if vm.RollupStatus != "degraded" {
		t.Errorf("vm rollup = %q, want degraded (R5 자식 롤업)", vm.RollupStatus)
	}
}

// ---------------------------------------------------------------------------
// R6/R7/R8 — 노드 장애 / 유지보수
// ---------------------------------------------------------------------------

func TestR6NodeDownDamped(t *testing.T) {
	c := baseCluster()
	c.Nodes[1].State = "stopped"
	topo := BuildClusterTopology(c)

	n1 := mustNode(t, topo, "c1/host:o2")
	if n1.Status != "critical" {
		t.Errorf("node1 status = %q, want critical", n1.Status)
	}
	cl := mustNode(t, topo, "c1/supernova:o0")
	if cl.RollupStatus != "degraded" {
		t.Errorf("cluster rollup = %q, want degraded (FT 흡수)", cl.RollupStatus)
	}
	if !hasReasonContaining(cl, "일부 노드 다운") {
		t.Errorf("cluster reasons = %v", cl.Reasons)
	}
	// R12 감쇠 근거가 남아야 한다
	if !hasReasonContaining(cl, "이중화가 흡수") {
		t.Errorf("cluster reasons = %v (R12 감쇠 근거 없음)", cl.Reasons)
	}
}

func TestR7AllNodesDown(t *testing.T) {
	c := baseCluster()
	c.Nodes[0].State = "stopped"
	c.Nodes[1].State = "stopped"
	topo := BuildClusterTopology(c)

	cl := mustNode(t, topo, "c1/supernova:o0")
	if cl.RollupStatus != "critical" {
		t.Errorf("cluster rollup = %q, want critical", cl.RollupStatus)
	}
	if !hasReasonContaining(cl, "모든 물리 노드 다운") {
		t.Errorf("cluster reasons = %v", cl.Reasons)
	}
}

func TestR8NodeMaintenance(t *testing.T) {
	c := baseCluster()
	c.Nodes[0].Mode = "maintenance"
	topo := BuildClusterTopology(c)

	n0 := mustNode(t, topo, "c1/host:o1")
	if n0.Status != "warning" {
		t.Errorf("node0 status = %q, want warning", n0.Status)
	}
	if mustMeta(t, n0, "maintenance") != true {
		t.Errorf("maintenance = %v, want true", mustMeta(t, n0, "maintenance"))
	}
}

// ---------------------------------------------------------------------------
// R9/R9b — 물리 NIC
// ---------------------------------------------------------------------------

// nicCluster 는 NIC 테스트용: 노드별 링크와 spine 매핑을 얹는다.
func nicCluster(oper0, oper1 string) ClusterInput {
	c := baseCluster()
	c.VMs = nil // 본질과 무관한 롤업 잡음 제거
	c.NodeMetrics = map[string]*NodeOSMetrics{
		"node0": {Links: []LinkInput{{Name: "ibiz0", OperState: oper0}}},
		"node1": {Links: []LinkInput{{Name: "ibiz0", OperState: oper1}}},
	}
	c.NICNetworkMap = NICNetworkMap{
		"node0": {"ibiz0": {Network: ptrStr("network0")}},
		"node1": {"ibiz0": {Network: ptrStr("network0")}},
	}
	return c
}

func ptrStr(s string) *string { return &s }

func TestR9NICDownAllPortsDown(t *testing.T) {
	topo := BuildClusterTopology(nicCluster("down", "down"))

	nic := mustNode(t, topo, "c1/nic:node0:ibiz0")
	if nic.Status != "critical" {
		t.Errorf("nic status = %q, want critical", nic.Status)
	}
	up := topo.FindEdge("uplink|c1/nic:node0:ibiz0|c1/sharednetwork:o10")
	if up == nil || up.Status != "critical" {
		t.Fatalf("uplink edge = %+v", up)
	}
	if edgeAttr(t, up, "evidence") != "config" || edgeAttr(t, up, "confidence") != 1.0 {
		t.Errorf("uplink attrs = %v", up.Attrs)
	}
	net := mustNode(t, topo, "c1/sharednetwork:o10")
	if net.Status != "critical" {
		t.Errorf("network status = %q, want critical (전부 다운)", net.Status)
	}
}

func TestR9NICDownPartial(t *testing.T) {
	topo := BuildClusterTopology(nicCluster("down", "up"))

	net := mustNode(t, topo, "c1/sharednetwork:o10")
	if net.Status != "degraded" {
		t.Errorf("network status = %q, want degraded (일부 다운)", net.Status)
	}
	if !hasReasonContaining(net, "일부 다운") {
		t.Errorf("network reasons = %v", net.Reasons)
	}
}

func TestR9bUnusedSparePort(t *testing.T) {
	c := baseCluster()
	c.VMs = nil
	// 확정 소스가 '소속 네트워크 없음' 이라고 말하는 예비 포트.
	// eth0 은 이름으로 역할을 알 수 없어 휴리스틱/짝짓기가 붙지 못한다 —
	// business 역할이 확정되는 포트(ibiz3 등)는 원본 Python 도 휴리스틱으로
	// 후보가 유일한 네트워크에 붙여버리므로 이 테스트에는 쓸 수 없다.
	c.NodeMetrics = map[string]*NodeOSMetrics{
		"node0": {Links: []LinkInput{{Name: "eth0", OperState: "down"}}},
	}
	c.NICNetworkMap = NICNetworkMap{
		"node0": {"eth0": {Network: nil, Evidence: "config"}},
	}
	topo := BuildClusterTopology(c)

	nic := mustNode(t, topo, "c1/nic:node0:eth0")
	if nic.Status != "ok" {
		t.Errorf("nic status = %q, want ok (미사용 포트)", nic.Status)
	}
	if mustMeta(t, nic, "unused") != true {
		t.Errorf("unused = %v, want true", mustMeta(t, nic, "unused"))
	}
	if mustMeta(t, nic, "mapping_unknown") != false {
		t.Errorf("mapping_unknown = %v, want false", mustMeta(t, nic, "mapping_unknown"))
	}
	// 확정 근거가 있으므로 mapping_evidence 가 남는다
	if mustMeta(t, nic, "mapping_evidence") != "config" {
		t.Errorf("mapping_evidence = %v", mustMeta(t, nic, "mapping_evidence"))
	}
}

func TestR9cMappingUnknown(t *testing.T) {
	c := baseCluster()
	c.VMs = nil
	// 이름으로 역할을 알 수 없는 포트(eth0) — 휴리스틱도 개수 짝짓기도 못 붙인다
	c.NodeMetrics = map[string]*NodeOSMetrics{
		"node0": {Links: []LinkInput{{Name: "eth0", OperState: "down"}}},
	}
	topo := BuildClusterTopology(c)

	nic := mustNode(t, topo, "c1/nic:node0:eth0")
	if nic.Status != "unknown" {
		t.Errorf("nic status = %q, want unknown (초록으로 단정 금지)", nic.Status)
	}
	if mustMeta(t, nic, "mapping_unknown") != true {
		t.Errorf("mapping_unknown = %v, want true", mustMeta(t, nic, "mapping_unknown"))
	}
	if mustMeta(t, nic, "unused") != false {
		t.Errorf("unused = %v, want false", mustMeta(t, nic, "unused"))
	}
}

// ---------------------------------------------------------------------------
// R10 — 스토리지 사용률
// ---------------------------------------------------------------------------

func TestR10StorageThresholds(t *testing.T) {
	c := baseCluster()
	c.VMs = nil
	c.StorageGroups = []StorageGroupInput{
		{Name: "sg-warn", ID: "storagegroup:o30", SizeBytes: i64(100 << 30), UsedBytes: i64(90 << 30)}, // 90%
		{Name: "sg-crit", ID: "storagegroup:o31", SizeBytes: i64(100 << 30), UsedBytes: i64(96 << 30)}, // 96%
	}
	topo := BuildClusterTopology(c)

	warn := mustNode(t, topo, "c1/storagegroup:o30")
	if warn.Status != "warning" {
		t.Errorf("sg-warn status = %q, want warning", warn.Status)
	}
	crit := mustNode(t, topo, "c1/storagegroup:o31")
	if crit.Status != "critical" {
		t.Errorf("sg-crit status = %q, want critical", crit.Status)
	}
	// 스토리지 사용률은 이중화 감쇠 대상이 아니므로 클러스터까지 critical 로 올라간다
	cl := mustNode(t, topo, "c1/supernova:o0")
	if cl.RollupStatus != "critical" {
		t.Errorf("cluster rollup = %q, want critical (감쇠 없음)", cl.RollupStatus)
	}
}

func TestR10StorageUnknown(t *testing.T) {
	c := baseCluster()
	c.VMs = nil
	c.StorageGroups = []StorageGroupInput{{Name: "sg0", ID: "storagegroup:o20"}} // 용량 정보 없음
	topo := BuildClusterTopology(c)

	sg := mustNode(t, topo, "c1/storagegroup:o20")
	if sg.Status != "unknown" {
		t.Errorf("sg status = %q, want unknown", sg.Status)
	}
}

// ---------------------------------------------------------------------------
// R11 — 알림 오버레이 (감쇠 규칙 §5.3)
// ---------------------------------------------------------------------------

func TestR11AlertOverlayDamped(t *testing.T) {
	c := baseCluster()
	c.Alerts = []AlertInput{{
		ID: "alert:1", Name: "Node down",
		Description: "Node node0 is unreachable", Severity: "0",
	}}
	topo := BuildClusterTopology(c)

	n0 := mustNode(t, topo, "c1/host:o1")
	if n0.Status != "ok" {
		t.Errorf("node0 status = %q, want ok (권위 상태는 알림이 못 건드림)", n0.Status)
	}
	if n0.AlertStatus == nil || *n0.AlertStatus != "critical" {
		t.Errorf("node0 alert_status = %v, want critical", n0.AlertStatus)
	}
	// 실시간 필드가 ok 인데 알림만 심각 → 최대 warning 까지만
	if n0.EffectiveStatus != "warning" {
		t.Errorf("node0 effective = %q, want warning (감쇠)", n0.EffectiveStatus)
	}
	if len(n0.Alerts) != 1 || *n0.Alerts[0] != "alert:1" {
		t.Errorf("node0 alerts = %v", n0.Alerts)
	}
	if len(topo.Alerts) != 1 || topo.Alerts[0].ClassifiedBy != "keyword:unreachable" {
		t.Errorf("alerts = %+v", topo.Alerts)
	}
}

func TestR11AlertClusterFallback(t *testing.T) {
	c := baseCluster()
	c.Alerts = []AlertInput{{
		ID: "alert:2", Name: "odd", Description: "something vague happened", Severity: "1",
	}}
	topo := BuildClusterTopology(c)

	cl := mustNode(t, topo, "c1/supernova:o0")
	if cl.AlertStatus == nil || *cl.AlertStatus != "warning" {
		t.Errorf("cluster alert_status = %v, want warning", cl.AlertStatus)
	}
	if topo.Alerts[0].TargetEvidence != "cluster-fallback" {
		t.Errorf("target_evidence = %q", topo.Alerts[0].TargetEvidence)
	}
	if len(topo.Alerts[0].Targets) != 1 || topo.Alerts[0].Targets[0] != "c1/supernova:o0" {
		t.Errorf("targets = %v", topo.Alerts[0].Targets)
	}
}

func TestR11QuorumFromAlert(t *testing.T) {
	c := baseCluster()
	c.Alerts = []AlertInput{{
		ID: "alert:3", Name: "quorum", Description: "Quorum server 172.30.1.90 is offline",
	}}
	topo := BuildClusterTopology(c)

	q := mustNode(t, topo, "c1/quorum:172.30.1.90")
	if q.Type != "quorum" || q.Level != 5 {
		t.Errorf("quorum node = %+v", q)
	}
	if q.Status != "unknown" {
		t.Errorf("quorum status = %q, want unknown", q.Status)
	}
	// 권위 소스가 없는 객체는 알림 심각도를 그대로 쓴다
	if q.EffectiveStatus != "critical" {
		t.Errorf("quorum effective = %q, want critical", q.EffectiveStatus)
	}
	e := topo.FindEdge("quorum|c1/supernova:o0|c1/quorum:172.30.1.90")
	if e == nil || edgeAttr(t, e, "evidence") != "alert-text" {
		t.Errorf("quorum edge = %+v", e)
	}
}

// ---------------------------------------------------------------------------
// R12 — 이중화 감쇠 (R6 테스트가 클러스터 경로를 덮고, 여기서는 미러 경로)
// ---------------------------------------------------------------------------

func TestR12DampingKeepsVolumeDegraded(t *testing.T) {
	// 미러 한쪽이 완전 오프라인이어도 볼륨은 degraded 까지만 (critical 자식 감쇠).
	// diskimage 자체는 degraded 까지만 오르므로, 감쇠 없이 롤업돼도 degraded 다.
	// 감쇠가 실제로 critical 을 낮추는 경로는 cluster<-node 뿐이다 (R6 테스트 참조).
	c := baseCluster()
	c.VMs[0].Volumes[0].DiskImages[1].EnableStatus = "DISABLED"
	topo := BuildClusterTopology(c)

	vol := mustNode(t, topo, "c1/volume:o110")
	if vol.RollupStatus != "degraded" {
		t.Errorf("volume rollup = %q, want degraded", vol.RollupStatus)
	}
	cl := mustNode(t, topo, "c1/supernova:o0")
	if cl.RollupStatus != "degraded" {
		t.Errorf("cluster rollup = %q, want degraded", cl.RollupStatus)
	}
}

// ---------------------------------------------------------------------------
// 골든 셰이프 — 최상위 키와 레벨 순서
// ---------------------------------------------------------------------------

func TestGoldenShape(t *testing.T) {
	fleet := FleetInput{
		Clusters: []ClusterView{
			{
				Key: "er-a", Platform: "everrun",
				Unit: UnitView{ID: "supernova:o0", Name: "er-a"},
				Nodes: []NodeView{
					{Name: "node0", ID: "host:o1", State: "running"},
					{Name: "node1", ID: "host:o2", State: "running"},
				},
			},
			{
				Key: "edge-b", Platform: "ztcedge",
				Unit: UnitView{ID: "supernova:o0", Name: "edge-b"},
				Nodes: []NodeView{
					{Name: "node0", ID: "host:o1", State: "running"},
					{Name: "node1", ID: "host:o2", State: "running"},
				},
			},
		},
		Sites: map[string]*SiteRef{
			"er-a":   {ID: "site:hq", Label: "본사"},
			"edge-b": {ID: "site:plant", Label: "공장"},
		},
	}
	topo := BuildFullTopology(fleet)

	raw, err := topo.ToJSON(false)
	if err != nil {
		t.Fatalf("ToJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wantKeys := []string{
		"schema_version", "generated_by", "roots", "levels", "edge_kinds",
		"status_rank", "propagation_rules", "layout", "summary", "alerts",
		"nodes", "edges",
	}
	if len(doc) != len(wantKeys) {
		t.Errorf("top-level key count = %d, want %d (keys: %v)", len(doc), len(wantKeys), doc)
	}
	for _, k := range wantKeys {
		if _, ok := doc[k]; !ok {
			t.Errorf("top-level key %q missing", k)
		}
	}

	if doc["schema_version"] != "1.0.0" {
		t.Errorf("schema_version = %v", doc["schema_version"])
	}

	// levels: 0..9 오름차순, 라벨 존재
	levelsArr, ok := doc["levels"].([]any)
	if !ok || len(levelsArr) != 10 {
		t.Fatalf("levels = %v", doc["levels"])
	}
	for i, lv := range levelsArr {
		m := lv.(map[string]any)
		if int(m["level"].(float64)) != i {
			t.Errorf("levels[%d].level = %v", i, m["level"])
		}
		if m["label"] == "" {
			t.Errorf("levels[%d].label empty", i)
		}
	}
	if levelsArr[0].(map[string]any)["label"] != "플릿" {
		t.Errorf("levels[0].label = %v", levelsArr[0].(map[string]any)["label"])
	}
	if levelsArr[2].(map[string]any)["label"] != "클러스터" {
		t.Errorf("levels[2].label = %v", levelsArr[2].(map[string]any)["label"])
	}

	// propagation_rules: R1..R12 순서 고정
	rules := doc["propagation_rules"].([]any)
	if len(rules) != 13 {
		t.Fatalf("propagation_rules = %d, want 13", len(rules))
	}
	wantRuleIDs := []string{
		"R1-localvm-to-vm", "R2-localvm-none", "R3-diskimage-to-volume",
		"R4-unit-syncing", "R5-volume-to-vm", "R6-node-down", "R7-all-nodes-down",
		"R8-node-maintenance", "R9-nic-down", "R9b-nic-unused", "R10-storage-usage",
		"R11-alert-overlay", "R12-redundancy-damping",
	}
	for i, id := range wantRuleIDs {
		if rules[i].(map[string]any)["id"] != id {
			t.Errorf("rules[%d].id = %v, want %s", i, rules[i].(map[string]any)["id"], id)
		}
	}

	// status_rank / edge_kinds
	sr := doc["status_rank"].(map[string]any)
	if sr["ok"] != 0.0 || sr["critical"] != 4.0 || len(sr) != 5 {
		t.Errorf("status_rank = %v", sr)
	}
	ek := doc["edge_kinds"].(map[string]any)
	if len(ek) != 13 {
		t.Errorf("edge_kinds = %d keys, want 13", len(ek))
	}

	// layout
	layout := doc["layout"].(map[string]any)
	if layout["mode"] != "layered" || layout["level_gap_y"] != 130.0 || layout["node_gap_x"] != 190.0 {
		t.Errorf("layout = %v", layout)
	}

	// fleet 루트/사이트 계층
	roots := doc["roots"].([]any)
	if len(roots) != 1 || roots[0] != "fleet:root" {
		t.Errorf("roots = %v", roots)
	}
	summary := doc["summary"].(map[string]any)
	clusters := summary["clusters"].([]any)
	if len(clusters) != 2 || clusters[0] != "er-a" || clusters[1] != "edge-b" {
		t.Errorf("summary.clusters = %v", clusters)
	}
	if topo.FindNode("site:hq") == nil || topo.FindNode("site:plant") == nil {
		t.Errorf("site nodes missing")
	}
	if n := mustNode(t, topo, "er-a/supernova:o0"); n.Parent == nil || *n.Parent != "site:hq" {
		t.Errorf("cluster parent = %v, want site:hq", n.Parent)
	}
}

// TestGoldenShapeSingleCluster 는 1클러스터·사이트 없음 경로: roots 는 클러스터 id.
func TestGoldenShapeSingleCluster(t *testing.T) {
	topo := BuildFullTopology(FleetInput{
		Clusters: []ClusterView{{
			Key: "only", Platform: "everrun",
			Unit:  UnitView{ID: "supernova:o0"},
			Nodes: []NodeView{{Name: "node0", ID: "host:o1", State: "running"}},
		}},
	})
	if len(topo.Roots) != 1 || topo.Roots[0] != "only/supernova:o0" {
		t.Errorf("roots = %v", topo.Roots)
	}
}

// ---------------------------------------------------------------------------
// XML 정규화 헬퍼
// ---------------------------------------------------------------------------

func TestNormalizeFromXML(t *testing.T) {
	xmls := map[string]string{
		"unit-info": `<unit-info><name>u1</name><id>supernova:o0</id><version>8.1.0.2-19</version>
			<configured>true</configured><syncing>false</syncing>
			<resources><total-vcpus>6</total-vcpus><used-vcpus>6.00</used-vcpus>
			<total-memory>13.95 GiB</total-memory><used-memory>12.00 GiB</used-memory></resources></unit-info>`,
		"node-info": `<node-info>
			<node><name>node0</name><id>host:o1</id><state>running</state><primary>true</primary>
				<manufacturer>ECS</manufacturer><model>H110M4-C43</model>
				<local-networks><local-network><ip-address>172.30.1.11</ip-address>
				<dns><address>1.1.1.1</address><address>8.8.8.8</address></dns></local-network></local-networks>
				<virtual-machines><virtual-machine><name>vm1</name></virtual-machine></virtual-machines></node>
			<node><name>node1</name><id>host:o2</id><state>running</state><primary>false</primary></node>
		</node-info>`,
		"network-info": `<network-info><shared-network><name>network0</name><id>sharednetwork:o10</id>
			<fault-tolerant>ft</fault-tolerant><role>business</role><bandwidth>1 Gb/s</bandwidth><mtu>1500</mtu></shared-network></network-info>`,
		"vm-info": `<vm-info><virtual-machine><name>vm1</name><id>vm:o100</id><state>running</state>
			<fault-tolerant>ft</fault-tolerant>
			<interfaces><interface><shared-network>network0</shared-network><MAC>00:11</MAC>
			<net0-status>ENABLED</net0-status><net1-status>ENABLED</net1-status></interface></interfaces>
			<volumes><volume><name>vm1_boot</name><id>volume:o110</id><size>50.00 GiB</size>
			<disk-images><disk-image><name>i0</name><id>diskimage:o112</id><enable-status>ENABLED</enable-status>
			<node><name>node0</name><id>host:o1</id></node></disk-image></disk-images></volume></volumes>
			<a-links><net_82><role>a-link</role><bandwidth>1 Gb/s</bandwidth></net_82></a-links>
			<local-virtual-machines><local-virtual-machine><name>lv0</name><ID>localvirtualmachine:o101</ID>
			<enable-status>ENABLED</enable-status><mtbf><status>normal</status></mtbf>
			<node><name>node0</name><id>host:o1</id></node></local-virtual-machine></local-virtual-machines>
		</virtual-machine></vm-info>`,
		"alert-info":   `<alert-info><alert><name>a1</name><id>alert:1</id><severity>1</severity><description>x</description></alert></alert-info>`,
		"license-info": `<license-info><license><name>eE_TRIAL</name><expires>false</expires><activated>true</activated></license></license-info>`,
		// 빈 응답은 흡수된다
		"volume-info": "",
	}
	c := NormalizeFromXML("er-x", xmls, nil)

	if c.Platform != "everrun" { // manufacturer/model 로 추정
		t.Errorf("platform = %q", c.Platform)
	}
	if c.Unit.TotalVCPUs != "6" || c.Unit.UsedMemory != "12.00 GiB" {
		t.Errorf("unit = %+v", c.Unit)
	}
	if c.Unit.Syncing {
		t.Errorf("syncing = true, want false")
	}
	if len(c.Nodes) != 2 || !c.Nodes[0].Primary || c.Nodes[0].IPAddress != "172.30.1.11" {
		t.Errorf("nodes = %+v", c.Nodes)
	}
	if len(c.Nodes[0].DNS) != 2 {
		t.Errorf("dns = %v", c.Nodes[0].DNS)
	}
	if len(c.VMs) != 1 || len(c.VMs[0].PlacementNodes) != 1 || c.VMs[0].PlacementNodes[0] != "node0" {
		t.Errorf("vm placements = %+v", c.VMs[0].PlacementNodes)
	}
	if len(c.VMs[0].ALinks) != 1 || c.VMs[0].ALinks[0].Network != "net_82" {
		t.Errorf("a_links = %+v", c.VMs[0].ALinks)
	}
	if c.VMs[0].Instances[0].ID != "localvirtualmachine:o101" { // 대문자 ID 태그
		t.Errorf("instance id = %q", c.VMs[0].Instances[0].ID)
	}
	if c.License == nil || c.License.ExpireDate != nil { // expires=false → expire-date 없음
		t.Errorf("license = %+v", c.License)
	}
	if c.Networks[0].MTU == nil || *c.Networks[0].MTU != 1500 {
		t.Errorf("mtu = %v", c.Networks[0].MTU)
	}

	// 정규화 결과가 곧바로 빌드 입력이 돼야 한다
	topo := BuildClusterTopology(c)
	if topo.FindNode("er-x/vm:o100") == nil || topo.FindNode("er-x/diskimage:o112") == nil {
		t.Errorf("built graph missing vm/diskimage")
	}
}

// ---------------------------------------------------------------------------
// 어댑터
// ---------------------------------------------------------------------------

func TestAdaptCluster(t *testing.T) {
	view := ClusterView{
		Key:      "k1",
		Platform: "everrun",
		Unit: UnitView{
			Name: "u1", ID: "supernova:o0",
			Resources: ResourcesView{
				TotalVCPUs: "6", UsedVCPUs: "6.00",
				TotalMemoryRaw: "13.95 GiB", UsedMemoryRaw: "12.00 GiB",
			},
		},
		Nodes: []NodeView{{
			Name: "node0", ID: "host:o1", State: "running",
			CPUPct: f64(18.1), MemPct: f64(89.7), UptimeSecs: f64(520448.44),
			VMPlacements: []VMPlacementRef{{Name: "vm1"}},
			OS: &NodeOSView{
				Links: []LinkView{
					{Name: "ibiz0", State: "up", SpeedMbps: i64(1000), MTU: i64(1500)},
					{Name: "eth0", State: "down"},
				},
				Net: []NetDevView{{
					Name: "ibiz0", RxErrDelta: i64(2), TxErrDelta: i64(3),
					RxDropDelta: i64(1), TxDropDelta: i64(3),
				}},
				Temps: []TempView{
					{Chip: "coretemp", Label: "Core 0", Celsius: f64(61.0)},
					{Label: "ambient", Celsius: f64(40.5)},
				},
				Source: "ssh:/proc+/sys",
			},
		}},
		VMs: []VMView{{Name: "vm1", ID: "vm:o100", HAMode: "ft", State: "running"}},
	}

	c := AdaptCluster(view, &SiteRef{ID: "site:hq"}, nil)

	if c.Unit.TotalMemory != "13.95 GiB" || c.Unit.TotalVCPUs != "6" {
		t.Errorf("unit = %+v", c.Unit)
	}
	if c.Site == nil || c.Site.ID != "site:hq" {
		t.Errorf("site = %+v", c.Site)
	}
	if len(c.VMs) != 1 || len(c.VMs[0].PlacementNodes) != 1 || c.VMs[0].PlacementNodes[0] != "node0" {
		t.Errorf("placement reverse index = %+v", c.VMs)
	}
	if c.VMs[0].FaultTolerant != "ft" { // ha_mode -> fault_tolerant
		t.Errorf("fault_tolerant = %q", c.VMs[0].FaultTolerant)
	}

	m := c.NodeMetrics["node0"]
	if m == nil {
		t.Fatal("node_metrics missing")
	}
	if len(m.Links) != 2 {
		t.Fatalf("links = %+v", m.Links)
	}
	// @link + @net 합산: 에러/드롭 델타가 이름으로 조인된다
	l0 := m.Links[0]
	if l0.OperState != "up" || *l0.RxErrors != 2 || *l0.TxErrors != 3 || *l0.DropsDelta != 4 {
		t.Errorf("link0 = %+v", l0)
	}
	// @net 엔트리가 없는 링크는 델타가 nil
	l1 := m.Links[1]
	if l1.RxErrors != nil || l1.DropsDelta != nil {
		t.Errorf("link1 = %+v", l1)
	}
	if m.TempsC["coretemp/Core 0"] != 61.0 || m.TempsC["ambient"] != 40.5 {
		t.Errorf("temps = %v", m.TempsC)
	}
	if m.CPUPct == nil || *m.CPUPct != 18.1 {
		t.Errorf("cpu_pct = %v", m.CPUPct)
	}
}
