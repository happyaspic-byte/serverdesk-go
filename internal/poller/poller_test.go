package poller

import (
	"os"
	"path/filepath"
	"testing"

	"serverdesk/internal/avcli"
	"serverdesk/internal/config"
	"serverdesk/internal/sshmetrics"
)

func TestRingSkipNilAndCap(t *testing.T) {
	r := NewRing(3)
	r.Push(1, nil) // nil 은 적립하지 않는다(첫 샘플 cpu_pct=null 계약)
	r.Push(2, 1.5)
	r.Push(3, 2.5)
	r.Push(4, 3.5)
	r.Push(5, 4.5)
	s := r.Series()
	if len(s) != 3 {
		t.Fatalf("링 크기 초과: %d", len(s))
	}
	first := s[0].(map[string]any)
	if first["t"].(int64) != 3 {
		t.Fatalf("가장 오래된 포인트가 밀리지 않음: %v", first["t"])
	}
}

func TestDeriveStatus(t *testing.T) {
	mk := func(state, standing, mode string) map[string]any {
		return map[string]any{"state": state, "standing_state": standing, "mode": mode}
	}
	view := map[string]any{"nodes": []any{
		mk("running", "normal", "normal"), mk("running", "normal", "normal")}}
	if got := DeriveStatus(view); got != "op" {
		t.Fatalf("정상 클러스터: %s", got)
	}
	view = map[string]any{"nodes": []any{mk("running", "normal", "normal"), mk("stopped", "", "")}}
	if got := DeriveStatus(view); got != "deg" {
		t.Fatalf("노드 1대 정지: %s", got)
	}
	view = map[string]any{"nodes": []any{mk("running", "normal", "maintenance"), mk("running", "normal", "normal")}}
	if got := DeriveStatus(view); got != "deg" {
		t.Fatalf("유지보수 모드: %s", got)
	}
	view = map[string]any{"nodes": []any{mk("stopped", "", ""), mk("stopped", "", "")}}
	if got := DeriveStatus(view); got != "down" {
		t.Fatalf("전부 정지: %s", got)
	}
	view = map[string]any{"nodes": []any{}}
	if got := DeriveStatus(view); got != "down" {
		t.Fatalf("노드 없음: %s", got)
	}
}

func TestDeriveSyncBrokenMirror(t *testing.T) {
	node := map[string]any{"state": "running", "standing_state": "normal", "mode": "normal"}
	// 미러 2개 중 1개 DISABLED — '깨진 미러'는 simplex 다(걸러내지 않는다).
	vm := map[string]any{"volumes": []any{map[string]any{
		"is_cdrom": false,
		"disk_images": []any{
			map[string]any{"enabled": true},
			map[string]any{"enabled": false},
		}}}}
	view := map[string]any{
		"nodes": []any{node, node}, "vms": []any{vm},
		"unit": map[string]any{"syncing": false}, "storage_groups": []any{}}
	if got := deriveSync(view, "op"); got != "simplex" {
		t.Fatalf("깨진 미러: %s", got)
	}
	vm2 := map[string]any{"volumes": []any{map[string]any{
		"is_cdrom": false,
		"disk_images": []any{
			map[string]any{"enabled": true},
			map[string]any{"enabled": true},
		}}}}
	view["vms"] = []any{vm2}
	if got := deriveSync(view, "op"); got != "sync" {
		t.Fatalf("정상 미러: %s", got)
	}
}

func TestAvailTrackerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	tr := NewAvailTracker(dir, func() [][2]string {
		return [][2]string{{"everrun", "op"}, {"nas", "down"}}
	})
	for i := 0; i < 5; i++ {
		tr.last -= 170 // 샘플 간격 시뮬레이션(180 초과면 갭으로 제외되는 계약 경계 아래)
		tr.Sample()
	}
	tr.Flush()
	// 영속 파일이 다시 읽히는지.
	tr2 := NewAvailTracker(dir, nil)
	devs := []map[string]any{{"id": "everrun"}, {"id": "nas"}, {"id": "unknown"}}
	tr2.Apply(devs)
	if v, ok := devs[0]["availN"]; !ok || v != 100.0 {
		t.Fatalf("op 장비 availN: %v", devs[0]["availN"])
	}
	if v, ok := devs[1]["availN"]; !ok || v != 0.0 {
		t.Fatalf("down 장비 availN: %v", devs[1]["availN"])
	}
	if _, ok := devs[2]["availN"]; ok {
		t.Fatalf("미관측 장비는 명목값 유지여야 한다: %v", devs[2]["availN"])
	}
	if _, ok := devs[0]["availDays"]; !ok {
		t.Fatalf("availDays 누락")
	}
	if _, err := os.Stat(filepath.Join(dir, "availability.json")); err != nil {
		t.Fatalf("영속 파일 없음: %v", err)
	}
}

func TestReconcileSpineNetworksOrdinal(t *testing.T) {
	// ztC Edge 형태: spine 이름(network0..2)과 avcli 표시명(P1..P3)이 다르고,
	// 같은 role 안에서 ordinal 순위로 짝짓는다.
	ord := func(v int64) *int64 { return &v }
	role := func(s string) *string { return &s }
	spine := []sshmetrics.SpineNetwork{
		{Name: "network0", Role: role("business"), Ordinal: ord(1)},
		{Name: "network1", Role: role("business"), Ordinal: ord(2)},
	}
	nets := []avcli.SharedNetwork{
		{Name: sp("P2"), Role: sp("business")},
		{Name: sp("P1"), Role: sp("business")},
	}
	x := reconcileSpineNetworks(spine, nets)
	if x["network0"].name != "P1" || x["network1"].name != "P2" {
		t.Fatalf("ordinal 짝짓기: %+v", x)
	}
	if x["network0"].evidence != "config-ordinal" {
		t.Fatalf("evidence: %s", x["network0"].evidence)
	}
	// 같은 이름이면 그대로(everRun 형태).
	spine2 := []sshmetrics.SpineNetwork{{Name: "priv0", Role: role("a-link"), Ordinal: ord(1)}}
	nets2 := []avcli.SharedNetwork{{Name: sp("priv0"), Role: sp("a-link")}}
	x2 := reconcileSpineNetworks(spine2, nets2)
	if x2["priv0"].name != "priv0" || x2["priv0"].evidence != "config" {
		t.Fatalf("직접 매칭: %+v", x2["priv0"])
	}
}

func sp(s string) *string { return &s }

func TestTrapViewSchema(t *testing.T) {
	traps := []map[string]any{{
		"ts": 1700000000.5, "src": "10.0.0.1", "trap_oid": "1.2.3", "name": "nodeDown",
		"sev": "critical", "desc": "", "pdu": "v2c-trap",
		"varbinds": []any{map[string]any{"oid": "1.1", "name": "sysUpTime", "display": "12s"}},
	}}
	off := int64(9 * 3600)
	out := TrapView(traps, &off, 50)
	if len(out) != 1 {
		t.Fatalf("건수: %d", len(out))
	}
	v := out[0].(map[string]any)
	if v["desc"] != "nodeDown" { // desc 폭백: name
		t.Fatalf("desc 폭백: %v", v["desc"])
	}
	if v["time"] != "2023-11-15 07:13:20" { // UTC+9 적용
		t.Fatalf("time: %v", v["time"])
	}
	vbs := v["varbinds"].([]any)
	if vbs[0].(map[string]any)["value"] != "12s" {
		t.Fatalf("varbind value: %v", vbs[0])
	}
}

func TestEventLogPersist(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "events.jsonl")
	el := NewEventLog(p, 500)
	el.Add("h1", "라벨", "state", "critical", "상태 가동 → 오프라인")
	el2 := NewEventLog(p, 500)
	lst := el2.List(10)
	if len(lst) != 1 {
		t.Fatalf("복원 건수: %d", len(lst))
	}
	ev := lst[0].(map[string]any)
	if ev["desc"] != "상태 가동 → 오프라인" || ev["sev"] != "critical" {
		t.Fatalf("내용: %v", ev)
	}
}

func TestFlatTopologyMinimal(t *testing.T) {
	view := map[string]any{
		"key": "c1", "name": "클1", "platform": "everrun",
		"health":   map[string]any{"level": "ok"},
		"nodes":    []any{map[string]any{"id": "node:o1", "name": "node0", "state": "running", "healthy": true}},
		"networks": []any{map[string]any{"id": "net:o1", "name": "priv0", "fault_tolerant": "ft", "role": "a-link"}},
		"storage_groups": []any{map[string]any{"id": "sg:o1", "name": "sg0", "used_pct": 50.0,
			"disks": []any{map[string]any{"id": "d:o1", "name": "d0", "standing_state": "normal"}}}},
		"vms":     []any{},
		"volumes": []any{map[string]any{"id": "vol:o1", "name": "root", "storage_group_id": "sg:o1"}},
		"alerts":  []any{},
	}
	g := BuildFlatTopology(view)
	if len(listVal(g["nodes"])) != 6 { // cluster + node + network + sg + disk + volume
		t.Fatalf("노드 수: %d", len(listVal(g["nodes"])))
	}
	if len(listVal(g["edges"])) == 0 {
		t.Fatalf("엣지 없음")
	}
}

func TestNodeTargetsUnion(t *testing.T) {
	cfg := &config.ClusterConfig{
		Key: "c", MgmtIP: "10.0.0.1", NodeRootPassword: "pw",
		Nodes: []config.NodeConfig{{Name: "node0", IP: "10.0.0.2", RootUser: "root", RootPassword: "pw0"}},
	}
	st := NewClusterState(cfg, 50)
	st.setNodes([]avcli.NodeInfo{{Name: sp("node1"), IP: sp("10.0.0.3")}})
	targets := st.NodeTargets()
	if len(targets) != 2 {
		t.Fatalf("합집합: %d", len(targets))
	}
	byIP := map[string]NodeTarget{}
	for _, tg := range targets {
		byIP[tg.IP] = tg
	}
	if byIP["10.0.0.2"].Password != "pw0" {
		t.Fatalf("설정 노드 암호 우선: %s", byIP["10.0.0.2"].Password)
	}
	if byIP["10.0.0.3"].Password != "pw" {
		t.Fatalf("발견 노드 기본 암호: %s", byIP["10.0.0.3"].Password)
	}
}
