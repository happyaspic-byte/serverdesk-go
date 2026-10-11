package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"serverdesk/internal/edge"
)

func readyEdgeManager(t *testing.T) *EdgeManager {
	t.Helper()
	return readyEdgeManagerWithDevices(t, []edge.DeviceConfig{
		{Key: "nas", Kind: "nas", IP: "192.0.2.1", Name: "before", Company: "original"},
		{Key: "other", Kind: "nas", IP: "192.0.2.2", Name: "unchanged"},
	})
}

func readyEdgeManagerWithDevices(t *testing.T, devices []edge.DeviceConfig) *EdgeManager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := edge.NewWorker(devices)
	w.Start(ctx) // Publish one real down snapshot without network I/O.
	mgr := &EdgeManager{root: ctx, devices: devices, worker: w}
	t.Cleanup(mgr.Stop)
	return mgr
}

func pendingEdgeRound(mgr *EdgeManager) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if mgr.cancel != nil {
		mgr.cancel()
		mgr.cancel = nil
	}
	// Represent a replacement worker whose first round has not completed.
	mgr.worker = edge.NewWorker(nil)
}

func TestEdgeMetadataPatchPreservesPublishedSnapshots(t *testing.T) {
	mgr := readyEdgeManager(t)
	workerSnapshot := mgr.worker.LatestDevices()
	previous := mgr.Latest()
	before, err := json.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	mgr.PatchMeta("nas", map[string]string{"label": "after", "floor_pos": "F2"})
	pendingEdgeRound(mgr)

	for name, snapshot := range map[string][]map[string]any{"previous response": previous, "worker": workerSnapshot} {
		after, err := json.Marshal(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Errorf("%s changed after metadata edit", name)
		}
	}
	current := mgr.Latest()
	if len(current) != 2 {
		t.Fatalf("devices disappeared during worker replacement: %v", current)
	}
	meta := current[0]["meta"].(map[string]any)
	if meta["label"] != "after" || meta["floorPos"] != "F2" || meta["company"] != "original" {
		t.Errorf("updated metadata = %v", meta)
	}
	if current[0]["status"] != "down" || current[1]["meta"].(map[string]any)["label"] != "unchanged" {
		t.Errorf("unrelated snapshot data changed: %v", current)
	}

	// A second edit while collection is pending must retain the first edit.
	mgr.PatchMeta("nas", map[string]string{"company": "updated"})
	pendingEdgeRound(mgr)
	meta = mgr.Latest()[0]["meta"].(map[string]any)
	if meta["label"] != "after" || meta["company"] != "updated" || meta["floorPos"] != "F2" {
		t.Errorf("consecutive metadata edits were lost: %v", meta)
	}
	if current[0]["meta"].(map[string]any)["company"] != "original" {
		t.Error("second edit changed the first edit's published snapshot")
	}
}

func TestEdgeLatestAllowsPerResponseAvailability(t *testing.T) {
	for _, source := range []string{"worker", "fallback"} {
		t.Run(source, func(t *testing.T) {
			mgr := readyEdgeManager(t)
			if source == "fallback" {
				// Seed lastGood via a real read, then drop to the pending replacement worker
				// so Latest must serve the cached snapshot.
				if first := mgr.Latest(); len(first) == 0 {
					t.Fatal("ready worker published no snapshot")
				}
				pendingEdgeRound(mgr)
			}
			first := mgr.Latest()
			if len(first) == 0 {
				t.Fatalf("no snapshot for %s source", source)
			}
			first[0]["availN"] = 42.0
			first[0]["availDays"] = 30.0
			next := mgr.Latest()
			if next[0]["availN"] != 99.0 || next[0]["availDays"] != nil {
				t.Errorf("request-local availability leaked into shared snapshot: %v", next[0])
			}
		})
	}
}

func TestEdgeMetadataPatchBeforeFirstRead(t *testing.T) {
	mgr := readyEdgeManager(t)
	mgr.PatchMeta("nas", map[string]string{"label": "after"})
	pendingEdgeRound(mgr)
	current := mgr.Latest()
	if len(current) != 2 {
		t.Fatalf("worker snapshot was lost before the first read: %v", current)
	}
	if got := current[0]["meta"].(map[string]any)["label"]; got != "after" {
		t.Errorf("label = %v, want after", got)
	}
}

func TestEdgeMetadataPatchDoesNotRefreshExpiredFallback(t *testing.T) {
	mgr := readyEdgeManager(t)
	mgr.Latest()
	pendingEdgeRound(mgr)
	mgr.lastGoodAt = time.Now().Add(-131 * time.Second)
	mgr.PatchMeta("nas", map[string]string{"label": "after"})
	pendingEdgeRound(mgr)
	if got := mgr.Latest(); got != nil {
		t.Errorf("metadata edit revived expired collection: %v", got)
	}
}

func TestEdgeMetadataPatchConcurrentSnapshotReaders(t *testing.T) {
	mgr := readyEdgeManager(t)
	previous := mgr.Latest()
	start := make(chan struct{})
	var readers sync.WaitGroup
	for range 4 {
		readers.Go(func() {
			<-start
			for range 100 {
				if _, err := json.Marshal(previous); err != nil {
					t.Error(err)
				}
				current := mgr.Latest()
				if len(current) > 0 {
					current[0]["availN"] = 42.0
				}
				if _, err := json.Marshal(current); err != nil {
					t.Error(err)
				}
			}
		})
	}
	close(start)
	for i := range 20 {
		mgr.PatchMeta("nas", map[string]string{"label": fmt.Sprintf("after-%d", i)})
	}
	readers.Wait()
	if previous[0]["meta"].(map[string]any)["label"] != "before" {
		t.Error("metadata edit changed a published response")
	}
}

func deviceByID(t *testing.T, devices []map[string]any, id string) map[string]any {
	t.Helper()
	for _, d := range devices {
		if got, _ := d["id"].(string); got == id {
			return d
		}
	}
	t.Fatalf("device %s missing from %v", id, devices)
	return nil
}

func TestEdgeMetadataFallbackUsesCanonicalProjection(t *testing.T) {
	mgr := readyEdgeManager(t)
	published := mgr.Latest()
	before, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}

	mgr.PatchMeta("nas", map[string]string{"company": "", "factory": "", "site": "Seoul"})
	pendingEdgeRound(mgr)
	fallback := mgr.Latest()
	nas := deviceByID(t, fallback, "nas")
	if nas["site"] != "Seoul" {
		t.Errorf("top-level site = %v, want Seoul", nas["site"])
	}
	if _, phantom := nas["meta"].(map[string]any)["site"]; phantom {
		t.Errorf("site written to phantom meta.site: %v", nas["meta"])
	}
	meta := nas["meta"].(map[string]any)
	if meta["company"] != "루비컴" || meta["factory"] != "192.0.2.0/24" || meta["label"] != "before" {
		t.Errorf("cleared override projection = %v", meta)
	}

	mgr.PatchMeta("nas", map[string]string{"site": ""})
	pendingEdgeRound(mgr)
	if got := deviceByID(t, mgr.Latest(), "nas")["site"]; got != "192.0.2.1" {
		t.Errorf("cleared site = %v, want management IP", got)
	}
	if fallback[0]["site"] != "Seoul" {
		t.Error("later site clear mutated the earlier published fallback")
	}
	after, err := json.Marshal(published)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("canonical projection mutated the response published before the edit")
	}

	// NAS success hardcodes vendor, so a cleared override has no config-derived display.
	// While down, the current producer emits the raw empty configured value.
	mgr.PatchMeta("nas", map[string]string{"vendor": "Override"})
	pendingEdgeRound(mgr)
	if got := deviceByID(t, mgr.Latest(), "nas")["meta"].(map[string]any)["vendor"]; got != "Override" {
		t.Errorf("vendor override = %v", got)
	}
	mgr.PatchMeta("nas", map[string]string{"vendor": ""})
	pendingEdgeRound(mgr)
	if got := deviceByID(t, mgr.Latest(), "nas")["meta"].(map[string]any)["vendor"]; got != "" {
		t.Errorf("cleared NAS vendor while down = %v, want raw empty config", got)
	}

	// Run a real replacement round synchronously; do not accidentally compare
	// the fallback with another fallback while an asynchronous worker starts.
	replacement := edge.NewWorker(mgr.Devices())
	replacement.Start(mgr.root)
	firstRound := deviceByID(t, replacement.LatestDevices(), "nas")
	cleared := deviceByID(t, mgr.Latest(), "nas")
	if firstRound["site"] != cleared["site"] {
		t.Errorf("fallback site %v disagrees with first real round %v", cleared["site"], firstRound["site"])
	}
	fallbackMeta := cleared["meta"].(map[string]any)
	roundMeta := firstRound["meta"].(map[string]any)
	for _, key := range []string{"company", "factory", "vendor"} {
		if roundMeta[key] != fallbackMeta[key] {
			t.Errorf("fallback %s=%v disagrees with first real round %v", key, fallbackMeta[key], roundMeta[key])
		}
	}
}

func TestEdgeMetadataNoopDoesNotRespawn(t *testing.T) {
	mgr := readyEdgeManager(t)
	worker := mgr.worker
	cached := mgr.Latest()
	cachedAt := mgr.lastGoodAt

	for _, fields := range []map[string]string{
		{},
		nil,
		{"ignored": "value"},
		{"label": "before"},
		{"company": "original", "factory": "", "vendor": ""},
		{"site": "192.0.2.1", "factory": "192.0.2.0/24"},
	} {
		mgr.PatchMeta("nas", fields)
		if mgr.worker != worker {
			t.Fatalf("fields %v replaced worker %p with %p", fields, worker, mgr.worker)
		}
	}
	if !mgr.lastGoodAt.Equal(cachedAt) {
		t.Fatal("no-op metadata request refreshed cached snapshot age")
	}
	if cached[0]["meta"].(map[string]any)["label"] != "before" {
		t.Fatal("no-op request mutated the previously published snapshot")
	}

	mgr.PatchMeta("nas", map[string]string{"asset_tag": "AT-1"})
	pendingEdgeRound(mgr)
	if mgr.worker == worker {
		t.Fatal("real metadata edit did not replace worker")
	}
	meta := deviceByID(t, mgr.Latest(), "nas")["meta"].(map[string]any)
	if meta["assetTag"] != "AT-1" || meta["label"] != "before" {
		t.Errorf("real edit projection = %v", meta)
	}
	if cached[0]["meta"].(map[string]any)["assetTag"] != "" {
		t.Error("real edit mutated the snapshot published before it")
	}
}

func TestEdgeMetadataMissingKeyDoesNotRespawn(t *testing.T) {
	mgr := readyEdgeManager(t)
	worker := mgr.worker
	cachedAt := mgr.lastGoodAt
	mgr.PatchMeta("missing", map[string]string{
		"label": "x", "company": "", "factory": "", "site": "x", "vendor": "",
	})
	if mgr.worker != worker || !mgr.lastGoodAt.Equal(cachedAt) {
		t.Fatalf("missing key changed collector=%p age=%v", mgr.worker, mgr.lastGoodAt)
	}
	if got := mgr.Devices()[0].Name; got != "before" {
		t.Fatalf("missing key changed device name to %q", got)
	}
}

func TestEdgeMetadataClearLabelUsesKey(t *testing.T) {
	mgr := readyEdgeManager(t)
	mgr.PatchMeta("nas", map[string]string{"label": ""})
	pendingEdgeRound(mgr)
	if got := deviceByID(t, mgr.Latest(), "nas")["meta"].(map[string]any)["label"]; got != "nas" {
		t.Errorf("cleared label = %v, want device key", got)
	}
	if mgr.Devices()[0].Name != "" {
		t.Fatal("cleared label stored a synthesized name in config")
	}
}

func TestEdgeVendorClearPreservesReachableProducerDefault(t *testing.T) {
	// Retained snapshot carries the previously patched override; a clear must
	// project the producer's op-path constant and must not mutate this map.
	nas := map[string]any{
		"id": "nas", "type": "NAS", "status": "op", "site": "192.0.2.1",
		"meta": map[string]any{"label": "before", "vendor": "Override", "company": "original", "factory": "custom"},
	}
	mgr := &EdgeManager{
		root: context.Background(),
		devices: []edge.DeviceConfig{{
			Key: "nas", Kind: "nas", IP: "192.0.2.1", Name: "before", Company: "original", Factory: "custom", Vendor: "Override",
		}},
		worker:     edge.NewWorker(nil),
		lastGood:   []map[string]any{nas},
		lastGoodAt: time.Now(),
	}
	t.Cleanup(mgr.Stop)
	mgr.PatchMeta("nas", map[string]string{"vendor": ""})
	pendingEdgeRound(mgr)
	if got := deviceByID(t, mgr.Latest(), "nas")["meta"].(map[string]any)["vendor"]; got != "Synology" {
		t.Errorf("reachable cleared NAS vendor = %v, want Synology", got)
	}
	if nas["meta"].(map[string]any)["vendor"] != "Override" {
		t.Error("vendor clear mutated the retained snapshot in place")
	}
}

func TestEdgeVendorClearUsesProducerDefault(t *testing.T) {
	// Down snapshots use baseDevice's raw configured vendor for every kind.
	// Proxmox substitutes "Proxmox" only after a non-down collection. Server
	// uses Redfish's maker only when that collection is reachable.
	cases := []struct {
		kind string
		want string
	}{
		{kind: "nas", want: ""},
		{kind: "printer", want: ""},
		{kind: "plc", want: ""},
		{kind: "proxmox", want: ""},
		{kind: "server", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			mgr := readyEdgeManagerWithDevices(t, []edge.DeviceConfig{{
				Key: tc.kind, Kind: tc.kind, IP: "192.0.2.1", Name: "before", Vendor: "Override",
			}})
			mgr.PatchMeta(tc.kind, map[string]string{"vendor": ""})
			pendingEdgeRound(mgr)
			if got := deviceByID(t, mgr.Latest(), tc.kind)["meta"].(map[string]any)["vendor"]; got != tc.want {
				t.Errorf("cleared %s vendor = %v, want %q", tc.kind, got, tc.want)
			}
		})
	}
}

func TestEdgeRemoveInvalidatesCachedIdentity(t *testing.T) {
	mgr := readyEdgeManager(t)
	published := mgr.Latest()
	mgr.Remove("nas")
	mgr.Add(edge.DeviceConfig{Key: "nas", Kind: "printer", IP: "192.0.2.9", Name: "reused"})
	pendingEdgeRound(mgr)

	for _, d := range mgr.Latest() {
		if id, _ := d["id"].(string); id == "nas" {
			t.Fatalf("cached NAS identity served for reincarnated key: %v", d)
		}
	}
	other := deviceByID(t, mgr.Latest(), "other")
	if other["status"] != "down" || other["meta"].(map[string]any)["label"] != "unchanged" {
		t.Errorf("other device fallback lost: %v", other)
	}
	if published[0]["meta"].(map[string]any)["label"] != "before" {
		t.Error("removal mutated a previously published snapshot")
	}
}
