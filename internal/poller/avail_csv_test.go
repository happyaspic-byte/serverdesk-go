package poller

import "testing"

func TestCSVSnapshotAndSLA(t *testing.T) {
	tr := NewAvailTrackerWithRetention(t.TempDir(), 90, nil)
	today := kstDay(nowFloat())
	day30Ago := kstDay(nowFloat() - 30*86400)
	day100Ago := kstDay(nowFloat() - 100*86400)

	tr.state["dev-b"] = &availRec{Days: map[string][]float64{
		today:     {3600, 36}, // 99.0
		day30Ago:  {3600, 0},  // 100.0
		day100Ago: {3600, 0},  // 90일 보존 밖
	}}
	tr.state["dev-a"] = &availRec{Days: map[string][]float64{
		today: {7200, 0}, // 100.0
	}}
	tr.state["dev-thin"] = &availRec{Days: map[string][]float64{
		today: {60, 0}, // 관측 10분 미만
	}}

	rows90 := tr.CSVSnapshotDays(90)
	if len(rows90) != 3 {
		t.Fatalf("90일 행 수 — got %d (%+v)", len(rows90), rows90)
	}

	rows30 := tr.CSVSnapshotDays(1)
	if len(rows30) != 2 {
		t.Fatalf("1일 행 수 — got %d (%+v)", len(rows30), rows30)
	}

	sla := tr.SLASnapshot(90)
	if len(sla) != 2 {
		t.Fatalf("SLA 집계 장비 수 — got %d (%+v)", len(sla), sla)
	}
	if sla[0].Device != "dev-a" || sla[0].Avail != 100.0 || sla[0].ObservedDays != 1 {
		t.Errorf("dev-a SLA 불일치: %+v", sla[0])
	}
	if sla[1].Device != "dev-b" || sla[1].ObservedDays != 2 {
		t.Errorf("dev-b SLA 불일치: %+v", sla[1])
	}

	tr.pruneLocked()
	if _, ok := tr.state["dev-b"].Days[day100Ago]; ok {
		t.Errorf("90일 지난 날짜가 prune되지 않음: %+v", tr.state["dev-b"].Days)
	}
}
