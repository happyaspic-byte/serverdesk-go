package snmp

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"net"
	"time"
)

// SnmpWorker 가 한 패킷에 묶어 조회하는 표준 OID 집합(파이썬 poller.py 의
// SNMP_OIDS). everRun 은 MIB view 제약으로 sysUpTime/sysName 만 응답하고
// 나머지는 KindNull 로 돌아온다 — 호출자는 이를 오류가 아니라 '미지원'으로
// 다뤄야 한다. ztC Edge 는 CPU/MEM 까지 응답한다.
const (
	OIDSysUpTime = "1.3.6.1.2.1.1.3.0"         // sysUpTime.0
	OIDSysName   = "1.3.6.1.2.1.1.5.0"         // sysName.0
	OIDCPUIdle   = "1.3.6.1.4.1.2021.11.11.0"  // ssCpuIdle.0 (UCD-SNMP)
	OIDMemTotal  = "1.3.6.1.4.1.2021.4.5.0"    // memTotalReal.0
	OIDMemAvail  = "1.3.6.1.4.1.2021.4.6.0"    // memAvailReal.0
	OIDLoad1     = "1.3.6.1.4.1.2021.10.1.3.1" // laLoad.1
)

// DefaultOIDs 는 생존 확인 폭백용 기본 조회 집합이다(파이썬 SNMP_OIDS 순서 유지).
var DefaultOIDs = []string{
	OIDSysUpTime, OIDSysName, OIDCPUIdle, OIDMemTotal, OIDMemAvail, OIDLoad1,
}

// buildGetRequest 는 여러 OID 를 하나의 GetRequest-PDU 로 묶는다
// (파이썬 snmp_get 의 요청 생성부와 동일). 왕복 1회로 끝내는 것이 핵심이다 —
// 폐쇄망 장비의 SNMP 에이전트는 느리고, OID 당 1왕복씩 하면 폴 주기를 초과한다.
func buildGetRequest(reqID int64, community string, oids []string) ([]byte, error) {
	var vbs []byte
	for _, o := range oids {
		oidTLV, err := encodeOID(o)
		if err != nil {
			return nil, err
		}
		vbs = append(vbs, tlv(0x30, append(oidTLV, tlv(tagNull, nil)...))...)
	}
	pdu := tlv(pduGet, concat(
		berInt(reqID), berInt(0), berInt(0), tlv(0x30, vbs)))
	return tlv(0x30, concat(
		berInt(1), // version: v2c
		tlv(tagOctets, []byte(community)),
		pdu,
	)), nil
}

func concat(parts ...[]byte) []byte {
	var n int
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// newReqID 는 [1, 2^30-1] 범위의 암호학적 난수 요청 ID를 생성한다.
func newReqID() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<30-1))
	if err != nil {
		return 1
	}
	return n.Int64() + 1
}

// Get은 단일 UDP GET으로 여러 OID를 묶어 조회한다. timeout은 호출 전체 상한.
//
// 반환 맵은 요청한 OID 중 에이전트가 응답한 것만 담는다. everRun 의 MIB view
// 제약처럼 일부 OID 만 KindNull 로 오는 경우는 오류가 아니다. ctx 가 먼저
// 끝나면 ctx.Err() 를 돌려준다.
func Get(ctx context.Context, ip string, port int, community string, oids []string, timeout time.Duration) (map[string]Value, error) {
	if len(oids) == 0 {
		return map[string]Value{}, nil
	}
	if timeout <= 0 {
		timeout = 3 * time.Second // 파이썬 snmp_get 의 기본값과 동일
	}
	raddr := &net.UDPAddr{IP: net.ParseIP(ip), Port: port}
	if raddr.IP == nil {
		return nil, fmt.Errorf("snmp: 잘못된 IP %q", ip)
	}

	overallDeadline := time.Now().Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(overallDeadline) {
		overallDeadline = d
	}

	const maxAttempts = 3 // 최초 1회 + 최대 2회 재전송
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		now := time.Now()
		if !now.Before(overallDeadline) {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, fmt.Errorf("snmp: %s:%d 응답 대기 실패: timeout", ip, port)
		}

		remainingTotal := overallDeadline.Sub(now)
		// 각 시도별 타임아웃: 지수 백오프(예: 남은 시도 횟수로 균등/점증 배분)
		attemptTimeout := remainingTotal / time.Duration(maxAttempts-attempt)
		if attemptTimeout < 50*time.Millisecond {
			attemptTimeout = remainingTotal
		}
		attemptDeadline := now.Add(attemptTimeout)
		if attemptDeadline.After(overallDeadline) {
			attemptDeadline = overallDeadline
		}

		reqID := newReqID()
		pkt, err := buildGetRequest(reqID, community, oids)
		if err != nil {
			return nil, err
		}

		conn, err := net.ListenUDP("udp", nil)
		if err != nil {
			return nil, fmt.Errorf("snmp: UDP 소켓 생성 실패: %w", err)
		}

		_ = conn.SetDeadline(attemptDeadline)
		stop := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				conn.Close()
			case <-stop:
			}
		}()

		_, writeErr := conn.WriteToUDP(pkt, raddr)
		if writeErr != nil {
			close(stop)
			conn.Close()
			return nil, fmt.Errorf("snmp: %s:%d 전송 실패: %w", ip, port, writeErr)
		}

		buf := make([]byte, 65535)
		n, _, readErr := conn.ReadFromUDP(buf)
		close(stop)
		conn.Close()

		if readErr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			lastErr = fmt.Errorf("snmp: %s:%d 응답 대기 실패: %w", ip, port, readErr)
			// UDP 패킷 유실 대비 1~2회 타임아웃 백오프 재전송 루프
			continue
		}

		return parseGetResponse(buf[:n])
	}

	return nil, lastErr
}

// parseGetResponse 는 GetResponse-PDU 를 {oid: Value} 맵으로 디코드한다
// (파이썬 snmp_get 의 응답 파싱부와 동일 구조).
func parseGetResponse(data []byte) (map[string]Value, error) {
	top := &reader{b: data}
	_, seq, err := top.read()
	if err != nil {
		return nil, err
	}
	r := &reader{b: seq}
	if _, _, err = r.read(); err != nil { // version
		return nil, err
	}
	if _, _, err = r.read(); err != nil { // community
		return nil, err
	}
	_, pduB, err := r.read()
	if err != nil {
		return nil, err
	}
	p := &reader{b: pduB}
	if _, _, err = p.read(); err != nil { // request-id
		return nil, err
	}
	_, errB, err := p.read() // error-status
	if err != nil {
		return nil, err
	}
	_, idxB, err := p.read() // error-index
	if err != nil {
		return nil, err
	}
	_, vbl, err := p.read()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Value)
	vr := &reader{b: vbl}
	for vr.more() {
		_, vb, err := vr.read()
		if err != nil {
			return nil, err
		}
		one := &reader{b: vb}
		_, oidB, err := one.mustRead()
		if err != nil {
			return nil, err
		}
		vt, vv, err := one.mustRead()
		if err != nil {
			return nil, err
		}
		oid, err := decodeOID(oidB)
		if err != nil {
			return nil, err
		}
		out[oid] = decGetValue(vt, vv)
	}
	// 에이전트가 error-status 를 올렸으면 부분 결과와 함께 오류를 돌린다 —
	// 조용히 빈 값을 삼키면 장비 오류(noSuchName 등)를 네트워크 타임아웃과
	// 구분할 수 없어 운영 디버깅이 어려워진다.
	if es := decUint(errB); es != 0 {
		return out, fmt.Errorf("snmp: 에이전트 오류 status=%d index=%d", es, decUint(idxB))
	}
	return out, nil
}
