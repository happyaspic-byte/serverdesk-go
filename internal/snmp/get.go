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
		for {
			n, src, readErr := conn.ReadFromUDP(buf)
			if readErr != nil {
				close(stop)
				conn.Close()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				lastErr = fmt.Errorf("snmp: %s:%d 응답 대기 실패: %w", ip, port, readErr)
				break
			}
			if src.Port != raddr.Port || !src.IP.Equal(raddr.IP) {
				continue
			}
			resp, parseErr := decodeGetResponse(buf[:n])
			if parseErr != nil || resp.id != reqID || resp.version != 1 || resp.community != community {
				continue
			}
			matched := true
			for oid := range resp.values {
				found := false
				for _, requested := range oids {
					if oid == requested {
						found = true
						break
					}
				}
				if !found {
					matched = false
					break
				}
			}
			if !matched {
				continue
			}
			close(stop)
			conn.Close()
			return resp.values, resp.agentError()
		}
	}

	return nil, lastErr
}

// parseGetResponse 는 GetResponse-PDU 를 {oid: Value} 맵으로 디코드한다
// (파이썬 snmp_get 의 응답 파싱부와 동일 구조).
func parseGetResponse(data []byte) (map[string]Value, error) {
	r, err := decodeGetResponse(data)
	if err != nil {
		return nil, err
	}
	return r.values, r.agentError()
}

type getResponse struct {
	version, id, status, index int64
	community                  string
	values                     map[string]Value
}

func (r getResponse) agentError() error {
	if r.status != 0 {
		return fmt.Errorf("snmp: 에이전트 오류 status=%d index=%d", r.status, r.index)
	}
	return nil
}

func decodeGetResponse(data []byte) (out getResponse, err error) {
	top := &reader{b: data}
	seq, err := top.readTag(0x30)
	if err != nil {
		return out, err
	}
	if top.more() {
		return out, fmt.Errorf("snmp: trailing message data")
	}
	r := &reader{b: seq}
	b, err := r.readTag(tagInt)
	if err != nil {
		return out, err
	}
	out.version, err = decInt(b)
	if err != nil || (out.version != 0 && out.version != 1) {
		return out, fmt.Errorf("snmp: invalid version")
	}
	b, err = r.readTag(tagOctets)
	if err != nil {
		return out, err
	}
	out.community = string(b)
	b, err = r.readTag(pduResponse)
	if err != nil {
		return out, err
	}
	if r.more() {
		return out, fmt.Errorf("snmp: trailing envelope data")
	}
	p := &reader{b: b}
	for _, n := range []*int64{&out.id, &out.status, &out.index} {
		b, err = p.readTag(tagInt)
		if err != nil {
			return out, err
		}
		*n, err = decInt(b)
		if err != nil {
			return out, err
		}
	}
	if out.status < 0 || out.status > 18 || out.index < 0 {
		return out, fmt.Errorf("snmp: invalid error fields")
	}
	b, err = p.readTag(0x30)
	if err != nil {
		return out, err
	}
	if p.more() {
		return out, fmt.Errorf("snmp: trailing PDU data")
	}
	out.values = make(map[string]Value)
	vr := &reader{b: b}
	for vr.more() {
		vb, err := vr.readTag(0x30)
		if err != nil {
			return out, err
		}
		one := &reader{b: vb}
		oidB, err := one.readTag(tagOID)
		if err != nil {
			return out, err
		}
		oid, err := decodeOID(oidB)
		if err != nil {
			return out, err
		}
		vt, vv, err := one.read()
		if err != nil {
			return out, err
		}
		if one.more() {
			return out, fmt.Errorf("snmp: trailing varbind data")
		}
		if err := validateValue(vt, vv); err != nil {
			return out, err
		}
		if _, duplicate := out.values[oid]; duplicate {
			return out, fmt.Errorf("snmp: duplicate OID")
		}
		out.values[oid] = decGetValue(vt, vv)
	}
	return out, nil
}
