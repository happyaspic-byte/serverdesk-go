package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"serverdesk/internal/alerting"
	"serverdesk/internal/config"
	"serverdesk/internal/poller"
)

type readinessTestSender struct{ calls int }

func (*readinessTestSender) ValidateNotifyTarget(string) error { return nil }
func (s *readinessTestSender) SendWebhook(context.Context, string, string, string) (int, error) {
	s.calls++
	return 204, nil
}

func TestNeedsArgvExposureCheckOnlyWithClusters(t *testing.T) {
	if needsArgvExposureCheck(nil) || needsArgvExposureCheck(&config.Config{}) {
		t.Fatal("argv exposure gate should be skipped when no AVCLI cluster can run")
	}
	if !needsArgvExposureCheck(&config.Config{Clusters: []config.ClusterConfig{{}}}) {
		t.Fatal("argv exposure gate must run when a Stratus cluster is configured")
	}
}

func TestLoggingAndLevelNormalization(t *testing.T) {
	for input, want := range map[string]string{"ERROR": "error", "Warn": "warn", "unknown": "info"} {
		if got := normalizeLevel(input); got != want {
			t.Fatalf("normalizeLevel(%q) = %q, want %q", input, got, want)
		}
	}
	oldStderr, oldLevel := os.Stderr, logLevel
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	logLevel = logLevels["debug"]
	t.Cleanup(func() {
		os.Stderr = oldStderr
		logLevel = oldLevel
		_ = reader.Close()
		_ = writer.Close()
	})
	config.RegisterSecret("log-test-secret")
	logMsg("warn", "cluster-a", "credential=log-test-secret")
	os.Stderr = oldStderr
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "[WARN ") || !strings.Contains(text, "[cluster-a]") ||
		strings.Contains(text, "log-test-secret") || !strings.Contains(text, "credential=***") {
		t.Fatalf("masked log = %q", text)
	}
}

func TestFleetAndEdgeConversionHelpers(t *testing.T) {
	input := []config.EdgeDevice{{
		Key: "edge-a", Kind: "server", Name: "Server A", IP: "10.0.0.1", Community: "community",
		Vendor: "Acme", Company: "Corp", Factory: "Plant", Site: "Seoul", AssetTag: "A-1", FloorPos: "1,2",
		ExtraIPs: []string{"10.0.0.2"}, FinsPort: 9600, FinsSrcNode: 84, User: "user", Password: "password",
		BmcIP: "10.0.0.3", BmcUser: "admin", BmcPassword: "bmc-password", TLSFingerprint: "sha256:pin",
	}}
	got := convertEdgeDevices(input)
	if len(got) != 1 || got[0].Key != "edge-a" || got[0].TLSFingerprint != "sha256:pin" ||
		got[0].BMCPassword != "bmc-password" || !reflect.DeepEqual(got[0].ExtraIPs, []string{"10.0.0.2"}) {
		t.Fatalf("converted edge = %#v", got)
	}
	input[0].ExtraIPs[0] = "changed"
	if got[0].ExtraIPs[0] != "10.0.0.2" {
		t.Fatal("conversion aliased source ExtraIPs")
	}
	clusters := fleetClusters(map[string]any{"clusters": []any{map[string]any{"key": "a"}, "bad", nil}})
	if len(clusters) != 1 || mapStr(clusters[0], "key") != "a" || mapStr(clusters[0], "missing") != "" {
		t.Fatalf("fleet helper = %#v", clusters)
	}
	if got := fleetClusters(map[string]any{}); len(got) != 0 {
		t.Fatalf("empty fleet clusters = %#v", got)
	}
}

func TestReadPassword(t *testing.T) {
	for _, input := range []string{"customer-password\n", "customer-password\r\n"} {
		got, err := readPassword(strings.NewReader(input))
		if err != nil {
			t.Fatalf("readPassword(%q): %v", input, err)
		}
		if got != "customer-password" {
			t.Fatalf("readPassword(%q) = %q", input, got)
		}
	}
}

func TestMigrateSecretsCLIContract(t *testing.T) {
	// The CLI delegates to config.MigratePlaintextSecrets; this smoke fixture guards the
	// exact production shape without spawning a second copy of the test binary.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"secret_policy":"allow-plaintext","clusters":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := config.MigratePlaintextSecrets(path, filepath.Join(dir, "credentials"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 0 {
		t.Fatalf("unexpected migration count: %d", result.Count)
	}
}

func TestReadPasswordRejectsInvalidInput(t *testing.T) {
	for name, input := range map[string]string{
		"missing newline": "customer-password",
		"trailing data":   "customer-password\nnot-whitespace",
		"too long":        strings.Repeat("x", 1025) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := readPassword(strings.NewReader(input)); err == nil {
				t.Fatal("readPassword accepted invalid input")
			}
		})
	}
}

func TestConvertEdgeDevicesPreservesTLSFingerprint(t *testing.T) {
	const fingerprint = "sha256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	got := convertEdgeDevices([]config.EdgeDevice{{
		Key: "pve-1", Kind: "proxmox", IP: "192.0.2.10", TLSFingerprint: fingerprint,
	}})
	if len(got) != 1 || got[0].TLSFingerprint != fingerprint {
		t.Fatalf("converted TLS fingerprint = %+v, want %q", got, fingerprint)
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

func TestNotificationSignalsMatchUIAckKeys(t *testing.T) {
	const (
		host     = "edge-a"
		downDesc = "Device offline — no node responded to the collector"
	)
	devices := []map[string]any{{
		"id": host, "status": "down",
		"meta": map[string]any{
			"downSince": "2026-08-24T09:10:11Z",
			"alerts": []any{
				map[string]any{"sev": "critical", "name": "DEVICE_STATE", "desc": "duplicate synthetic"},
				map[string]any{"severity": "critical", "name": "POWER_LOSS", "description": "power lost", "time": "2026-08-24T09:12:13Z"},
				map[string]any{"sev": "warning", "name": "NOT_CRITICAL"},
			},
		},
	}}
	signals := notificationSignals(devices)
	if len(signals) != 2 {
		t.Fatalf("signals = %#v", signals)
	}
	if got, want := signals[0].AckKey, strings.Join([]string{host, "DEVICE_STATE", downDesc, "2026-08-24 09:10:11"}, "\x01"); got != want {
		t.Fatalf("down ack key = %q, want %q", got, want)
	}
	if got, want := signals[1].AckKey, strings.Join([]string{host, "POWER_LOSS", "power lost", "2026-08-24 09:12:13"}, "\x01"); got != want {
		t.Fatalf("alert ack key = %q, want %q", got, want)
	}
	if !signals[0].StartedAt.Equal(time.Date(2026, 8, 24, 9, 10, 11, 0, time.UTC)) {
		t.Fatalf("down start = %v", signals[0].StartedAt)
	}
	spaceTime := notificationSignals([]map[string]any{{
		"id": host, "status": "ok", "meta": map[string]any{"alerts": []any{
			map[string]any{"sev": "critical", "name": "POWER_LOSS", "desc": "power lost", "time": "2026-08-24 09:12:13"},
		}},
	}})
	if len(spaceTime) != 1 || spaceTime[0].Key != signals[1].Key {
		t.Fatalf("equivalent timestamp changed condition key: RFC=%q space=%#v", signals[1].Key, spaceTime)
	}

	noTime := notificationSignals([]map[string]any{{"id": host, "status": "down", "meta": map[string]any{}}})
	if len(noTime) != 1 || !strings.HasSuffix(noTime[0].AckKey, "\x01no-time") {
		t.Fatalf("missing-time down signal = %#v", noTime)
	}
}

func TestNotificationSourceReadinessAndArgvExposureScope(t *testing.T) {
	fleet := map[string]any{"clusters": []any{}}
	if notificationSourceReady(nil, nil, poller.EdgeCollectorStatus{}) {
		t.Fatal("nil fleet marked ready")
	}
	if !notificationSourceReady(fleet, nil, poller.EdgeCollectorStatus{}) {
		t.Fatal("completed zero-device snapshot not ready")
	}
	completed := poller.EdgeCollectorStatus{Configured: 1, LastRoundAt: time.Now()}
	if !notificationSourceReady(fleet, nil, completed) {
		t.Fatal("completed edge collector not ready")
	}
	completed.LastError = "round panic"
	if notificationSourceReady(fleet, nil, completed) {
		t.Fatal("failed edge collector round marked ready")
	}

	cfgA := config.ClusterConfig{Key: "ft-a", MgmtIP: "192.0.2.10"}
	stateA := poller.NewClusterState(&cfgA, 50)
	states := []*poller.ClusterState{stateA}
	cache := poller.NewFleetCache()
	cache.Update(states)
	fleet, _, _ = cache.Snapshot()
	if notificationSourceReady(fleet, states, poller.EdgeCollectorStatus{}) {
		t.Fatal("cluster was marked ready before its first fast-tier success")
	}
	stateA.Mark("fast", "")
	if notificationSourceReady(fleet, states, poller.EdgeCollectorStatus{}) {
		t.Fatal("cluster was marked ready before the post-success cache refresh")
	}
	cache.Update(states)
	fleet, _, _ = cache.Snapshot()
	if !notificationSourceReady(fleet, states, poller.EdgeCollectorStatus{}) {
		t.Fatal("cluster was not ready after fast-tier success and cache refresh")
	}
	stateA.Mark("fast", "AVCLI round failed")
	cache.Update(states)
	fleet, _, _ = cache.Snapshot()
	if notificationSourceReady(fleet, states, poller.EdgeCollectorStatus{}) {
		t.Fatal("cluster with a failed current fast-tier snapshot was marked ready")
	}
	stateA.Mark("fast", "")
	cache.Update(states)
	fleet, _, _ = cache.Snapshot()
	cfgB := config.ClusterConfig{Key: "ft-b", MgmtIP: "192.0.2.11"}
	stateB := poller.NewClusterState(&cfgB, 50)
	states = append(states, stateB)
	cache.Update(states)
	fleet, _, _ = cache.Snapshot()
	if notificationSourceReady(fleet, states, poller.EdgeCollectorStatus{}) {
		t.Fatal("fleet was marked ready while one configured cluster was unready")
	}
	if needsArgvExposureCheck(&config.Config{}) {
		t.Fatal("empty deployment should not enforce AVCLI argv policy")
	}
	if !needsArgvExposureCheck(&config.Config{Clusters: []config.ClusterConfig{{Key: "ft"}}}) {
		t.Fatal("FT deployment did not enforce AVCLI argv policy")
	}
}

func TestNotificationEngineWaitsForClusterSuccessAndCacheRefresh(t *testing.T) {
	cfg := config.ClusterConfig{Key: "ft-a", MgmtIP: "192.0.2.10"}
	state := poller.NewClusterState(&cfg, 50)
	states := []*poller.ClusterState{state}
	cache := poller.NewFleetCache()
	cache.Update(states) // publishes the startup view before AVCLI has succeeded
	sender := &readinessTestSender{}
	engine, err := alerting.New(alerting.Options{
		StateDir: t.TempDir(),
		Config: config.NotificationConfig{
			Enabled: true, WebhookURL: "https://allowed.example/token",
			RetryMax: 1, RetryBaseSeconds: 1,
		},
		Sender: sender,
		Snapshot: func() ([]alerting.Signal, map[string]bool, bool) {
			fleet, _, _ := cache.Snapshot()
			hosts, ready := notificationSourceReadiness(fleet, states, poller.EdgeCollectorStatus{})
			return []alerting.Signal{{
				Key: "state:ft-a:down", Host: "ft-a", Description: "startup view looks down",
				SourceUnready: !hosts["ft-a"],
			}}, hosts, ready
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.ScanOnce(context.Background())
	if sender.calls != 0 || engine.Status()["source_ready"] != false {
		t.Fatalf("pre-success scan sent=%d status=%#v", sender.calls, engine.Status())
	}
	state.Mark("fast", "")
	engine.ScanOnce(context.Background())
	if sender.calls != 0 {
		t.Fatalf("pre-refresh scan sent %d webhooks", sender.calls)
	}
	cache.Update(states)
	engine.ScanOnce(context.Background())
	if sender.calls != 1 || engine.Status()["source_ready"] != true {
		t.Fatalf("post-success scan sent=%d status=%#v", sender.calls, engine.Status())
	}
}

func TestNotificationCollectorFailureSignalIsCurrentAndSecretSafe(t *testing.T) {
	cfg := config.ClusterConfig{Key: "ft-a", MgmtIP: "192.0.2.10"}
	state := poller.NewClusterState(&cfg, 50)
	cache := poller.NewFleetCache()
	state.Mark("fast", "AVCLI password private-token rejected")
	cache.Update([]*poller.ClusterState{state})
	fleet, _, _ := cache.Snapshot()
	signals := notificationCollectorSignals(fleet, []*poller.ClusterState{state})
	if len(signals) != 1 || signals[0].Key != "collector:ft-a:fast" ||
		!signals[0].SuppressEscalation || strings.Contains(signals[0].Description, "private-token") {
		t.Fatalf("collector signals = %#v", signals)
	}
	trusted, allReady := notificationSourceReadiness(fleet, []*poller.ClusterState{state}, poller.EdgeCollectorStatus{})
	if allReady || trusted["ft-a"] {
		t.Fatalf("failed collector readiness = all=%v hosts=%#v", allReady, trusted)
	}

	state.Mark("fast", "")
	cache.Update([]*poller.ClusterState{state})
	fleet, _, _ = cache.Snapshot()
	if got := notificationCollectorSignals(fleet, []*poller.ClusterState{state}); len(got) != 0 {
		t.Fatalf("recovered collector still signaled: %#v", got)
	}
	trusted, allReady = notificationSourceReadiness(fleet, []*poller.ClusterState{state}, poller.EdgeCollectorStatus{})
	if !allReady || !trusted["ft-a"] {
		t.Fatalf("recovered collector readiness = all=%v hosts=%#v", allReady, trusted)
	}

	edgeFailed := poller.EdgeCollectorStatus{
		Configured: 2, LastError: "round panic private-edge-token",
	}
	edgeSignals := notificationEdgeCollectorSignals(edgeFailed)
	if len(edgeSignals) != 1 || edgeSignals[0].Key != "collector:edge:round" ||
		edgeSignals[0].Host != edgeCollectorConditionHost || !edgeSignals[0].SuppressEscalation ||
		strings.Contains(edgeSignals[0].Description, "private-edge-token") {
		t.Fatalf("edge collector signals = %#v", edgeSignals)
	}
	edgeFailed.LastError = ""
	edgeFailed.LastRoundAt = time.Now()
	if got := notificationEdgeCollectorSignals(edgeFailed); len(got) != 0 {
		t.Fatalf("recovered edge collector still signaled: %#v", got)
	}
}

func TestNotificationSilenceUsesDeviceIDAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	ui := map[string]any{
		"ack": map[string]any{"ack-key": map[string]any{"at": "irrelevant"}},
		"maint": map[string]any{
			"edge-a": map[string]any{"until": now.Add(time.Hour).Format(time.RFC3339)},
			"edge-b": map[string]any{"until": now.Add(-time.Second).Format(time.RFC3339)},
		},
	}
	acked, maintenance := notificationSilence(ui, now)
	if !acked["ack-key"] || !maintenance["edge-a"] || maintenance["edge-b"] {
		t.Fatalf("acked=%#v maintenance=%#v", acked, maintenance)
	}
}
