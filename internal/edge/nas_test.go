package edge

import (
	"testing"
)

func TestUCDCPU(t *testing.T) {
	if c, ok := ucdCPU(92); !ok || c != 8 {
		t.Fatalf("idle92 = %d %v", c, ok)
	}
	if _, ok := ucdCPU(101); ok {
		t.Fatal("101 must fail")
	}
	if _, ok := ucdCPU(-1); ok {
		t.Fatal("-1 must fail")
	}
	if c, ok := ucdCPU(0); !ok || c != 100 {
		t.Fatalf("idle0 = %d", c)
	}
}

func TestUCDMemPct(t *testing.T) {
	// 캐시/버퍼를 가용으로 되돌린 실사용률.
	if m, ok := ucdMemPct(1024, 256, 128, 128); !ok || m != 50 {
		t.Fatalf("mem = %d %v, want 50", m, ok)
	}
	// 음수 클램프.
	if m, _ := ucdMemPct(1000, 1100, 0, 0); m != 0 {
		t.Fatalf("negative clamp = %d", m)
	}
	// 100 클램프.
	if m, _ := ucdMemPct(1000, -100, 0, 0); m != 100 {
		t.Fatalf("over clamp = %d", m)
	}
	if _, ok := ucdMemPct(0, 0, 0, 0); ok {
		t.Fatal("total 0 must fail")
	}
}

func TestSynRaidText(t *testing.T) {
	if synRaidText(1) != "normal" || synRaidText(11) != "degraded" || synRaidText(12) != "crashed" {
		t.Fatal("raid text mapping")
	}
	if synRaidText(8) != "parity checking" {
		t.Fatal("raid 8")
	}
	if synRaidText(99) != "normal" {
		t.Fatal("unknown → normal (Python parity)")
	}
}

func TestSynDiskOK(t *testing.T) {
	if !synDiskOK(1) || !synDiskOK(2) {
		t.Fatal("1/2 must be ok")
	}
	if synDiskOK(5) || synDiskOK(0) {
		t.Fatal("5/0 must not be ok")
	}
}

func nasFake() fakeSNMP {
	return fakeSNMP{
		"10.0.0.7": {
			oSysUptime:          vticks(17280000), // 2일
			oSynStatus:          vint(1),
			oSynTemp:            vint(41),
			oSynPower:           vint(1),
			oSynFanSys:          vint(1),
			oSynFanCPU:          vint(1),
			oSynUpgrade:         vint(1),
			oUCDCPUIdle:         vint(92),
			oUCDMemTot:          vint(1024),
			oUCDMemAvail:        vint(256),
			oUCDMemBuf:          vint(128),
			oUCDMemCache:        vint(128),
			oSynModel:           vstrv("DS920+"),
			oSynSerial:          vstrv("20A1B2C3"),
			oSynDSM:             vstrv("7.2-64570"),
			oSysName:            vstrv("happynas"),
			oSynDiskID + "0":    vstrv("Drive 1"),
			oSynDiskModel + "0": vstrv("WD40EFRX"),
			oSynDiskStat + "0":  vint(5), // crashed
			oSynDiskTemp + "0":  vint(38),
			oSynRaidName + "0":  vstrv("Volume 1"),
			oSynRaidStat + "0":  vint(11), // degraded
		},
	}
}

func TestPollNAS(t *testing.T) {
	w, pc := testWorker(nasFake())
	pc.refresh = true
	dev := DeviceConfig{Key: "nas1", Kind: "nas", IP: "10.0.0.7", ExtraIPs: []string{"10.0.0.8"}}
	d, st := w.pollNAS(pc, dev, nil)
	if d["status"] != "deg" {
		t.Fatalf("status = %v", d["status"])
	}
	if d["uptime"] != int64(2) {
		t.Fatalf("uptime = %v", d["uptime"])
	}
	if d["cpu0"] != int64(8) || d["cpuNA"] != false {
		t.Fatalf("cpu = %v", d["cpu0"])
	}
	if d["mem0"] != int64(50) || d["memNA"] != false {
		t.Fatalf("mem = %v", d["mem0"])
	}
	m := d["meta"].(map[string]any)
	if m["vendor"] != "Synology" || m["version"] != "7.2-64570" {
		t.Fatalf("meta = %v %v", m["vendor"], m["version"])
	}
	nas := m["nas"].(map[string]any)
	if nas["model"] != "DS920+" || nas["tempC"] != int64(41) || nas["upgradeAvailable"] != true {
		t.Fatalf("nas = %v", nas)
	}
	disks := nas["disks"].([]any)
	if len(disks) != 1 {
		t.Fatalf("disks = %v", disks)
	}
	dk := disks[0].(map[string]any)
	if dk["ok"] != false || dk["status"] != "warning" || dk["tempC"] != int64(38) {
		t.Fatalf("disk = %v", dk)
	}
	raids := nas["raid"].([]any)
	rd := raids[0].(map[string]any)
	if rd["status"] != "degraded" || rd["ok"] != false {
		t.Fatalf("raid = %v", rd)
	}
	// 랜포트: 본체 up + extra_ips 미응답 down (정보성 — 상태 사유 아님).
	lan := nas["lanPorts"].([]any)
	if lan[0].(map[string]any)["up"] != true || lan[1].(map[string]any)["up"] != false {
		t.Fatalf("lanPorts = %v", lan)
	}
	// 경보: DEVICE_STATE(deg) + DISK_FAULT + RAID_DEGRADED (모두 critical 급 취급 확인).
	alerts := m["alerts"].([]any)
	names := []string{}
	for _, a := range alerts {
		names = append(names, a.(map[string]any)["name"].(string))
	}
	want := []string{"DEVICE_STATE", "DISK_FAULT", "RAID_DEGRADED"}
	if len(names) != 3 {
		t.Fatalf("alerts = %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("alerts = %v, want %v", names, want)
		}
	}
	if alerts[1].(map[string]any)["severity"] != "critical" {
		t.Fatal("DISK_FAULT must be critical")
	}
	if st == nil || st.Model != "DS920+" || len(st.Disks) != 1 || len(st.Raids) != 1 {
		t.Fatalf("static = %+v", st)
	}
}

func TestPollNASAllNormal(t *testing.T) {
	fake := nasFake()
	fake["10.0.0.7"][oSynDiskStat+"0"] = vint(1)
	fake["10.0.0.7"][oSynRaidStat+"0"] = vint(1)
	w, pc := testWorker(fake)
	pc.refresh = true
	dev := DeviceConfig{Key: "nas1", Kind: "nas", IP: "10.0.0.7"}
	d, _ := w.pollNAS(pc, dev, nil)
	if d["status"] != "op" {
		t.Fatalf("status = %v", d["status"])
	}
	if n := len(d["meta"].(map[string]any)["alerts"].([]any)); n != 0 {
		t.Fatalf("alerts = %d", n)
	}
}

func TestPollNASDown(t *testing.T) {
	w, pc := testWorker(fakeSNMP{})
	dev := DeviceConfig{Key: "nas1", Kind: "nas", IP: "10.0.0.7"}
	prev := &nasStatic{Model: "kept"}
	d, st := w.pollNAS(pc, dev, prev)
	if d["status"] != "down" || st != prev {
		t.Fatalf("down = %v", d["status"])
	}
	m := d["meta"].(map[string]any)
	if v, ok := m["nas"]; !ok || v != nil {
		t.Fatalf("meta.nas must be nil, got %v ok=%v", v, ok)
	}
}

func TestPollNASShrinkGuard(t *testing.T) {
	fake := nasFake()
	w, pc := testWorker(fake)
	pc.refresh = true
	dev := DeviceConfig{Key: "nas1", Kind: "nas", IP: "10.0.0.7"}
	_, st := w.pollNAS(pc, dev, nil)
	// 재조회 쪼그라듦 → 기존 인벤토리 유지.
	delete(fake["10.0.0.7"], oSynDiskID+"0")
	delete(fake["10.0.0.7"], oSynRaidName+"0")
	pc.now += 60
	_, st = w.pollNAS(pc, dev, st)
	if len(st.Disks) != 1 || len(st.Raids) != 1 {
		t.Fatalf("shrink-guard disks=%d raids=%d", len(st.Disks), len(st.Raids))
	}
}
