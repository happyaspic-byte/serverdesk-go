package main

import (
	"strings"
	"testing"

	"serverdesk/internal/config"
)

func TestValidateDemoModeAcceptsOnlyIsolatedLoopbackConfig(t *testing.T) {
	cfg := &config.Config{}
	transport := listenerTransport{addr: "127.0.0.1:6005"}
	if err := validateDemoMode(true, false, cfg, transport); err != nil {
		t.Fatalf("valid demo mode rejected: %v", err)
	}
	if err := validateDemoMode(false, true, nil, listenerTransport{}); err != nil {
		t.Fatalf("disabled demo mode changed production validation: %v", err)
	}
}

func TestValidateDemoModeFailsClosed(t *testing.T) {
	tests := []struct {
		name      string
		once      bool
		cfg       *config.Config
		transport listenerTransport
		want      string
	}{
		{name: "once", once: true, cfg: &config.Config{}, transport: listenerTransport{addr: "127.0.0.1:6005"}, want: "-once"},
		{name: "nil config", cfg: nil, transport: listenerTransport{addr: "127.0.0.1:6005"}, want: "유효한 설정"},
		{name: "public listener", cfg: &config.Config{}, transport: listenerTransport{addr: "0.0.0.0:6005", allowInsecureHTTP: true}, want: "루프백"},
		{name: "wildcard ipv6", cfg: &config.Config{}, transport: listenerTransport{addr: "[::]:6005", allowInsecureHTTP: true}, want: "루프백"},
		{name: "live cluster", cfg: &config.Config{Clusters: []config.ClusterConfig{{Key: "live"}}}, transport: listenerTransport{addr: "localhost:6005"}, want: "clusters"},
		{name: "live edge", cfg: &config.Config{EdgeDevices: []config.EdgeDevice{{Key: "live"}}}, transport: listenerTransport{addr: "localhost:6005"}, want: "edge_devices"},
		{name: "notifications", cfg: &config.Config{Notifications: config.NotificationConfig{Enabled: true}}, transport: listenerTransport{addr: "localhost:6005"}, want: "notifications.enabled"},
		{name: "trap", cfg: &config.Config{Trap: config.TrapConfig{Enabled: true}}, transport: listenerTransport{addr: "localhost:6005"}, want: "trap.enabled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDemoMode(true, test.once, test.cfg, test.transport)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateDemoMode() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestDemoRuntimeDirIsSeparated(t *testing.T) {
	if got := demoRuntimeDir("data"); got != "data-demo" {
		t.Fatalf("demoRuntimeDir(data) = %q", got)
	}
	if got := demoRuntimeDir("  "); got != "data-demo" {
		t.Fatalf("demoRuntimeDir(empty) = %q", got)
	}
}
