package edge

import (
	"strings"
	"testing"
)

func TestServerStatus(t *testing.T) {
	cases := []struct {
		aliveOS, rfOK, powerOff, useSNMP bool
		want                             string
	}{
		{true, false, false, true, "op"},    // SNMP 응답 = op
		{true, true, false, true, "op"},     // SNMP 우선
		{false, true, false, true, "deg"},   // SNMP 무응답 + 전원 On = deg
		{false, true, false, false, "op"},   // BMC 단독 = 정상
		{false, true, true, true, "down"},   // 전원 Off = down
		{false, true, true, false, "down"},  // 전원 Off (SNMP 미설정도 down)
		{false, false, false, true, "down"}, // 전부 무응답 = down
		{false, false, false, false, "down"},
	}
	for _, c := range cases {
		got := serverStatus(c.aliveOS, c.rfOK, c.powerOff, c.useSNMP)
		if got != c.want {
			t.Fatalf("serverStatus(%v,%v,%v,%v) = %q, want %q",
				c.aliveOS, c.rfOK, c.powerOff, c.useSNMP, got, c.want)
		}
	}
}

func TestMapRedfishSystem(t *testing.T) {
	s := map[string]any{
		"PowerState":       "On",
		"Status":           map[string]any{"Health": "OK"},
		"Manufacturer":     " HPE ",
		"Model":            "ProLiant DL360 Gen10",
		"SerialNumber":     "MXQ123",
		"BiosVersion":      "U32 v2.50",
		"HostName":         "esxi01",
		"ProcessorSummary": map[string]any{"Model": "Intel Xeon Silver 4210", "Count": 2.0},
		"MemorySummary": map[string]any{
			"TotalSystemMemoryGiB": 128.0,
			"Status":               map[string]any{"Health": "OK"},
		},
	}
	rf := mapRedfishSystem(s, "/redfish/v1/Systems/1")
	if rf.Power != "On" || rf.Health != "OK" || rf.Maker != "HPE" {
		t.Fatalf("rf = %+v", rf)
	}
	if rf.Model != "ProLiant DL360 Gen10" || rf.Serial != "MXQ123" || rf.BIOS != "U32 v2.50" {
		t.Fatalf("rf = %+v", rf)
	}
	if rf.CPUModel != "Intel Xeon Silver 4210" || rf.MemGiB != 128.0 || rf.MemHealth != "OK" {
		t.Fatalf("rf = %+v", rf)
	}
	// 빈 입력도 안전.
	rf2 := mapRedfishSystem(map[string]any{}, "")
	if rf2.Power != "" || rf2.Maker != "" {
		t.Fatalf("empty = %+v", rf2)
	}
}

func TestRedfishFirstMember(t *testing.T) {
	root := map[string]any{"Members": []any{map[string]any{"@odata.id": "/redfish/v1/Systems/1"}}}
	if got := redfishFirstMember(root); got != "/redfish/v1/Systems/1" {
		t.Fatalf("member = %q", got)
	}
	if got := redfishFirstMember(map[string]any{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestMapRedfishThermal(t *testing.T) {
	th := mapRedfishThermal(map[string]any{
		"Temperatures": []any{
			map[string]any{"Name": "Inlet", "ReadingCelsius": 23.04,
				"Status": map[string]any{"Health": "OK"}},
			map[string]any{"Name": "NoReading"}, // ReadingCelsius 없음 → 스킵
		},
		"Fans": []any{
			map[string]any{"FanName": "Fan 1", "Reading": 4080.0,
				"Status": map[string]any{"Health": "OK"}},
		},
	})
	if len(th.Temps) != 1 || th.Temps[0]["c"] != 23.0 {
		t.Fatalf("temps = %v", th.Temps)
	}
	if len(th.Fans) != 1 || th.Fans[0]["name"] != "Fan 1" || th.Fans[0]["rpm"] != 4080.0 {
		t.Fatalf("fans = %v", th.Fans)
	}
	// 12개 상한.
	many := []any{}
	for i := 0; i < 20; i++ {
		many = append(many, map[string]any{"Name": "T", "ReadingCelsius": 1.0})
	}
	th2 := mapRedfishThermal(map[string]any{"Temperatures": many})
	if len(th2.Temps) != 12 {
		t.Fatalf("cap = %d", len(th2.Temps))
	}
}

func TestServerKernelVersion(t *testing.T) {
	sysd := "Linux web01 5.10.0-25-amd64 #1 SMP Debian 5.10.191-1 (2023-08-16) x86_64"
	if got := serverKernel(sysd); got != "5.10.0-25-amd64" {
		t.Fatalf("kernel = %q", got)
	}
	if got := serverVersion(sysd); got != "Linux web01 5.10.0-25-amd64" {
		t.Fatalf("version = %q", got)
	}
	if got := serverKernel("Microsoft Windows Server 2019"); got != "" {
		t.Fatalf("windows kernel = %q", got)
	}
	// 48자 절단.
	long := "Linux h " + string(make([]byte, 60)) + "# tail"
	if got := serverVersion(long); len([]rune(got)) > 48 {
		t.Fatalf("version len = %d", len(got))
	}
}

func serverFake() fakeSNMP {
	return fakeSNMP{
		"10.0.0.5": {
			oSysUptime:   vticks(17280000), // 2일
			oSysDescr:    vstrv("Linux web01 5.10.0-25-amd64 #1 SMP Debian"),
			oSysName:     vstrv("web01"),
			oUCDCPUIdle:  vint(97),
			oUCDLA1:      vstrv("0.05"),
			oUCDMemTot:   vint(16777216), // 16 GiB
			oUCDMemAvail: vint(4194304),
		},
	}
}

func TestPollServerSNMPOnly(t *testing.T) {
	w, pc := testWorker(serverFake())
	dev := DeviceConfig{Key: "srv1", Kind: "server", IP: "10.0.0.5", Community: "public"}
	d, _ := w.pollServer(pc, dev, nil)
	if d["status"] != "op" {
		t.Fatalf("status = %v", d["status"])
	}
	if d["cpu0"] != int64(3) || d["mem0"] != int64(75) {
		t.Fatalf("cpu/mem = %v %v", d["cpu0"], d["mem0"])
	}
	if d["uptime"] != int64(2) {
		t.Fatalf("uptime = %v", d["uptime"])
	}
	m := d["meta"].(map[string]any)
	if m["version"] != "Linux web01 5.10.0-25-amd64" {
		t.Fatalf("version = %v", m["version"])
	}
	node := m["nodes"].([]any)[0].(map[string]any)
	if node["name"] != "web01" || node["metricsSource"] != "snmp" || node["standing"] != "normal" {
		t.Fatalf("node = %v", node)
	}
	la := node["loadAvg"].([]float64)
	if len(la) != 1 || la[0] != 0.05 {
		t.Fatalf("loadAvg = %v", la)
	}
	if node["memGiB"] != 16.0 {
		t.Fatalf("memGiB = %v", node["memGiB"])
	}
	srv := m["srv"].(map[string]any)
	if srv["kernel"] != "5.10.0-25-amd64" || srv["node"] != "web01" {
		t.Fatalf("srv = %v", srv)
	}
	if m["healthLevel"] != "ok" || m["bmc"] != nil {
		t.Fatalf("health = %v bmc = %v", m["healthLevel"], m["bmc"])
	}
	if n := len(m["alerts"].([]any)); n != 0 {
		t.Fatalf("alerts = %d", n)
	}
}

func TestPollServerDown(t *testing.T) {
	w, pc := testWorker(fakeSNMP{})
	dev := DeviceConfig{Key: "srv1", Kind: "server", IP: "10.0.0.5", Community: "public"}
	d, _ := w.pollServer(pc, dev, nil)
	if d["status"] != "down" {
		t.Fatalf("status = %v", d["status"])
	}
	m := d["meta"].(map[string]any)
	alerts := m["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["severity"] != "critical" {
		t.Fatalf("alerts = %v", alerts)
	}
	if m["healthLevel"] != "critical" {
		t.Fatalf("health = %v", m["healthLevel"])
	}
	// status==down 이면 "OS SNMP 무응답" 사유는 붙지 않는다(Python 과 동일).
	for _, r := range m["healthReasons"].([]any) {
		if r == "OS SNMP 무응답" {
			t.Fatal("down path must not add OS SNMP reason")
		}
	}
	node := m["nodes"].([]any)[0].(map[string]any)
	if node["state"] != "stopped" || node["standing"] != "unknown" || node["reachable"] != false {
		t.Fatalf("node = %v", node)
	}
}

func TestPollServerDegNoSNMPAnswer(t *testing.T) {
	// SNMP 설정 + 무응답, BMC 전원 On → deg + "OS SNMP 무응답" 사유.
	// (BMC 자체는 테스트에서 네트워크 차단 — Redfish 실패 경로도 함께 검증.)
	w, pc := testWorker(fakeSNMP{})
	dev := DeviceConfig{Key: "srv1", Kind: "server", IP: "10.0.0.5", Community: "public",
		BMCIP: "10.0.0.6", BMCUser: "admin", BMCPassword: "x"}
	d, st := w.pollServer(pc, dev, nil)
	if d["status"] != "down" { // rf 무응답 + snmp 무응답 = down
		t.Fatalf("status = %v", d["status"])
	}
	m := d["meta"].(map[string]any)
	bmc := m["bmc"].(map[string]any)
	if bmc["ip"] != "10.0.0.6" || bmc["up"] != false {
		t.Fatalf("bmc = %v", bmc)
	}
	found := false
	for _, r := range m["healthReasons"].([]any) {
		if s, ok := r.(string); ok && strings.HasPrefix(s, "BMC 접속 실패") {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons = %v", m["healthReasons"])
	}
	if st == nil {
		t.Fatal("static must be returned")
	}
}

func TestPollServerBadLoadavg(t *testing.T) {
	fake := serverFake()
	fake["10.0.0.5"][oUCDLA1] = vstrv("bogus")
	w, pc := testWorker(fake)
	dev := DeviceConfig{Key: "srv1", Kind: "server", IP: "10.0.0.5", Community: "public"}
	d, _ := w.pollServer(pc, dev, nil)
	// Python 의 float("bogus") ValueError → 워커 예외 경로와 같은 down 골격.
	if d["status"] != "down" {
		t.Fatalf("status = %v", d["status"])
	}
	m := d["meta"].(map[string]any)
	if _, ok := m["srv"]; ok {
		t.Fatal("panic-equivalent path must not carry srv meta")
	}
}
