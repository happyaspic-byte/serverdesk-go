package poller

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"serverdesk/internal/edge"
	"serverdesk/internal/snmp"
)

func readyEdgeManager(t *testing.T) *EdgeManager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	devices := []edge.DeviceConfig{
		{Key: "nas", Kind: "nas", IP: "192.0.2.1", Name: "before", Company: "original"},
		{Key: "other", Kind: "nas", IP: "192.0.2.2", Name: "unchanged"},
	}
	w := edge.NewWorker(devices)
	w.SNMPGet = func(context.Context, string, int, string, []string, time.Duration) (map[string]snmp.Value, error) {
		return nil, nil
	}
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
	mgr := readyEdgeManager(t)
	for _, source := range []string{"worker", "fallback"} {
		t.Run(source, func(t *testing.T) {
			if source == "fallback" {
				pendingEdgeRound(mgr)
			}
			first := mgr.Latest()
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
