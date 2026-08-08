package snmp

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"
)

// Stats 는 수신기 누적 카운터다(파이썬 TrapReceiver.stats + 발신 IP 별 집계).
// dropped_badcommunity 는 community 불일치 폐기 — 필터를 켰을 때 잡음 발신을
// 보는 지표다. Sources 는 발신 IP 별 수신 건수다.
type Stats struct {
	Received            uint64            // UDP 데이터그램 수신 수
	Parsed              uint64            // BER/PDU 디코드 성공 수
	Delivered           uint64            // onTrap 전달 성공 수
	DroppedBadCommunity uint64            // community 불일치 폐기 수
	Errors              uint64            // 파싱 실패 + onTrap 예외 수
	Sources             map[string]uint64 // 발신 IP 별 Received
}

// TrapReceiver 는 UDP 트랩 리스너다(파이썬 TrapReceiver 스레드 포팅).
// datagram → decode → community 필터 → store 적립 → onTrap 콜백.
// 트랩은 '상태'가 아니라 '이벤트'다 — 수신기는 헬스 판정에 관여하지 않고
// 이벤트 피드로만 흘려보낸다.
type TrapReceiver struct {
	bind      string
	port      int
	community string // "" 이면 모든 community 허용 (파이썬 community=None)
	decoder   *Decoder
	store     *TrapStore
	onTrap    func(Trap)

	mu       sync.Mutex // stats 보호
	stats    Stats
	conn     *net.UDPConn
	done     chan struct{}
	closeOne sync.Once
	started  bool
}

// NewTrapReceiver 는 트랩 수신기를 만든다.
//
//   - bind/port: UDP 바인드 주소. udp/162 는 특권 포트라 권한 없는 환경에서는
//     10162 같은 높은 포트를 쓴다(파이썬 기본과 동일한 운영 제약).
//   - community: "" 이면 모든 community 허용, 값이 있으면 일치하는 트랩만 받는다.
//   - mibDir: Stratus MIB 디렉터리. "" 이면 표준 이름만 해석한다.
//   - store: nil 이면 파일 영속 없이 콜백만 호출한다.
//   - onTrap: 정규화된 트랩 콜백. 수신 고루틴에서 호출되므로 블로킹은 피하고,
//     패닉이 나도 수신 루프는 계속된다.
func NewTrapReceiver(bind string, port int, community string, mibDir string, store *TrapStore, onTrap func(Trap)) *TrapReceiver {
	return &TrapReceiver{
		bind:      bind,
		port:      port,
		community: community,
		decoder:   NewDecoderFromDir(mibDir),
		store:     store,
		onTrap:    onTrap,
		done:      make(chan struct{}),
		stats:     Stats{Sources: map[string]uint64{}},
	}
}

// Start 는 소켓을 바인드하고 수신 고루틴을 시작한다. 바인드 실패는 즉시 오류로
// 돌린다(파이썬은 로그 후 비활성 — Go 는 호출자가 재시도/포기를 선택하게 한다).
// ctx 가 끝나거나 Close 되면 수신을 멈추고 소켓을 닫는다.
func (r *TrapReceiver) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return errors.New("snmp: 트랩 수신기는 이미 시작됨")
	}
	r.started = true
	r.mu.Unlock()

	lc := net.ListenConfig{
		// SO_REUSEADDR 는 best-effort — 플랫폼별 구현은 reuseaddr_*.go(Windows 는 no-op).
		Control: reuseaddrControl,
	}
	pc, err := lc.ListenPacket(ctx, "udp", net.JoinHostPort(r.bind, strconv.Itoa(r.port)))
	if err != nil {
		return err
	}
	conn, ok := pc.(*net.UDPConn)
	if !ok {
		pc.Close()
		return errors.New("snmp: UDP 리스너 타입 오류")
	}
	r.mu.Lock()
	r.conn = conn
	r.mu.Unlock()
	go r.loop(ctx)
	return nil
}

func (r *TrapReceiver) loop(ctx context.Context) {
	defer close(r.done)
	for {
		if ctx.Err() != nil {
			return
		}
		_ = r.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)) // ctx 폴 주기
		buf := make([]byte, 65535)
		n, addr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // 타임아웃 — ctx 확인 후 계속
			}
			return // 소켓 닫힘(Close) 또는 치명 오류
		}
		src := addr.IP.String()
		r.bump(func(s *Stats) {
			s.Received++
			s.Sources[src]++
		})
		trap, ok := r.decoder.Decode(buf[:n], src)
		if !ok {
			r.bump(func(s *Stats) { s.Errors++ })
			continue
		}
		r.bump(func(s *Stats) { s.Parsed++ })
		if r.community != "" && trap.Community != r.community {
			r.bump(func(s *Stats) { s.DroppedBadCommunity++ })
			continue
		}
		if r.store != nil {
			r.store.Add(trap)
		}
		if r.onTrap != nil {
			func() {
				defer func() {
					if recover() != nil { // 콜백 패닉이 수신 루프를 죽이면 안 된다
						r.bump(func(s *Stats) { s.Errors++ })
					}
				}()
				r.onTrap(trap)
				r.bump(func(s *Stats) { s.Delivered++ })
			}()
		}
	}
}

func (r *TrapReceiver) bump(f func(*Stats)) {
	r.mu.Lock()
	f(&r.stats)
	r.mu.Unlock()
}

// Close 는 수신을 멈추고 소켓을 닫는다. Start 전이거나 이미 닫혔으면 무해하다.
func (r *TrapReceiver) Close() {
	r.closeOne.Do(func() {
		r.mu.Lock()
		conn := r.conn
		r.mu.Unlock()
		if conn != nil {
			conn.Close() // ReadFromUDP 를 깨워 루프 종료
		}
	})
	r.mu.Lock()
	started := r.started
	r.mu.Unlock()
	if started {
		<-r.done
	}
}

// Stats 는 누적 카운터의 스냅숏을 돌려준다(Sources 맵은 복사본).
func (r *TrapReceiver) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.stats
	out.Sources = make(map[string]uint64, len(r.stats.Sources))
	for k, v := range r.stats.Sources {
		out.Sources[k] = v
	}
	return out
}
