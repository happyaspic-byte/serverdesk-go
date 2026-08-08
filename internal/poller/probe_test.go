package poller

import (
	"context"
	"net"
	"testing"
	"time"
)

// 로컬 리스너로 ok/slow/down 세 경로를 검증한다(외부 네트워크 불필요).
func TestProbeTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// ok — 열린 포트
	st := ProbeTCP(context.Background(), "127.0.0.1", port, time.Second)
	if st.State != "ok" || st.Ms <= 0 {
		t.Fatalf("열린 포트: %+v, ok+ms>0 기대", st)
	}

	// ok(ECONNREFUSED 환산) — 닫힌 포트지만 호스트 응답
	ln2, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := ln2.Addr().(*net.TCPAddr).Port
	ln2.Close()
	st = ProbeTCP(context.Background(), "127.0.0.1", closedPort, time.Second)
	if st.State != "ok" {
		t.Fatalf("닫힌 포트: %+v, refused→ok 환산 기대", st)
	}

	// down — 무응답(라우팅 불가 대역, 짧은 타임아웃)
	st = ProbeTCP(context.Background(), "192.0.2.254", 443, 300*time.Millisecond)
	if st.State != "down" {
		t.Fatalf("무응답: %+v, down 기대", st)
	}

	// slow — 임계 판정 단위 로직
	if reachOkSlow(SlowThresholdMs+1) != "slow" || reachOkSlow(1.0) != "ok" {
		t.Fatal("slow 임계 판정 오류")
	}
}
