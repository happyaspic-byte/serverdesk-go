package edge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"serverdesk/internal/snmp"
)

func TestLoadDevices(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"key":"prn1","kind":"printer","ip":"172.30.1.113","community":"public","extra_ips":["172.30.1.98"]}`),
		json.RawMessage(`{"key":"nas1","type":"nas","ip":"172.30.1.99","fins_port":9600,"bmc_ip":"10.0.0.6","bmc_user":"admin"}`),
	}
	devs, err := LoadDevices(raw)
	if err != nil {
		t.Fatalf("LoadDevices: %v", err)
	}
	if len(devs) != 2 {
		t.Fatalf("len = %d", len(devs))
	}
	if devs[0].kind() != "printer" || devs[0].ExtraIPs[0] != "172.30.1.98" {
		t.Fatalf("dev0 = %+v", devs[0])
	}
	// type 별칭도 kind 로 인식.
	if devs[1].kind() != "nas" || devs[1].FinsPort != 9600 || devs[1].BMCIP != "10.0.0.6" {
		t.Fatalf("dev1 = %+v", devs[1])
	}
	// 잘못된 항목은 에러(조용한 감시 누락 방지).
	_, err = LoadDevices([]json.RawMessage{json.RawMessage(`{"key": 1}`)})
	if err == nil {
		t.Fatal("bad json must error")
	}
	var ce *ConfigError
	if !errors.As(err, &ce) {
		t.Fatalf("err type = %T", err)
	}
}

func TestNewWorkerFilters(t *testing.T) {
	w := NewWorker([]DeviceConfig{
		{Key: "ok", Kind: "printer", IP: "1.1.1.1"},
		{Key: "badkind", Kind: "router", IP: "1.1.1.2"},
		{Key: "noip", Kind: "nas"},
		{Kind: "server", IP: "1.1.1.3"}, // key 없음
	})
	if len(w.devices) != 1 || w.devices[0].Key != "ok" {
		t.Fatalf("devices = %+v", w.devices)
	}
}

func TestBaseDeviceSchema(t *testing.T) {
	d := baseDevice(DeviceConfig{Key: "k", Kind: "nas", IP: "10.20.30.40"}, "NAS", "deg", 3)
	// 플랫 스키마 키 + 타입.
	if d["id"] != "k" || d["host"] != "k" || d["type"] != "NAS" || d["status"] != "deg" {
		t.Fatalf("base = %v", d)
	}
	if d["availN"] != 99.9 || d["sync"] != "simplex" || d["uptime"] != int64(3) {
		t.Fatalf("deg fields = %v %v %v", d["availN"], d["sync"], d["uptime"])
	}
	if d["site"] != "10.20.30.40" { // site 기본값 = ip
		t.Fatalf("site = %v", d["site"])
	}
	if d["cpu0"] != int64(-1) || d["cpuNA"] != true {
		t.Fatalf("cpu = %v", d["cpu0"])
	}
	// 빈 값은 null 이 아니라 빈 배열/객체여야 한다(프런트 스키마).
	if hc, ok := d["histCpu"].([]int64); !ok || hc == nil || len(hc) != 0 {
		t.Fatalf("histCpu = %#v", d["histCpu"])
	}
	m := d["meta"].(map[string]any)
	for _, k := range []string{"alerts", "healthReasons", "traps", "snmp", "nodes", "vmList", "events"} {
		if v, ok := m[k].([]any); !ok || v == nil {
			t.Fatalf("meta.%s = %#v", k, m[k])
		}
	}
	for _, k := range []string{"alertCounts", "collection"} {
		if v, ok := m[k].(map[string]any); !ok || v == nil {
			t.Fatalf("meta.%s = %#v", k, m[k])
		}
	}
	if m["company"] != "루비컴" || m["factory"] != "10.20.30.0/24" || m["tzName"] != "KST" {
		t.Fatalf("defaults = %v %v %v", m["company"], m["factory"], m["tzName"])
	}
	if m["platform"] != "nas" || m["label"] != "k" {
		t.Fatalf("meta = %v %v", m["platform"], m["label"])
	}
	// op/down 의 avail·sync 스케일.
	d2 := baseDevice(DeviceConfig{Key: "k", Kind: "plc", IP: "1.1.1.1"}, "PLC", "op", 0)
	if d2["availN"] != 99.99 || d2["sync"] != "sync" {
		t.Fatalf("op = %v", d2["availN"])
	}
	d3 := baseDevice(DeviceConfig{Key: "k", Kind: "plc", IP: "1.1.1.1"}, "PLC", "down", 0)
	if d3["availN"] != 99.0 || d3["sync"] != "offline" {
		t.Fatalf("down = %v", d3["availN"])
	}
}

func TestDownBaseDropsExtras(t *testing.T) {
	// Python 워커 예외 경로는 asset_tag/floor_pos/vendor 를 떨군다.
	dev := DeviceConfig{Key: "k", Kind: "printer", IP: "1.1.1.1",
		AssetTag: "AT-1", FloorPos: "1F", Vendor: "X"}
	d := downBase(dev, 1_700_000_000.0)
	m := d["meta"].(map[string]any)
	if m["assetTag"] != "" || m["floorPos"] != "" || m["vendor"] != "" {
		t.Fatalf("extras = %v %v %v", m["assetTag"], m["floorPos"], m["vendor"])
	}
	if d["type"] != "PRN" || d["status"] != "down" {
		t.Fatalf("type = %v", d["type"])
	}
	alerts := m["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["name"] != "DEVICE_STATE" {
		t.Fatalf("alerts = %v", alerts)
	}
}

func TestWorkerHistoryCap(t *testing.T) {
	dev := DeviceConfig{Key: "srv1", Kind: "server", IP: "10.0.0.5", Community: "public"}
	w := NewWorker([]DeviceConfig{dev})
	w.SNMPGet = serverFake().get
	for i := 0; i < 60; i++ {
		w.pollRound(context.Background())
	}
	latest := w.LatestDevices()
	if len(latest) != 1 {
		t.Fatalf("latest = %d", len(latest))
	}
	hc := latest[0]["histCpu"].([]int64)
	hm := latest[0]["histMem"].([]int64)
	if len(hc) != 48 || len(hm) != 48 {
		t.Fatalf("hist len = %d %d", len(hc), len(hm))
	}
	if hc[47] != 3 || hm[47] != 75 {
		t.Fatalf("hist values = %d %d", hc[47], hm[47])
	}
	if latest[0]["status"] != "op" {
		t.Fatalf("status = %v", latest[0]["status"])
	}
}

func TestWorkerSkipsNAHistory(t *testing.T) {
	// cpuNA 인 장비(프린터 down)는 히스토리에 쌓지 않는다.
	dev := DeviceConfig{Key: "prn1", Kind: "printer", IP: "10.0.0.9"}
	w := NewWorker([]DeviceConfig{dev})
	w.SNMPGet = fakeSNMP{}.get // 무응답
	w.sws = blockedClient()
	w.pollRound(context.Background())
	d := w.LatestDevices()[0]
	if len(d["histCpu"].([]int64)) != 0 || len(d["histMem"].([]int64)) != 0 {
		t.Fatalf("NA hist = %v", d["histCpu"])
	}
	if d["status"] != "down" {
		t.Fatalf("status = %v", d["status"])
	}
}

func TestWorkerPanicIsolation(t *testing.T) {
	// 한 장비의 패닉이 라운드를 죽이지 않고 down 골격으로 대챸다.
	boom := SNMPGetFunc(func(context.Context, string, int, string, []string, time.Duration) (map[string]snmp.Value, error) {
		panic("boom")
	})
	devs := []DeviceConfig{
		{Key: "bad", Kind: "printer", IP: "10.0.0.9"},
		{Key: "good", Kind: "server", IP: "10.0.0.5", Community: "public"},
	}
	w := NewWorker(devs)
	sn := serverFake()
	w.SNMPGet = func(ctx context.Context, ip string, port int, comm string, oids []string, to time.Duration) (map[string]snmp.Value, error) {
		if ip == "10.0.0.9" {
			return boom(ctx, ip, port, comm, oids, to)
		}
		return sn.get(ctx, ip, port, comm, oids, to)
	}
	w.sws = blockedClient()
	logged := []string{}
	w.Logf = func(level, comp, msg string) { logged = append(logged, level+":"+comp+":"+msg) }
	w.pollRound(context.Background())
	latest := w.LatestDevices()
	if len(latest) != 2 {
		t.Fatalf("latest = %d", len(latest))
	}
	if latest[0]["status"] != "down" || latest[0]["type"] != "PRN" {
		t.Fatalf("panic device = %v", latest[0]["status"])
	}
	if latest[1]["status"] != "op" {
		t.Fatalf("good device = %v", latest[1]["status"])
	}
	if len(logged) == 0 {
		t.Fatal("panic must be logged")
	}
}

func TestStaticRoundRefreshFlag(t *testing.T) {
	// 5라운드마다 refresh 가 켜지는지 — 라운드 카운터 기준.
	w := NewWorker(nil)
	got := []bool{}
	for r := 0; r < 11; r++ {
		w.round = r
		// pollRound 낶부 계산과 동일 식.
		got = append(got, w.round%staticEvery == 0)
	}
	want := []bool{true, false, false, false, false, true, false, false, false, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("round %d refresh = %v", i, got[i])
		}
	}
}
