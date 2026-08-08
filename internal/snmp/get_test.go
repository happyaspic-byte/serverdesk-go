package snmp

import (
	"context"
	"net"
	"testing"
	"time"
)

// fakeAgent 는 한 번만 응답하는 인-프로세스 UDP SNMP 에이전트다.
// Get 이 만든 요청을 실제 BER 로 파싱해(request-id 추출) 정상 응답을 돌려준다 —
// 인코더와 디코더를 둘 다 거치는 종단 검증이 된다.
type fakeAgent struct {
	conn *net.UDPConn
	addr string
}

func startFakeAgent(t *testing.T, values map[string]Value) *fakeAgent {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	fa := &fakeAgent{conn: conn, addr: conn.LocalAddr().String()}
	go func() {
		buf := make([]byte, 65535)
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		resp, err := buildGetResponse(buf[:n], values)
		if err != nil {
			return
		}
		conn.WriteToUDP(resp, src)
	}()
	return fa
}

func (fa *fakeAgent) close() { fa.conn.Close() }

func (fa *fakeAgent) port() int {
	return fa.conn.LocalAddr().(*net.UDPAddr).Port
}

// buildGetResponse 는 GetRequest 를 파싱해 같은 request-id 의 GetResponse 를 만든다.
// values 에 없는 OID 는 noSuchInstance(0x81) 로 돌린다 — everRun 의 MIB view
// 제약 응답을 흉낸다.
func buildGetResponse(req []byte, values map[string]Value) ([]byte, error) {
	top := &reader{b: req}
	_, seq, err := top.read()
	if err != nil {
		return nil, err
	}
	r := &reader{b: seq}
	_, _, err = r.read() // version
	if err != nil {
		return nil, err
	}
	_, commB, err := r.read() // community
	if err != nil {
		return nil, err
	}
	_, pduB, err := r.read()
	if err != nil {
		return nil, err
	}
	p := &reader{b: pduB}
	_, reqIDB, err := p.read()
	if err != nil {
		return nil, err
	}
	if _, _, err = p.read(); err != nil { // error-status
		return nil, err
	}
	if _, _, err = p.read(); err != nil { // error-index
		return nil, err
	}
	_, vbl, err := p.read()
	if err != nil {
		return nil, err
	}
	var vbs []byte
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
		oid, err := decodeOID(oidB)
		if err != nil {
			return nil, err
		}
		var valTLV []byte
		if v, ok := values[oid]; ok {
			switch v.Kind {
			case KindInt:
				valTLV = berInt(v.Int)
			case KindTimeticks:
				valTLV = tlv(tagTimeTicks, berUintRaw(uint64(v.Int)))
			case KindString:
				valTLV = tlv(tagOctets, []byte(v.Str))
			default:
				valTLV = tlv(tagNull, nil)
			}
		} else {
			valTLV = []byte{0x81, 0x00} // noSuchInstance
		}
		vbs = append(vbs, tlv(0x30, concat(tlv(tagOID, oidB), valTLV))...)
	}
	pdu := tlv(pduResponse, concat(
		tlv(tagInt, reqIDB), berInt(0), berInt(0), tlv(0x30, vbs)))
	return tlv(0x30, concat(berInt(1), tlv(tagOctets, commB), pdu)), nil
}

func TestGetAgainstFakeAgent(t *testing.T) {
	values := map[string]Value{
		OIDSysUpTime: {Kind: KindTimeticks, Int: 987654},
		OIDSysName:   {Kind: KindString, Str: "ztc-node-1"},
		OIDCPUIdle:   {Kind: KindInt, Int: 97},
		OIDMemTotal:  {Kind: KindInt, Int: 32768}, // 0x8000 — 부호 비트 경계
		OIDMemAvail:  {Kind: KindInt, Int: 12345},
		OIDLoad1:     {Kind: KindString, Str: "0.07"},
	}
	fa := startFakeAgent(t, values)
	defer fa.close()

	// DefaultOIDs + 미지원 OID 1개(noSuchInstance → KindNull 경로)
	oids := append(append([]string{}, DefaultOIDs...), "1.3.6.1.4.1.99999.1.0")
	got, err := Get(context.Background(), "127.0.0.1", fa.port(), "public", oids, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(oids) {
		t.Fatalf("응답 값 %d개, want %d", len(got), len(oids))
	}
	if v := got[OIDSysUpTime]; v.Kind != KindTimeticks || v.Int != 987654 {
		t.Errorf("sysUpTime = %+v", v)
	}
	if v := got[OIDSysName]; v.Kind != KindString || v.Str != "ztc-node-1" {
		t.Errorf("sysName = %+v", v)
	}
	if v := got[OIDCPUIdle]; v.Kind != KindInt || v.Int != 97 {
		t.Errorf("ssCpuIdle = %+v", v)
	}
	if v := got[OIDMemTotal]; v.Kind != KindInt || v.Int != 32768 {
		t.Errorf("memTotal = %+v (부호 비트 경계 손상 가능)", v)
	}
	if v := got[OIDLoad1]; v.Kind != KindString || v.Str != "0.07" {
		t.Errorf("laLoad = %+v", v)
	}
	if v := got["1.3.6.1.4.1.99999.1.0"]; v.Kind != KindNull {
		t.Errorf("미지원 OID = %+v, want KindNull", v)
	}
}

func TestGetTimeout(t *testing.T) {
	// 응답하지 않는 포트: 소켓만 열어 두고 아무것도 돌려주지 않는다.
	blk, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer blk.Close()
	port := blk.LocalAddr().(*net.UDPAddr).Port

	start := time.Now()
	_, err = Get(context.Background(), "127.0.0.1", port, "public", DefaultOIDs, 150*time.Millisecond)
	if err == nil {
		t.Fatal("타임아웃 오류 기대")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("타임아웃 상한 위반: %v", d)
	}
}

func TestGetContextCancel(t *testing.T) {
	blk, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer blk.Close()
	port := blk.LocalAddr().(*net.UDPAddr).Port

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 이미 취소된 ctx — 타임아웃(3s+)보다 먼저 끝나야 한다
	_, err = Get(ctx, "127.0.0.1", port, "public", DefaultOIDs, 30*time.Second)
	if err == nil {
		t.Fatal("ctx 취소 오류 기대")
	}
}

func TestGetBadInput(t *testing.T) {
	if _, err := Get(context.Background(), "999.999.1.1", 161, "public", DefaultOIDs, time.Second); err == nil {
		t.Error("잘못된 IP 에 오류 기대")
	}
	if _, err := Get(context.Background(), "127.0.0.1", 161, "public", []string{"bad.oid"}, time.Second); err == nil {
		t.Error("잘못된 OID 에 오류 기대")
	}
	got, err := Get(context.Background(), "127.0.0.1", 161, "public", nil, time.Second)
	if err != nil || len(got) != 0 {
		t.Errorf("빈 OID 목록: %v %v", got, err)
	}
}
