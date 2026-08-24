package demo

import (
	"net"
	"strings"
	"testing"
)

func TestDevicesAreThreeSafeSyntheticRecords(t *testing.T) {
	devices := Devices()
	if len(devices) != 3 {
		t.Fatalf("sample device count = %d, want 3", len(devices))
	}

	want := map[string]string{"EV": "op", "EDGE": "deg", "NAS": "op"}
	seenID := map[string]bool{}
	seenType := map[string]bool{}
	for _, device := range devices {
		id, _ := device["id"].(string)
		if id == "" || seenID[id] {
			t.Fatalf("sample id must be non-empty and unique: %q", id)
		}
		seenID[id] = true

		typeName, _ := device["type"].(string)
		status, ok := want[typeName]
		if !ok || device["status"] != status {
			t.Fatalf("sample %q type/status = %q/%v", id, typeName, device["status"])
		}
		seenType[typeName] = true
		if device["source"] != Source || device["live"] != false {
			t.Fatalf("sample %q source/live = %v/%v", id, device["source"], device["live"])
		}

		meta, ok := device["meta"].(map[string]any)
		if !ok || meta["demo"] != true || meta["sample"] != true {
			t.Fatalf("sample %q missing explicit demo/sample metadata: %#v", id, meta)
		}
		if label, _ := meta["label"].(string); !strings.HasPrefix(label, "[샘플]") {
			t.Fatalf("sample %q label is not visibly marked: %q", id, label)
		}
		if meta["company"] != "샘플 고객사" || meta["factory"] != "데모 공장" {
			t.Fatalf("sample %q contains a non-synthetic organization: %#v", id, meta)
		}
		assertSafeSampleValue(t, id, device)
	}
	if len(seenType) != len(want) {
		t.Fatalf("sample types = %#v, want EV, EDGE, NAS", seenType)
	}
}

func TestDevicesReturnsIndependentCopies(t *testing.T) {
	first := Devices()
	first[0]["status"] = "down"
	firstMeta := first[0]["meta"].(map[string]any)
	firstMeta["label"] = "changed"
	firstNodes := firstMeta["nodes"].([]any)
	firstNodes[0].(map[string]any)["ip"] = "10.0.0.1"

	second := Devices()
	if second[0]["status"] != "op" {
		t.Fatalf("sample status was aliased across calls: %v", second[0]["status"])
	}
	secondMeta := second[0]["meta"].(map[string]any)
	if secondMeta["label"] == "changed" || secondMeta["nodes"].([]any)[0].(map[string]any)["ip"] == "10.0.0.1" {
		t.Fatal("nested sample data was aliased across calls")
	}
}

func assertSafeSampleValue(t *testing.T, id string, value any) {
	t.Helper()
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			lower := strings.ToLower(key)
			for _, forbidden := range []string{"password", "secret", "community", "token", "webhook"} {
				if strings.Contains(lower, forbidden) {
					t.Fatalf("sample %q contains sensitive field name %q", id, key)
				}
			}
			if (lower == "ip" || lower == "mgmt") && child != nil {
				address, ok := child.(string)
				if !ok || !isDocumentationIP(address) {
					t.Fatalf("sample %q has non-documentation address %q=%v", id, key, child)
				}
			}
			assertSafeSampleValue(t, id, child)
		}
	case []any:
		for _, child := range item {
			assertSafeSampleValue(t, id, child)
		}
	}
}

func isDocumentationIP(value string) bool {
	ip := net.ParseIP(value)
	if ip == nil {
		return false
	}
	for _, block := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24"} {
		_, subnet, _ := net.ParseCIDR(block)
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}
