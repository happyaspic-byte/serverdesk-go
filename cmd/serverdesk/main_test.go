package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"serverdesk/internal/config"
)

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
