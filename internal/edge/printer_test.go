package edge

import (
	"testing"
)

func TestPrtErrNames(t *testing.T) {
	cases := []struct {
		b0   byte
		want []string
	}{
		{0x80, []string{"Low paper"}},
		{0x40, []string{"No paper"}},
		{0xA0, []string{"Low paper", "Low toner"}},
		{0x01, []string{"Service requested"}},
		{0x00, []string{}},
		{0xFF, []string{"Low paper", "No paper", "Low toner", "No toner", "Door open", "Jammed", "Offline", "Service requested"}},
	}
	for _, c := range cases {
		got := prtErrNames(c.b0)
		if len(got) != len(c.want) {
			t.Fatalf("b0=%02X got %v want %v", c.b0, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("b0=%02X got %v want %v", c.b0, got, c.want)
			}
		}
	}
}

func TestPaperLatchStep(t *testing.T) {
	cases := []struct {
		name                     string
		prev, bit, tray, printed bool
		wantLatch, wantSynth     bool
	}{
		{"quiet", false, false, false, false, false, false},
		{"bit sets latch", false, true, false, false, true, false},
		{"latch holds over volatile bit", true, false, false, false, true, true},
		{"clear on tray paper", true, false, true, false, false, false},
		{"clear on successful print", true, false, false, true, false, false},
		{"bit wins over clear signals", true, true, true, true, true, false},
		{"fresh bit with paper still latches", false, true, true, false, true, false},
	}
	for _, c := range cases {
		latch, synth := paperLatchStep(c.prev, c.bit, c.tray, c.printed)
		if latch != c.wantLatch || synth != c.wantSynth {
			t.Fatalf("%s: got (%v,%v) want (%v,%v)", c.name, latch, synth, c.wantLatch, c.wantSynth)
		}
	}
}

func TestSupplyPct(t *testing.T) {
	if got := supplyPct(nil, i64p(253)); got != -2 {
		t.Fatalf("nil lvl = %d, want -2", got)
	}
	if got := supplyPct(i64p(-3), i64p(253)); got != -3 {
		t.Fatalf("-3 sentinel = %d", got)
	}
	if got := supplyPct(i64p(-2), i64p(253)); got != -2 {
		t.Fatalf("-2 sentinel = %d", got)
	}
	if got := supplyPct(i64p(50), i64p(253)); got != 20 {
		t.Fatalf("50/253 = %d, want 20", got)
	}
	// banker's rounding: 12.5 → 12 (Python round 와 동일).
	if got := supplyPct(i64p(1), i64p(8)); got != 12 {
		t.Fatalf("1/8 = %d, want 12", got)
	}
	if got := supplyPct(i64p(0), nil); got != -2 {
		t.Fatalf("nil max = %d, want -2", got)
	}
	if got := supplyPct(i64p(0), i64p(100)); got != 0 {
		t.Fatalf("0/100 = %d, want 0", got)
	}
}

func TestLowTonerName(t *testing.T) {
	if !lowTonerName("Black Toner", 10) {
		t.Fatal("10% toner must alert")
	}
	if lowTonerName("Black Toner", 11) {
		t.Fatal("11% must not alert")
	}
	if lowTonerName("Imaging Unit", 5) {
		t.Fatal("non-toner name must not alert")
	}
	if lowTonerName("Black Toner", -3) {
		t.Fatal("sentinel must not alert")
	}
}

func TestTraysOut(t *testing.T) {
	st := &printerStatic{Trays: []*trayInfo{
		{Name: "Tray 1", Max: i64p(253), Level: i64p(0)},
		{Name: "Tray 2", Max: i64p(50), Level: i64p(10)},
		{Name: "MP", Max: nil, Level: nil},
	}}
	out := traysOut(st)
	if out[0].(map[string]any)["level"] != int64(0) {
		t.Fatalf("unsuspect level = %v", out[0])
	}
	if out[2].(map[string]any)["level"] != nil {
		t.Fatalf("nil level must stay nil, got %v", out[2])
	}
	// 학습된 무의미 센서: 0/음수는 -2 로, 양수는 그대로, nil 은 nil.
	st.TrayLevelSuspect = true
	st.Trays[0].Level = i64p(-1)
	out = traysOut(st)
	if out[0].(map[string]any)["level"] != int64(-2) {
		t.Fatalf("suspect -1 = %v, want -2", out[0])
	}
	if out[1].(map[string]any)["level"] != int64(10) {
		t.Fatalf("suspect positive = %v, want 10", out[1])
	}
	if out[2].(map[string]any)["level"] != nil {
		t.Fatalf("suspect nil = %v, want nil", out[2])
	}
	// 원본(static)은 건드리지 않는다 — 래치 판정이 원본 잔량을 쓴다.
	if *st.Trays[0].Level != -1 {
		t.Fatal("static must not be mutated")
	}
}

func TestPrinterStatus(t *testing.T) {
	cases := []struct {
		dv       int64
		errors   []string
		lowToner bool
		want     string
	}{
		{2, nil, false, "op"},
		{3, nil, false, "deg"},
		{5, nil, false, "deg"},
		{2, []string{"Offline"}, false, "deg"},
		{2, []string{"Door open"}, false, "deg"},
		{2, nil, true, "deg"},
		{1, []string{}, false, "op"},
	}
	for _, c := range cases {
		if got := printerStatus(c.dv, c.errors, c.lowToner); got != c.want {
			t.Fatalf("dv=%d errors=%v lt=%v → %q, want %q", c.dv, c.errors, c.lowToner, got, c.want)
		}
	}
}

// printerFake — 기본 응답 고정구 생성(각 테스트가 변형).
func printerFake() fakeSNMP {
	return fakeSNMP{
		"10.0.0.9": {
			oSysUptime:         vticks(8640000), // 1일
			oPrtDevStatus:      vint(2),
			oPrtStatus:         vint(3),
			oPrtErrState:       vbytes([]byte{0x40}), // No paper
			oPrtPages:          vint(100),
			oPrtModel:          vstrv("SL-C565W"),
			oPrtSerial:         vstrv("CN12345"),
			oPrtSupDesc + "1":  vstrv("Black Toner"),
			oPrtSupMax + "1":   vint(1000),
			oPrtSupLvl + "1":   vint(500),
			oPrtTrayName + "1": vstrv("Tray 1"),
			oPrtTrayMax + "1":  vint(253),
			oPrtTrayLvl + "1":  vint(0),
		},
	}
}

func TestPollPrinterDown(t *testing.T) {
	w, pc := testWorker(fakeSNMP{})
	pc.refresh = true
	dev := DeviceConfig{Key: "prn1", Kind: "printer", IP: "10.0.0.9"}
	prev := &printerStatic{Model: "kept"}
	d, st := pollPrinterCall(w, pc, dev, prev)
	if d["status"] != "down" || d["type"] != "PRN" {
		t.Fatalf("down device = %v %v", d["status"], d["type"])
	}
	if st != prev {
		t.Fatal("static must be preserved on down")
	}
	m := d["meta"].(map[string]any)
	alerts := m["alerts"].([]any)
	if len(alerts) != 1 || alerts[0].(map[string]any)["name"] != "DEVICE_STATE" {
		t.Fatalf("alerts = %v", alerts)
	}
	if _, ok := m["printer"]; ok {
		t.Fatal("down path must not set meta.printer (Python parity)")
	}
}

func pollPrinterCall(w *Worker, pc *pollCtx, dev DeviceConfig, st *printerStatic) (map[string]any, *printerStatic) {
	return w.pollPrinter(pc, dev, st)
}

func TestPollPrinterLatchLifecycle(t *testing.T) {
	fake := printerFake()
	w, pc := testWorker(fake)
	dev := DeviceConfig{Key: "prn1", Kind: "printer", IP: "10.0.0.9"}

	// 라운드 1(refresh): No paper 비트 관측 → 래치 세트 + critical 경보.
	pc.refresh = true
	d1, st := w.pollPrinter(pc, dev, nil)
	m1 := d1["meta"].(map[string]any)
	if d1["status"] != "deg" {
		t.Fatalf("r1 status = %v", d1["status"])
	}
	if d1["uptime"] != int64(1) {
		t.Fatalf("r1 uptime = %v", d1["uptime"])
	}
	p1 := m1["printer"].(map[string]any)
	if p1["model"] != "SL-C565W" || p1["pages"] != int64(100) {
		t.Fatalf("r1 printer = %v %v", p1["model"], p1["pages"])
	}
	errs1 := p1["errors"].([]string)
	if len(errs1) != 1 || errs1[0] != "No paper" {
		t.Fatalf("r1 errors = %v", errs1)
	}
	sup := p1["supplies"].([]any)
	if sup[0].(map[string]any)["pct"] != int64(50) {
		t.Fatalf("r1 supply pct = %v", sup[0])
	}
	alerts1 := m1["alerts"].([]any)
	if alerts1[0].(map[string]any)["name"] != "DEVICE_STATE" ||
		alerts1[1].(map[string]any)["name"] != "PRINTER_ERROR" ||
		alerts1[1].(map[string]any)["severity"] != "critical" {
		t.Fatalf("r1 alerts = %v", alerts1)
	}
	t1 := alerts1[1].(map[string]any)["time"].(string)
	if !st.PaperLatch || *st.LastPages != 100 {
		t.Fatalf("r1 latch=%v pages=%v", st.PaperLatch, st.LastPages)
	}

	// 라운드 2(fast): 절전으로 비트 소실 → 래치 유지, 경보 최초시각 보존.
	fake["10.0.0.9"][oPrtErrState] = vbytes([]byte{0x00})
	pc.refresh = false
	pc.now += 60
	d2, st := w.pollPrinter(pc, dev, st)
	p2 := d2["meta"].(map[string]any)["printer"].(map[string]any)
	errs2 := p2["errors"].([]string)
	if len(errs2) != 1 || errs2[0] != "No paper" {
		t.Fatalf("r2 errors = %v (latch must survive volatile bits)", errs2)
	}
	t2 := d2["meta"].(map[string]any)["alerts"].([]any)[1].(map[string]any)["time"].(string)
	if t2 != t1 {
		t.Fatalf("alert time must persist: %q vs %q", t1, t2)
	}

	// 라운드 3: 인쇄 성공(페이지 증가) → 래치 해제 + 트레이 센서 학습.
	fake["10.0.0.9"][oPrtPages] = vint(101)
	pc.now += 60
	d3, st := w.pollPrinter(pc, dev, st)
	p3 := d3["meta"].(map[string]any)["printer"].(map[string]any)
	if len(p3["errors"].([]string)) != 0 {
		t.Fatalf("r3 errors = %v (latch must clear on print)", p3["errors"])
	}
	if st.PaperLatch {
		t.Fatal("r3 latch must be cleared")
	}
	if !st.TrayLevelSuspect {
		t.Fatal("r3 must learn trayLevelSuspect (printed with level 0)")
	}
	trays3 := p3["trays"].([]any)
	if trays3[0].(map[string]any)["level"] != int64(-2) {
		t.Fatalf("r3 tray level = %v, want -2 masked", trays3[0])
	}

	// 라운드 4: 양수 잔량 관측 → 학습 해제, 실제 잔량 표시.
	fake["10.0.0.9"][oPrtTrayLvl+"1"] = vint(50)
	pc.now += 60
	d4, st := w.pollPrinter(pc, dev, st)
	p4 := d4["meta"].(map[string]any)["printer"].(map[string]any)
	if st.TrayLevelSuspect {
		t.Fatal("r4 suspect must be cleared")
	}
	if p4["trays"].([]any)[0].(map[string]any)["level"] != int64(50) {
		t.Fatalf("r4 tray = %v", p4["trays"])
	}
	if d4["status"] != "op" {
		t.Fatalf("r4 status = %v", d4["status"])
	}
}

func TestPollPrinterLowTonerAlert(t *testing.T) {
	fake := printerFake()
	fake["10.0.0.9"][oPrtErrState] = vbytes([]byte{0x00})
	fake["10.0.0.9"][oPrtSupLvl+"1"] = vint(80) // 80/1000 = 8%
	w, pc := testWorker(fake)
	pc.refresh = true
	dev := DeviceConfig{Key: "prn1", Kind: "printer", IP: "10.0.0.9"}
	d, _ := w.pollPrinter(pc, dev, nil)
	m := d["meta"].(map[string]any)
	if d["status"] != "deg" {
		t.Fatalf("status = %v", d["status"])
	}
	alerts := m["alerts"].([]any)
	found := false
	for _, a := range alerts {
		if a.(map[string]any)["name"] == "TONER_LOW" {
			found = true
		}
	}
	if !found {
		t.Fatalf("alerts = %v, want TONER_LOW", alerts)
	}
}

func TestPollPrinterShrinkGuard(t *testing.T) {
	fake := printerFake()
	w, pc := testWorker(fake)
	dev := DeviceConfig{Key: "prn1", Kind: "printer", IP: "10.0.0.9"}
	pc.refresh = true
	_, st := w.pollPrinter(pc, dev, nil)
	if len(st.Sup) != 1 || len(st.Trays) != 1 {
		t.Fatalf("initial static sup=%d trays=%d", len(st.Sup), len(st.Trays))
	}
	// 재조회가 절전으로 쪼그라듦(소모품/트레이 무응답, 모델 무응답) → 기존 유지.
	m := fake["10.0.0.9"]
	delete(m, oPrtSupDesc+"1")
	delete(m, oPrtTrayName+"1")
	delete(m, oPrtModel)
	pc.now += 60
	_, st = w.pollPrinter(pc, dev, st)
	if len(st.Sup) != 1 || len(st.Trays) != 1 || st.Model != "SL-C565W" {
		t.Fatalf("shrink-guard: sup=%d trays=%d model=%q", len(st.Sup), len(st.Trays), st.Model)
	}
}
