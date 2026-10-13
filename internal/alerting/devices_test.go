package alerting

import (
	"context"
	"testing"
	"time"
)

func TestDeviceSignalsObservationGate(t *testing.T) {
	for _, tc := range []struct {
		name               string
		edge, worker, host bool
		observed           any
		wantUnready        bool
	}{
		{"fresh edge partial host", true, true, false, true, false},
		{"retained edge ready host", true, true, true, false, true},
		{"worker failed old positive", true, false, true, true, true},
		{"worker startup old positive", true, false, false, true, true},
		{"edge absent flag ready", true, true, true, nil, false},
		{"edge absent flag incomplete", true, true, false, nil, true},
		{"other collector no override", false, true, false, true, true},
		{"other collector legacy ready", false, true, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			alert := map[string]any{"name": "FAULT", "desc": "fault", "sev": "critical"}
			if tc.observed != nil {
				alert["observed"] = tc.observed
			}
			d := map[string]any{"id": "a", "meta": map[string]any{"alerts": []any{alert}}}
			got := DeviceSignals([]map[string]any{d}, &DeviceSignalReadiness{
				HostRecoveryReady: map[string]bool{"a": tc.host}, EdgeHosts: map[string]bool{"a": tc.edge}, EdgeWorkerReady: tc.worker,
			})
			if len(got) != 1 || got[0].SourceUnready != tc.wantUnready {
				t.Fatalf("signals=%v", got)
			}
		})
	}
}

func TestUnreadySignalBlocksActivationRecoveryAndPendingRetryEvenWhenAllReady(t *testing.T) {
	now := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	signals := []Signal{{Key: "hardware", Host: "edge", Description: "fault", StartedAt: now, SourceUnready: true}}
	sender := &fakeSender{failures: 1}
	e, err := New(Options{StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Now: func() time.Time { return now }, Snapshot: func() ([]Signal, map[string]bool, bool) {
			return signals, map[string]bool{"edge": true}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.ScanOnce(context.Background())
	if len(sender.calls) != 0 || e.Status()["active_conditions"] != 0 {
		t.Fatal("allReady blessed unready activation")
	}
	signals[0].SourceUnready = false
	e.ScanOnce(context.Background()) // authorized attempt fails; retry remains pending
	if len(sender.calls) != 1 || e.Status()["pending"] != 1 {
		t.Fatal("initial attempt not retained")
	}
	var pending *delivery
	for _, item := range e.state.Deliveries {
		pending = item
	}
	attempts, nextAttempt := pending.Attempts, pending.NextAttempt
	signals[0].SourceUnready = true
	now = now.Add(time.Minute)
	e.ScanOnce(context.Background())
	if pending.Attempts != attempts || !pending.NextAttempt.Equal(nextAttempt) {
		t.Fatal("uncertainty consumed retry attempts/backoff")
	}
	if len(sender.calls) != 1 || e.Status()["pending"] != 1 || e.Status()["active_conditions"] != 1 {
		t.Fatal("uncertain evidence retried or falsely recovered")
	}
	signals[0].SourceUnready = false
	e.ScanOnce(context.Background())
	if len(sender.calls) != 2 || e.Status()["pending"] != 0 {
		t.Fatal("current evidence failed to resume same retry")
	}
	signals[0].SourceUnready = true
	now = now.Add(time.Minute)
	e.ScanOnce(context.Background())
	if len(sender.calls) != 2 || e.Status()["active_conditions"] != 1 {
		t.Fatal("delivered condition falsely recovered")
	}
	signals = nil
	e.ScanOnce(context.Background())
	if len(sender.calls) != 3 || e.Status()["active_conditions"] != 0 {
		t.Fatal("authoritative absence failed recovery")
	}
}

func TestNotificationTimeAndAckKeyContract(t *testing.T) {
	for input, want := range map[string]string{
		"":                     "no-time",
		"2026-08-24T12:34:56Z": "2026-08-24 12:34:56",
		"2026-08-24 12:34:56":  "2026-08-24 12:34:56",
	} {
		if got := normalizeNotificationAckTime(input); got != want {
			t.Fatalf("normalizeNotificationAckTime(%q) = %q, want %q", input, got, want)
		}
	}
	naive := parseNotificationTime("2026-08-24 12:00:00")
	if got := naive.UTC(); !got.Equal(time.Date(2026, 8, 24, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("naive KST parsed as %v", got)
	}
	withZone := parseNotificationTime("2026-08-24T12:00:00Z")
	if !withZone.Equal(time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("RFC3339 parsed as %v", withZone)
	}
}
