package deviceview

import (
	"testing"
)

func TestBuildDevicesBasic(t *testing.T) {
	node := map[string]any{
		"name":           "node0",
		"state":          "running",
		"standing_state": "normal",
		"mode":           "normal",
		"reachable":      true,
		"cpu_pct":        25.0,
		"mem_pct":        50.0,
		"uptime_secs":    10000.0,
		"manufacturer":   "Stratus",
		"serial":         "SN123",
		"bios":           "1.0",
		"ip":             "10.0.0.1",
	}
	cluster := map[string]any{
		"key":            "c1",
		"name":           "Cluster 1",
		"platform":       "everrun",
		"mgmt_ip":        "10.0.0.10",
		"nodes":          []any{node},
		"vms":            []any{},
		"storage_groups": []any{},
		"networks":       []any{},
		"alerts":         []any{},
		"health":         map[string]any{"level": "ok"},
		"unit":           map[string]any{"version": "8.1.0"},
		"version":        "8.1.0",
		"tz_offset_secs": 32400.0,
	}
	fleet := map[string]any{
		"clusters":       []any{cluster},
		"generated_at":   int64(1700000000),
		"poller_version": "1.0.0",
		"overall":        "ok",
		"stale":          false,
	}
	cfgMap := map[string]DisplayMeta{
		"c1": {
			Label:    "Cluster 1 Display",
			Company:  "TestCo",
			Factory:  "PlantA",
			Site:     "Seoul",
			AssetTag: "TAG-001",
			FloorPos: "1,1",
		},
	}

	res := BuildDevices(fleet, cfgMap, 30)
	if res["schema"] != "serverdesk/device@1" {
		t.Fatalf("schema = %v, want serverdesk/device@1", res["schema"])
	}
	if res["count"] != 1 {
		t.Fatalf("count = %v, want 1", res["count"])
	}
	devs, ok := res["devices"].([]any)
	if !ok || len(devs) != 1 {
		t.Fatalf("invalid devices slice: %v", res["devices"])
	}
	dev := devs[0].(map[string]any)
	if dev["id"] != "c1" || dev["type"] != "EV" || dev["status"] != "op" {
		t.Fatalf("device fields mismatch: id=%v, type=%v, status=%v", dev["id"], dev["type"], dev["status"])
	}
	meta := dev["meta"].(map[string]any)
	if meta["label"] != "Cluster 1 Display" || meta["company"] != "TestCo" {
		t.Fatalf("meta mismatch: label=%v, company=%v", meta["label"], meta["company"])
	}
}

func TestDeriveStatusAndSync(t *testing.T) {
	mk := func(state, standing, mode string) map[string]any {
		return map[string]any{"state": state, "standing_state": standing, "mode": mode}
	}
	view := map[string]any{
		"nodes": []any{
			mk("running", "normal", "normal"),
			mk("running", "normal", "normal"),
		},
	}
	if got := DeriveStatus(view); got != "op" {
		t.Fatalf("DeriveStatus want op, got %s", got)
	}
	if got := DeriveSync(view, "op"); got != "sync" {
		t.Fatalf("DeriveSync want sync, got %s", got)
	}
}
