package sshmetrics

import (
	"testing"
	"time"
)

// delta_test 의 기대값은 parse_test.go 의 sampleA/sampleB 픽스처에서 손계산했다.
// 두 샘플의 T= 차이는 10초다.

func TestCPUPctFromDelta(t *testing.T) {
	tests := []struct {
		name string
		cur  []int64
		prev []int64
		want *float64
	}{
		// d=[300,0,100,1400,100,0,100,0] total=2000 idle=1500 → 25.0
		{"정상 25%", []int64{1300, 0, 600, 81400, 2100, 0, 400, 0},
			[]int64{1000, 0, 500, 80000, 2000, 0, 300, 0}, fptr(25.0)},
		// idle 델타 0 → 100%
		{"풀 로드", []int64{1100, 0, 100, 1000, 0, 0, 0, 0},
			[]int64{1000, 0, 0, 1000, 0, 0, 0, 0}, fptr(100.0)},
		// total <= 0 → 계산 불가
		{"동일 샘플", []int64{1, 2, 3, 4, 5, 6, 7, 8},
			[]int64{1, 2, 3, 4, 5, 6, 7, 8}, nil},
		// 8필드 미만 → 계산 불가(Python len<8 가드)
		{"짧은 행", []int64{1, 2, 3}, []int64{1, 2, 3, 4, 5, 6, 7, 8}, nil},
		{"nil", nil, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cpuPctFromDelta(tt.cur, tt.prev)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			if got != nil && *got != *tt.want {
				t.Errorf("got %v, want %v", *got, *tt.want)
			}
		})
	}
}

func TestRate(t *testing.T) {
	if r := rate(1000, 0, 3); r == nil || *r != 333.3 {
		t.Errorf("rate = %v", r)
	}
	if r := rate(500, 1000, 10); r != nil {
		t.Errorf("카운터 리셋은 nil 이어야 한다: %v", *r)
	}
	if r := rate(1000, 0, 0); r != nil {
		t.Errorf("dt=0 은 nil 이어야 한다: %v", *r)
	}
}

func TestRoundN(t *testing.T) {
	// Python round() 와의 동치: 정확한 10진 반올림 + ties-to-even.
	tests := []struct {
		x    float64
		n    int
		want float64
	}{
		{0.125, 2, 0.12}, // 정확한 타이 → 짝수로
		{0.375, 2, 0.38}, // 정확한 타이 → 짝수로(올림)
		{2.675, 2, 2.67}, // 이진 표현이 2.67499... 이라 내림
		{14.7540983, 1, 14.8},
		{50.0, 1, 50.0},
	}
	for _, tt := range tests {
		if got := roundN(tt.x, tt.n); got != tt.want {
			t.Errorf("roundN(%v, %d) = %v, want %v", tt.x, tt.n, got, tt.want)
		}
	}
}

// TestApplyDeltas 는 두 샘플의 델타로 cpu%/bps/drop/busy% 를 검증한다.
func TestApplyDeltas(t *testing.T) {
	h := &hostState{}

	mA, sA := parseMetrics(sampleA, testNow)
	applyDeltasLocked(h, mA, sA)
	// 첫 샘플: 델타 기준이 없어 cpu_pct 는 nil, net/diskio 는 아예 없다.
	if mA.CPUPct != nil {
		t.Errorf("첫 샘플 cpu_pct = %v, want nil", *mA.CPUPct)
	}
	if mA.Net != nil || mA.DiskIO != nil || mA.InterconnectDrops != nil {
		t.Errorf("첫 샘플에 net/diskio 가 있으면 안 된다: %+v", mA)
	}
	h.prev = sA

	mB, sB := parseMetrics(sampleB, testNow)
	applyDeltasLocked(h, mB, sB)

	if got := fval(t, mB.CPUPct); got != 25.0 {
		t.Errorf("cpu_pct = %v, want 25.0", got)
	}

	if len(mB.Net) != 3 { // eth0, eth1, priv0 (lo 제외, 이름 정렬)
		t.Fatalf("net = %+v", mB.Net)
	}
	eth0 := mB.Net[0]
	if eth0.Name != "eth0" {
		t.Fatalf("net[0] = %+v", eth0)
	}
	// rx Δ80000B/10s = 8000.0 → ×8 = 64000.0 bps / tx Δ40000 → 32000.0 bps
	if got := fval(t, eth0.RxBps); got != 64000.0 {
		t.Errorf("eth0 rx_bps = %v", got)
	}
	if got := fval(t, eth0.TxBps); got != 32000.0 {
		t.Errorf("eth0 tx_bps = %v", got)
	}
	if got := ival(t, eth0.RxDropDelta); got != 2 {
		t.Errorf("eth0 rx_drop_delta = %d", got)
	}
	if got := ival(t, eth0.TxDropDelta); got != 0 {
		t.Errorf("eth0 tx_drop_delta = %d", got)
	}
	if got := ival(t, eth0.RxErrDelta); got != 1 {
		t.Errorf("eth0 rx_err_delta = %d", got)
	}
	if eth0.Interconnect || eth0.InterconnectEvidence != "spine-config" {
		t.Errorf("eth0 는 business 다: %+v", eth0)
	}
	// eth1 은 직전 샘플에 없었다 → 이름/플래그만 있고 비율은 nil.
	eth1 := mB.Net[1]
	if eth1.Name != "eth1" || eth1.RxBps != nil || eth1.RxDropDelta != nil {
		t.Errorf("새 NIC 는 비율이 nil 이어야 한다: %+v", eth1)
	}
	priv0 := mB.Net[2]
	if got := fval(t, priv0.RxBps); got != 0.0 {
		t.Errorf("priv0 rx_bps = %v", got)
	}
	if !priv0.Interconnect {
		t.Errorf("priv0 은 a-link 다: %+v", priv0)
	}
	// interconnect_drops = priv0 rx_drop Δ3 + tx_drop Δ1 = 4
	if got := ival(t, mB.InterconnectDrops); got != 4 {
		t.Errorf("interconnect_drops = %d, want 4", got)
	}

	if len(mB.DiskIO) != 2 {
		t.Fatalf("diskio = %+v", mB.DiskIO)
	}
	sda := mB.DiskIO[0]
	// read Δ512000sectors/10s = 51200.0 → ×512 = 26214400.0 B/s
	if got := fval(t, sda.ReadBps); got != 26214400.0 {
		t.Errorf("sda read_bps = %v", got)
	}
	if got := fval(t, sda.WriteBps); got != 13107200.0 {
		t.Errorf("sda write_bps = %v", got)
	}
	// busy = Δio_ms 500 / (10s×1000) ×100 = 5.0
	if got := fval(t, sda.BusyPct); got != 5.0 {
		t.Errorf("sda busy_pct = %v", got)
	}
	if got := fval(t, mB.DiskIO[1].BusyPct); got != 12.0 {
		t.Errorf("sdb busy_pct = %v", got)
	}
}

// TestApplyDeltasCounterReset 은 카운터가 뒤로 감긴 경우 음수가 아니라 0/nil 로
// 처리되는지 확인한다(NIC 교체·리부팅 직후).
func TestApplyDeltasCounterReset(t *testing.T) {
	const s1 = "T=100\n@net\n  eth0: 5000 10 0 5 0 0 0 0 6000 10 0 7 0 0 0 0\n"
	const s2 = "T=110\n@net\n  eth0: 100 1 0 2 0 0 0 0 200 1 0 3 0 0 0 0\n"
	h := &hostState{}
	m1, sa := parseMetrics(s1, testNow)
	applyDeltasLocked(h, m1, sa)
	h.prev = sa
	m2, sb := parseMetrics(s2, testNow)
	applyDeltasLocked(h, m2, sb)
	if len(m2.Net) != 1 {
		t.Fatalf("net = %+v", m2.Net)
	}
	n := m2.Net[0]
	if got := fval(t, n.RxBps); got != 0.0 {
		t.Errorf("리셋 후 rx_bps = %v, want 0", got)
	}
	if got := ival(t, n.RxDropDelta); got != 0 {
		t.Errorf("리셋 후 rx_drop_delta = %d, want 0", got)
	}
}

// TestRebootCheck 는 uptime 감소 → 리부트 판정과 24시간 표시 만료를 검증한다.
func TestRebootCheck(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	h := &hostState{}
	m := &Metrics{}

	// uptime 없음 → 아무 것도 하지 않는다.
	rebootCheckLocked(h, m, t0)
	if h.uptimeLast != nil || m.RecentlyBooted != nil {
		t.Fatalf("uptime 없는데 상태 변경: %+v", m)
	}

	// 첫 샘플: 기준만 저장. uptime 86425s → recently_booted=false.
	m = &Metrics{UptimeSecs: i64ptr(86425)}
	rebootCheckLocked(h, m, t0)
	if m.RebootedAt != nil || m.RecentlyBooted == nil || *m.RecentlyBooted {
		t.Errorf("첫 샘플: %+v", m)
	}

	// 120초 허용오차 안의 감소는 리부트가 아니다(수집 지연 흡수).
	m = &Metrics{UptimeSecs: i64ptr(86330)}
	rebootCheckLocked(h, m, t0.Add(time.Second))
	if m.RebootedAt != nil {
		t.Errorf("95초 감소는 리부트가 아니다: %+v", m)
	}

	// uptime 이 크게 뒤로 → 리부트. 시각은 감지한 폴 시각이다.
	t1 := t0.Add(10 * time.Second)
	m = &Metrics{UptimeSecs: i64ptr(500)}
	rebootCheckLocked(h, m, t1)
	if got := ival(t, m.RebootedAt); got != t1.Unix() {
		t.Errorf("rebooted_at = %d, want %d", got, t1.Unix())
	}
	if got := ival(t, m.RebootAgoSecs); got != 0 {
		t.Errorf("reboot_ago_secs = %d", got)
	}
	if !*m.RecentlyBooted {
		t.Errorf("uptime 500s 는 recently_booted=true")
	}

	// 1시간 뒤에도 24시간 내이므로 계속 표시(ago 만 증가). uptime 4200s 는 1시간을
	// 넘겼으므로 recently_booted=false 다.
	t2 := t1.Add(time.Hour)
	m = &Metrics{UptimeSecs: i64ptr(4200)}
	rebootCheckLocked(h, m, t2)
	if got := ival(t, m.RebootedAt); got != t1.Unix() {
		t.Errorf("rebooted_at 유지 = %d", got)
	}
	if got := ival(t, m.RebootAgoSecs); got != 3600 {
		t.Errorf("reboot_ago_secs = %d, want 3600", got)
	}
	if *m.RecentlyBooted {
		t.Errorf("uptime 4200s 는 recently_booted=false")
	}

	// 25시간 경과 → 만료되어 더 이상 표시하지 않는다.
	t3 := t1.Add(25 * time.Hour)
	m = &Metrics{UptimeSecs: i64ptr(95000)}
	rebootCheckLocked(h, m, t3)
	if m.RebootedAt != nil || m.RebootAgoSecs != nil {
		t.Errorf("24시간 지난 리부트는 만료: %+v", m)
	}
	if h.rebootAt != nil {
		t.Errorf("만료 후 rebootAt 상태도 지워져야 한다")
	}
	if *m.RecentlyBooted {
		t.Errorf("uptime 95000s 는 recently_booted=false")
	}
}
