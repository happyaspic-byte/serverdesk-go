package poller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"serverdesk/internal/avcli"
	"serverdesk/internal/config"
	"serverdesk/internal/edge"
	"serverdesk/internal/snmp"
	"serverdesk/internal/sshmetrics"
)

func fixtureRoot(t *testing.T, name string) *avcli.Element {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "avcli", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	root, err := avcli.ParseXML(string(b))
	if err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	return root
}

func int64p(v int64) *int64 { return &v }

func TestEdgeManagerLifecycleAndFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // workers still execute one deterministic empty round, then stop

	first := edge.DeviceConfig{
		Key: "edge-a", Kind: "unsupported", IP: "192.0.2.10",
		Name: "old", Company: "old", Factory: "old", Site: "old",
		AssetTag: "old", FloorPos: "old", Vendor: "old",
	}
	mgr := NewEdgeManager(ctx, []edge.DeviceConfig{first}, nil)
	t.Cleanup(mgr.Stop)

	gotDevices := mgr.Devices()
	if len(gotDevices) != 1 || gotDevices[0].Key != first.Key {
		t.Fatalf("initial devices=%+v", gotDevices)
	}
	gotDevices[0].Name = "caller mutation"
	if mgr.Devices()[0].Name != "old" {
		t.Fatal("Devices returned an aliased slice")
	}

	mgr.mu.Lock()
	mgr.lastGood = []map[string]any{
		{"id": "edge-a", "status": "up"},
		{"id": "removed", "status": "up"},
	}
	mgr.lastGoodAt = time.Now()
	mgr.mu.Unlock()
	if got := mgr.Latest(); len(got) != 1 || got[0]["id"] != "edge-a" {
		t.Fatalf("fresh fallback=%v", got)
	}

	mgr.mu.Lock()
	mgr.lastGoodAt = time.Now().Add(-131 * time.Second)
	mgr.mu.Unlock()
	if got := mgr.Latest(); got != nil {
		t.Fatalf("stale fallback=%v, want nil", got)
	}

	status := mgr.CollectorStatus()
	if status.Configured != 1 || status.Observed != 0 {
		t.Fatalf("collector status=%+v", status)
	}

	mgr.PatchMeta("edge-a", map[string]string{
		"label": "new", "company": "acme", "factory": "plant",
		"site": "seoul", "asset_tag": "rack-1", "floor_pos": "F1",
		"vendor": "vendor", "ignored": "value",
	})
	patched := mgr.Devices()[0]
	if patched.Name != "new" || patched.Company != "acme" || patched.Factory != "plant" ||
		patched.Site != "seoul" || patched.AssetTag != "rack-1" ||
		patched.FloorPos != "F1" || patched.Vendor != "vendor" {
		t.Fatalf("patched device=%+v", patched)
	}

	mgr.Add(edge.DeviceConfig{Key: "edge-b", Kind: "unsupported", IP: "192.0.2.11"})
	if len(mgr.Devices()) != 2 {
		t.Fatalf("after Add devices=%+v", mgr.Devices())
	}
	mgr.Remove("missing")
	mgr.Remove("edge-a")
	if got := mgr.Devices(); len(got) != 1 || got[0].Key != "edge-b" {
		t.Fatalf("after Remove devices=%+v", got)
	}
	mgr.Stop()
	mgr.Stop() // idempotent

	empty := NewEdgeManager(ctx, nil, nil)
	if got := empty.CollectorStatus(); got.Configured != 0 || got.Observed != 0 {
		t.Fatalf("empty collector status=%+v", got)
	}
	if got := empty.Latest(); got != nil {
		t.Fatalf("empty latest=%v", got)
	}
	empty.PatchMeta("missing", map[string]string{"label": "ignored"})
	empty.Stop()
}

// populatedState deliberately uses the captured avcli responses rather than
// hand-written maps. It exercises the same typed values that production view
// and topology builders receive, while remaining deterministic and offline.
func populatedState(t *testing.T) *ClusterState {
	t.Helper()
	off := int64(9 * 60 * 60)
	cfg := &config.ClusterConfig{
		Key:              "plant-ft",
		Name:             "Plant FT",
		MgmtIP:           "172.30.1.10",
		NodeRootPassword: "default-pw",
		TzOffsetSecs:     &off,
		Company:          "Acme",
		Factory:          "Plant 1",
		Site:             "Seoul",
		Intervals: config.Intervals{
			Fast: 60, Slow: 300, Static: 86400, OS: 10, SNMP: 60,
		},
		HistoryPoints: 3,
		Nodes: []config.NodeConfig{
			{Name: "node0", IP: "172.30.1.11", RootUser: "admin", RootPassword: "node-pw"},
		},
		NicNetworkMap: map[string]map[string]string{
			"node1": {"eth9": "network0"},
		},
	}
	st := NewClusterState(cfg, 3)
	st.setNodes(avcli.ParseNodeInfo(fixtureRoot(t, "node_info.xml")))
	st.setVMs(avcli.ParseVMInfo(fixtureRoot(t, "vm_info.xml")))
	st.setNetworks(avcli.ParseNetworkInfo(fixtureRoot(t, "network_info.xml")))
	st.setStorageGroups(avcli.ParseStorageInfo(fixtureRoot(t, "storage_info_v2.xml")))
	st.setVolumes(avcli.ParseVolumeInfo(fixtureRoot(t, "volume_info.xml")))
	st.setContainers(avcli.ParseImageContainerInfo(fixtureRoot(t, "image_container_info.xml")))
	st.setAlerts(avcli.ParseAlertInfo(fixtureRoot(t, "alert_info.xml")))
	st.setUnit(avcli.ParseUnitInfo(fixtureRoot(t, "unit_info.xml")))
	st.setLicense(avcli.ParseLicenseInfo(fixtureRoot(t, "license_info.xml")))
	st.setLED(avcli.ParseLEDInfo(fixtureRoot(t, "led_info.xml")))
	st.joinImageContainers()
	st.setPlatform("everrun")

	roleALink := "a-link"
	roleBusiness := "business"
	priv0 := "priv0"
	network0 := "network0"
	spine := &sshmetrics.Spine{
		Networks: []sshmetrics.SpineNetwork{
			{Name: "priv0", Role: &roleALink, Ordinal: int64p(1)},
			{Name: "network0", Role: &roleBusiness, Ordinal: int64p(2)},
		},
		NICNetworks: map[string]*string{
			"eth0": &network0,
			"eth1": &priv0,
			"eth2": nil,
		},
	}
	now := nowFloat()
	st.setNodeOS("172.30.1.11", map[string]any{
		"ip": "172.30.1.11", "name": "node0", "reachable": true,
		"source": "ssh", "ts": now - 2, "last_ssh_ts": now - 2,
		"cpu_pct": 12.5, "mem_pct": 55.5, "uptime_secs": 3600.0,
		"temp_max_c": 44.0, "tz_offset_secs": float64(9 * 60 * 60), "tz_name": "KST",
		"links": []any{map[string]any{
			"name": "eth0", "state": "up", "speed_mbps": 1000.0, "mtu": 1500.0,
			"up": true, "physical": true, "guest_tap": false,
		}},
		"net": []any{map[string]any{
			"name": "eth0", "guest_tap": false, "interconnect": false,
			"rx_bps": 1000.0, "tx_bps": 2000.0,
		}},
		"temps": []any{map[string]any{
			"chip": "coretemp", "label": "Package", "celsius": 44.0,
		}},
	}, spine)
	st.snmpNodeOS("172.30.1.12", "node1", map[string]any{
		"reachable": true, "cpu_pct": 20.0, "mem_pct": 60.0, "uptime_secs": 7200.0,
	})
	st.RingFor("172.30.1.11", "cpu").Push(100, 10.0)
	st.RingFor("172.30.1.11", "mem").Push(100, 50.0)
	st.Mark("fast", "")
	st.Mark("slow", "")
	st.Mark("static", "")
	st.Mark("static", "license temporarily unavailable")
	st.AddTrap(map[string]any{
		"ts": 1700000001.0, "src": "172.30.1.11", "oid": "1.3.6.1.4.1",
		"name": "nodeAlarm", "sev": "warning",
		"varbinds": []any{map[string]any{"oid": "1.2", "name": "detail", "value": "hot"}},
	})
	return st
}

func TestClusterStateAndViewFromCapturedResponses(t *testing.T) {
	st := populatedState(t)

	if got := st.GetPlatform(); got != "everrun" {
		t.Fatalf("platform=%q", got)
	}
	st.setPlatform("ztcedge") // first successful detection wins
	if got := st.GetPlatform(); got != "everrun" {
		t.Fatalf("platform was overwritten: %q", got)
	}
	if n, v := st.NodeCounts(); n != 2 || v != 2 {
		t.Fatalf("counts=(%d,%d)", n, v)
	}
	if got := st.TierErr("static"); got == "" {
		t.Fatal("expected recorded static-tier error")
	}
	if age := st.Age("fast"); age == nil || *age < 0 {
		t.Fatalf("fast age=%v", age)
	}
	if age := st.Age("never"); age != nil {
		t.Fatalf("unseen tier age=%v", *age)
	}
	if reach := st.OSReachable(); !reach["172.30.1.11"] || reach["172.30.1.12"] {
		// SNMP reachability is deliberately secondary; OSReachable reports SSH.
		t.Fatalf("OS reachability=%v", reach)
	}
	if len(st.TrapsSnapshot()) != 1 {
		t.Fatal("trap was not retained")
	}

	snap := st.snapshot()
	snap.nodeOS["172.30.1.11"]["cpu_pct"] = 99.0
	if got := st.snapshot().nodeOS["172.30.1.11"]["cpu_pct"]; got != 12.5 {
		t.Fatalf("snapshot leaked mutation: %v", got)
	}

	now := time.Unix(1800000000, 0)
	view, typed := BuildClusterViews(st, now)
	if view["key"] != "plant-ft" || view["name"] != "Plant FT" || view["platform"] != "everrun" {
		t.Fatalf("view identity=%v/%v/%v", view["key"], view["name"], view["platform"])
	}
	if view["tz_offset_secs"] != int64(32400) || view["tz_name"] != "KST" {
		t.Fatalf("view timezone=%v/%v", view["tz_offset_secs"], view["tz_name"])
	}
	if len(listVal(view["nodes"])) != 2 || len(typed.Nodes) != 2 || len(typed.VMs) != 2 {
		t.Fatalf("typed/untyped cardinality nodes=%d/%d vms=%d",
			len(listVal(view["nodes"])), len(typed.Nodes), len(typed.VMs))
	}
	if typed.Nodes[0].OS == nil || len(typed.Nodes[0].OS.Links) != 1 || len(typed.Nodes[0].OS.Net) != 1 {
		t.Fatalf("OS topology conversion=%+v", typed.Nodes[0].OS)
	}
	if len(typed.Networks) != 3 || len(typed.StorageGroups) == 0 || len(typed.Volumes) == 0 ||
		len(typed.ImageContainers) == 0 || len(typed.Alerts) == 0 || typed.License == nil {
		t.Fatalf("incomplete typed view: %+v", typed)
	}
	nicMap := dictVal(view["nic_network_map"])
	if len(nicMap) != 2 || len(typed.NICNetworkMap) != 2 {
		t.Fatalf("NIC map=%v typed=%v", nicMap, typed.NICNetworkMap)
	}
	collection := dictVal(view["collection"])
	if dictVal(collection["errors"])["static"] != "license temporarily unavailable" {
		t.Fatalf("collection errors=%v", collection["errors"])
	}
	if len(listVal(view["traps"])) != 1 {
		t.Fatalf("view traps=%v", view["traps"])
	}

	graph := BuildFlatTopology(view)
	if len(listVal(graph["nodes"])) < 10 || len(listVal(graph["edges"])) < 10 {
		t.Fatalf("captured topology too small: nodes=%d edges=%d", len(listVal(graph["nodes"])), len(listVal(graph["edges"])))
	}
}

func TestFleetCachePublishesAndFallsBackToStaleView(t *testing.T) {
	cache := NewFleetCache()
	if fleet, topo, ts := cache.Snapshot(); fleet != nil || topo != nil || ts != 0 {
		t.Fatalf("new cache snapshot=%v/%v/%v", fleet, topo, ts)
	}
	if full, ts := cache.SnapshotFull(); full != nil || ts != 0 {
		t.Fatalf("new full snapshot=%v/%v", full, ts)
	}

	cache.Update(nil)
	empty, emptyTopo, emptyTS := cache.Snapshot()
	if empty == nil || len(listVal(empty["clusters"])) != 0 || emptyTopo == nil || emptyTS == 0 {
		t.Fatalf("empty fleet not published: %v/%v/%v", empty, emptyTopo, emptyTS)
	}

	st := populatedState(t)
	cache.Update([]*ClusterState{st})
	fleet, topo, ts := cache.Snapshot()
	full, fullTS := cache.SnapshotFull()
	if len(listVal(fleet["clusters"])) != 1 || len(listVal(topo["clusters"])) != 1 || ts == 0 {
		t.Fatalf("cache snapshot incomplete: fleet=%v topo=%v ts=%v", fleet, topo, ts)
	}
	if full == nil || full["generated_at"] == nil || fullTS != ts {
		t.Fatalf("full topology snapshot=%v/%v", full, fullTS)
	}

	// A malformed live config exercises the per-cluster panic boundary. The
	// cache must retain the last good view instead of dropping the cluster.
	cfg := st.Cfg
	st.Cfg = nil
	cache.Update([]*ClusterState{st})
	st.Cfg = cfg
	fallbackFleet, _, _ := cache.Snapshot()
	clusters := listVal(fallbackFleet["clusters"])
	if len(clusters) != 1 {
		t.Fatalf("fallback cluster count=%d", len(clusters))
	}
	fallback := dictVal(clusters[0])
	if !boolVal(fallback["stale"]) {
		t.Fatalf("fallback was not marked stale: %v", fallback)
	}
	errs := dictVal(dictVal(fallback["collection"])["errors"])
	if errs["view_build"] == nil {
		t.Fatalf("fallback error missing: %v", errs)
	}

	if _, _, err := safeBuildViews(nil, time.Unix(0, 0)); err == nil {
		t.Fatal("safeBuildViews did not recover nil-state panic")
	}
	if staleCluster(nil) != nil {
		t.Fatal("nil fallback should remain nil")
	}
	direct := staleCluster(map[string]any{"key": "c", "collection": map[string]any{}})
	if !boolVal(direct["stale"]) || dictVal(dictVal(direct["collection"])["errors"])["view_build"] == nil {
		t.Fatalf("direct stale clone=%v", direct)
	}
	if got := buildFullTopologyMap(nil, 1234); got == nil || got["generated_at"] != int64(1234) {
		t.Fatalf("empty full topology=%v", got)
	}
}

func TestStateOSFallbackAndDisplayMetadata(t *testing.T) {
	cfg := &config.ClusterConfig{Key: "c", Name: "old", HistoryPoints: 2}
	st := NewClusterState(cfg, 2)
	st.setNodeOS("10.0.0.1", map[string]any{
		"ip": "10.0.0.1", "name": "n1", "reachable": true, "source": "ssh",
		"ts": 100.0, "cpu_pct": 80.0, "mem_pct": 70.0,
		"tz_name": "KST", "snmp": map[string]any{"reachable": true},
	}, nil)
	st.failNodeOS("10.0.0.1", "")
	failed := st.snapshot().nodeOS["10.0.0.1"]
	if boolVal(failed["reachable"]) || failed["cpu_pct"] != nil || failed["last_ssh_ts"] != 100.0 || failed["name"] != "n1" {
		t.Fatalf("SSH failure cleanup=%v", failed)
	}
	st.snmpNodeOS("10.0.0.1", "n1", map[string]any{
		"reachable": true, "cpu_pct": 25.0, "mem_pct": 35.0, "uptime_secs": 400.0,
	})
	viaSNMP := st.snapshot().nodeOS["10.0.0.1"]
	if viaSNMP["source"] != "snmp" || viaSNMP["cpu_pct"] != 25.0 {
		t.Fatalf("SNMP fallback=%v", viaSNMP)
	}
	st.failNodeOS("10.0.0.1", "n1")
	preserved := st.snapshot().nodeOS["10.0.0.1"]
	if preserved["source"] != "snmp" || preserved["cpu_pct"] != 25.0 {
		t.Fatalf("SNMP values were not preserved=%v", preserved)
	}
	st.setNodeOS("10.0.0.1", map[string]any{"reachable": true, "source": "ssh", "cpu_pct": 10.0}, nil)
	if st.snapshot().nodeOS["10.0.0.1"]["snmp"] == nil {
		t.Fatal("SSH update discarded secondary SNMP signal")
	}
	st.snmpNodeOS("10.0.0.2", "n2", map[string]any{"reachable": false})
	if st.snapshot().nodeOS["10.0.0.2"]["source"] != "snmp" {
		t.Fatal("SNMP identity entry was not created")
	}

	st.PatchDisplayMeta(map[string]string{
		"label": "new", "company": "Acme", "factory": "F1", "site": "S1", "ignored": "x",
	})
	if st.DisplayName() != "new" || cfg.Company != "Acme" || cfg.Factory != "F1" || cfg.Site != "S1" {
		t.Fatalf("display metadata=%+v", cfg)
	}
	st.AddTrap(map[string]any{"name": "first"})
	st.AddTrap(map[string]any{"name": "second"})
	st.AddTrap(map[string]any{"name": "third"})
	traps := st.TrapsSnapshot()
	if len(traps) != 2 || traps[0]["name"] != "third" {
		t.Fatalf("trap cap/order=%v", traps)
	}
}

func TestNICMapDevicesAndWorkerPureContracts(t *testing.T) {
	role := "business"
	ord := int64(1)
	p1 := "spine-p1"
	missing := "missing"
	spines := map[string]*sshmetrics.Spine{
		"nodeA": {
			Networks:    []sshmetrics.SpineNetwork{{Name: p1, Role: &role, Ordinal: &ord}, {Name: "", Role: nil}},
			NICNetworks: map[string]*string{"eth0": &p1, "eth1": nil, "eth2": &missing},
		},
		"":      {NICNetworks: map[string]*string{"eth0": nil}},
		"nodeB": nil,
	}
	nets := []avcli.SharedNetwork{{Name: sp("P1"), Role: sp("business")}}
	m := BuildNICNetworkMap(spines, nets, map[string]map[string]string{
		"nodeA": {"eth9": "override"}, "nodeC": {"eth0": "P1"},
	})
	a := dictVal(m["nodeA"])
	if len(a) != 3 || a["eth2"] != nil || a["eth9"] != "override" {
		t.Fatalf("NIC reconciliation=%v", m)
	}
	unused := dictVal(a["eth1"])
	if unused["network"] != nil || unused["evidence"] != "config" {
		t.Fatalf("unused NIC evidence=%v", unused)
	}
	typed := TypedNICNetworkMap(map[string]any{
		"nodeA": a,
		"bad":   "not-a-map",
	})
	if len(typed) != 1 || typed["nodeA"]["eth0"].Network == nil || typed["nodeA"]["eth1"].Network != nil {
		t.Fatalf("typed NIC map=%v", typed)
	}
	if spineRole(sshmetrics.SpineNetwork{}) != "" || spineOrdinal(sshmetrics.SpineNetwork{}) != 1<<30 {
		t.Fatal("nil spine defaults changed")
	}

	alerts := make([]any, 0, 30)
	for i := 0; i < 30; i++ {
		alerts = append(alerts, map[string]any{
			"name": "alarm", "description": "", "time": time.Unix(int64(i), 0).UTC().Format(time.RFC3339),
			"severity": "unexpected",
		})
	}
	fleet := map[string]any{"clusters": []any{
		map[string]any{"key": "c1", "name": "view name", "nodes": []any{map[string]any{"state": "running"}}, "alerts": alerts},
		"invalid",
		map[string]any{"key": "", "nodes": []any{}, "alerts": []any{}},
	}}
	devices := BuildDevices(fleet, map[string]DisplayMeta{"c1": {Label: "configured"}}, 5)
	list := listVal(devices["devices"])
	if len(list) != 2 || dictVal(dictVal(list[0])["meta"])["label"] != "configured" {
		t.Fatalf("devices=%v", devices)
	}
	gotAlerts := listVal(dictVal(dictVal(list[0])["meta"])["alerts"])
	if len(gotAlerts) != 25 || dictVal(gotAlerts[0])["severity"] != "info" {
		t.Fatalf("normalized alerts=%v", gotAlerts)
	}
	if AvailNominal("op") != 99.99 || availNominal("deg") != 99.9 || AvailNominal("down") != 99.0 {
		t.Fatal("nominal availability mapping changed")
	}

	workerCfg := &config.ClusterConfig{Key: "workers", Intervals: config.Intervals{Fast: 1, Slow: 2, Static: 3, OS: 4, SNMP: 5}}
	workerState := NewClusterState(workerCfg, 1)
	aw := NewAvcliWorker(workerState, nil)
	ow := NewOsMetricsWorker(workerState, nil)
	sw := NewSnmpWorker(workerState)
	if aw.fast != time.Second || aw.slow != 2*time.Second || aw.static != 3*time.Second ||
		ow.interval != 4*time.Second || sw.interval != 5*time.Second {
		t.Fatalf("worker intervals=%v/%v/%v/%v/%v", aw.fast, aw.slow, aw.static, ow.interval, sw.interval)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	aw.Start(cancelled)
	ow.Start(cancelled)
	sw.Start(context.Background()) // disabled configuration returns immediately

	for _, kind := range []snmp.ValueKind{
		snmp.KindInt, snmp.KindCounter, snmp.KindGauge, snmp.KindTimeticks, snmp.KindCounter64,
	} {
		if got, ok := snmpInt(snmp.Value{Kind: kind, Int: 42}); !ok || got != 42 {
			t.Fatalf("snmpInt kind=%v got=%v/%v", kind, got, ok)
		}
	}
	if _, ok := snmpInt(snmp.Value{Kind: snmp.KindString, Str: "42"}); ok {
		t.Fatal("string SNMP value accepted as integer")
	}
	if errString(nil) != "" || errString(errors.New("boom")) != "boom" {
		t.Fatal("error formatting contract changed")
	}
}
