package poller

import "testing"

func TestUsageThresholdsDefault(t *testing.T) {
	w, c := UsageThresholds()
	if w != 78 || c != 90 {
		t.Fatalf("기본값 78/90 이어야 함, got %v/%v", w, c)
	}
}

func TestSetThresholds(t *testing.T) {
	SetThresholds(80, 95)
	defer SetThresholds(78, 90) // 다른 테스트 오염 방지
	w, c := UsageThresholds()
	if w != 80 || c != 95 {
		t.Fatalf("got %v/%v", w, c)
	}
}
