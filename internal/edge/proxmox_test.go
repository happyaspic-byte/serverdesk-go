package edge

import (
	"testing"
)

func TestPVEMAC(t *testing.T) {
	e := map[string]any{"altnames": []any{"ens18", "enxD0000613273E"}}
	if got := pveMAC(e); got != "d0:00:06:13:27:3e" {
		t.Fatalf("mac = %q", got)
	}
	if got := pveMAC(map[string]any{"altnames": []any{"ens18"}}); got != "" {
		t.Fatalf("no enx = %q", got)
	}
	if got := pveMAC(map[string]any{"altnames": []any{"enxZZZZ613273e0"}}); got != "" {
		t.Fatalf("bad hex = %q", got)
	}
	if got := pveMAC(map[string]any{}); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestPVENet(t *testing.T) {
	rows := []any{
		map[string]any{"type": "bridge", "iface": "vmbr0", "active": 1.0,
			"cidr": "10.0.0.1/24", "gateway": "10.0.0.254", "bridge_ports": "eth0"},
		map[string]any{"type": "eth", "iface": "eth0", "active": true,
			"altnames": []any{"enxd0000613273e"}},
		map[string]any{"type": "alias", "iface": "eth0:1"}, // 필터 대상
		map[string]any{"type": "bond", "iface": "bond0", "active": false},
	}
	out := pveNet(rows)
	if len(out) != 3 {
		t.Fatalf("net len = %d", len(out))
	}
	if out[0]["kind"] != "eth" || out[1]["kind"] != "bond" || out[2]["kind"] != "bridge" {
		t.Fatalf("sort = %v %v %v", out[0]["kind"], out[1]["kind"], out[2]["kind"])
	}
	if out[0]["mac"] != "d0:00:06:13:27:3e" || out[0]["up"] != true {
		t.Fatalf("eth0 = %v", out[0])
	}
	if out[2]["ip"] != "10.0.0.1/24" || out[2]["gw"] != "10.0.0.254" || out[2]["ports"] != "eth0" {
		t.Fatalf("bridge = %v", out[2])
	}
}

func TestPVEDisks(t *testing.T) {
	rows := []any{
		map[string]any{"devpath": "/dev/sdb", "model": "Samsung_SSD_870", "serial": "S5XY",
			"size": 499999999999.9, "type": "ssd", "health": "PASSED", "wearout": 87.9, "rpm": 0.0},
		map[string]any{"devpath": "/dev/sda", "model": "WDC WD40", "serial": "WD-X",
			"size": 4e12, "type": "hdd", "health": "OK", "wearout": "N/A", "rpm": 7200.0},
	}
	out := pveDisks(rows)
	if len(out) != 2 || out[0]["dev"] != "sda" { // dev 이름 정렬
		t.Fatalf("disks = %v", out)
	}
	b := out[1]
	if b["model"] != "Samsung SSD 870" || b["sizeGB"] != int64(500) {
		t.Fatalf("sdb = %v", b)
	}
	if b["wearout"] != int64(87) { // int() 절단
		t.Fatalf("wearout = %v", b["wearout"])
	}
	if b["rpm"] != nil { // rpm 0 → nil
		t.Fatalf("rpm0 = %v", b["rpm"])
	}
	if out[0]["wearout"] != nil || out[0]["rpm"] != int64(7200) {
		t.Fatalf("sda = %v", out[0])
	}
}

func TestPVEStorage(t *testing.T) {
	rows := []any{
		map[string]any{"storage": "local-lvm", "type": "lvmthin", "active": 1.0,
			"total": 1099511627776.0, "used": 549755813888.0, "used_fraction": 0.5},
		map[string]any{"storage": "iso", "type": "dir", "active": 0.0}, // 비활성 제외
		map[string]any{"storage": "backup", "type": "dir", "active": true,
			"total": 1000.0, "used": 250.0}, // used_fraction 없음 → used/tot 폴fallback
	}
	out := pveStorage(rows)
	if len(out) != 2 || out[0]["name"] != "backup" { // 이름 정렬
		t.Fatalf("storage = %v", out)
	}
	if out[0]["pct"] != int64(25) {
		t.Fatalf("fallback pct = %v", out[0]["pct"])
	}
	lvm := out[1]
	if lvm["pct"] != int64(50) || lvm["totalGiB"] != 1024.0 || lvm["usedGiB"] != 512.0 {
		t.Fatalf("lvm = %v", lvm)
	}
}

func TestPVEVM(t *testing.T) {
	v := pveVM(map[string]any{
		"vmid": 100.0, "name": "web", "status": "running",
		"maxmem": 4294967296.0, "mem": 2147483648.0,
		"cpu": 0.5, "cpus": 4.0, "uptime": 86400.0, "maxdisk": 107374182400.0,
	}, "qemu", "node1")
	if v["name"] != "web" || v["kind"] != "qemu" || v["node"] != "node1" {
		t.Fatalf("vm = %v", v)
	}
	if v["memMiB"] != int64(4096) || v["cpuPct"] != int64(50) || v["memPct"] != int64(50) {
		t.Fatalf("vm metrics = %v", v)
	}
	if v["upDays"] != int64(1) || v["diskGiB"] != 100.0 {
		t.Fatalf("vm up/disk = %v", v)
	}
	// maxmem 0 → memPct nil; name 없음 → vmid 문자열.
	v2 := pveVM(map[string]any{"vmid": 200.0, "status": "stopped", "maxmem": 0.0}, "lxc", "node1")
	if v2["memPct"] != nil || v2["name"] != "200" {
		t.Fatalf("vm2 = %v", v2)
	}
}

func TestPVEHealth(t *testing.T) {
	// SMART 실패 → critical, 사유에 원문 health 유지.
	lvl, reasons := pveHealth(
		[]map[string]any{{"dev": "sda", "kind": "ssd", "health": "FAILED", "wearout": int64(42)}},
		[]map[string]any{{"name": "local", "pct": int64(95)}})
	if lvl != "critical" || len(reasons) != 2 || reasons[0] != "sda SMART FAILED" || reasons[1] != "local 95%" {
		t.Fatalf("crit = %q %v", lvl, reasons)
	}
	// usb SMART 는 무시, UNKNOWN 도 정상 취급.
	lvl, _ = pveHealth([]map[string]any{
		{"dev": "sdb", "kind": "usb", "health": "FAILED", "wearout": nil},
		{"dev": "nvme0", "kind": "nvme", "health": "UNKNOWN", "wearout": nil},
	}, nil)
	if lvl != "ok" {
		t.Fatalf("usb/unknown = %q", lvl)
	}
	// 수명 10% 경고 (SMART 분기가 우선이면 wearout 평가 안 함 — elif 관계).
	lvl, reasons = pveHealth([]map[string]any{
		{"dev": "sdc", "kind": "ssd", "health": "PASSED", "wearout": int64(10)},
	}, nil)
	if lvl != "warning" || reasons[0] != "sdc 수명 10%" {
		t.Fatalf("wearout = %q %v", lvl, reasons)
	}
	// 스토리지 89% 는 정상.
	lvl, _ = pveHealth(nil, []map[string]any{{"name": "x", "pct": int64(89)}})
	if lvl != "ok" {
		t.Fatalf("89%% = %q", lvl)
	}
}

func TestPVEMap(t *testing.T) {
	pc := &pollCtx{now: 1_700_000_000.0}
	dev := DeviceConfig{Key: "pve1", Kind: "proxmox", IP: "10.0.0.21"}
	st := &pveStatic{
		Node: "node1", Version: "8.1.3",
		Net:     []map[string]any{{"name": "eth0", "kind": "eth"}},
		Disks:   []map[string]any{{"dev": "sda", "kind": "ssd", "health": "FAILED", "wearout": int64(42)}},
		Storage: []map[string]any{{"name": "local-lvm", "pct": int64(95)}},
	}
	raw := pveRaw{
		NodeStatus: map[string]any{
			"cpu": 0.045, "uptime": 172800.0,
			"memory":  map[string]any{"total": 34359738368.0, "used": 17179869184.0},
			"loadavg": []any{"0.10", "0.20", "0.30"},
			"cpuinfo": map[string]any{"model": "Intel(R) Xeon(R) CPU E5-2673 v4",
				"sockets": 1.0, "cores": 4.0, "cpus": 8.0},
			"kversion":  "Linux 6.5.11-7-pve #1 SMP PREEMPT_DYNAMIC",
			"swap":      map[string]any{"total": 0.0, "used": 0.0},
			"rootfs":    map[string]any{"total": 107374182400.0, "used": 53687091200.0},
			"boot-info": map[string]any{"mode": "efi", "secureboot": true},
			"wait":      0.012,
		},
		Qemu: []any{map[string]any{"vmid": 100.0, "name": "web", "status": "running",
			"maxmem": 4294967296.0, "mem": 2147483648.0, "cpu": 0.5,
			"cpus": 4.0, "uptime": 86400.0, "maxdisk": 107374182400.0}},
		Lxc: []any{map[string]any{"vmid": 200.0, "status": "stopped", "maxmem": 0.0}},
	}
	d, err := pveMap(pc, dev, st, raw)
	if err != nil {
		t.Fatalf("pveMap: %v", err)
	}
	if d["status"] != "op" || d["type"] != "SRV" {
		t.Fatalf("status = %v", d["status"])
	}
	// banker's rounding: 4.5 → 4 (Python round 와 동일).
	if d["cpu0"] != int64(4) || d["mem0"] != int64(50) {
		t.Fatalf("cpu/mem = %v %v", d["cpu0"], d["mem0"])
	}
	if d["uptime"] != int64(2) {
		t.Fatalf("uptime = %v", d["uptime"])
	}
	m := d["meta"].(map[string]any)
	if m["version"] != "PVE 8.1.3" || m["platform"] != "proxmox" || m["vendor"] != "Proxmox" {
		t.Fatalf("meta = %v %v %v", m["version"], m["platform"], m["vendor"])
	}
	if m["vms"] != 2 || m["vmRunning"] != 1 {
		t.Fatalf("vms = %v/%v", m["vms"], m["vmRunning"])
	}
	node := m["nodes"].([]any)[0].(map[string]any)
	if node["cpu_pct"] != int64(4) || node["cpu_pct1"] != 4.5 || node["vmCount"] != 1 {
		t.Fatalf("node = %v", node)
	}
	if node["loadAvg"].([]float64)[1] != 0.20 {
		t.Fatalf("loadAvg = %v", node["loadAvg"])
	}
	if node["memGiB"] != 32.0 || node["cpuModel"] != "Intel Xeon CPU E5-2673 v4" {
		t.Fatalf("node mem/cpu = %v %v", node["memGiB"], node["cpuModel"])
	}
	srv := m["srv"].(map[string]any)
	if srv["kernel"] != "6.5.11-7-pve" || srv["boot"] != "UEFI · Secure Boot" {
		t.Fatalf("srv = %v %v", srv["kernel"], srv["boot"])
	}
	if srv["swapUsedPct"] != nil || srv["rootfsPct"] != int64(50) || srv["iowaitPct"] != 1.2 {
		t.Fatalf("srv pct = %v %v %v", srv["swapUsedPct"], srv["rootfsPct"], srv["iowaitPct"])
	}
	// 건강: SMART critical → DISK_SMART 경보가 맨 앞, 사유 join.
	if m["healthLevel"] != "critical" {
		t.Fatalf("health = %v", m["healthLevel"])
	}
	alerts := m["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["name"] != "DISK_SMART" {
		t.Fatalf("alerts = %v", alerts)
	}
	if alerts[0].(map[string]any)["desc"] != "sda SMART FAILED; local-lvm 95%" {
		t.Fatalf("desc = %v", alerts[0])
	}
	// loadavg 파싱 실패 → 매핑 에러(Python 의 ValueError → 워커 down 경로).
	raw.NodeStatus["loadavg"] = []any{"bogus"}
	if _, err := pveMap(pc, dev, st, raw); err == nil {
		t.Fatal("bad loadavg must error")
	}
}
