package poller

import "testing"

// CSVSnapshot — 30일 창 필터·정렬·가용성 계산.
func TestCSVSnapshot(t *testing.T) {
	tr := &AvailTracker{state: map[string]*availRec{}}
	today := kstDay(nowFloat())
	old := kstDay(nowFloat() - 40*86400) // 35일 prune 창 밖 → 제외
	tr.state["dev-b"] = &availRec{Days: map[string][]float64{
		today: {3600, 36}, // 1% 다운 → 99.0
		old:   {3600, 0},
	}}
	tr.state["dev-a"] = &availRec{Days: map[string][]float64{
		today: {7200, 0}, // 100
	}}
	tr.state["dev-thin"] = &availRec{Days: map[string][]float64{
		today: {60, 0}, // 관측 10분 미만 → 제외
	}}
	rows := tr.CSVSnapshot()
	if len(rows) != 2 {
		t.Fatalf("행 수 — got %d (%+v)", len(rows), rows)
	}
	if rows[0].Device != "dev-a" || rows[1].Device != "dev-b" {
		t.Errorf("장비 id 정렬: %+v", rows)
	}
	if rows[1].Avail != 99.0 {
		t.Errorf("가용성 계산 100*(1-36/3600)=99.0 — got %v", rows[1].Avail)
	}
	if rows[0].Avail != 100 {
		t.Errorf("무다운 100 — got %v", rows[0].Avail)
	}
}
