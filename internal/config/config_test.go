package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) *Config {
	t.Helper()
	c, err := Load("testdata/config.json")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

func TestLoadDefaults(t *testing.T) {
	c := loadFixture(t)

	// 파일에 명시된 값
	if c.Listen != "0.0.0.0:9890" {
		t.Errorf("listen = %q", c.Listen)
	}
	if c.AvcliTimeout != 45 {
		t.Errorf("avcli_timeout = %d, want 45", c.AvcliTimeout)
	}
	if c.SNMPEnabled {
		t.Error("snmp_enabled should be false (명시값)")
	}
	// 기본값(poller.py DEFAULTS)
	if c.AvcliBin != "avcli" {
		t.Errorf("avcli_bin = %q", c.AvcliBin)
	}
	if c.HistoryPoints != 120 {
		t.Errorf("history_points = %d, want 120", c.HistoryPoints)
	}
	if c.CacheRefresh != 5 {
		t.Errorf("cache_refresh = %d, want 5", c.CacheRefresh)
	}
	if c.RuntimeDir != "~/.everrun-poller" {
		t.Errorf("runtime_dir = %q", c.RuntimeDir)
	}
	if c.SSHTimeout != 20 {
		t.Errorf("ssh_timeout = %d, want 20", c.SSHTimeout)
	}
	if c.LogLevel != "info" {
		t.Errorf("log_level = %q", c.LogLevel)
	}
	if c.SimDevices != 50 {
		t.Errorf("sim_devices = %d, want 50", c.SimDevices)
	}
	if c.SimSeed != 20260720 {
		t.Errorf("sim_seed = %d", c.SimSeed)
	}
	if c.HTTPTimeout != 30 {
		t.Errorf("http_timeout = %d, want 30", c.HTTPTimeout)
	}
	if c.CORSAllowedOrigins == nil || len(c.CORSAllowedOrigins) != 0 {
		t.Errorf("cors_allowed_origins = %#v, want empty non-nil", c.CORSAllowedOrigins)
	}
	// intervals 부분 병합: fast 만 명시
	wantIv := Intervals{Fast: 30, Slow: 300, Static: 86400, OS: 10, SNMP: 60}
	if c.Intervals != wantIv {
		t.Errorf("intervals = %+v, want %+v", c.Intervals, wantIv)
	}
	// trap: 기본값 + 평면 키 오버라이드
	if !c.Trap.Enabled {
		t.Error("trap.enabled should default true")
	}
	if c.Trap.Port != 162 {
		t.Errorf("trap.port = %d, want 162 (flat trap_port)", c.Trap.Port)
	}
	if c.Trap.Community == nil || *c.Trap.Community != "fake-trap-community" {
		t.Errorf("trap.community = %v", c.Trap.Community)
	}
	if c.Trap.Bind != "0.0.0.0" || c.Trap.Persist != "traps.jsonl" ||
		c.Trap.Ring != 500 || c.Trap.ViewMax != 50 || c.Trap.MibDir != "docs/mibs" {
		t.Errorf("trap defaults wrong: %+v", c.Trap)
	}
	if c.Path != "testdata/config.json" {
		t.Errorf("Path = %q", c.Path)
	}
}

func TestNestedTrapOverlay(t *testing.T) {
	data := []byte(`{
		"clusters": [{"key": "a", "mgmt_ip": "10.0.0.1"}],
		"trap": {"enabled": false, "port": 1162, "community": "fake-nested-community"}
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Trap.Enabled {
		t.Error("nested trap.enabled should win")
	}
	if c.Trap.Port != 1162 {
		t.Errorf("trap.port = %d, want 1162", c.Trap.Port)
	}
	if c.Trap.Ring != 500 {
		t.Errorf("trap.ring = %d, want default 500", c.Trap.Ring)
	}
}

func TestClusterInheritance(t *testing.T) {
	c := loadFixture(t)
	if len(c.Clusters) != 2 {
		t.Fatalf("clusters = %d", len(c.Clusters))
	}
	full := c.Clusters[0]
	if full.TzOffsetSecs == nil || *full.TzOffsetSecs != 32400 {
		t.Errorf("tz_offset_secs = %v", full.TzOffsetSecs)
	}
	if full.NicNetworkMap["node0"]["ibiz0"] != "network0" {
		t.Errorf("nic_network_map = %#v", full.NicNetworkMap)
	}
	// 최상위 intervals 위에 클러스터 오버라이드(slow)만 병합
	wantIv := Intervals{Fast: 30, Slow: 600, Static: 86400, OS: 10, SNMP: 60}
	if full.Intervals != wantIv {
		t.Errorf("cluster intervals = %+v, want %+v", full.Intervals, wantIv)
	}
	if full.SNMPCommunity != "fake-community" {
		t.Errorf("cluster snmp_community = %q (최상위 상속)", full.SNMPCommunity)
	}
	if full.SNMPEnabled {
		t.Error("cluster snmp_enabled should inherit false")
	}
	if full.SSHTimeout != 20 || full.HistoryPoints != 120 {
		t.Errorf("cluster inherit ssh_timeout/history_points: %d/%d", full.SSHTimeout, full.HistoryPoints)
	}
	if full.Nodes[1].RootUser != "root" {
		t.Errorf("ssh_user alias → root_user = %q", full.Nodes[1].RootUser)
	}

	minimal := c.Clusters[1]
	if minimal.AdminUser != "admin" {
		t.Errorf("admin_user default = %q", minimal.AdminUser)
	}
	if !minimal.SNMPEnabled {
		t.Error("cluster-level snmp_enabled=true must beat top-level false")
	}
	if minimal.TzOffsetSecs != nil {
		t.Errorf("tz_offset_secs should be nil(자동 판별), got %v", *minimal.TzOffsetSecs)
	}
	if minimal.Platform != "ztcedge" {
		t.Errorf("platform = %q", minimal.Platform)
	}
}

func TestEdgeDevices(t *testing.T) {
	c := loadFixture(t)
	if len(c.EdgeDevices) != 5 {
		t.Fatalf("edge_devices = %d", len(c.EdgeDevices))
	}
	byKey := map[string]EdgeDevice{}
	for _, d := range c.EdgeDevices {
		byKey[d.Key] = d
	}
	if byKey["printer-1"].WebPassword != "fake-printer-web-pw" {
		t.Errorf("printer web_password = %q", byKey["printer-1"].WebPassword)
	}
	if len(byKey["nas-1"].ExtraIPs) != 1 || byKey["nas-1"].ExtraIPs[0] != "10.0.0.98" {
		t.Errorf("nas extra_ips = %#v", byKey["nas-1"].ExtraIPs)
	}
	if byKey["plc-1"].FinsPort != 9600 || byKey["plc-1"].FinsSrcNode != 84 {
		t.Errorf("plc fins = %d/%d", byKey["plc-1"].FinsPort, byKey["plc-1"].FinsSrcNode)
	}
	if byKey["pve-1"].User != "root@pam" || byKey["pve-1"].Password != "fake-pve-password" {
		t.Errorf("proxmox = %#v", byKey["pve-1"])
	}
	// "type" 키 별칭으로 kind 판별
	if byKey["srv-1"].Kind != "server" {
		t.Errorf("srv-1 kind = %q (type 별칭)", byKey["srv-1"].Kind)
	}
	if byKey["srv-1"].BmcIP != "10.0.0.131" || byKey["srv-1"].BmcPassword != "fake-bmc-password" {
		t.Errorf("srv-1 bmc = %#v", byKey["srv-1"])
	}
}

func TestLoadValidation(t *testing.T) {
	if _, err := Parse([]byte(`{"clusters": []}`)); err == nil {
		t.Error("empty clusters should fail")
	}
	if _, err := Parse([]byte(`{"clusters": [{"key": "a"}]}`)); err == nil {
		t.Error("missing mgmt_ip should fail")
	}
	if _, err := Parse([]byte(`{"clusters": [{"mgmt_ip": "10.0.0.1"}]}`)); err == nil {
		t.Error("missing key should fail")
	}
	if _, err := Parse([]byte(`{`)); err == nil {
		t.Error("broken JSON should fail")
	}
}

func TestSecretsMasking(t *testing.T) {
	_ = loadFixture(t) // Load 가 자격증명을 레지스트리에 등록한다
	got := Mask("login admin_password=fake-admin-password-1 ok")
	if got != "login admin_password=*** ok" {
		t.Errorf("Mask = %q", got)
	}
	// 방어적 패턴: 등록되지 않은 값도 -p / SSH_PW 뒤는 지운다
	if got := Mask("avcli -H 1.2.3.4 -u admin -p unregistered-secret-x"); got != "avcli -H 1.2.3.4 -u admin -p ***" {
		t.Errorf("Mask -p = %q", got)
	}
	if got := Mask("env SSH_PW=whatever-secret ssh node"); got != "env SSH_PW=*** ssh node" {
		t.Errorf("Mask SSH_PW = %q", got)
	}
	// 엣지 장비 비밀도 로드 시 등록된다
	if got := Mask("pve pw: fake-pve-password"); got != "pve pw: ***" {
		t.Errorf("Mask edge = %q", got)
	}
	// 3자 미만은 등록되지 않는다
	RegisterSecret("ab")
	if got := Mask("ab cd"); got != "ab cd" {
		t.Errorf("Mask short = %q", got)
	}
	if got := Mask(""); got != "" {
		t.Errorf("Mask empty = %q", got)
	}
}


func TestParseThresholds(t *testing.T) {
	// 키 없음 → 기본 78/90
	c, err := Parse([]byte(`{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}]}`))
	if err != nil {
		t.Fatalf("기본 파스 실패: %v", err)
	}
	if c.Thresholds.Warn != 78 || c.Thresholds.Crit != 90 {
		t.Errorf("기본값 78/90 이어야 함, got %+v", c.Thresholds)
	}
	// 정상 지정
	c, err = Parse([]byte(`{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}], "thresholds":{"warn":80,"crit":95}}`))
	if err != nil {
		t.Fatalf("지정 파스 실패: %v", err)
	}
	if c.Thresholds.Warn != 80 || c.Thresholds.Crit != 95 {
		t.Errorf("got %+v", c.Thresholds)
	}
	// 역전·부분 지정은 에러
	for _, bad := range []string{
		`{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}], "thresholds":{"warn":95,"crit":80}}`,
		`{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}], "thresholds":{"warn":80}}`,
		`{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}], "thresholds":{"warn":0,"crit":0}}`,
		`{"clusters":[{"key":"a","mgmt_ip":"10.0.0.1"}], "thresholds":{"warn":80,"crit":101}}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s → 에러여야 함", bad)
		}
	}
}

func TestCheckPerms(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckPerms(p); err != nil {
		t.Errorf("0600: %v", err)
	}
	if err := os.Chmod(p, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CheckPerms(p); err == nil {
		t.Error("0644 should warn")
	}
	if err := CheckPerms(filepath.Join(dir, "missing.json")); err != nil {
		t.Errorf("missing file: %v (poller.py 는 OSError 를 삼킨다)", err)
	}
}

func TestParseProcMounts(t *testing.T) {
	const mounts = `sysfs /sys sysfs rw,nosuid,nodev,noexec,relatime 0 0
proc /proc proc rw,nosuid,nodev,noexec,relatime,hidepid=2 0 0
`
	if got := parseProcMounts(mounts); got != "2" {
		t.Errorf("hidepid=2 → %q", got)
	}
	const subset = "proc /proc proc rw,nosuid,nodev,noexec,relatime,subset=pid 0 0\n"
	if got := parseProcMounts(subset); got != "subset=pid" {
		t.Errorf("subset=pid → %q", got)
	}
	const plain = "proc /proc proc rw,nosuid,nodev,noexec,relatime 0 0\n"
	if got := parseProcMounts(plain); got != "" {
		t.Errorf("no hidepid → %q, want empty", got)
	}
	if got := parseProcMounts(""); got != "" {
		t.Errorf("empty → %q", got)
	}
}

func TestParsePasswd(t *testing.T) {
	const passwd = `root:x:0:0:root:/root:/bin/bash
daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin
ubuntu:x:1000:1000:Ubuntu:/home/ubuntu:/bin/bash
other:x:1001:1001::/home/other:/bin/bash
nologin-user:x:1002:1002::/home/x:/usr/sbin/nologin
sync-user:x:1003:1003::/home/y:/bin/sync
nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin
`
	got := parsePasswd(passwd, "ubuntu")
	if len(got) != 1 || got[0] != "other" {
		t.Errorf("parsePasswd = %#v, want [other]", got)
	}
}

func TestEvalArgvExposure(t *testing.T) {
	// /proc 보호됨 → 정보 메시지, 에러 없음
	warn, err := evalArgvExposure("2", true, []string{"other"}, false)
	if err != nil || !strings.Contains(warn, "보호됨") {
		t.Errorf("protected: warn=%q err=%v", warn, err)
	}
	// 다른 로그인 계정 없음 → 경고만
	warn, err = evalArgvExposure("", false, nil, false)
	if err != nil || !strings.Contains(warn, "hidepid") {
		t.Errorf("no others: warn=%q err=%v", warn, err)
	}
	// 위험 조건 → 기동 거부
	warn, err = evalArgvExposure("", false, []string{"ops1", "ops2"}, false)
	if err == nil || !strings.Contains(err.Error(), "ops1, ops2") || warn != "" {
		t.Errorf("danger: warn=%q err=%v", warn, err)
	}
	// allow 오버라이드 → 에러 대신 경고
	warn, err = evalArgvExposure("", false, []string{"ops1"}, true)
	if err != nil || !strings.Contains(warn, "allow-argv-exposure") {
		t.Errorf("allow: warn=%q err=%v", warn, err)
	}
}
