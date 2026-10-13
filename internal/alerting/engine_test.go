package alerting

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"serverdesk/internal/config"
)

type fakeSender struct {
	mu       sync.Mutex
	calls    []string
	ids      []string
	failures int
	failCode int
}

func (f *fakeSender) ValidateNotifyTarget(target string) error {
	if !strings.HasPrefix(target, "https://allowed.example/") {
		return errors.New("target not allowed")
	}
	return nil
}

func (f *fakeSender) SendWebhook(_ context.Context, _ string, message, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, message)
	f.ids = append(f.ids, id)
	if f.failures > 0 {
		f.failures--
		code := f.failCode
		if code == 0 {
			code = 503
		}
		return code, errors.New("delivery failure")
	}
	return 204, nil
}

func enabledConfig() config.NotificationConfig {
	return config.NotificationConfig{
		Enabled: true, WebhookURL: "https://allowed.example/secret-token",
		RetryMax: 3, RetryBaseSeconds: 2,
	}
}

func TestDisabledConfiguredDestinationStillFailsClosed(t *testing.T) {
	_, err := New(Options{
		StateDir: t.TempDir(),
		Config: config.NotificationConfig{
			Enabled: false, WebhookURL: "http://not-allowed.example/token",
			RetryMax: 3, RetryBaseSeconds: 2,
		},
		Sender: &fakeSender{},
		Source: func() ([]Signal, bool) { return nil, true },
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("disabled unsafe destination was accepted: %v", err)
	}
	if _, err := New(Options{
		StateDir: t.TempDir(),
		Config: config.NotificationConfig{
			Enabled: false, RetryMax: 3, RetryBaseSeconds: 2,
		},
		Sender: &fakeSender{},
		Source: func() ([]Signal, bool) { return nil, true },
	}); err != nil {
		t.Fatalf("disabled blank destination rejected: %v", err)
	}
}

func TestEngineDeliversCriticalOnceAcrossRestartAndRecovery(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	signals := []Signal{{Key: "alert:node-a:disk", Host: "node-a", Description: "disk failed", AckKey: "ack-a", StartedAt: now.Add(-time.Minute)}}
	sender := &fakeSender{}
	opts := Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return append([]Signal(nil), signals...), true },
		Now:    func() time.Time { return now },
	}
	engine, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 || !strings.Contains(sender.calls[0], "CRITICAL") {
		t.Fatalf("initial calls = %#v", sender.calls)
	}
	firstID := sender.ids[0]

	restarted, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	restarted.ScanOnce(context.Background())
	if len(sender.calls) != 1 {
		t.Fatalf("restart duplicated delivery: %#v", sender.calls)
	}

	signals = nil
	now = now.Add(time.Minute)
	restarted.ScanOnce(context.Background())
	if len(sender.calls) != 2 || !strings.Contains(sender.calls[1], "RECOVERED") {
		t.Fatalf("recovery calls = %#v", sender.calls)
	}
	if sender.ids[1] == firstID {
		t.Fatal("recovery reused critical idempotency key")
	}
}

func TestEngineRetriesWithBackoffThenDeadLetters(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	sender := &fakeSender{failures: 10}
	cfg := enabledConfig()
	cfg.RetryMax = 3
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: cfg, Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{Key: "state:node-a", Host: "node-a", Description: "offline", StartedAt: now}}, true
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 {
		t.Fatalf("attempts after first scan = %d", len(sender.calls))
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 {
		t.Fatal("retried before backoff elapsed")
	}
	now = now.Add(2 * time.Second)
	engine.ScanOnce(context.Background())
	now = now.Add(4 * time.Second)
	engine.ScanOnce(context.Background())
	status := engine.Status()
	if len(sender.calls) != 3 || status["dead_letter"] != 1 || status["healthy"] != false {
		t.Fatalf("calls=%d status=%#v", len(sender.calls), status)
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 3 {
		t.Fatal("dead letter kept retrying")
	}
}

func TestEngineMaintenanceSuppressesInitialAndAckSuppressesOnlyEscalation(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	sender := &fakeSender{}
	acked := map[string]bool{"ack-key": true}
	maint := map[string]bool{"node-a": true}
	cfg := enabledConfig()
	cfg.EscalationHours = 4
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: cfg, Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{
				Key: "alert:node-a:power", Host: "node-a", Description: "power redundancy lost",
				AckKey: "ack-key", StartedAt: now.Add(-5 * time.Hour),
			}}, true
		},
		Silence: func() (map[string]bool, map[string]bool) { return acked, maint },
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 0 {
		t.Fatal("maintenance condition was delivered")
	}
	delete(maint, "node-a")
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 || !strings.Contains(sender.calls[0], "CRITICAL") {
		t.Fatalf("ack suppressed initial critical: %#v", sender.calls)
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 {
		t.Fatalf("acknowledged escalation was delivered: %#v", sender.calls)
	}
	delete(acked, "ack-key")
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 2 || !strings.Contains(sender.calls[1], "unacknowledged 4h+") {
		t.Fatalf("unsilenced escalation calls = %#v", sender.calls)
	}
}

func TestUpdateConfigRequeuesDeadLettersOnlyWhenDestinationChanges(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 0, 0, 0, time.UTC)
	sender := &fakeSender{failures: 1}
	cfg := enabledConfig()
	cfg.RetryMax = 1
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: cfg, Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{Key: "alert:x", Host: "x", Description: "critical"}}, true
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if engine.Status()["dead_letter"] != 1 {
		t.Fatalf("status = %#v", engine.Status())
	}
	cfg.WebhookURL = "https://allowed.example/rotated-token"
	if err := engine.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if engine.Status()["dead_letter"] != 0 || len(sender.calls) != 2 {
		t.Fatalf("requeue status=%#v calls=%d", engine.Status(), len(sender.calls))
	}
}

func TestDisabledToEnabledRequeuesSameDestinationDeadLetters(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 30, 0, 0, time.UTC)
	sender := &fakeSender{failures: 1}
	cfg := enabledConfig()
	cfg.RetryMax = 1
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: cfg, Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{Key: "alert:reactivate", Host: "x", Description: "critical"}}, true
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if engine.Status()["dead_letter"] != 1 {
		t.Fatalf("initial status = %#v", engine.Status())
	}
	cfg.Enabled = false
	if err := engine.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Enabled = true
	if err := engine.UpdateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 2 || engine.Status()["dead_letter"] != 0 {
		t.Fatalf("reactivated calls=%d status=%#v", len(sender.calls), engine.Status())
	}
}

func TestDeliveryResultPersistenceFailureStaysUnhealthyUntilDurable(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 45, 0, 0, time.UTC)
	sender := &fakeSender{}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{Key: "alert:persist-result", Host: "x", Description: "critical"}}, true
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	persistCalls := 0
	engine.persistHook = func() error {
		persistCalls++
		if persistCalls == 3 { // reconcile, claim, then delivery result
			return errors.New("injected result persistence failure")
		}
		return nil
	}
	engine.ScanOnce(context.Background())
	status := engine.Status()
	if status["healthy"] != false || !strings.Contains(status["last_error"].(string), "delivery result") {
		t.Fatalf("failed persistence status = %#v", status)
	}
	engine.persistHook = nil
	engine.ScanOnce(context.Background())
	status = engine.Status()
	if status["healthy"] != true || status["last_error"] != "" {
		t.Fatalf("recovered persistence status = %#v", status)
	}
}

type blockingTargetSender struct {
	mu      sync.Mutex
	targets []string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingTargetSender) ValidateNotifyTarget(string) error { return nil }

func (s *blockingTargetSender) SendWebhook(_ context.Context, target, _, _ string) (int, error) {
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return http.StatusNoContent, nil
}

func TestUpdateConfigSerializesWithActiveScan(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 50, 0, 0, time.UTC)
	sender := &blockingTargetSender{entered: make(chan struct{}), release: make(chan struct{})}
	signals := []Signal{{Key: "alert:first", Host: "x", Description: "first"}}
	cfg := enabledConfig()
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: cfg, Sender: sender,
		Source: func() ([]Signal, bool) { return append([]Signal(nil), signals...), true },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	scanDone := make(chan struct{})
	go func() {
		engine.ScanOnce(context.Background())
		close(scanDone)
	}()
	<-sender.entered
	cfg.WebhookURL = "https://allowed.example/rotated-token"
	updateDone := make(chan error, 1)
	go func() { updateDone <- engine.UpdateConfig(cfg) }()
	select {
	case err := <-updateDone:
		t.Fatalf("UpdateConfig returned during old-destination scan: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(sender.release)
	<-scanDone
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	signals = append(signals, Signal{Key: "alert:second", Host: "y", Description: "second"})
	engine.ScanOnce(context.Background())
	sender.mu.Lock()
	targets := append([]string(nil), sender.targets...)
	sender.mu.Unlock()
	if len(targets) != 2 || targets[0] != "https://allowed.example/secret-token" || targets[1] != cfg.WebhookURL {
		t.Fatalf("delivery targets = %#v", targets)
	}
}

func TestUpdateConfigSerializesWithActiveTestDelivery(t *testing.T) {
	now := time.Date(2026, 8, 24, 13, 55, 0, 0, time.UTC)
	sender := &blockingTargetSender{entered: make(chan struct{}), release: make(chan struct{})}
	cfg := enabledConfig()
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: cfg, Sender: sender,
		Source: func() ([]Signal, bool) { return nil, true },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	testDone := make(chan error, 1)
	go func() {
		_, err := engine.Test(context.Background(), "")
		testDone <- err
	}()
	<-sender.entered
	cfg.WebhookURL = "https://allowed.example/rotated-token"
	updateDone := make(chan error, 1)
	go func() { updateDone <- engine.UpdateConfig(cfg) }()
	select {
	case err := <-updateDone:
		t.Fatalf("UpdateConfig returned during old-destination test delivery: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(sender.release)
	if err := <-testDone; err != nil {
		t.Fatal(err)
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Test(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	targets := append([]string(nil), sender.targets...)
	sender.mu.Unlock()
	if len(targets) != 2 || targets[0] != "https://allowed.example/secret-token" || targets[1] != cfg.WebhookURL {
		t.Fatalf("test delivery targets = %#v", targets)
	}
}

func TestRestartSkipsRecoveryUntilSourceIsReady(t *testing.T) {
	now := time.Date(2026, 8, 24, 14, 0, 0, 0, time.UTC)
	signal := Signal{Key: "state:edge-a:down", Host: "edge-a", Description: "offline", StartedAt: now.Add(-time.Minute)}
	sender := &fakeSender{}
	dir := t.TempDir()
	initial, err := New(Options{
		StateDir: dir, Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return []Signal{signal}, true },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	initial.ScanOnce(context.Background())
	if len(sender.calls) != 1 {
		t.Fatalf("initial calls = %#v", sender.calls)
	}

	ready := false
	signals := []Signal(nil)
	restarted, err := New(Options{
		StateDir: dir, Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return append([]Signal(nil), signals...), ready },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted.ScanOnce(context.Background())
	if len(sender.calls) != 1 || restarted.Status()["active_conditions"] != 1 || restarted.Status()["source_ready"] != false {
		t.Fatalf("unready restart calls=%#v status=%#v", sender.calls, restarted.Status())
	}
	ready, signals = true, []Signal{signal}
	restarted.ScanOnce(context.Background())
	if len(sender.calls) != 1 {
		t.Fatalf("ready snapshot duplicated critical: %#v", sender.calls)
	}
	signals = nil
	now = now.Add(time.Minute)
	restarted.ScanOnce(context.Background())
	if len(sender.calls) != 2 || !strings.Contains(sender.calls[1], "RECOVERED") {
		t.Fatalf("ready empty snapshot did not recover: %#v", sender.calls)
	}
}

func TestPartialCollectorOutageDoesNotSuppressHealthySignalsOrRecoveries(t *testing.T) {
	now := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	sender := &fakeSender{}
	signals := []Signal{{Key: "alert:healthy", Host: "ft-b", Description: "power lost", StartedAt: now}}
	recoveryReady := map[string]bool{"ft-a": false, "ft-b": true}
	allReady := false
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Snapshot: func() ([]Signal, map[string]bool, bool) {
			return append([]Signal(nil), signals...), recoveryReady, allReady
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 || !strings.Contains(sender.calls[0], "ft-b") {
		t.Fatalf("healthy-source critical was suppressed: %#v", sender.calls)
	}

	// A healthy host may recover even while another collector remains down.
	signals = nil
	now = now.Add(time.Minute)
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 2 || !strings.Contains(sender.calls[1], "RECOVERED") {
		t.Fatalf("healthy-source recovery was globally blocked: %#v", sender.calls)
	}

	// Stale positive data from the failed source is not authoritative.
	signals = []Signal{{
		Key: "alert:stale", Host: "ft-a", Description: "stale cached alert",
		StartedAt: now, SourceUnready: true,
	}}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 2 {
		t.Fatalf("unready stale signal was delivered: %#v", sender.calls)
	}

	// The current collector failure itself is a trusted positive condition, but
	// it cannot be recovered until that host publishes a successful snapshot.
	signals = []Signal{{
		Key: "collector:ft-a:fast", Host: "ft-a", Description: "collector failed",
		SuppressEscalation: true,
	}}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 3 || !strings.Contains(sender.calls[2], "collector failed") {
		t.Fatalf("collector failure was not delivered: %#v", sender.calls)
	}
	signals = nil
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 3 {
		t.Fatalf("unready collector emitted false recovery: %#v", sender.calls)
	}
	recoveryReady["ft-a"], allReady = true, true
	now = now.Add(time.Minute)
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 4 || !strings.Contains(sender.calls[3], "RECOVERED") {
		t.Fatalf("collector recovery was not delivered after readiness: %#v", sender.calls)
	}
}

func TestSilenceStateReadFailurePausesDeliveryAndDegradesHealth(t *testing.T) {
	sender := &fakeSender{}
	failing := true
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{Key: "critical", Host: "ft-a", Description: "power loss"}}, true
		},
		SilenceWithError: func() (map[string]bool, map[string]bool, error) {
			if failing {
				return nil, nil, errors.New("corrupt ack state")
			}
			return map[string]bool{}, map[string]bool{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	status := engine.Status()
	if len(sender.calls) != 0 || status["healthy"] != false ||
		!strings.Contains(status["last_error"].(string), "operator acknowledgement") {
		t.Fatalf("silence read failure calls=%#v status=%#v", sender.calls, status)
	}
	failing = false
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 || engine.Status()["healthy"] != true {
		t.Fatalf("delivery did not recover calls=%#v status=%#v", sender.calls, engine.Status())
	}
}

func TestAttemptClaimPersistenceFailureSkipsOutbound(t *testing.T) {
	now := time.Date(2026, 8, 24, 15, 0, 0, 0, time.UTC)
	sender := &fakeSender{}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{Key: "alert:claim", Host: "node-a", Description: "claim test"}}, true
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	persistCalls := 0
	engine.persistHook = func() error {
		persistCalls++
		if persistCalls == 2 {
			return errors.New("injected claim persistence failure")
		}
		return nil
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 0 {
		t.Fatalf("outbound happened without durable claim: %#v", sender.calls)
	}
	if got := engine.Status()["last_error"]; !strings.Contains(got.(string), "delivery claim") {
		t.Fatalf("status = %#v", engine.Status())
	}
}

func TestPermanentClientErrorDoesNotRetry(t *testing.T) {
	now := time.Date(2026, 8, 24, 16, 0, 0, 0, time.UTC)
	sender := &fakeSender{failures: 10, failCode: http.StatusBadRequest}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) {
			return []Signal{{Key: "alert:bad-request", Host: "node-a", Description: "bad request"}}, true
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	now = now.Add(time.Hour)
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 1 || engine.Status()["dead_letter"] != 1 {
		t.Fatalf("calls=%d status=%#v", len(sender.calls), engine.Status())
	}
}

type stormSender struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	full      chan struct{}
}

func (s *stormSender) ValidateNotifyTarget(string) error { return nil }

func (s *stormSender) SendWebhook(ctx context.Context, _ string, _ string, _ string) (int, error) {
	s.mu.Lock()
	s.calls++
	firstWave := s.calls <= maxDeliveryConcurrency
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	if s.calls == maxDeliveryConcurrency {
		close(s.full)
	}
	s.mu.Unlock()
	if firstWave {
		select {
		case <-s.full:
		case <-ctx.Done():
		}
	}
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	return http.StatusNoContent, nil
}

func TestAlarmStormUsesBoundedBatchAndConcurrency(t *testing.T) {
	now := time.Date(2026, 8, 24, 17, 0, 0, 0, time.UTC)
	signals := make([]Signal, 100)
	for i := range signals {
		signals[i] = Signal{Key: fmt.Sprintf("alert:%03d", i), Host: fmt.Sprintf("node-%03d", i), Description: "critical"}
	}
	sender := &stormSender{full: make(chan struct{})}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return signals, true },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	engine.ScanOnce(ctx)
	sender.mu.Lock()
	calls, maxActive := sender.calls, sender.maxActive
	sender.mu.Unlock()
	if calls != maxDeliveryBatch || maxActive < 2 || maxActive > maxDeliveryConcurrency {
		t.Fatalf("calls=%d maxActive=%d", calls, maxActive)
	}
	if pending := engine.Status()["pending"]; pending != 100-maxDeliveryBatch {
		t.Fatalf("pending=%v status=%#v", pending, engine.Status())
	}
}

type mixedResultSender struct{}

func (mixedResultSender) ValidateNotifyTarget(string) error { return nil }

func (mixedResultSender) SendWebhook(_ context.Context, _ string, message, _ string) (int, error) {
	if strings.Contains(message, "failing condition") {
		return http.StatusServiceUnavailable, errors.New("failing condition delivery error")
	}
	time.Sleep(15 * time.Millisecond) // ensure success is processed after the failure
	return http.StatusNoContent, nil
}

func TestParallelSuccessDoesNotHidePendingDeliveryFailure(t *testing.T) {
	now := time.Date(2026, 8, 24, 18, 0, 0, 0, time.UTC)
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: mixedResultSender{},
		Source: func() ([]Signal, bool) {
			return []Signal{
				{Key: "alert:failure", Host: "node-a", Description: "failing condition"},
				{Key: "alert:success", Host: "node-b", Description: "successful condition"},
			}, true
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	status := engine.Status()
	if status["healthy"] != false || status["pending"] != 1 ||
		!strings.Contains(status["last_error"].(string), "failing condition") {
		t.Fatalf("parallel delivery status = %#v", status)
	}
}

func TestPendingDeliveryRechecksLifecycleAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name, kind, change     string
		wantCalls, wantPending int
	}{
		{"initial maintenance", "critical", "maintenance", 1, 1},
		{"initial ACK", "critical", "ack", 2, 0},
		{"initial recovery", "critical", "recovery", 1, 0},
		{"escalation maintenance", "escalation", "maintenance", 2, 1},
		{"escalation ACK", "escalation", "ack", 2, 1},
		{"escalation recovery", "escalation", "recovery", 3, 0},
		{"escalation offline", "escalation", "offline", 3, 0},
	} {
		for _, restart := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/restart=%t", tc.name, restart), func(t *testing.T) {
				now := time.Date(2026, 8, 24, 19, 0, 0, 0, time.UTC)
				signals := []Signal{{Key: "alert:lifecycle", Host: "node-a", AckKey: "ack-a", Description: "power lost", StartedAt: now.Add(-5 * time.Hour)}}
				ready := true
				acked, maintenance := map[string]bool{}, map[string]bool{}
				sender := &fakeSender{}
				cfg := enabledConfig()
				cfg.EscalationHours = 4
				opts := Options{
					StateDir: t.TempDir(), Config: cfg, Sender: sender,
					Source:  func() ([]Signal, bool) { return signals, ready },
					Silence: func() (map[string]bool, map[string]bool) { return acked, maintenance },
					Now:     func() time.Time { return now },
				}
				engine, err := New(opts)
				if err != nil {
					t.Fatal(err)
				}
				if tc.kind == "escalation" {
					engine.ScanOnce(context.Background())
				}
				sender.failures = 1
				engine.ScanOnce(context.Background())
				failedID := sender.ids[len(sender.ids)-1]
				switch tc.change {
				case "maintenance":
					maintenance["node-a"] = true
				case "ack":
					acked["ack-a"] = true
				case "recovery":
					signals = nil
				case "offline":
					signals, ready = nil, false
				}
				if restart {
					engine, err = New(opts)
					if err != nil {
						t.Fatal(err)
					}
				}
				now = now.Add(2 * time.Second)
				engine.ScanOnce(context.Background())
				if len(sender.calls) != tc.wantCalls || engine.Status()["pending"] != tc.wantPending {
					t.Fatalf("calls=%#v status=%#v", sender.calls, engine.Status())
				}
				if tc.change == "recovery" && tc.kind == "escalation" && !strings.Contains(sender.calls[2], "RECOVERED") {
					t.Fatalf("stale escalation instead of recovery: %#v", sender.calls)
				}
				if tc.change == "offline" && engine.Status()["active_conditions"] != 1 {
					t.Fatalf("offline source falsely recovered: %#v", engine.Status())
				}
				if tc.wantPending == 1 {
					item := engine.state.Deliveries[failedID]
					if item == nil || item.Attempts != 1 || item.DeadLetter || !item.NextAttempt.Equal(now) {
						t.Fatalf("paused retry consumed attempt/backoff: %#v", item)
					}
					delete(maintenance, "node-a")
					delete(acked, "ack-a")
					engine.ScanOnce(context.Background())
					if len(sender.calls) != tc.wantCalls+1 || sender.ids[len(sender.ids)-1] != failedID {
						t.Fatalf("paused retry lost identity/backoff: %#v ids=%#v", sender.calls, sender.ids)
					}
				}
			})
		}
	}
}

type slotPolicySender struct {
	mu      sync.Mutex
	calls   int
	block   bool
	entered chan struct{}
	release chan struct{}
}

func (s *slotPolicySender) ValidateNotifyTarget(string) error { return nil }
func (s *slotPolicySender) SendWebhook(ctx context.Context, _ string, _ string, _ string) (int, error) {
	s.mu.Lock()
	s.calls++
	block := s.block
	s.mu.Unlock()
	if block {
		s.entered <- struct{}{}
		select {
		case <-s.release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	return 204, nil
}

func TestQueuedConcurrencySlotRechecksACKWithoutConsumingAttempt(t *testing.T) {
	now := time.Date(2026, 8, 24, 19, 30, 0, 0, time.UTC)
	signals := make([]Signal, 9)
	for i := range signals {
		signals[i] = Signal{Key: fmt.Sprintf("slot:%d", i), Host: "node-a", AckKey: "ack-a", StartedAt: now.Add(-5 * time.Hour)}
	}
	var policyMu sync.Mutex
	acked := false
	sender := &slotPolicySender{entered: make(chan struct{}, 9), release: make(chan struct{})}
	cfg := enabledConfig()
	cfg.EscalationHours = 4
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: cfg, Sender: sender,
		Source: func() ([]Signal, bool) { return signals, true },
		Silence: func() (map[string]bool, map[string]bool) {
			policyMu.Lock()
			defer policyMu.Unlock()
			return map[string]bool{"ack-a": acked}, nil
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	sender.mu.Lock()
	sender.block = true
	sender.mu.Unlock()
	done := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer func() { cancel(); <-done }()
	go func() { engine.ScanOnce(ctx); close(done) }()
	for i := 0; i < 8; i++ {
		select {
		case <-sender.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("delivery slots did not fill")
		}
	}
	policyMu.Lock()
	acked = true
	policyMu.Unlock()
	close(sender.release)
	<-done
	sender.mu.Lock()
	calls := sender.calls
	sender.mu.Unlock()
	if calls != 17 || engine.Status()["pending"] != 1 {
		t.Fatalf("queued stale escalation sent: calls=%d status=%#v", calls, engine.Status())
	}
	for _, item := range engine.state.Deliveries {
		if item.DeliveredAt.IsZero() && (item.Attempts != 0 || !item.NextAttempt.Equal(now) || item.DeadLetter) {
			t.Fatalf("policy skip consumed retry: %#v", item)
		}
	}
}

func TestScanCancellationWhileWaitingForSlotJoinsClaims(t *testing.T) {
	now := time.Date(2026, 8, 24, 19, 35, 0, 0, time.UTC)
	signals := make([]Signal, 9)
	for i := range signals {
		signals[i] = Signal{Key: fmt.Sprintf("cancel:%d", i)}
	}
	sender := &slotPolicySender{block: true, entered: make(chan struct{}, 9), release: make(chan struct{})}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return signals, true }, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan struct{})
	defer func() { cancel(); <-done }()
	go func() { engine.ScanOnce(ctx); close(done) }()
	for i := 0; i < 8; i++ {
		select {
		case <-sender.entered:
		case <-ctx.Done():
			t.Fatal("delivery slots did not fill")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled scan did not join deliveries")
	}
	if !engine.scanMu.TryLock() {
		t.Fatal("cancelled scan retained scan mutex")
	}
	engine.scanMu.Unlock()
	sender.mu.Lock()
	calls := sender.calls
	sender.mu.Unlock()
	if calls != 8 {
		t.Fatalf("cancelled ninth request started: calls=%d", calls)
	}
	claims := 0
	for _, item := range engine.state.Deliveries {
		claims += item.Attempts
		if item.Attempts == 1 && (item.LastError == "" || !item.NextAttempt.Equal(now.Add(2*time.Second))) {
			t.Fatalf("started delivery result not recorded: %#v", item)
		}
	}
	if claims != 8 {
		t.Fatalf("unsent request durably claimed: claims=%d", claims)
	}
}

func TestPartialClaimFailureJoinsStartedDelivery(t *testing.T) {
	now := time.Date(2026, 8, 24, 19, 40, 0, 0, time.UTC)
	sender := &blockingTargetSender{entered: make(chan struct{}), release: make(chan struct{})}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return []Signal{{Key: "claim:first"}, {Key: "claim:second"}}, true },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	persistCalls := 0
	failed := make(chan struct{})
	engine.persistHook = func() error {
		persistCalls++
		if persistCalls == 3 {
			close(failed)
			return errors.New("second claim failed")
		}
		return nil
	}
	done := make(chan struct{})
	defer func() {
		select {
		case <-sender.release:
		default:
			close(sender.release)
		}
		<-done
	}()
	go func() { engine.ScanOnce(context.Background()); close(done) }()
	select {
	case <-failed:
	case <-time.After(5 * time.Second):
		t.Fatal("second claim did not fail")
	}
	select {
	case <-done:
		t.Fatal("scan returned before joining started delivery")
	default:
	}
	close(sender.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not join successful delivery")
	}
	if status := engine.Status(); status["delivered_retained"] != 1 || status["pending"] != 1 {
		t.Fatalf("partial claim lost delivery result: %#v", status)
	}
	for _, item := range engine.state.Deliveries {
		if item.DeliveredAt.IsZero() && item.Attempts != 0 {
			t.Fatalf("failed claim consumed attempt: %#v", item)
		}
	}
}

func TestRecurrenceWithSameStartedAtGetsNewActivation(t *testing.T) {
	now := time.Date(2026, 8, 24, 19, 45, 0, 0, time.UTC)
	signals := []Signal{{Key: "recurring", Host: "node-a", StartedAt: now}}
	original := signals[0]
	sender := &fakeSender{}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return signals, true }, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	signals = nil
	engine.ScanOnce(context.Background())
	signals = []Signal{original}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 3 || sender.ids[2] == sender.ids[0] {
		t.Fatalf("same StartedAt recurrence deduped: calls=%#v ids=%#v", sender.calls, sender.ids)
	}
}

func TestRecurrenceAfterConditionPruningDoesNotReuseRetainedRecovery(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%t", restart), func(t *testing.T) {
			now := time.Date(2026, 8, 24, 22, 0, 0, 0, time.UTC)
			signal := Signal{Key: "pruned-recurrence", Host: "node-a", StartedAt: now}
			signals := []Signal{signal}
			sender := &fakeSender{}
			opts := Options{
				StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
				Source: func() ([]Signal, bool) { return signals, true }, Now: func() time.Time { return now },
			}
			engine, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			engine.ScanOnce(context.Background())
			signals = nil
			sender.failures = 1
			engine.ScanOnce(context.Background())
			now = now.Add(29 * 24 * time.Hour)
			engine.ScanOnce(context.Background()) // delayed recovery succeeds
			now = now.Add(2 * 24 * time.Hour)
			engine.ScanOnce(context.Background())
			if engine.state.Conditions[signal.Key] != nil || engine.Status()["delivered_retained"] != 1 {
				t.Fatalf("fixture did not prune condition while retaining late recovery: %#v", engine.Status())
			}
			if restart {
				engine, err = New(opts)
				if err != nil {
					t.Fatal(err)
				}
			}
			signals = []Signal{signal}
			engine.ScanOnce(context.Background())
			if len(sender.calls) != 4 || sender.ids[3] == sender.ids[0] {
				t.Fatalf("recurrence reused retained activation: calls=%#v ids=%#v", sender.calls, sender.ids)
			}
			signals = nil
			engine.ScanOnce(context.Background())
			if len(sender.calls) != 5 || sender.ids[4] == sender.ids[2] || !strings.Contains(sender.calls[4], "RECOVERED") {
				t.Fatalf("second recovery suppressed by retained marker: calls=%#v ids=%#v", sender.calls, sender.ids)
			}
		})
	}
}

func TestLegacyDeliveryWithoutInactiveAtUsesResolutionRetention(t *testing.T) {
	now := time.Date(2026, 8, 24, 22, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	// Version 1 fixture predates inactive_at. The initial delivery is old, but
	// resolution is recent; migration must not immediately discard its history.
	fixture := `{
		"version": 1,
		"conditions": {"legacy": {"key": "legacy", "host": "node-a", "first_seen": "2026-06-01T00:00:00Z", "last_seen": "2026-08-23T22:00:00Z", "active": false}},
		"deliveries": {"%s": {"id": "%s", "condition_key": "legacy", "kind": "critical", "created_at": "2026-06-01T00:00:00Z", "next_attempt": "2026-06-01T00:00:00Z", "attempts": 1, "delivered_at": "2026-06-01T00:00:00Z"}}
	}`
	id := deliveryID("legacy", "critical", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if err := os.WriteFile(filepath.Join(dir, "notification-state.json"), []byte(fmt.Sprintf(fixture, id, id)), 0o600); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	opts := Options{
		StateDir: dir, Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return nil, true }, Now: func() time.Time { return now },
	}
	engine, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if engine.Status()["delivered_retained"] != 1 || len(sender.calls) != 0 {
		t.Fatalf("legacy inactive history discarded or resent: %#v calls=%#v", engine.Status(), sender.calls)
	}
	engine, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * 24 * time.Hour)
	engine.ScanOnce(context.Background())
	if engine.Status()["delivered_retained"] != 0 || len(engine.state.Conditions) != 0 || len(sender.calls) != 0 {
		t.Fatalf("legacy inactive history leaked after restart: %#v calls=%#v", engine.Status(), sender.calls)
	}
}

func TestActiveDeliveryDedupeSurvivesRetentionAndRestart(t *testing.T) {
	for _, deadLetter := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadLetter=%t", deadLetter), func(t *testing.T) {
			now := time.Date(2026, 8, 24, 20, 0, 0, 0, time.UTC)
			sender := &fakeSender{}
			cfg := enabledConfig()
			if deadLetter {
				sender.failures, cfg.RetryMax = 1, 1
			}
			cfg.EscalationHours = 4
			opts := Options{
				StateDir: t.TempDir(), Config: cfg, Sender: sender,
				Source: func() ([]Signal, bool) {
					return []Signal{{Key: "long-lived", Host: "node-a", Description: "critical", StartedAt: now.Add(-5 * time.Hour)}}, true
				},
				Now: func() time.Time { return now },
			}
			engine, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			engine.ScanOnce(context.Background())
			engine.ScanOnce(context.Background())
			want := len(sender.calls)
			now = now.Add(31 * 24 * time.Hour)
			engine.ScanOnce(context.Background())
			engine, err = New(opts)
			if err != nil {
				t.Fatal(err)
			}
			engine.ScanOnce(context.Background())
			engine.ScanOnce(context.Background())
			if len(sender.calls) != want {
				t.Fatalf("active markers expired; calls=%#v", sender.calls)
			}
			if deadLetter && engine.Status()["dead_letter"] != 1 {
				t.Fatalf("active dead letter expired: %#v", engine.Status())
			}
		})
	}
}

func TestResolvedDeliveryRetentionAndRecurrence(t *testing.T) {
	now := time.Date(2026, 8, 24, 21, 0, 0, 0, time.UTC)
	signal := Signal{Key: "recurring", Host: "node-a", AckKey: "ack-a", Description: "critical", StartedAt: now}
	signals := []Signal{signal}
	maintenance := map[string]bool{}
	sender := &fakeSender{}
	opts := Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source:  func() ([]Signal, bool) { return signals, true },
		Silence: func() (map[string]bool, map[string]bool) { return map[string]bool{"ack-a": true}, maintenance },
		Now:     func() time.Time { return now },
	}
	engine, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	now = now.Add(31 * 24 * time.Hour)
	signals, maintenance["node-a"] = nil, true
	sender.failures = 1
	engine.ScanOnce(context.Background())
	now = now.Add(2 * time.Second)
	engine, err = New(opts)
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 3 || !strings.Contains(sender.calls[2], "RECOVERED") {
		t.Fatalf("valid recovery retry suppressed: %#v", sender.calls)
	}
	if engine.Status()["delivered_retained"] != 2 {
		t.Fatalf("resolved initial expired before inactive retention: %#v", engine.Status())
	}
	delete(maintenance, "node-a")
	signals = []Signal{signal} // collector reuses its old StartedAt; activation must still be new
	engine.ScanOnce(context.Background())
	if len(sender.calls) != 4 || sender.ids[0] == sender.ids[3] || !strings.Contains(sender.calls[3], "CRITICAL") {
		t.Fatalf("recurrence was deduped against old activation: calls=%#v ids=%#v", sender.calls, sender.ids)
	}
	signals = nil
	engine.ScanOnce(context.Background())
	now = now.Add(31 * 24 * time.Hour)
	engine.ScanOnce(context.Background())
	if engine.Status()["delivered_retained"] != 0 || engine.Status()["active_conditions"] != 0 {
		t.Fatalf("inactive history not pruned: %#v", engine.Status())
	}
}

func TestPersistenceFaultTakesPriorityThenRevealsDeliveryFailure(t *testing.T) {
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: &fakeSender{},
		Source: func() ([]Signal, bool) { return nil, true },
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 24, 18, 30, 0, 0, time.UTC)
	engine.mu.Lock()
	engine.state.Deliveries["failed"] = &delivery{
		ID: "failed", LastError: "pending delivery failed", LastErrorAt: now,
	}
	engine.markPersistFailureLocked("persist notification delivery result", errors.New("disk full"), now.Add(time.Second))
	engine.refreshDeliveryErrorLocked()
	engine.mu.Unlock()
	if status := engine.Status(); !strings.Contains(status["last_error"].(string), "disk full") {
		t.Fatalf("persistence fault lost priority: %#v", status)
	}
	engine.mu.Lock()
	if err := engine.persistLocked(); err != nil {
		engine.mu.Unlock()
		t.Fatal(err)
	}
	engine.mu.Unlock()
	status := engine.Status()
	if status["healthy"] != false || status["last_error"] != "pending delivery failed" {
		t.Fatalf("delivery failure not restored after persistence recovery: %#v", status)
	}
}
