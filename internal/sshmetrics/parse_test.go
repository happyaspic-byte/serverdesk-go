package sshmetrics

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// sampleA/sampleB 는 실제 노드 출력을 본뜬 두 개의 연속 샘플이다(T= 가 10초 차이).
// 델타 테스트의 기대값은 이 픽스처 숫자에서 손으로 계산했다.
const sampleA = `T=1700000000
@stat
cpu  1000 0 500 80000 2000 0 300 0 0 0
ctxt 9876543
procs_running 3
procs_blocked 1
@load
1.25 1.10 0.95 2/345 6789
@up
86425.50 70000.00
@mem
MemTotal:       16384000 kB
MemFree:         4096000 kB
MemAvailable:    8192000 kB
Buffers:          512000 kB
Cached:          2048000 kB
SwapTotal:       2097152 kB
SwapFree:        1048576 kB
Dirty:              4096 kB
@disk
/dev/mapper/vg-root 107374182400 53687091200 48318382080 53% /
/dev/sda1 104857600 52428800 52428800 50% /boot
@diskio
   8       0 sda 100000 0 5120000 60000 50000 0 2048000 40000 0 30000 70000
   8      16 sdb 200000 0 8192000 80000 70000 0 4096000 50000 0 40000 90000
@net
  eth0: 1000000000 8000000 2 10 0 0 0 0 2000000000 9000000 3 20 0 0 0 0
  priv0: 500000000 4000000 0 5 0 0 0 0 700000000 5000000 0 6 0 0 0 0
    lo: 123456 1000 0 0 0 0 0 0 123456 1000 0 0 0 0 0 0
@temp
coretemp:Core 0=42500
coretemp:Core 1=44000
@link
eth0|up|10000|phy|1500
priv0|up|10000|phy|9000
br0|up||virt|1500
vnet0|unknown||virt|1500
@spine
F|/etc/opt/ft/node-config-uuid|uuid-node-A
F|/etc/opt/ft/spine/networks/uuid-net-1/name|--- priv0
F|/etc/opt/ft/spine/networks/uuid-net-1/role|--- ALINK
F|/etc/opt/ft/spine/networks/uuid-net-1/ordinal|--- 0
F|/etc/opt/ft/spine/networks/uuid-net-1/mtu|--- 9000
F|/etc/opt/ft/spine/networks/net_82/name|--- net_82
F|/etc/opt/ft/spine/networks/net_82/role|--- BUSINESS
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-1/name|--- eth0
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-1/parent_uuid|--- net_82
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-2/name|--- priv0
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-2/parent_uuid|--- uuid-net-1
@md
md0 : active raid1 sdb1[1] sda1[0]
      104320 blocks [2/2] [UU]
@drbd
 1: cs:Connected ro:Primary/Secondary ds:UpToDate/UpToDate
@vm
vm-one
vm-two
@tz
+0900
KST
@plat
everrun
`

const sampleB = `T=1700000010
@stat
cpu  1300 0 600 81400 2100 0 400 0 0 0
ctxt 10000001
procs_running 1
procs_blocked 0
@load
0.50 0.90 1.00 1/300 7000
@up
86435.50 70010.00
@mem
MemTotal:       16384000 kB
MemFree:         4000000 kB
MemAvailable:    8000000 kB
Buffers:          512000 kB
Cached:          2048000 kB
SwapTotal:       2097152 kB
SwapFree:        1048576 kB
Dirty:               128 kB
@disk
/dev/mapper/vg-root 107374182400 53687091200 48318382080 53% /
/dev/sda1 104857600 52428800 52428800 50% /boot
@diskio
   8       0 sda 100500 0 5632000 65000 51000 0 2304000 45000 0 30500 75000
   8      16 sdb 200100 0 8200000 81000 70100 0 4100000 51000 0 41200 95000
@net
  eth0: 1000080000 8001000 3 12 0 0 0 0 2000040000 9001000 3 20 0 0 0 0
  eth1: 10 1 0 0 0 0 0 0 20 1 0 0 0 0 0 0
  priv0: 500000000 4000000 0 8 0 0 0 0 700000000 5000000 0 7 0 0 0 0
    lo: 123556 1001 0 0 0 0 0 0 123556 1001 0 0 0 0 0 0
@temp
coretemp:Core 0=43000
coretemp:Core 1=44500
@link
eth0|up|10000|phy|1500
priv0|up|10000|phy|9000
br0|up||virt|1500
vnet0|unknown||virt|1500
@spine
F|/etc/opt/ft/node-config-uuid|uuid-node-A
F|/etc/opt/ft/spine/networks/uuid-net-1/name|--- priv0
F|/etc/opt/ft/spine/networks/uuid-net-1/role|--- ALINK
F|/etc/opt/ft/spine/networks/uuid-net-1/ordinal|--- 0
F|/etc/opt/ft/spine/networks/uuid-net-1/mtu|--- 9000
F|/etc/opt/ft/spine/networks/net_82/name|--- net_82
F|/etc/opt/ft/spine/networks/net_82/role|--- BUSINESS
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-1/name|--- eth0
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-1/parent_uuid|--- net_82
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-2/name|--- priv0
F|/etc/opt/ft/spine/nodes/uuid-node-A/networks/uuid-nic-2/parent_uuid|--- uuid-net-1
@md
md0 : active raid1 sdb1[1] sda1[0]
      104320 blocks [2/2] [UU]
@tz
+0900
KST
@plat
everrun
`

var testNow = time.Unix(1700000100, 0)

func fval(t *testing.T, p *float64) float64 {
	t.Helper()
	if p == nil {
		t.Fatal("unexpected nil *float64")
	}
	return *p
}

func ival(t *testing.T, p *int64) int64 {
	t.Helper()
	if p == nil {
		t.Fatal("unexpected nil *int64")
	}
	return *p
}

func sval(t *testing.T, p *string) string {
	t.Helper()
	if p == nil {
		t.Fatal("unexpected nil *string")
	}
	return *p
}

// TestMetricsScriptMatchesGolden 은 원격 스크립트가 Python 원본과 바이트 단위로
// 같은지 묶는다. 골든은 poller.py 의 METRICS_SCRIPT 를 AST 로 추출해 만들었다.
func TestMetricsScriptMatchesGolden(t *testing.T) {
	golden, err := os.ReadFile("testdata/metrics_script.golden")
	if err != nil {
		t.Fatal(err)
	}
	if MetricsScript != string(golden) {
		t.Fatalf("MetricsScript 가 골든과 다르다 (got %d bytes, want %d bytes)",
			len(MetricsScript), len(golden))
	}
}

func TestParseMetricsFull(t *testing.T) {
	m, s := parseMetrics(sampleA, testNow)

	if m.TS != 1700000000 {
		t.Errorf("ts = %d", m.TS)
	}
	wantSections := []string{"disk", "diskio", "drbd", "link", "load", "md", "mem",
		"net", "plat", "spine", "stat", "temp", "tz", "up", "vm"}
	if !reflect.DeepEqual(m.RawSections, wantSections) {
		t.Errorf("raw_sections = %v", m.RawSections)
	}
	if got := ival(t, m.Ctxt); got != 9876543 {
		t.Errorf("ctxt = %d", got)
	}
	if got := ival(t, m.ProcsRunning); got != 3 {
		t.Errorf("procs_running = %d", got)
	}
	if got := ival(t, m.ProcsBlocked); got != 1 {
		t.Errorf("procs_blocked = %d", got)
	}
	if !reflect.DeepEqual(m.Load, []float64{1.25, 1.1, 0.95}) {
		t.Errorf("load = %v", m.Load)
	}
	if got := ival(t, m.UptimeSecs); got != 86425 {
		t.Errorf("uptime_secs = %d", got)
	}
	if got := fval(t, m.UptimeDays); got != 1.0 {
		t.Errorf("uptime_days = %v", got)
	}

	// mem: kB → 바이트. used = total - available, pct 는 소수 1자리.
	if got := ival(t, m.MemTotalBytes); got != 16384000*1024 {
		t.Errorf("mem_total_bytes = %d", got)
	}
	if got := ival(t, m.MemAvailableBytes); got != 8192000*1024 {
		t.Errorf("mem_available_bytes = %d", got)
	}
	if got := ival(t, m.MemUsedBytes); got != 8192000*1024 {
		t.Errorf("mem_used_bytes = %d", got)
	}
	if got := fval(t, m.MemPct); got != 50.0 {
		t.Errorf("mem_pct = %v", got)
	}
	if got := ival(t, m.SwapTotalBytes); got != 2097152*1024 {
		t.Errorf("swap_total_bytes = %d", got)
	}
	if got := ival(t, m.SwapUsedBytes); got != 1048576*1024 {
		t.Errorf("swap_used_bytes = %d", got)
	}
	if got := fval(t, m.SwapPct); got != 50.0 {
		t.Errorf("swap_pct = %v", got)
	}
	if got := ival(t, m.DirtyBytes); got != 4096*1024 {
		t.Errorf("dirty_bytes = %d", got)
	}

	if len(m.Filesystems) != 2 {
		t.Fatalf("filesystems = %v", m.Filesystems)
	}
	d0 := m.Filesystems[0]
	if d0.Device != "/dev/mapper/vg-root" || d0.Mount != "/" ||
		d0.SizeBytes != 107374182400 || d0.UsedBytes != 53687091200 ||
		d0.AvailBytes != 48318382080 || ival(t, d0.UsedPct) != 53 {
		t.Errorf("disk0 = %+v", d0)
	}
	if got := ival(t, m.FSMaxPct); got != 53 {
		t.Errorf("fs_max_pct = %d", got)
	}

	// 원시 누적값은 rawSample 에만 있다.
	if len(s.cpuJiffies) != 10 || s.cpuJiffies[0] != 1000 || s.cpuJiffies[3] != 80000 {
		t.Errorf("cpuJiffies = %v", s.cpuJiffies)
	}
	if len(s.netRaw) != 2 { // lo 제외
		t.Fatalf("netRaw = %v", s.netRaw)
	}
	eth0 := s.netRaw["eth0"]
	if eth0.rxBytes != 1000000000 || eth0.txBytes != 2000000000 || eth0.rxDrop != 10 {
		t.Errorf("eth0 = %+v", eth0)
	}
	// spine role 로 판정: eth0 은 business, priv0 은 a-link.
	if eth0.interconnect || eth0.interconnectEvidence != "spine-config" {
		t.Errorf("eth0 interconnect = %+v", eth0)
	}
	if p := s.netRaw["priv0"]; !p.interconnect || p.interconnectEvidence != "spine-config" {
		t.Errorf("priv0 interconnect = %+v", p)
	}
	if len(s.diskRaw) != 2 || s.diskRaw["sda"].readSectors != 5120000 ||
		s.diskRaw["sda"].ioMS != 30000 {
		t.Errorf("diskRaw = %+v", s.diskRaw)
	}

	if len(m.Temps) != 2 || m.Temps[0].Celsius != 42.5 || m.Temps[1].Celsius != 44.0 {
		t.Errorf("temps = %+v", m.Temps)
	}
	if got := fval(t, m.TempMaxC); got != 44.0 {
		t.Errorf("temp_max_c = %v", got)
	}

	if len(m.Links) != 4 {
		t.Fatalf("links = %+v", m.Links)
	}
	l0 := m.Links[0]
	if l0.Name != "eth0" || sval(t, l0.State) != "up" || !l0.Up ||
		ival(t, l0.SpeedMbps) != 10000 || l0.Physical == nil || !*l0.Physical ||
		ival(t, l0.MTU) != 1500 || l0.GuestTap {
		t.Errorf("link0 = %+v", l0)
	}
	// br0: speed 빈 칸 → nil, virt 마커 → physical=false
	br0 := m.Links[2]
	if br0.SpeedMbps != nil || br0.Physical == nil || *br0.Physical || ival(t, br0.MTU) != 1500 {
		t.Errorf("br0 = %+v", br0)
	}
	// vnet0: operstate unknown 은 가상 인터페이스의 정상값이라 Up=false 로만 둔다.
	vn := m.Links[3]
	if vn.Up || !vn.GuestTap || sval(t, vn.State) != "unknown" {
		t.Errorf("vnet0 = %+v", vn)
	}

	sp := m.Spine
	if sp == nil {
		t.Fatal("spine nil")
	}
	if sval(t, sp.SelfUUID) != "uuid-node-A" {
		t.Errorf("self_uuid = %v", *sp.SelfUUID)
	}
	if len(sp.Networks) != 2 || sp.Networks[0].Name != "net_82" || sp.Networks[1].Name != "priv0" {
		t.Fatalf("networks = %+v", sp.Networks)
	}
	if sval(t, sp.Networks[0].Role) != "business" {
		t.Errorf("net_82 role = %v", *sp.Networks[0].Role)
	}
	priv := sp.Networks[1]
	if sval(t, priv.Role) != "a-link" || ival(t, priv.Ordinal) != 0 || ival(t, priv.MTU) != 9000 {
		t.Errorf("priv0 net = %+v", priv)
	}
	if sval(t, sp.NICNetworks["eth0"]) != "net_82" || sval(t, sp.NICRoles["eth0"]) != "business" {
		t.Errorf("eth0 mapping = %v/%v", sp.NICNetworks["eth0"], sp.NICRoles["eth0"])
	}
	if sval(t, sp.NICNetworks["priv0"]) != "priv0" || sval(t, sp.NICRoles["priv0"]) != "a-link" {
		t.Errorf("priv0 mapping = %v/%v", sp.NICNetworks["priv0"], sp.NICRoles["priv0"])
	}

	if len(m.MDStat) != 2 || *m.MDDegraded {
		t.Errorf("md = %v degraded=%v", m.MDStat, *m.MDDegraded)
	}
	if len(m.DRBD) != 1 || !*m.DRBDUpToDate {
		t.Errorf("drbd = %v uptodate=%v", m.DRBD, *m.DRBDUpToDate)
	}
	if got := ival(t, m.TZOffsetSecs); got != 9*3600 {
		t.Errorf("tz_offset_secs = %d", got)
	}
	if m.TZName != "KST" {
		t.Errorf("tz_name = %q", m.TZName)
	}
	if !reflect.DeepEqual(m.RunningDomains, []string{"vm-one", "vm-two"}) {
		t.Errorf("vms = %v", m.RunningDomains)
	}
	if m.OSPlatform != "everrun" {
		t.Errorf("os_platform = %q", m.OSPlatform)
	}
	// 델타는 parse 단계에서 계산하지 않는다.
	if m.CPUPct != nil || m.Net != nil || m.DiskIO != nil {
		t.Errorf("파싱 단계에서 델타가 채워짐: cpu=%v net=%v diskio=%v", m.CPUPct, m.Net, m.DiskIO)
	}
}

func TestSplitSectionsEdge(t *testing.T) {
	// T= 는 첫 섹션 전에만 타임스탬프다. 섹션 안의 T= 는 데이터다.
	// 같은 섹션이 두 번 나오면 마지막 것이 이긴다(Python sections[cur]=[] 재할당).
	raw := "T=100\r\n@a\nT=999\nfirst\n@a\nsecond\n@b\r\nx\r\n"
	sections, ts := splitSections(raw)
	if ts != 100 {
		t.Errorf("ts = %d", ts)
	}
	if got := sections["a"]; !reflect.DeepEqual(got, []string{"second"}) {
		t.Errorf("section a = %v", got)
	}
	if got := sections["b"]; !reflect.DeepEqual(got, []string{"x"}) {
		t.Errorf("section b = %v", got)
	}

	// T= 가 깨졌거나 없으면 폴 시각으로 대체한다.
	m, s := parseMetrics("garbage only\n", testNow)
	if m.TS != testNow.Unix() || s.ts != testNow.Unix() {
		t.Errorf("ts fallback = %d", m.TS)
	}
	if len(m.RawSections) != 0 {
		t.Errorf("raw_sections = %v", m.RawSections)
	}
}

func TestParseStatBareLine(t *testing.T) {
	// 값 없는 "ctxt" 단독 행이 와도 패닉 없이 건너뛴다.
	m, _ := parseMetrics("T=1\n@stat\nctxt\nprocs_running\n", testNow)
	if m.Ctxt != nil || m.ProcsRunning != nil {
		t.Errorf("값 없는 stat 행이 파싱됐다: %+v", m)
	}
}

func TestParseMemFallback(t *testing.T) {
	// MemAvailable 없는 구형 커널 → MemFree 로 대체. SwapTotal 없으면 swap 필드 없음.
	m, _ := parseMetrics("T=1\n@mem\nMemTotal: 1000 kB\nMemFree: 250 kB\n", testNow)
	if got := ival(t, m.MemAvailableBytes); got != 250*1024 {
		t.Errorf("mem_available_bytes = %d", got)
	}
	if got := fval(t, m.MemPct); got != 75.0 {
		t.Errorf("mem_pct = %v", got)
	}
	if m.SwapTotalBytes != nil || m.SwapPct != nil || m.DirtyBytes != nil {
		t.Errorf("swap/dirty 가 없어야 한다: %+v", m)
	}
	// MemTotal 이 없으면 mem 필드 전체가 없다.
	m2, _ := parseMetrics("T=1\n@mem\nMemFree: 250 kB\n", testNow)
	if m2.MemPct != nil || m2.MemTotalBytes != nil {
		t.Errorf("MemTotal 없는데 mem 필드 존재: %+v", m2)
	}
}

func TestParseDiskSection(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int // 파싱되는 디스크 수
		pct   *int64
	}{
		{"정상", []string{"/dev/sda1 1000 500 400 56% /"}, 1, i64ptr(56)},
		{"필드 부족", []string{"/dev/sda1 1000 500"}, 0, nil},
		{"숫자 깨짐", []string{"/dev/sda1 abc 500 400 56% /"}, 0, nil},
		{"pct 깨짐", []string{"/dev/sda1 1000 500 400 - /"}, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDiskSection(tt.lines)
			if len(got) != tt.want {
				t.Fatalf("disks = %+v", got)
			}
			if tt.want == 1 {
				if (got[0].UsedPct == nil) != (tt.pct == nil) {
					t.Errorf("pct = %v, want %v", got[0].UsedPct, tt.pct)
				} else if got[0].UsedPct != nil && *got[0].UsedPct != *tt.pct {
					t.Errorf("pct = %d, want %d", *got[0].UsedPct, *tt.pct)
				}
			}
		})
	}
}

func TestParseTempSection(t *testing.T) {
	temps := parseTempSection([]string{
		"coretemp:Core 0=42500",
		"noColon=33000",      // 콜론 없으면 chip=label
		"bad=notanumber",     // 값이 깨지면 버린다
		"no-equals-here",     // '=' 없으면 버린다
		"lm85:temp=in=36500", // label 에 '=' 가 들어가면 마지막 '=' 기준
	})
	if len(temps) != 3 {
		t.Fatalf("temps = %+v", temps)
	}
	if temps[0].Chip != "coretemp" || temps[0].Label != "Core 0" || temps[0].Celsius != 42.5 {
		t.Errorf("temps[0] = %+v", temps[0])
	}
	if temps[1].Chip != "noColon" || temps[1].Label != "noColon" || temps[1].Celsius != 33.0 {
		t.Errorf("temps[1] = %+v", temps[1])
	}
	if temps[2].Chip != "lm85" || temps[2].Label != "temp=in" || temps[2].Celsius != 36.5 {
		t.Errorf("temps[2] = %+v", temps[2])
	}
}

func TestParseLinkSection(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		wantNil  bool // 이 행이 버려지는가
		speed    *int64
		physical *bool
		up       bool
		mtu      *int64
	}{
		{"정상 물리", "eth0|up|10000|phy|1500", false, i64ptr(10000), bptr(true), true, i64ptr(1500)},
		{"캐리어 없음 speed=-1", "eth1|down|-1|phy|1500", false, nil, bptr(true), false, i64ptr(1500)},
		{"speed 빈 칸(밀림 방지)", "br0|up||virt|1500", false, nil, bptr(false), true, i64ptr(1500)},
		{"마커 없음(구형)", "eth2|up|1000||1500", false, i64ptr(1000), nil, true, i64ptr(1500)},
		{"공백 구분 레거시", "eth3 up 1000", false, i64ptr(1000), nil, true, nil},
		{"빈 이름", "|up||virt|1500", true, nil, nil, false, nil},
		{"빈 행", "   ", true, nil, nil, false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLinkSection([]string{tt.line})
			if tt.wantNil {
				if len(got) != 0 {
					t.Fatalf("버려져야 하는데 파싱됨: %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("파싱 실패: %q", tt.line)
			}
			l := got[0]
			if (l.SpeedMbps == nil) != (tt.speed == nil) ||
				(l.SpeedMbps != nil && *l.SpeedMbps != *tt.speed) {
				t.Errorf("speed = %v, want %v", l.SpeedMbps, tt.speed)
			}
			if (l.Physical == nil) != (tt.physical == nil) ||
				(l.Physical != nil && *l.Physical != *tt.physical) {
				t.Errorf("physical = %v, want %v", l.Physical, tt.physical)
			}
			if l.Up != tt.up {
				t.Errorf("up = %v, want %v", l.Up, tt.up)
			}
			if (l.MTU == nil) != (tt.mtu == nil) || (l.MTU != nil && *l.MTU != *tt.mtu) {
				t.Errorf("mtu = %v, want %v", l.MTU, tt.mtu)
			}
		})
	}
}

// TestParseNetSectionInterconnect 는 A-Link 판정 우선순위를 묶는다:
// spine role 이 있으면 그것이 이기고, 없거나(매핑 자체가 없거나/role 이 nil)이면
// 이름 휴리스틱(priv*/alink*)으로 내린다.
func TestParseNetSectionInterconnect(t *testing.T) {
	line := func(name string) string {
		return "  " + name + ": 1 2 3 4 0 0 0 0 5 6 7 8 0 0 0 0"
	}
	// spine 없음 → 이름 휴리스틱
	net := parseNetSection([]string{line("priv0"), line("alink1"), line("eth0"), line("net_82")}, nil)
	for _, tc := range []struct {
		name string
		ic   bool
	}{
		{"priv0", true}, {"alink1", true}, {"eth0", false}, {"net_82", false},
	} {
		c := net[tc.name]
		if c.interconnect != tc.ic || c.interconnectEvidence != "name-heuristic" {
			t.Errorf("%s: ic=%v ev=%s", tc.name, c.interconnect, c.interconnectEvidence)
		}
	}
	// spine role 이 이름과 반대여도 spine 이 이긴다(net_82 가 A-Link 인 실장비 케이스).
	alink, business := "a-link", "business"
	roles := map[string]*string{"net_82": &alink, "priv0": &business, "orphan": nil}
	net = parseNetSection([]string{line("net_82"), line("priv0"), line("orphan")}, roles)
	if c := net["net_82"]; !c.interconnect || c.interconnectEvidence != "spine-config" {
		t.Errorf("net_82 spine a-link: %+v", c)
	}
	if c := net["priv0"]; c.interconnect || c.interconnectEvidence != "spine-config" {
		t.Errorf("priv0 spine business(이름과 반대): %+v", c)
	}
	// 매핑은 있지만 role nil(부모 네트워크 미상) → 휴리스틱 폐기 아니고 평소대로 평가.
	if c := net["orphan"]; c.interconnect || c.interconnectEvidence != "name-heuristic" {
		t.Errorf("orphan: %+v", c)
	}
}

func TestParseSpine(t *testing.T) {
	t.Run("uuid/name 디렉터리 중복 정의는 이름으로 통합", func(t *testing.T) {
		sp := parseSpine([]string{
			"F|/etc/opt/ft/spine/networks/4f2c-uuid/name|--- biz0",
			"F|/etc/opt/ft/spine/networks/4f2c-uuid/role|--- BUSINESS",
			"F|/etc/opt/ft/spine/networks/biz0/name|--- biz0",
			"F|/etc/opt/ft/spine/networks/biz0/role|--- BUSINESS",
		})
		if sp == nil || len(sp.Networks) != 1 {
			t.Fatalf("networks = %+v", sp)
		}
		if sp.Networks[0].Name != "biz0" {
			t.Errorf("networks = %+v", sp.Networks)
		}
	})

	t.Run("로컬 노드 우선 + 첫 항목 승리", func(t *testing.T) {
		sp := parseSpine([]string{
			"F|/etc/opt/ft/node-config-uuid|self",
			"F|/etc/opt/ft/spine/networks/n1/name|--- net1",
			"F|/etc/opt/ft/spine/networks/n1/role|--- BUSINESS",
			"F|/etc/opt/ft/spine/networks/n2/name|--- net2",
			"F|/etc/opt/ft/spine/networks/n2/role|--- ALINK",
			// 로컬 노드: eth0 → n1
			"F|/etc/opt/ft/spine/nodes/self/networks/x1/name|--- eth0",
			"F|/etc/opt/ft/spine/nodes/self/networks/x1/parent_uuid|--- n1",
			// 같은 노드 안에서 같은 ifn 이 두 번이면 첫 항목이 이긴다.
			"F|/etc/opt/ft/spine/nodes/self/networks/x2/name|--- eth0",
			"F|/etc/opt/ft/spine/nodes/self/networks/x2/parent_uuid|--- n2",
			// 다른 노드의 매핑은 로컬 것이 있으면 아예 보지 않는다(break).
			"F|/etc/opt/ft/spine/nodes/other/networks/y1/name|--- eth9",
			"F|/etc/opt/ft/spine/nodes/other/networks/y1/parent_uuid|--- n2",
		})
		if sp == nil {
			t.Fatal("nil")
		}
		if got := sval(t, sp.NICNetworks["eth0"]); got != "net1" {
			t.Errorf("eth0 → %s, want net1", got)
		}
		if _, ok := sp.NICNetworks["eth9"]; ok {
			t.Errorf("로컬 노드가 매핑을 만들었으면 다른 노드는 보지 않는다: %v", sp.NICNetworks)
		}
	})

	t.Run("로컬 uuid 가 노드 목록에 없으면 정렬 순 폐백", func(t *testing.T) {
		sp := parseSpine([]string{
			"F|/etc/opt/ft/node-config-uuid|absent",
			"F|/etc/opt/ft/spine/networks/n1/name|--- net1",
			"F|/etc/opt/ft/spine/networks/n1/role|--- BUSINESS",
			"F|/etc/opt/ft/spine/nodes/bbb/networks/x1/name|--- eth0",
			"F|/etc/opt/ft/spine/nodes/bbb/networks/x1/parent_uuid|--- n1",
		})
		if sp == nil {
			t.Fatal("nil")
		}
		if got := sval(t, sp.NICNetworks["eth0"]); got != "net1" {
			t.Errorf("eth0 → %s, want net1", got)
		}
		if got := sval(t, sp.SelfUUID); got != "absent" {
			t.Errorf("self_uuid = %s", got)
		}
	})

	t.Run("parent_uuid 미상 → nil 값", func(t *testing.T) {
		sp := parseSpine([]string{
			"F|/etc/opt/ft/node-config-uuid|self",
			"F|/etc/opt/ft/spine/nodes/self/networks/x1/name|--- eth0",
			"F|/etc/opt/ft/spine/nodes/self/networks/x1/parent_uuid|--- ghost",
		})
		if sp == nil {
			t.Fatal("nil") // nic 매핑만 있어도 spine 은 유효하다
		}
		v, ok := sp.NICNetworks["eth0"]
		if !ok || v != nil {
			t.Errorf("NICNetworks[eth0] = %v(%v), want nil 값", v, ok)
		}
		if len(sp.Networks) != 0 {
			t.Errorf("networks = %+v", sp.Networks)
		}
	})

	t.Run("따옴표/역할 매핑", func(t *testing.T) {
		sp := parseSpine([]string{
			"F|/etc/opt/ft/spine/networks/n1/name|--- 'biz0'",
			"F|/etc/opt/ft/spine/networks/n1/role|--- MANAGEMENT",
			"F|/etc/opt/ft/spine/networks/n2/name|--- custom0",
			"F|/etc/opt/ft/spine/networks/n2/role|--- WeirdRole",
			"F|/etc/opt/ft/spine/networks/n3/name|--- norole0",
			"F|/etc/opt/ft/spine/networks/n3/bridge_name|--- br9",
		})
		if sp == nil || len(sp.Networks) != 3 {
			t.Fatalf("spine = %+v", sp)
		}
		// 이름 정렬: biz0, custom0, norole0
		if sp.Networks[0].Name != "biz0" || sval(t, sp.Networks[0].Role) != "business" {
			t.Errorf("biz0 = %+v", sp.Networks[0])
		}
		if got := sval(t, sp.Networks[1].Role); got != "weirdrole" {
			t.Errorf("미지의 role 은 소문자 통과: %s", got)
		}
		if sp.Networks[2].Role != nil {
			t.Errorf("role 없음 → nil: %+v", sp.Networks[2].Role)
		}
		if got := sval(t, sp.Networks[2].Bridge); got != "br9" {
			t.Errorf("bridge = %s", got)
		}
	})

	t.Run("빈 입력 → nil", func(t *testing.T) {
		if sp := parseSpine(nil); sp != nil {
			t.Errorf("빈 입력인데 %+v", sp)
		}
		if sp := parseSpine([]string{"not-a-spine-line", "F|short"}); sp != nil {
			t.Errorf("쓰레기 입력인데 %+v", sp)
		}
	})
}

// TestJSONContract 는 프런트/DB 가 보는 JSON 키 계약을 묶는다.
// cpu_pct 는 첫 샘플에서 반드시 null 로 존재하고, 없는 섹션은 키 자체가 빠진다.
func TestJSONContract(t *testing.T) {
	m, _ := parseMetrics(sampleA, testNow)
	m.IP = "10.0.0.1"
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"cpu_pct":null`, `"ts":1700000000`, `"ip":"10.0.0.1"`,
		`"mem_pct":50`, `"temp_max_c":44`, `"tz_offset_secs":32400`, `"tz_name":"KST"`,
		`"os_platform":"everrun"`, `"fs_max_pct":53`,
		`"nic_networks":{"eth0":"net_82","priv0":"priv0"}`,
		`"nic_roles":{"eth0":"business","priv0":"a-link"}`,
		`"running_domains":["vm-one","vm-two"]`, `"md_degraded":false`, `"drbd_uptodate":true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("JSON 에 %s 가 없다: %s", want, s)
		}
	}
	// 키 부재는 ": 값" 형태로 확인한다(raw_sections 안의 섹션 이름과 혼동 방지).
	for _, absent := range []string{`"net":[`, `"diskio":[`, `"interconnect_drops":`, `"rebooted_at":`, `"recently_booted":`} {
		if strings.Contains(s, absent) {
			t.Errorf("JSON 에 %s 가 있으면 안 된다(첫 샘플): %s", absent, s)
		}
	}
}
