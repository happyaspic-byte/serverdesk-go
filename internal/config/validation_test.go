package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseRejectsUnsafeSchedulerAndAllocationValues(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"fast zero", `"intervals":{"fast":0}`, "intervals.fast"},
		{"os zero", `"intervals":{"os":0}`, "intervals.os"},
		{"snmp negative", `"intervals":{"snmp":-1}`, "intervals.snmp"},
		{"slow excessive", fmt.Sprintf(`"intervals":{"slow":%d}`, maxPollIntervalSeconds+1), "intervals.slow"},
		{"avcli timeout", `"avcli_timeout":0`, "avcli_timeout"},
		{"ssh timeout", `"ssh_timeout":0`, "ssh_timeout"},
		{"http timeout", `"http_timeout":0`, "http_timeout"},
		{"cache refresh", `"cache_refresh":0`, "cache_refresh"},
		{"history zero", `"history_points":0`, "history_points"},
		{"history excessive", fmt.Sprintf(`"history_points":%d`, maxHistoryPoints+1), "history_points"},
		{"trap port zero", `"trap":{"port":0}`, "trap.port"},
		{"trap port excessive", `"trap":{"port":70000}`, "trap.port"},
		{"trap ring", `"trap":{"ring":0}`, "trap.ring"},
		{"trap view", `"trap":{"view_max":0}`, "trap.view_max"},
		{"trap view exceeds ring", `"trap":{"ring":5,"view_max":6}`, "must not exceed"},
		{"listen port", `"listen":"127.0.0.1:70000"`, "listen port"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"clusters":[],"edge_devices":[],` + tc.body + `}`)
			if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseRejectsUnsafeClusterOverrides(t *testing.T) {
	for _, tc := range []struct {
		fragment string
		want     string
	}{
		{`"history_points":0`, "clusters[0].history_points"},
		{`"ssh_timeout":-1`, "clusters[0].ssh_timeout"},
		{`"intervals":{"fast":0}`, "clusters[0].intervals.fast"},
		{`"intervals":{"os":-5}`, "clusters[0].intervals.os"},
	} {
		raw := []byte(`{"secret_policy":"allow-plaintext","clusters":[{"key":"ft","mgmt_ip":"10.0.0.1",` + tc.fragment + `}]}`)
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("fragment %s error = %v, want %q", tc.fragment, err, tc.want)
		}
	}
}

func TestParseRejectsDuplicateAndCrossTypeDeviceKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"cluster duplicate", `{"clusters":[{"key":"same","mgmt_ip":"10.0.0.1"},{"key":"same","mgmt_ip":"10.0.0.2"}]}`},
		{"edge duplicate", `{"clusters":[],"edge_devices":[{"key":"same","kind":"nas"},{"key":"same","kind":"server"}]}`},
		{"cross type", `{"clusters":[{"key":"same","mgmt_ip":"10.0.0.1"}],"edge_devices":[{"key":"same","kind":"nas"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.raw)); err == nil || !strings.Contains(err.Error(), `duplicate device key "same"`) {
				t.Fatalf("Parse error = %v", err)
			}
		})
	}
}

func TestParseRejectsInvalidPLCNetworkValues(t *testing.T) {
	for _, tc := range []struct {
		fragment string
		want     string
	}{
		{`"fins_port":70000`, "fins_port"},
		{`"fins_port":-1`, "fins_port"},
		{`"fins_src_node":255`, "fins_src_node"},
		{`"fins_src_node":-1`, "fins_src_node"},
	} {
		raw := []byte(`{"clusters":[],"edge_devices":[{"key":"plc","kind":"plc","ip":"10.0.0.2",` + tc.fragment + `}]}`)
		if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("fragment %s error = %v", tc.fragment, err)
		}
	}
}

func TestParseAcceptsBoundaryConfiguration(t *testing.T) {
	raw := fmt.Sprintf(`{
		"clusters":[{"key":"ft","mgmt_ip":"10.0.0.1","history_points":1,
			"ssh_timeout":1,"intervals":{"fast":1,"slow":1,"static":1,"os":1,"snmp":1}}],
		"edge_devices":[{"key":"plc","kind":"plc","ip":"10.0.0.2","fins_port":65535,"fins_src_node":254}],
		"avcli_timeout":1,"ssh_timeout":1,"http_timeout":1,"cache_refresh":1,"history_points":1,
		"intervals":{"fast":%d,"slow":1,"static":1,"os":1,"snmp":1},
		"trap":{"port":65535,"ring":1,"view_max":1}
	}`, maxPollIntervalSeconds)
	if _, err := Parse([]byte(raw)); err != nil {
		t.Fatalf("boundary config rejected: %v", err)
	}
}

func TestNotificationConfigDefaultsAndValidation(t *testing.T) {
	cfg, err := Parse([]byte(`{"clusters":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notifications.Enabled || cfg.Notifications.RetryMax != 5 || cfg.Notifications.RetryBaseSeconds != 5 {
		t.Fatalf("notification defaults = %#v", cfg.Notifications)
	}
	for name, raw := range map[string]string{
		"non-object":      `{"clusters":[],"notifications":[]}`,
		"null":            `{"clusters":[],"notifications":null}`,
		"enabled no URL":  `{"clusters":[],"notifications":{"enabled":true}}`,
		"bad escalation":  `{"clusters":[],"notifications":{"escalation_hours":5}}`,
		"retry zero":      `{"clusters":[],"notifications":{"retry_max":0}}`,
		"retry excessive": `{"clusters":[],"notifications":{"retry_max":21}}`,
		"base zero":       `{"clusters":[],"notifications":{"retry_base_seconds":0}}`,
		"base excessive":  `{"clusters":[],"notifications":{"retry_base_seconds":301}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err == nil {
				t.Fatalf("invalid notifications accepted: %s", raw)
			}
		})
	}
	for _, hours := range []int{0, 4, 24} {
		raw := fmt.Sprintf(`{"secret_policy":"allow-plaintext","clusters":[],"notifications":{"enabled":true,"webhook_url":"https://hooks.slack.com/services/test","escalation_hours":%d}}`, hours)
		if _, err := Parse([]byte(raw)); err != nil {
			t.Fatalf("escalation_hours=%d rejected: %v", hours, err)
		}
	}
}
