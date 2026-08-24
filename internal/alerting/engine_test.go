package alerting

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
}

func (s *stormSender) ValidateNotifyTarget(string) error { return nil }

func (s *stormSender) SendWebhook(context.Context, string, string, string) (int, error) {
	s.mu.Lock()
	s.calls++
	s.active++
	if s.active > s.maxActive {
		s.maxActive = s.active
	}
	s.mu.Unlock()
	time.Sleep(5 * time.Millisecond)
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
	sender := &stormSender{}
	engine, err := New(Options{
		StateDir: t.TempDir(), Config: enabledConfig(), Sender: sender,
		Source: func() ([]Signal, bool) { return signals, true },
		Now:    func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
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
