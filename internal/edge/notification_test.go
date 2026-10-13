package edge

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"serverdesk/internal/alerting"
	"serverdesk/internal/config"
)

// Only the outbound sender is fake: real collectors feed the production adapter and engine.
type observationSender struct {
	mu       sync.Mutex
	messages []string
}

func (*observationSender) ValidateNotifyTarget(string) error { return nil }
func (s *observationSender) SendWebhook(_ context.Context, _ string, message, _ string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, message)
	return 204, nil
}

func (s *observationSender) count(text string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, message := range s.messages {
		if strings.Contains(message, text) {
			n++
		}
	}
	return n
}

type observationSource struct {
	device      map[string]any
	workerReady bool
	now         time.Time
}

func (s *observationSource) snapshot() ([]alerting.Signal, map[string]bool, bool) {
	host := s.device["id"].(string)
	stale, _ := s.device["meta"].(map[string]any)["stale"].(bool)
	hosts := map[string]bool{host: s.workerReady && !stale}
	return alerting.DeviceSignals([]map[string]any{s.device}, &alerting.DeviceSignalReadiness{
		HostRecoveryReady: hosts, EdgeHosts: map[string]bool{host: true}, EdgeWorkerReady: s.workerReady,
	}), hosts, hosts[host]
}

func observationEngine(t *testing.T, source *observationSource, sender *observationSender) *alerting.Engine {
	t.Helper()
	e, err := alerting.New(alerting.Options{
		StateDir: t.TempDir(), Sender: sender, Snapshot: source.snapshot,
		Now:    func() time.Time { return source.now },
		Config: config.NotificationConfig{Enabled: true, WebhookURL: "https://allowed.example/token", RetryMax: 1, RetryBaseSeconds: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNASFreshFaultPartialHealthDeliversWithoutInventingRetainedActivation(t *testing.T) {
	fake := nasFake()
	fake["10.0.0.7"][oSynDiskStat+"0"] = vint(3)
	fake["10.0.0.7"][oSynRaidStat+"0"] = vint(1)
	delete(fake["10.0.0.7"], oSynFanCPU)
	w, pc := testWorker(fake)
	dev := DeviceConfig{Key: "nas1", Kind: "nas", IP: "10.0.0.7"}
	first, st := w.pollNAS(pc, dev, nil)
	frozen, _ := json.Marshal(first)
	source := &observationSource{device: first, workerReady: true, now: time.Unix(int64(pc.now), 0)}
	sender := &observationSender{}
	e := observationEngine(t, source, sender)
	e.ScanOnce(context.Background())
	if sender.count("CRITICAL — nas1: Drive 1 abnormal") != 1 || e.Status()["source_ready"] != false {
		t.Fatalf("fresh disk fault suppressed by missing fan: messages=%v status=%v", sender.messages, e.Status())
	}
	firstSignals, _, _ := source.snapshot()
	staleSender := &observationSender{}
	source.workerReady = false
	observationEngine(t, source, staleSender).ScanOnce(context.Background())
	if staleSender.count("CRITICAL") != 0 {
		t.Fatal("failed NAS worker round blessed old observed flag")
	}
	source.workerReady = true
	pc.now += 60
	source.device, st = w.pollNAS(pc, dev, st)
	source.now = source.now.Add(time.Minute)
	e.ScanOnce(context.Background())
	repeatSignals, _, _ := source.snapshot()
	if sender.count("CRITICAL — nas1: Drive 1 abnormal") != 1 || firstSignals[0].Key != repeatSignals[0].Key || firstSignals[0].AckKey != repeatSignals[0].AckKey || !firstSignals[0].StartedAt.Equal(repeatSignals[0].StartedAt) {
		t.Fatal("continuing observed fault changed activation")
	}
	// Disk is now unknown while the formerly missing fan explicitly recovers.
	delete(fake["10.0.0.7"], oSynDiskStat+"0")
	fake["10.0.0.7"][oSynFanCPU] = vint(1)
	pc.now += 60
	source.device, st = w.pollNAS(pc, dev, st)
	source.now = source.now.Add(time.Minute)
	e.ScanOnce(context.Background())
	if sender.count("RECOVERED") != 0 {
		t.Fatal("missing disk falsely recovered")
	}
	restartedSender := &observationSender{}
	restarted := observationEngine(t, source, restartedSender)
	restarted.ScanOnce(context.Background())
	if restartedSender.count("CRITICAL") != 0 {
		t.Fatal("retained disk activated on restart")
	}
	// A fresh down observation still sends, without blessing retained hardware history.
	w.SNMPGet = fakeSNMP{}.get
	pc.snmp = w.SNMPGet
	pc.now += 60
	source.device, st = w.pollNAS(pc, dev, st)
	source.now = source.now.Add(time.Minute)
	e.ScanOnce(context.Background())
	restarted.ScanOnce(context.Background())
	if sender.count("Device offline") != 1 || sender.count("RECOVERED") != 0 || restartedSender.count("Drive 1 abnormal") != 0 {
		t.Fatalf("down fabricated hardware activation/recovery: %v / %v", sender.messages, restartedSender.messages)
	}
	fake["10.0.0.7"][oSynDiskStat+"0"] = vint(1)
	pc.snmp = fake.get
	pc.now += 60
	source.device, _ = w.pollNAS(pc, dev, st)
	source.now = source.now.Add(time.Minute)
	e.ScanOnce(context.Background())
	if sender.count("RECOVERED — nas1: Drive 1 abnormal") != 1 {
		t.Fatalf("explicit normal failed recovery: %v", sender.messages)
	}
	if after, _ := json.Marshal(first); string(after) != string(frozen) {
		t.Fatal("later polls mutated prior snapshot")
	}
}

func TestPLCFreshFINSFaultMissingCIPDeliversWithoutInventingRetainedActivation(t *testing.T) {
	var phase atomic.Int32
	dev := plcLoopback(t, func(cmd []byte) []byte {
		if cmd[0] == 6 && cmd[1] == 1 {
			if phase.Load() == 1 {
				return nil
			}
			resp := plcReadFixture(cmd)
			if phase.Load() == 0 {
				resp[16], resp[17] = 0x12, 0x34
			}
			return resp
		}
		return plcReadFixture(cmd)
	})
	w, pc := plcWorker()
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity { return nil }
	first, st := w.pollPLC(pc, dev, nil)
	frozen, _ := json.Marshal(first)
	source := &observationSource{device: first, workerReady: true, now: time.Unix(int64(pc.now), 0)}
	sender := &observationSender{}
	e := observationEngine(t, source, sender)
	e.ScanOnce(context.Background())
	if sender.count("CRITICAL — plc1: FAL 1234") != 1 || e.Status()["source_ready"] != false {
		t.Fatalf("fresh FINS fatal suppressed by missing CIP: %v status=%v", sender.messages, e.Status())
	}
	// Old per-alert positive authority must not survive a failed worker round.
	source.workerReady = false
	staleSender := &observationSender{}
	observationEngine(t, source, staleSender).ScanOnce(context.Background())
	if staleSender.count("CRITICAL") != 0 {
		t.Fatal("worker stale blessed old observed flag")
	}
	source.workerReady = true
	phase.Store(1)
	pc.now += 60
	source.device, st = w.pollPLC(pc, dev, st)
	source.now = source.now.Add(time.Minute)
	e.ScanOnce(context.Background())
	restartedSender := &observationSender{}
	observationEngine(t, source, restartedSender).ScanOnce(context.Background())
	if sender.count("Device offline") != 1 || sender.count("RECOVERED") != 0 || restartedSender.count("FAL 1234") != 0 {
		t.Fatalf("missing FINS fabricated activation/recovery: %v / %v", sender.messages, restartedSender.messages)
	}
	phase.Store(2)
	w.cipIdentity = func(context.Context, string, time.Duration) *cipIdentity { return &cipIdentity{State: 3} }
	pc.now += 60
	source.device, _ = w.pollPLC(pc, dev, st)
	source.now = source.now.Add(time.Minute)
	e.ScanOnce(context.Background())
	if sender.count("RECOVERED — plc1: FAL 1234") != 1 {
		t.Fatalf("explicit FINS normal failed recovery: %v", sender.messages)
	}
	if after, _ := json.Marshal(first); string(after) != string(frozen) {
		t.Fatal("later polls mutated prior snapshot")
	}
}
