package deviceview

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func richClusterView() map[string]any {
	alerts := []any{nil}
	for i := 0; i < 30; i++ {
		severity := "INFO"
		if i == 28 {
			severity = "warning"
		}
		if i == 29 {
			severity = "critical"
		}
		alerts = append(alerts, map[string]any{
			"name":        fmt.Sprintf("alert-%02d", i),
			"description": fmt.Sprintf("Business condition %02d", i),
			"time":        fmt.Sprintf("2026-08-24 12:%02d:00", i),
			"severity":    severity,
		})
	}
	alerts = append(alerts, map[string]any{
		"name": "fallback-description", "time": "2026-08-24 11:00:00", "severity": "unexpected",
	})

	return map[string]any{
		"key":            "cluster-rich",
		"name":           "Rich cluster",
		"platform":       "ZTC_EDGE",
		"mgmt_ip":        "10.20.30.40",
		"version":        "9.0",
		"uuid":           "cluster-uuid",
		"tz_offset_secs": float64(9 * 60 * 60),
		"tz_name":        "Asia/Seoul",
		"stale":          true,
		"nodes": []any{
			map[string]any{
				"name": "node-a", "state": "running", "standing_state": "normal", "mode": "production",
				"reachable": true, "primary": true, "cpu_pct": 20.4, "mem_pct": 40.5,
				"uptime_secs": 7200.0, "manufacturer": "Acme", "model": "X1", "cpus": 8,
				"memory_raw": "32 GiB", "memory_bytes": 32 * gib, "ip": "10.20.30.41",
				"serial": "SER-A", "bios": "BIOS-A", "metrics_source": "ssh", "temp_max_c": 51.2,
				"os": map[string]any{
					"cpu_model": "Xeon", "cpu_cores": float32(8), "mem_total_bytes": 31 * gib,
					"load": []any{0.1, 0.2, 0.3}, "fs_max_pct": 61.0,
				},
				"vm_placements": []any{map[string]any{"name": "vm-active"}, nil},
				"history": map[string]any{
					"cpu": []any{map[string]any{"v": 10.04}, map[string]any{"v": 20.06}, "bad"},
					"mem": []any{30.0, 40.0, 50.0},
				},
			},
			map[string]any{
				"name": "node-b", "state": "running", "standing_state": "normal", "mode": "normal",
				"reachable": false, "cpu_pct": 99.0, "mem_pct": 99.0, "uptime_secs": 172800.0,
				"manufacturer": "Acme", "memory_bytes": 16 * gib, "ip": "10.20.30.42",
				"vm_placements": []any{},
				"history": map[string]any{
					"cpu": []any{5.0, 15.0, 25.0},
					"mem": []any{20.0, 30.0},
				},
			},
			nil,
		},
		"vms": []any{
			map[string]any{
				"name": "vm-active", "state": "running", "ha_mode": "FT", "cpus": json.Number("4"),
				"memory_raw": "8 GiB", "standing_state": "normal", "nodes": []any{"node-a", "node-b"},
				"os_type": "linux", "uuid": "vm-uuid", "redundancy": "duplex",
				"disk_mirrored": true, "nic_redundant": true,
				"volumes": []any{
					map[string]any{"name": "data-vol", "size_bytes": float64(2 * 1024 * 1024), "is_cdrom": false},
					map[string]any{"name": "cdrom", "size_bytes": float64(8 * 1024 * 1024), "is_cdrom": true},
					nil,
				},
				"interfaces": []any{
					map[string]any{"shared_network": "Business"}, map[string]any{"shared_network": ""}, nil,
				},
			},
			map[string]any{
				"name": "vm-stopped", "state": "stopped", "nodes": []any{"node-b"},
				"volumes": []any{}, "interfaces": []any{},
			},
			nil,
		},
		"unit": map[string]any{
			"name": "", "version": "", "syncing": true, "uuid": "unit-uuid", "ntp": nil,
			"configured": true,
			"resources": map[string]any{
				"total_vcpus": 32, "used_vcpus": int64(12),
				"total_memory_bytes": 64 * gib, "used_memory_bytes": 24 * gib,
				"vcpu_pct": 37.5, "memory_pct": 37.5,
			},
		},
		"license": map[string]any{
			"name": "license", "type": "perpetual", "edition": "enterprise",
			"install_epoch": 1.0, "expires": true, "expire_epoch": 86401.0,
			"activated": true, "days_left": 100,
		},
		"alerts": alerts,
		"health": map[string]any{
			"level": "warning", "alert_counts": map[string]any{"warning": 1}, "reasons": []any{"storage"},
		},
		"collection": map[string]any{
			"errors": map[string]any{"slow": "timeout"}, "last_success": "2026-08-24 12:00:00",
		},
		"traps": []any{map[string]any{"oid": "1.2.3"}},
		"nic_network_map": map[string]any{
			"node-a": map[string]any{
				"eth0": map[string]any{"network": "Business"}, "eth1": "A-Link", "ignored": "",
			},
			"node-b":  map[string]any{"eth0": map[string]any{"network": "Business"}},
			"invalid": "not-a-map",
		},
		"networks": []any{
			map[string]any{"name": "Business", "id": "net-b", "role": "business", "fault_tolerant": "yes", "bandwidth_raw": "1G", "mtu": 1500},
			map[string]any{"name": "A-Link", "id": "net-a", "role": "a-link", "fault_tolerant": "yes", "bandwidth_raw": "10G", "mtu": json.Number("9000")},
			nil,
		},
		"volumes": []any{
			map[string]any{"name": "data-vol", "storage_group_name": "sg-critical"},
			map[string]any{"name": "other-vol", "storage_group_name": "sg-degraded"}, nil,
		},
		"storage_groups": []any{
			map[string]any{
				"name": "sg-critical", "id": "sg1", "used_bytes": 95.0, "size_bytes": 100.0,
				"used_raw": "95G", "size_raw": "100G",
				"disks": []any{
					map[string]any{"name": "disk-a", "node_name": "node-a", "standing_state": "normal", "size_raw": "100G"},
					map[string]any{"name": "disk-b", "node_name": "node-b", "standing_state": "", "size_raw": "100G"}, nil,
				},
			},
			map[string]any{
				"name": "sg-degraded", "id": "sg2", "used_bytes": 80.0, "size_bytes": 100.0,
				"disks": []any{map[string]any{"name": "disk-c", "node_name": "node-a", "standing_state": "BROKEN"}},
			},
			map[string]any{
				"name": "sg-ok", "id": "sg3", "used_bytes": 1.0, "size_bytes": 0.0,
				"disks": []any{},
			},
			nil,
		},
	}
}

func TestScalarConversionHelpers(t *testing.T) {
	t.Run("numbers", func(t *testing.T) {
		cases := []struct {
			name string
			in   any
			want float64
			ok   bool
		}{
			{"float64", float64(1.25), 1.25, true},
			{"float32", float32(2.5), 2.5, true},
			{"int", int(3), 3, true},
			{"int64", int64(4), 4, true},
			{"json", json.Number("5.5"), 5.5, true},
			{"bad-json", json.Number("x"), 0, false},
			{"bool", true, 0, false},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got, ok := numVal(tc.in)
				if got != tc.want || ok != tc.ok {
					t.Fatalf("numVal(%v) = (%v,%v), want (%v,%v)", tc.in, got, ok, tc.want, tc.ok)
				}
			})
		}
	})

	if got := pctOf(25, 40); got != 62.5 {
		t.Fatalf("pctOf = %v", got)
	}
	for _, args := range [][2]any{{"bad", 1}, {1, "bad"}, {1, 0}} {
		if got := pctOf(args[0], args[1]); got != nil {
			t.Fatalf("pctOf%v = %v, want nil", args, got)
		}
	}
	if got := gibOf(3 * gib); got != 3.0 {
		t.Fatalf("gibOf = %v", got)
	}
	if got := gibOf("bad"); got != nil {
		t.Fatalf("gibOf bad = %v", got)
	}
	if got := numOrNil(json.Number("7.5")); got != 7.5 {
		t.Fatalf("numOrNil = %v", got)
	}
	if got := numOrNil(false); got != nil {
		t.Fatalf("numOrNil bool = %v", got)
	}
	if got := loadOrNil([]any{}); got != nil {
		t.Fatalf("empty load = %v", got)
	}
	if got := loadOrNil([]any{1.0}); !reflect.DeepEqual(got, []any{1.0}) {
		t.Fatalf("load = %#v", got)
	}
}

func TestFormattingAndSeriesHelpers(t *testing.T) {
	if tsFmt(0, 0) != "" || tsFmt(1, 3600) != "1970-01-01 01:00:01" {
		t.Fatalf("unexpected tsFmt values: %q %q", tsFmt(0, 0), tsFmt(1, 3600))
	}
	if licDateFmt(0, 0) != "" || licDateFmt(1, 0) != "Thu Jan 01 00:00:01 KST 1970" {
		t.Fatalf("unexpected licDateFmt values: %q %q", licDateFmt(0, 0), licDateFmt(1, 0))
	}
	for _, tc := range []struct{ in, want string }{
		{"10.2.3.44", "10.2.3.0/24"},
		{"10.2.3", ""},
		{"10.2..4", ""},
		{"10.2.x.4", ""},
	} {
		if got := cidr24(tc.in); got != tc.want {
			t.Errorf("cidr24(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	history := []any{"bad", map[string]any{"v": "bad"}}
	for i := 0; i < 55; i++ {
		if i%2 == 0 {
			history = append(history, map[string]any{"v": float64(i) + 0.06})
		} else {
			history = append(history, float64(i)+0.06)
		}
	}
	series := seriesOf(history)
	if len(series) != 48 || series[0] != 7.1 || series[47] != 54.1 {
		t.Fatalf("series trim/round = len %d, first %v, last %v", len(series), series[0], series[47])
	}
}

func TestNodeAndSNMPMetadataBranches(t *testing.T) {
	view := richClusterView()
	nodes := metaNodes(view)
	if len(nodes) != 2 {
		t.Fatalf("nodes len = %d", len(nodes))
	}
	first := nodes[0].(map[string]any)
	if first["standing"] != "normal" || first["cpus"] != "8" || first["memGiB"] != 31.0 || first["vmCount"] != 2 {
		t.Fatalf("first node metadata = %#v", first)
	}
	second := nodes[1].(map[string]any)
	if second["standing"] != "normal" || second["memGiB"] != 16.0 || second["loadAvg"] != nil {
		t.Fatalf("second node fallback metadata = %#v", second)
	}

	snmp := metaSNMP(view, 10000)
	a := snmp[0].(map[string]any)
	b := snmp[1].(map[string]any)
	if a["cpu"] != int64(20) || a["mem"] != int64(40) || a["rebooted_at"] != int64(2800) || a["fresh"].(bool) {
		t.Fatalf("reachable SNMP metadata = %#v", a)
	}
	if _, ok := b["cpu"]; ok {
		t.Fatalf("unreachable node leaked CPU: %#v", b)
	}
}

func TestVMMetadataPlacementAndFallback(t *testing.T) {
	view := richClusterView()
	place := placements(view)
	if !reflect.DeepEqual(place["vm-active"], []string{"node-a"}) {
		t.Fatalf("placements = %#v", place)
	}
	vms := metaVMs(view)
	if len(vms) != 2 {
		t.Fatalf("VM count = %d", len(vms))
	}
	active := vms[0].(map[string]any)
	if active["node"] != "node-a" || active["diskMB"] != int64(2) || active["cpus"] != "4" || active["ft"] != "ft" {
		t.Fatalf("active VM = %#v", active)
	}
	if !reflect.DeepEqual(active["standbyNodes"], []any{"node-b"}) ||
		!reflect.DeepEqual(active["networks"], []any{"Business"}) {
		t.Fatalf("active VM relations = %#v", active)
	}
	stopped := vms[1].(map[string]any)
	if stopped["node"] != "node-b" || !reflect.DeepEqual(stopped["nodes"], []any{"node-b"}) {
		t.Fatalf("stopped VM instance fallback = %#v", stopped)
	}
}

func TestUnitLicenseAlertsEventsAndReboot(t *testing.T) {
	view := richClusterView()
	unit := metaUnit(view)
	if unit["name"] != "10.20.30.40" || unit["version"] != "9.0" || unit["syncing"] != "true" || unit["totMem"] != 64.0 {
		t.Fatalf("unit metadata = %#v", unit)
	}
	if !reflect.DeepEqual(unit["ntp"], []any{}) {
		t.Fatalf("nil NTP was not normalized: %#v", unit["ntp"])
	}
	lic := metaLicense(view, 0).(map[string]any)
	if lic["install"] != "Thu Jan 01 00:00:01 KST 1970" || lic["expire"] != "Fri Jan 02 00:00:01 KST 1970" {
		t.Fatalf("license dates = %#v", lic)
	}
	if metaLicense(map[string]any{}, 0) != nil {
		t.Fatal("missing license should be nil")
	}

	alerts := metaAlerts(view)
	if len(alerts) != 25 {
		t.Fatalf("alerts not capped: %d", len(alerts))
	}
	if alerts[0].(map[string]any)["name"] != "alert-29" || alerts[0].(map[string]any)["sev"] != "critical" {
		t.Fatalf("alerts not sorted/normalized: %#v", alerts[0])
	}
	events := metaEvents(view, alerts)
	if len(events) != 6 || events[0].(map[string]any)["host"] != "Rich cluster" {
		t.Fatalf("events = %#v", events)
	}

	reboot := lastReboot(view, 10000).(map[string]any)
	if reboot["node"] != "node-a" || reboot["agoSecs"] != int64(7200) {
		t.Fatalf("last reboot = %#v", reboot)
	}
	if lastReboot(map[string]any{"nodes": []any{nil, map[string]any{"uptime_secs": 90000.0}}}, 10000) != nil {
		t.Fatal("old or invalid reboot should be nil")
	}
}

func TestTopologyGroupingStatusAndOrdering(t *testing.T) {
	topo := metaTopo(richClusterView())
	networks := topo["networks"].([]any)
	if len(networks) != 2 || networks[0].(map[string]any)["name"] != "A-Link" {
		t.Fatalf("network order = %#v", networks)
	}
	business := networks[1].(map[string]any)
	if business["status"] != "deg" {
		t.Fatalf("alert propagation status = %#v", business)
	}
	wantNICs := map[string]any{"node-a": []any{"eth0"}, "node-b": []any{"eth0"}}
	if !reflect.DeepEqual(business["nics"], wantNICs) {
		t.Fatalf("network NIC grouping = %#v", business["nics"])
	}

	storage := topo["storage"].([]any)
	wantStatus := []string{"down", "deg", "op"}
	for i, want := range wantStatus {
		if got := storage[i].(map[string]any)["status"]; got != want {
			t.Errorf("storage[%d] status = %v, want %s", i, got, want)
		}
	}
	if storage[0].(map[string]any)["mirrored"] != true || storage[0].(map[string]any)["volumes"] != 1 {
		t.Fatalf("storage grouping = %#v", storage[0])
	}

	vmNetworks := topo["vmNetworks"].(map[string]any)
	vmStorage := topo["vmStorage"].(map[string]any)
	vmNodes := topo["vmNodes"].(map[string]any)
	vmStandby := topo["vmStandby"].(map[string]any)
	if !reflect.DeepEqual(vmNetworks["vm-active"], []any{"Business"}) ||
		!reflect.DeepEqual(vmStorage["vm-active"], []any{"sg-critical"}) ||
		!reflect.DeepEqual(vmNodes["vm-active"], []any{"node-a"}) ||
		!reflect.DeepEqual(vmStandby["vm-active"], []any{"node-b"}) {
		t.Fatalf("VM topology relations: networks=%#v storage=%#v nodes=%#v standby=%#v", vmNetworks, vmStorage, vmNodes, vmStandby)
	}
	if !reflect.DeepEqual(vmNodes["vm-stopped"], []any{"node-b"}) {
		t.Fatalf("stopped VM node fallback = %#v", vmNodes["vm-stopped"])
	}
}

func TestBuildDeviceTypeConfigAndStatusBranches(t *testing.T) {
	baseNodes := func(states ...string) []any {
		out := make([]any, 0, len(states))
		for i, state := range states {
			out = append(out, map[string]any{
				"name": fmt.Sprintf("n%d", i), "state": state, "standing_state": "normal", "mode": "normal",
				"reachable": true, "cpu_pct": float64(10 + i*20), "mem_pct": float64(20 + i*20),
				"uptime_secs": float64((i + 1) * 86400),
			})
		}
		return out
	}
	platforms := []struct{ platform, want string }{
		{"everrun", "EV"}, {"ZTCEDGE", "EDGE"}, {"ztc_edge", "EDGE"},
		{"endurance", "END"}, {"ztcendurance", "END"}, {"ftserver", "FTS"}, {"unknown", "EV"},
	}
	for _, tc := range platforms {
		t.Run(tc.platform, func(t *testing.T) {
			view := map[string]any{"key": "k", "platform": tc.platform, "nodes": baseNodes("running", "running")}
			if got := BuildDevice(view, DisplayMeta{}, 1000)["type"]; got != tc.want {
				t.Fatalf("type = %v, want %s", got, tc.want)
			}
		})
	}

	cases := []struct {
		name       string
		view       map[string]any
		cfg        DisplayMeta
		wantStatus string
		wantSync   string
	}{
		{"down", map[string]any{"nodes": []any{}}, DisplayMeta{}, "down", "offline"},
		{"degraded", map[string]any{"key": "d", "nodes": baseNodes("running", "stopped")}, DisplayMeta{}, "deg", "simplex"},
		{"configured", map[string]any{"key": "c", "name": "ignored", "mgmt_ip": "10.1.2.3", "nodes": baseNodes("running", "running")}, DisplayMeta{Label: "Label", Company: "Co", Factory: "Factory", Site: "Site", AssetTag: "A", FloorPos: "1,2"}, "op", "sync"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dev := BuildDevice(tc.view, tc.cfg, 1000)
			if dev["status"] != tc.wantStatus || dev["sync"] != tc.wantSync {
				t.Fatalf("status/sync = %v/%v", dev["status"], dev["sync"])
			}
		})
	}

	defaults := BuildDevice(map[string]any{}, DisplayMeta{}, 1000)
	meta := defaults["meta"].(map[string]any)
	checks := map[string]struct{ got, want any }{
		"id":          {defaults["id"], "cluster"},
		"cpu0":        {defaults["cpu0"], int64(-1)},
		"mem0":        {defaults["mem0"], int64(-1)},
		"label":       {meta["label"], "cluster"},
		"company":     {meta["company"], "루비컴"},
		"factory":     {meta["factory"], "—"},
		"site":        {defaults["site"], "—"},
		"vendor":      {meta["vendor"], "Stratus Technologies"},
		"healthLevel": {meta["healthLevel"], "unknown"},
	}
	for name, check := range checks {
		if !reflect.DeepEqual(check.got, check.want) {
			t.Errorf("default %s = %#v (%T), want %#v (%T)", name, check.got, check.got, check.want, check.want)
		}
	}

	rich := BuildDevice(richClusterView(), DisplayMeta{}, 20000)
	richMeta := rich["meta"].(map[string]any)
	if rich["type"] != "EDGE" || rich["cpu0"] != int64(20) || rich["mem0"] != int64(40) || rich["uptime"] != int64(2) {
		t.Fatalf("rich summary = %#v", rich)
	}
	if richMeta["factory"] != "10.20.30.0/24" || rich["site"] != "10.20.30.40" || richMeta["error"] == nil || richMeta["vmRunning"] != 1 {
		t.Fatalf("rich metadata defaults = %#v", richMeta)
	}
	if !reflect.DeepEqual(rich["histCpu"], []any{int64(12), int64(23)}) ||
		!reflect.DeepEqual(rich["histMem"], []any{int64(30), int64(40)}) {
		t.Fatalf("averaged history = cpu %#v mem %#v", rich["histCpu"], rich["histMem"])
	}
}

func TestBuildDevicesNormalization(t *testing.T) {
	fleet := map[string]any{
		"clusters":       []any{nil, map[string]any{"key": "one", "name": "One", "nodes": []any{map[string]any{"state": "running"}}}, "bad"},
		"generated_at":   nil,
		"poller_version": "v1",
		"overall":        "ok",
		"stale":          true,
	}
	got := BuildDevices(fleet, map[string]DisplayMeta{"one": {Label: "Configured"}}, 0)
	if got["count"] != 1 || got["refreshSec"] != int64(30) || got["generated_at"] == nil || got["stale"] != true {
		t.Fatalf("normalized fleet = %#v", got)
	}
	dev := got["devices"].([]any)[0].(map[string]any)
	if dev["meta"].(map[string]any)["label"] != "Configured" {
		t.Fatalf("config lookup failed: %#v", dev)
	}

	withTimestamp := BuildDevices(map[string]any{"clusters": []any{}, "generated_at": int64(123)}, nil, 15)
	if withTimestamp["generated_at"] != int64(123) || withTimestamp["refreshSec"] != int64(15) {
		t.Fatalf("explicit fleet fields lost: %#v", withTimestamp)
	}
}

func TestCollectionHelperBranches(t *testing.T) {
	if mapGet(map[string]any{"a": "not-map"}, "a", "b") != nil {
		t.Fatal("mapGet should stop at non-map")
	}
	if mapGet(map[string]any{"a": map[string]any{"b": 2}}, "a", "b") != 2 {
		t.Fatal("mapGet nested value missing")
	}
	if !reflect.DeepEqual(sortedKeys(map[string]bool{"z": true, "a": true}), []any{"a", "z"}) {
		t.Fatal("sortedKeys order mismatch")
	}
	if !reflect.DeepEqual(strListVal([]any{"x", 1, "y"}), []string{"x", "", "y"}) {
		t.Fatal("strListVal normalization mismatch")
	}
	if !reflect.DeepEqual(strSliceAny([]string{"x", "y"}), []any{"x", "y"}) {
		t.Fatal("strSliceAny conversion mismatch")
	}
}
