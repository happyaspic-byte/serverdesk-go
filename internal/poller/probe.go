package poller

// TCP 도달성 프로버 — Endurance 서브시스템(BMC/Standby OS/MGMT UI/Windows)의
// 생존 검사용. ICMP ping 이 아니라 서비스 포트 접속을 쓴다:
//   - ping 은 raw 소켓에 root/CAP_NET_RAW 가 필요하고 현장 방화벽에 막히는 사례가 있음
//   - 우리가 알고 싶은 건 "호스트가 살았나"가 아니라 "그 서비스가 응답하나"다
//   - stdlib net.DialTimeout 만으로 동작(무의존성 계약 유지), RTT 로 '느림' 표현 가능
//
// 기본 포트 맵(설치 절차서·관리 경로 기준): BMC 443(Redfish/웹), Standby OS 22(SSH),
// MGMT UI 443(Endurance Console), Windows 445(또는 RDP 3389).
// 등록된 Endurance 장비에 대해서만 meta.endurance.reach 실측값을 만든다.

import (
	"context"
	"errors"
	"net"
	"strconv"
	"syscall"
	"time"
)

// ReachState 는 박스 하나의 도달 상태다.
type ReachState struct {
	State string  `json:"state"` // "ok" | "slow" | "down"
	Ms    float64 `json:"ms"`    // 왕복 시간(ms). down 이면 0
}

// ReachPort 는 박스 종류별 기본 서비스 포트다.
var ReachPort = map[string]int{
	"bmc":     443, // BMC 웹/Redfish
	"standby": 22,  // Standby Ubuntu SSH
	"mgmt":    443, // Endurance Console
	"windows": 445, // Windows SMB(RDP 3389 대안)
}

// SlowThresholdMs — 이 응답 시간을 넘으면 ok 가 아니라 slow 로 표시한다.
const SlowThresholdMs = 800.0

// ProbeTCP 는 ip:port 로 TCP 접속을 시도해 상태와 RTT 를 돌려준다.
// ECONNREFUSED(포트 닫힘)는 down 이 아니라 ok 로 환산한다 — RST 가 왔다는 건
// 호스트가 살아 있다는 뜻이고, 포트 열림 여부는 서비스 설정 차이일 수 있기 때문이다.
func ProbeTCP(ctx context.Context, ip string, port int, timeout time.Duration) ReachState {
	if timeout <= 0 {
		timeout = 1500 * time.Millisecond
	}
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
	ms := round1(float64(time.Since(start).Microseconds()) / 1000.0)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) {
			return ReachState{State: reachOkSlow(ms), Ms: ms}
		}
		return ReachState{State: "down", Ms: 0}
	}
	_ = conn.Close()
	return ReachState{State: reachOkSlow(ms), Ms: ms}
}

func reachOkSlow(ms float64) string {
	if ms > SlowThresholdMs {
		return "slow"
	}
	return "ok"
}
