package poller

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAvailTrackerRecoversIncompleteDayHistory(t *testing.T) {
	for _, tc := range []struct {
		name string
		cell []float64
	}{
		{name: "null"},
		{name: "empty", cell: []float64{}},
		{name: "missing downtime", cell: []float64{1200}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			now := nowFloat()
			today, yesterday := kstDay(now), kstDay(now-86400)
			history := map[string]*availRec{
				"damaged": {Days: map[string][]float64{
					today: tc.cell, yesterday: {1200, 120},
				}},
				"healthy": {Days: map[string][]float64{today: {600, 60}}},
			}
			data, err := json.Marshal(history)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "availability.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			tr := NewAvailTracker(dir, func() [][2]string {
				// Stop after the first sample; shutdown must also flush the history.
				cancel()
				return [][2]string{{"damaged", "down"}, {"healthy", "op"}}
			})
			done := make(chan struct{})
			go func() {
				tr.Start(ctx)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("availability tracker did not finish sampling and flush on shutdown")
			}
			if kstDay(tr.last) != today {
				t.Skip("KST date changed between the persisted fixture and the sample")
			}

			// Only the incomplete bucket is reset. Complete history and subsequent
			// devices must still contribute to availability after a restart.
			reloaded := NewAvailTracker(dir, nil)
			devices := []map[string]any{{"id": "damaged"}, {"id": "healthy"}}
			reloaded.Apply(devices)
			if got := devices[0]["availN"]; got != 85.714 { // 180 down / 1260 observed
				t.Errorf("recovered device availability = %v, want 85.714", got)
			}
			if got := devices[1]["availN"]; got != 90.909 { // 60 down / 660 observed
				t.Errorf("healthy device availability = %v, want 90.909", got)
			}
			rows := reloaded.CSVSnapshot()
			if len(rows) != 2 {
				t.Fatalf("CSV rows = %+v, want preserved history and healthy device", rows)
			}
			if rows[0].Device != "damaged" || rows[0].Day != yesterday || rows[0].Avail != 90 || rows[0].ObservedSec != 1200 {
				t.Errorf("previous history changed: %+v", rows[0])
			}
			if rows[1].Device != "healthy" || rows[1].Day != today || rows[1].ObservedSec != 660 {
				t.Errorf("healthy device history = %+v", rows[1])
			}
		})
	}
}
