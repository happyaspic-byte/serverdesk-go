package httpapi

import (
	"net/http"
	"testing"
	"time"

	"serverdesk/internal/config"
	demodata "serverdesk/internal/demo"
	"serverdesk/internal/poller"
)

func demoAPIServer() *Server {
	clusterCfg := config.ClusterConfig{
		Key: "live-cluster-that-must-not-leak", MgmtIP: "10.0.0.10", Platform: "everRun",
		Intervals: config.Intervals{Fast: 60, Slow: 300, Static: 86400},
	}
	state := poller.NewClusterState(&clusterCfg, 50)
	cache := poller.NewFleetCache()
	cache.Update([]*poller.ClusterState{state})
	return &Server{
		Cache: cache, States: []*poller.ClusterState{state}, Cfg: &config.Config{},
		StartedAt: time.Now(), displayOverlay: map[string]map[string]string{},
		DemoMode: true, SampleDevices: demodata.Devices,
	}
}

func TestDemoDevicesReplaceLiveCache(t *testing.T) {
	server := demoAPIServer()
	for _, target := range []string{"/api/devices", "/api/fleet?format=devices"} {
		recorder, response := execRequest(server, http.MethodGet, target, nil, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", target, recorder.Code, recorder.Body.String())
		}
		if response["source"] != demodata.Source || response["demo"] != true || response["sample"] != true {
			t.Fatalf("GET %s mode metadata = %#v", target, response)
		}
		if response["count"] != float64(3) || response["stale"] != false || response["cache_age_secs"] != float64(0) {
			t.Fatalf("GET %s sample summary = %#v", target, response)
		}
		devices, ok := response["devices"].([]any)
		if !ok || len(devices) != 3 {
			t.Fatalf("GET %s devices = %#v", target, response["devices"])
		}
		for _, item := range devices {
			device := item.(map[string]any)
			if device["id"] == "live-cluster-that-must-not-leak" {
				t.Fatalf("GET %s mixed live cache data into demo response", target)
			}
			meta := device["meta"].(map[string]any)
			if meta["demo"] != true || meta["sample"] != true || device["source"] != demodata.Source {
				t.Fatalf("GET %s returned unmarked sample: %#v", target, device)
			}
		}
	}
}

func TestDemoDevicesFailClosedWithoutProvider(t *testing.T) {
	server := demoAPIServer()
	server.SampleDevices = nil
	recorder, response := execRequest(server, http.MethodGet, "/api/devices", nil, "")
	if recorder.Code != http.StatusServiceUnavailable || response["code"] != "sample_data_unavailable" {
		t.Fatalf("missing sample provider = %d %#v", recorder.Code, response)
	}
	if devices, ok := response["devices"].([]any); !ok || len(devices) != 0 {
		t.Fatalf("missing sample provider leaked live data: %#v", response)
	}
}

func TestDemoModeBlocksEveryAPIMutationBeforeRouting(t *testing.T) {
	server := demoAPIServer()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		for _, target := range []string{"/api/clusters", "/api/admin/notifications", "/api/clusters/live/action"} {
			recorder, response := execRequest(server, method, target, map[string]any{}, "")
			if recorder.Code != http.StatusForbidden || response["code"] != "demo_mode_read_only" || response["source"] != demodata.Source {
				t.Fatalf("%s %s = %d %#v", method, target, recorder.Code, response)
			}
		}
	}
	recorder, _ := execRequest(server, http.MethodOptions, "/api/clusters", nil, "")
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS in demo mode = %d", recorder.Code)
	}
}

func TestProductionDevicesRemainLiveWhenDemoDisabled(t *testing.T) {
	server := demoAPIServer()
	server.DemoMode = false
	recorder, response := execRequest(server, http.MethodGet, "/api/devices", nil, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("production GET /api/devices = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, exists := response["source"]; exists {
		t.Fatalf("production response unexpectedly marked as sample: %#v", response)
	}
	devices := response["devices"].([]any)
	if len(devices) != 1 || devices[0].(map[string]any)["id"] != "live-cluster-that-must-not-leak" {
		t.Fatalf("production devices changed by demo feature: %#v", devices)
	}
}
