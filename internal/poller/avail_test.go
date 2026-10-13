package poller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func boundedAvailabilityCall(t *testing.T, name string, f func()) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		var failure any
		defer func() {
			if r := recover(); r != nil {
				failure = r
			}
			done <- failure
		}()
		f()
	}()
	select {
	case failure := <-done:
		if failure != nil {
			t.Fatalf("%s panicked: %v", name, failure)
		}
	case <-time.After(time.Second):
		t.Fatalf("%s blocked", name)
	}
}

func TestAvailabilityMalformedDaysRemainUsable(t *testing.T) {
	for _, cell := range []string{"[]", "[1200]", "null", "[-1,0]", "[600,700]", "[600,-1]", "[600,0,9]", "[1e999,0]", "[1200,null]", "[null,0]", "[null,null]", `"invalid"`} {
		t.Run(cell, func(t *testing.T) {
			dir := t.TempDir()
			day := kstDay(nowFloat())
			raw := `{"broken":{"days":{"` + day + `":` + cell + `}},"valid":{"days":{"` + day + `":[1200,300]}}}`
			if err := os.WriteFile(filepath.Join(dir, "availability.json"), []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			tr := NewAvailTracker(dir, func() [][2]string { return [][2]string{{"broken", "down"}} })
			boundedAvailabilityCall(t, "Sample", tr.Sample)
			tr.last = nowFloat() - 60
			boundedAvailabilityCall(t, "continuous Sample", tr.Sample)
			devices := []map[string]any{{"id": "valid"}, {"id": "broken", "availN": 99.0}}
			boundedAvailabilityCall(t, "Apply", func() { tr.Apply(devices) })
			if devices[0]["availN"] != 75.0 {
				t.Fatalf("valid history lost: %v", devices[0])
			}
			if devices[1]["availN"] != 99.0 {
				t.Fatalf("malformed history counted: %v", devices[1])
			}
			boundedAvailabilityCall(t, "CSVSnapshot", func() {
				rows := tr.CSVSnapshot()
				if len(rows) != 1 || rows[0].Device != "valid" || rows[0].ObservedSec != 1200 {
					panic("invalid CSV history")
				}
			})
			boundedAvailabilityCall(t, "Flush", tr.Flush)
			restarted := NewAvailTracker(dir, nil)
			boundedAvailabilityCall(t, "restart", func() { restarted.Sample(); restarted.Apply(devices); restarted.CSVSnapshot(); restarted.Flush() })
			if devices[0]["availN"] != 75.0 {
				t.Fatalf("restart lost history: %v", devices[0])
			}
		})
	}
}

func TestAvailabilityStatusReportsProgressAndPersistenceFailure(t *testing.T) {
	tr := NewAvailTracker(t.TempDir(), func() [][2]string { return nil })
	tr.Sample()
	status := tr.Status()
	if status["samples"] != 1 || status["last_sample_at"] == nil || status["healthy"] != true {
		t.Fatalf("sample progress missing: %v", status)
	}
	tr.path = filepath.Join(t.TempDir(), "missing", "availability.json")
	tr.Flush()
	status = tr.Status()
	if status["healthy"] != false || status["last_error"] == nil {
		t.Fatalf("persistence failure hidden: %v", status)
	}
	tr.path = filepath.Join(t.TempDir(), "availability.json")
	tr.Flush()
	if tr.Status()["healthy"] != true {
		t.Fatal("successful flush did not recover health")
	}
}

func TestAvailabilityHealthReportsSamplerFailureAndRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := NewAvailTracker(t.TempDir(), func() [][2]string { cancel(); panic("collector failed") })
	boundedAvailabilityCall(t, "Start", func() { tr.Start(ctx) })
	if tr.Status()["healthy"] != false {
		t.Fatal("recovered sampler panic hidden")
	}
	tr.GetStatuses = nil
	tr.Sample()
	if tr.Status()["healthy"] != true {
		t.Fatal("sample recovery not reported")
	}
}

func TestAvailabilityStatusDoesNotWaitForPersistenceLock(t *testing.T) {
	tr := NewAvailTracker(t.TempDir(), nil)
	tr.Sample()
	tr.path = filepath.Join(t.TempDir(), "missing", "availability.json")
	tr.Flush()
	previous := tr.Status()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	boundedAvailabilityCall(t, "Status during persistence", func() {
		status := tr.Status()
		if status["healthy"] != false || status["busy"] != true || status["samples"] != previous["samples"] || status["last_error"] != previous["last_error"] {
			panic("busy health lost truthful prior progress/error")
		}
	})
}

func TestAvailabilitySamplerPanicBreaksObservationContinuity(t *testing.T) {
	statuses := func() [][2]string { return [][2]string{{"device", "down"}} }
	tr := NewAvailTracker(t.TempDir(), statuses)
	tr.Sample()
	tr.last = nowFloat() - 60
	tr.Sample()
	before := observedSeconds(tr, "device")
	ctx, cancel := context.WithCancel(context.Background())
	tr.GetStatuses = func() [][2]string { cancel(); panic("collector failed") }
	tr.last = nowFloat() - 120
	boundedAvailabilityCall(t, "failed sample loop", func() { tr.Start(ctx) })
	tr.GetStatuses = statuses
	tr.Sample()
	if got := observedSeconds(tr, "device"); got != before {
		t.Fatalf("failed observation interval charged: before=%.2f after=%.2f", before, got)
	}
	tr.last = nowFloat() - 60
	tr.Sample()
	if got := observedSeconds(tr, "device"); got < before+59 || got > before+61 {
		t.Fatalf("subsequent continuous interval lost: %.2f", got)
	}
}

func observedSeconds(tr *AvailTracker, id string) float64 {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	var total float64
	if rec := tr.state[id]; rec != nil {
		for _, cell := range rec.Days {
			if len(cell) == 2 {
				total += cell[0]
			}
		}
	}
	return total
}

func TestAvailabilityExcludesFirstAndResumedIntervals(t *testing.T) {
	statuses := [][2]string{{"steady", "op"}, {"returning", "down"}}
	tr := NewAvailTracker(t.TempDir(), func() [][2]string { return statuses })
	tr.Sample()
	if got := observedSeconds(tr, "steady"); got != 0 {
		t.Fatalf("first sample charged %.2fs", got)
	}
	tr.last = nowFloat() - 60
	tr.Sample()
	before := observedSeconds(tr, "returning")
	if before < 59 || before > 61 {
		t.Fatalf("continuous interval = %.2f", before)
	}
	statuses = [][2]string{{"steady", "op"}}
	tr.last = nowFloat() - 60
	tr.Sample()
	statuses = [][2]string{{"steady", "op"}, {"returning", "down"}, {"unknown", "unknown"}}
	tr.last = nowFloat() - 60
	tr.Sample()
	if got := observedSeconds(tr, "returning"); got != before {
		t.Fatalf("resume charged gap: before=%.2f after=%.2f", before, got)
	}
	if got := observedSeconds(tr, "steady"); got < 179 || got > 181 {
		t.Fatalf("healthy device lost intervals: %.2f", got)
	}
	if got := observedSeconds(tr, "unknown"); got != 0 {
		t.Fatalf("unknown status charged %.2fs", got)
	}
	before = observedSeconds(tr, "steady")
	tr.last = nowFloat() - 300
	tr.Sample()
	if got := observedSeconds(tr, "steady"); got != before {
		t.Fatalf("long gap charged: before=%.2f after=%.2f", before, got)
	}
}
