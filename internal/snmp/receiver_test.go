package snmp

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// freeUDPPort — 테스트용 빈 UDP 포트를 고른다(잠깐 열었다 닫는다).
func freeUDPPort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func sendTrap(t *testing.T, port int, pkt []byte) {
	t.Helper()
	c, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write(pkt); err != nil {
		t.Fatal(err)
	}
}

// TestReceiverEndToEnd — 바인드→수신→디코드→저장→콜백 전체 경로.
func TestReceiverEndToEnd(t *testing.T) {
	port := freeUDPPort(t)
	store := NewTrapStore(filepath.Join(t.TempDir(), "traps.jsonl"), 10)
	got := make(chan Trap, 1)
	rx := NewTrapReceiver("127.0.0.1", port, "", testMIBDir, store, func(tr Trap) {
		got <- tr
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rx.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rx.Close()

	pkt, err := BuildV2cTrap("public", "1.3.6.1.4.1.458.115.2.0.3",
		[]V2cVarbind{{OID: "1.3.6.1.4.1.458.115.3.1", Kind: "str", Value: "Node Unreachable Trap."}},
		555)
	if err != nil {
		t.Fatal(err)
	}
	sendTrap(t, port, pkt)

	select {
	case tr := <-got:
		if tr.Name != "everRunNodeUnreachableTrap" || tr.Sev != "critical" {
			t.Errorf("name/sev = %q/%q", tr.Name, tr.Sev)
		}
		if tr.Src != "127.0.0.1" {
			t.Errorf("src = %q", tr.Src)
		}
		if tr.Desc != "Node Unreachable Trap." {
			t.Errorf("desc = %q", tr.Desc)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onTrap 미호출")
	}

	// 저장소에도 실렸는지
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(store.Snapshot()) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(store.Load()) != 1 {
		t.Errorf("저장소 건수 = %d, want 1", len(store.Load()))
	}

	// onTrap 전달과 Delivered 카운터 증가는 같은 고루틴에서 순차 실행되므로,
	// 채널 수신 직후에는 카운터 증가가 아직 관찰되지 않을 수 있다.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		st := rx.Stats()
		if st.Received == 1 && st.Parsed == 1 && st.Delivered == 1 && st.Errors == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := rx.Stats()
	if st.Received != 1 || st.Parsed != 1 || st.Delivered != 1 || st.Errors != 0 {
		t.Errorf("stats = %+v", st)
	}
	if st.Sources["127.0.0.1"] != 1 {
		t.Errorf("발신 IP 집계 = %+v", st.Sources)
	}
	rx.Close()
	rx.Close() // 이중 Close 무해 검증
}

// TestReceiverCommunityFilter — community 불일치는 폐기하고 카운터만 올린다.
func TestReceiverCommunityFilter(t *testing.T) {
	port := freeUDPPort(t)
	got := make(chan Trap, 1)
	rx := NewTrapReceiver("127.0.0.1", port, "secret", testMIBDir, nil, func(tr Trap) {
		got <- tr
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rx.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rx.Close()

	bad, _ := BuildV2cTrap("public", "1.3.6.1.4.1.458.115.2.0.1", nil, 1)
	good, _ := BuildV2cTrap("secret", "1.3.6.1.4.1.458.115.2.0.1", nil, 1)
	sendTrap(t, port, bad)
	sendTrap(t, port, good)

	select {
	case tr := <-got:
		if tr.Community != "secret" {
			t.Errorf("필터 통과 트랩 community = %q", tr.Community)
		}
		if tr.Name != "everRunGenericTrap" {
			t.Errorf("name = %q", tr.Name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("필터 통과 트랩 미수신")
	}
	deadline := time.Now().Add(time.Second)
	for {
		st := rx.Stats()
		if st.DroppedBadCommunity == 1 && st.Delivered == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats = %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestReceiverCorruptPacket — 쓰레기 데이터그램은 Errors 로만 집계.
func TestReceiverCorruptPacket(t *testing.T) {
	port := freeUDPPort(t)
	rx := NewTrapReceiver("127.0.0.1", port, "", testMIBDir, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := rx.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer rx.Close()

	sendTrap(t, port, []byte("garbage-not-snmp"))
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if rx.Stats().Errors == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("stats = %+v — 쓰레기 패킷이 Errors 로 집계되지 않음", rx.Stats())
}

// TestReceiverContextStop — ctx 종료가 수신 루프를 멈춘다.
func TestReceiverContextStop(t *testing.T) {
	port := freeUDPPort(t)
	rx := NewTrapReceiver("127.0.0.1", port, "", "", nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	if err := rx.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan struct{})
	go func() { rx.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 취소 후 Close 가 막힘")
	}
}

func TestReceiverBindError(t *testing.T) {
	// 점유된 포트에 바인드하면 오류를 돌려야 한다(조용한 비활성 대신).
	blk, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer blk.Close()
	port := blk.LocalAddr().(*net.UDPAddr).Port
	// SO_REUSEADDR 환경에서도 동일 주소:포트 이중 바인드는 거부된다.
	rx := NewTrapReceiver("127.0.0.1", port, "", "", nil, nil)
	if err := rx.Start(context.Background()); err == nil {
		rx.Close()
		t.Skip("커널이 이중 바인드를 허용함 — 스킵")
	}
}
