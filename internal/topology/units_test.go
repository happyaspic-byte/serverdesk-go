package topology

import (
	"reflect"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want *int64
	}{
		{"110.81 GiB", i64(118981331517)},
		{"10.00 GiB", i64(10737418240)},
		{"1.5 KiB", i64(1536)},
		{"512 B", i64(512)},
		{"512", i64(512)},
		{"100 MB", i64(100000000)}, // 십진 단위도 받는다
		{"bogus", nil},
		{"", nil},
		{"1.5 XB", nil},
	}
	for _, c := range cases {
		got := ParseSize(c.in)
		if (got == nil) != (c.want == nil) {
			t.Errorf("ParseSize(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		if got != nil && *got != *c.want {
			t.Errorf("ParseSize(%q) = %d, want %d", c.in, *got, *c.want)
		}
	}
}

func TestParseBandwidth(t *testing.T) {
	cases := []struct {
		in   string
		want *int64
	}{
		{"10 Gb/s", i64(10000000000)},
		{"1 Gb/s", i64(1000000000)},
		{"100 Mb/s", i64(100000000)},
		{"10 gb/s", i64(10000000000)}, // 대소문자 무관
		{"bad", nil},
		{"", nil},
	}
	for _, c := range cases {
		got := ParseBandwidth(c.in)
		if (got == nil) != (c.want == nil) {
			t.Errorf("ParseBandwidth(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		if got != nil && *got != *c.want {
			t.Errorf("ParseBandwidth(%q) = %d, want %d", c.in, *got, *c.want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	if got := HumanSize(i64(118981331517)); got == nil || *got != "110.81 GiB" {
		t.Errorf("HumanSize = %v, want 110.81 GiB", got)
	}
	if got := HumanSize(i64(500)); got == nil || *got != "500.00 B" {
		t.Errorf("HumanSize = %v, want 500.00 B", got)
	}
	if got := HumanSize(nil); got != nil {
		t.Errorf("HumanSize(nil) = %v, want nil", got)
	}
}

func TestPct(t *testing.T) {
	if got := Pct(i64(50), i64(100)); got == nil || *got != 50.0 {
		t.Errorf("Pct(50,100) = %v, want 50", got)
	}
	if got := Pct(i64(1), i64(3)); got == nil || *got != 33.33 {
		t.Errorf("Pct(1,3) = %v, want 33.33", got)
	}
	// 원본 계약: 0 은 '정보 없음' 과 구분하지 않고 nil
	if got := Pct(i64(0), i64(100)); got != nil {
		t.Errorf("Pct(0,100) = %v, want nil", got)
	}
	if got := Pct(i64(1), nil); got != nil {
		t.Errorf("Pct(1,nil) = %v, want nil", got)
	}
}

func TestParseBool(t *testing.T) {
	if got := ParseBool("true", nil); got == nil || !*got {
		t.Errorf("ParseBool(true) = %v", got)
	}
	if got := ParseBool("Disabled", nil); got == nil || *got {
		t.Errorf("ParseBool(Disabled) = %v", got)
	}
	def := false
	if got := ParseBool("???", &def); got == nil || *got != false {
		t.Errorf("ParseBool(???) = %v, want false(def)", got)
	}
	if got := ParseBool("???", nil); got != nil {
		t.Errorf("ParseBool(???, nil) = %v, want nil", got)
	}
}

func TestStatusMax(t *testing.T) {
	if got := StatusMax(); got != "unknown" {
		t.Errorf("StatusMax() = %q", got)
	}
	if got := StatusMax("ok", "degraded", "warning"); got != "degraded" {
		t.Errorf("StatusMax = %q", got)
	}
	if got := StatusMax("bogus"); got != "unknown" {
		t.Errorf("StatusMax(bogus) = %q, want unknown", got)
	}
}

func TestClassifyAlert(t *testing.T) {
	sev, why := ClassifyAlert(AlertInput{Name: "x", Description: "Node node0 rebooted unexpectedly", Severity: "0"})
	if sev != "critical" || why != "keyword:rebooted unexpectedly" {
		t.Errorf("classify = (%q,%q)", sev, why)
	}
	// 키워드 미매치 시에만 숫자 severity
	sev, why = ClassifyAlert(AlertInput{Name: "x", Description: "something vague", Severity: "0"})
	if sev != "degraded" || why != "severity:0" {
		t.Errorf("classify = (%q,%q)", sev, why)
	}
	sev, _ = ClassifyAlert(AlertInput{Description: "Unit is not configured to send E-Alert", Severity: "0"})
	if sev != "warning" {
		t.Errorf("classify not-configured = %q, want warning", sev)
	}
	sev, why = ClassifyAlert(AlertInput{Description: "totally fine"})
	if sev != "unknown" || why != "unclassified" {
		t.Errorf("classify = (%q,%q)", sev, why)
	}
}

func TestExtractAlertTargets(t *testing.T) {
	// 이어붙인 텍스트가 'Network Network' 를 통째로 소비하는 함정 케이스
	got := ExtractAlertTargets(AlertInput{
		Name:        "Detection of Bad Network",
		Description: "Network P1 has lost connectivity with the intranet.",
	})
	want := []AlertTarget{
		{Type: "sharednetwork", Name: "P1", Evidence: "alert-text"},
		{Type: "sharednetwork", Name: "Network", Evidence: "alert-text"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("targets = %#v, want %#v", got, want)
	}

	got = ExtractAlertTargets(AlertInput{Description: "Quorum server 172.30.1.90 is offline"})
	if len(got) != 1 || got[0].Type != "quorum" || got[0].Name != "172.30.1.90" {
		t.Errorf("quorum targets = %#v", got)
	}

	// 점이 들어간 VM 이름이 잘리지 않아야 한다
	got = ExtractAlertTargets(AlertInput{Description: "VM ubuntu_Server_26.04_03 is down"})
	if len(got) != 1 || got[0].Type != "vm" || got[0].Name != "ubuntu_Server_26.04_03" {
		t.Errorf("vm targets = %#v", got)
	}
}

func TestDeriveNICMapFromAlerts(t *testing.T) {
	got := DeriveNICMapFromAlerts([]AlertInput{{
		Name:        "a",
		Description: "SharedNetwork P3 currently has a disconnected or uncabled ibiz2 localNetwork",
	}})
	if !reflect.DeepEqual(got, map[string]string{"ibiz2": "P3"}) {
		t.Errorf("nic map = %#v", got)
	}
}

func TestGuessNICRole(t *testing.T) {
	cases := []struct {
		in   string
		kind string
		role string
	}{
		{"priv0", "physical", "a-link"},
		{"alink1", "physical", "a-link"},
		{"ibiz0", "physical", "business"},
		{"p38p2", "physical", ""}, // 이름만으로는 역할을 단정하지 않는다
		{"eth0", "physical", ""},
		{"biz0", "bridge", "business"},
		{"vnet3", "guest-tap", ""},
		{"lo", "ignore", ""},
		{"alo0", "unknown", "unknown"},
	}
	for _, c := range cases {
		kind, role := GuessNICRole(c.in)
		if kind != c.kind || role != c.role {
			t.Errorf("GuessNICRole(%q) = (%q,%q), want (%q,%q)", c.in, kind, role, c.kind, c.role)
		}
	}
}
