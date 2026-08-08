package edge

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"time"
)

// ── EtherNet/IP (CIP) 읽기전용 클라이언트 — 옴론 PLC 이더넷 포트 실측용 ─────
// 사용하는 것은 읽기 전용 명령뿐: ListIdentity(0x63), RegisterSession(0x65),
// SendRRData(0x6F) + Get_Attribute_Single(0x0E). Set_Attribute 등 쓰기 서비스는
// 어떤 경로로도 부르지 않는다. NX 는 Get_Attribute_All(0x01? 실측 0x08 미지원)을
// 받지 않아 단일 속성(0x0E)만으로 읽는다.

const (
	cipPort        = 44818
	cipTimeout     = 2500 * time.Millisecond
	cipIDTimeout   = 2000 * time.Millisecond
	cipContext8    = "serverdk" // encap context 필드 8바이트 식별자
	cipSessTimeout = 10         // SendRRData 타임아웃 힌트
)

// cipEncap — EtherNet/IP 캡슐화 헤더(24B) + payload.
// struct "<HHII8sI": cmd, length, session, status(0), context, options(0).
func cipEncap(cmd uint16, session uint32, payload []byte) []byte {
	b := make([]byte, 24, 24+len(payload))
	binary.LittleEndian.PutUint16(b[0:2], cmd)
	binary.LittleEndian.PutUint16(b[2:4], uint16(len(payload)))
	binary.LittleEndian.PutUint32(b[4:8], session)
	copy(b[12:20], cipContext8)
	return append(b, payload...)
}

// buildCIPGetAttr — Get_Attribute_Single(0x0E) 요청 경로: class/instance/attr.
func buildCIPGetAttr(cls, inst, attr byte) []byte {
	return []byte{0x0E, 0x03, 0x20, cls, 0x24, inst, 0x30, attr}
}

// buildCIPSendRRData — UCMM SendRRData payload: CPF(null addr + unconnected data).
func buildCIPSendRRData(cip []byte) []byte {
	p := make([]byte, 0, 16+len(cip))
	var tmp [4]byte
	binary.LittleEndian.PutUint32(tmp[:4], 0) // interface handle
	p = append(p, tmp[:]...)
	binary.LittleEndian.PutUint16(tmp[:2], cipSessTimeout)
	p = append(p, tmp[:2]...)
	binary.LittleEndian.PutUint16(tmp[:2], 2) // item count
	p = append(p, tmp[:2]...)
	binary.LittleEndian.PutUint16(tmp[:2], 0x00) // null address item
	p = append(p, tmp[:2]...)
	binary.LittleEndian.PutUint16(tmp[:2], 0)
	p = append(p, tmp[:2]...)
	binary.LittleEndian.PutUint16(tmp[:2], 0xB2) // unconnected data item
	p = append(p, tmp[:2]...)
	binary.LittleEndian.PutUint16(tmp[:2], uint16(len(cip)))
	p = append(p, tmp[:2]...)
	return append(p, cip...)
}

// parseCIPAttrData — SendRRData 응답에서 CIP 데이터부를 꺼낸다.
// 오프셋: encap 24 + interface handle 4 + timeout 2 + item count 2 + item hdr 4 = 36.
// data[2]=general status(0=성공), data[3]=추가 상태 워드 수 — 그 뒤가 실제 값.
func parseCIPAttrData(r []byte) []byte {
	if len(r) < 44 {
		return nil
	}
	off := 24 + 4 + 2 + 2 + 4
	ln := int(binary.LittleEndian.Uint16(r[off+2 : off+4]))
	end := off + 4 + ln
	if end > len(r) {
		end = len(r)
	}
	data := r[off+4 : end]
	if len(data) >= 4 && data[2] == 0 {
		start := 4 + int(data[3])*2
		if start > len(data) {
			return nil
		}
		return data[start:]
	}
	return nil
}

// cipReadAttrs — 한 TCP 세션에서 Get_Attribute_Single 여러 건.
// attrs: [class, instance, attribute]. 결과는 요청과 같은 순서(실패 시 nil).
func cipReadAttrs(ctx context.Context, ip string, attrs [][3]byte, timeout time.Duration) [][]byte {
	out := make([][]byte, len(attrs))
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip, strconv.Itoa(cipPort)))
	if err != nil {
		return out
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	// RegisterSession
	reg := []byte{1, 0, 0, 0} // protocol version 1, options 0
	if _, err := conn.Write(cipEncap(0x65, 0, reg)); err != nil {
		return out
	}
	rbuf := make([]byte, 2048)
	n, err := conn.Read(rbuf)
	if err != nil || n < 28 {
		return out
	}
	session := binary.LittleEndian.Uint32(rbuf[4:8])

	for i, a := range attrs {
		payload := buildCIPSendRRData(buildCIPGetAttr(a[0], a[1], a[2]))
		if _, err := conn.Write(cipEncap(0x6F, session, payload)); err != nil {
			return out
		}
		n, err := conn.Read(rbuf)
		if err != nil {
			return out
		}
		out[i] = parseCIPAttrData(rbuf[:n])
	}
	return out
}

// cipIdentity — ListIdentity(0x63) 응답 해석 결과. 읽기 전용 디스커버리.
type cipIdentity struct {
	Serial      string // 8자리 대문자 hex
	Name        string
	Rev         string // "major.minor(2자리)"
	Vendor      int
	DeviceType  int
	ProductCode int
	Status      uint16 // CIP Identity status word
	State       int    // 마지막 바이트(3=operational), 없으면 -1
	MinorFault  bool   // status bit 8/9 (회복/비회복 마이너)
	MajorFault  bool   // status bit 10/11 (회복/비회복 메이저)
	IOConn      int    // 확장 상태 bit4-7: EtherNet/IP I/O(태그 데이터링크) 연결 상태
}

// parseCIPIdentity — ListIdentity 응답 파싱. 길이/구조 이상 시 ok=false.
func parseCIPIdentity(d []byte) (cipIdentity, bool) {
	var id cipIdentity
	if len(d) < 63 {
		return id, false
	}
	idx := 24     // encap 헤더
	idx += 2      // item count
	idx += 4      // item type + length
	idx += 2 + 16 // encap version + sockaddr
	need := func(n int) bool { return idx+n <= len(d) }
	if !need(2 + 2 + 2 + 2 + 2 + 4 + 1) {
		return id, false
	}
	id.Vendor = int(binary.LittleEndian.Uint16(d[idx:]))
	idx += 2
	id.DeviceType = int(binary.LittleEndian.Uint16(d[idx:]))
	idx += 2
	id.ProductCode = int(binary.LittleEndian.Uint16(d[idx:]))
	idx += 2
	id.Rev = fmt.Sprintf("%d.%02d", d[idx], d[idx+1])
	idx += 2
	id.Status = binary.LittleEndian.Uint16(d[idx:])
	idx += 2
	id.Serial = fmt.Sprintf("%08X", binary.LittleEndian.Uint32(d[idx:]))
	idx += 4
	nlen := int(d[idx])
	idx++
	if !need(nlen) {
		return id, false
	}
	id.Name = trimSpaceASCII(asciiReplace(d[idx : idx+nlen]))
	id.State = -1
	if len(d) > idx+nlen {
		id.State = int(d[idx+nlen])
	}
	id.MinorFault = id.Status&0x0300 != 0
	id.MajorFault = id.Status&0x0C00 != 0
	id.IOConn = int(id.Status>>4) & 0x0F
	return id, true
}

// cipIdentityReq — ListIdentity UDP 요청 1회. 실패 시 nil.
func cipIdentityReq(ctx context.Context, ip string, timeout time.Duration) *cipIdentity {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(ip, strconv.Itoa(cipPort)))
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := conn.Write(cipEncap(0x63, 0, nil)); err != nil {
		return nil
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil
	}
	id, ok := parseCIPIdentity(buf[:n])
	if !ok {
		return nil
	}
	return &id
}

// ip4FromLE32 — Python _ip4: LE 로 읽은 uint32 를 상위 바이트부터 찍는다
// (옴론이 IP 를 LE 워드로 실어 복내기 때문 — Python 과 같은 수식 유지).
func ip4FromLE32(v uint32) string {
	return fmt.Sprintf("%d.%d.%d.%d", (v>>24)&255, (v>>16)&255, (v>>8)&255, v&255)
}

// cipEthLink — Ethernet Link(0xF6): 속도·플래그·MAC + TCP/IP(0xF5): 호스트명·IP 구성.
// 속성이 하나라도 응답되면(값이 nil 이더라도) 맵을, 전부 실패하면 nil.
func cipEthLink(ctx context.Context, ip string) map[string]any {
	attrs := cipReadAttrs(ctx, ip, [][3]byte{
		{0xF6, 1, 1}, // interface speed
		{0xF6, 1, 2}, // interface flags
		{0xF6, 1, 3}, // physical address (MAC)
		{0xF5, 1, 6}, // host name
		{0xF5, 1, 5}, // interface configuration (IP/MASK/GW)
	}, cipTimeout)
	return mapCIPEthLink(attrs)
}

// mapCIPEthLink — 읽어온 속성 바이트들을 맵으로. 순수 함수(테스트 대상).
func mapCIPEthLink(attrs [][]byte) map[string]any {
	r := map[string]any{}
	if b := attrs[0]; len(b) >= 4 {
		v := binary.LittleEndian.Uint32(b)
		if v > 0 && v <= 100000 {
			r["speedMbps"] = int64(v)
		} else {
			r["speedMbps"] = nil
		}
	}
	if b := attrs[1]; len(b) >= 4 {
		f := binary.LittleEndian.Uint32(b)
		r["linkUp"] = f&1 != 0
		r["fullDuplex"] = f&2 != 0
	}
	if b := attrs[2]; len(b) >= 6 {
		r["mac"] = fmt.Sprintf("%02X:%02X:%02X:%02X:%02X:%02X",
			b[0], b[1], b[2], b[3], b[4], b[5])
	}
	if b := attrs[3]; len(b) >= 2 {
		hl := int(binary.LittleEndian.Uint16(b))
		end := 2 + hl
		if end > len(b) {
			end = len(b)
		}
		if h := trimSpaceASCII(asciiReplace(b[2:end])); h != "" {
			r["hostname"] = h
		}
	}
	if b := attrs[4]; len(b) >= 12 {
		r["netMask"] = ip4FromLE32(binary.LittleEndian.Uint32(b[4:8]))
		gw := ip4FromLE32(binary.LittleEndian.Uint32(b[8:12]))
		if gw == "0.0.0.0" {
			gw = ""
		}
		r["gateway"] = gw
	}
	if len(r) == 0 {
		return nil
	}
	return r
}

// cipCounters — Ethernet Link attr4(인터페이스 카운터 11×UDINT) + attr5(미디어 카운터).
// 실측 트래픽/오류 — B/s 델타는 호출자가 직전 샘플과 비교해 계산한다.
type cipCounters struct {
	InOctets   uint32
	InErrors   uint32
	OutOctets  uint32
	OutErrors  uint32
	CRCErrors  *uint32 // attr5 앞쪽: alignment/FCS(CRC)
	Collisions *uint32 // single + multiple collision 합산
}

// parseCIPIfCounters — attr4(44B 이상) 필수, attr5(48B 이상) 선택.
func parseCIPIfCounters(c4, c5 []byte) *cipCounters {
	if len(c4) < 44 {
		return nil
	}
	u := func(off int) uint32 { return binary.LittleEndian.Uint32(c4[off:]) }
	out := &cipCounters{
		InOctets: u(0), InErrors: u(16), OutOctets: u(24), OutErrors: u(40),
	}
	if len(c5) >= 48 {
		crc := binary.LittleEndian.Uint32(c5[4:])
		col := binary.LittleEndian.Uint32(c5[8:]) + binary.LittleEndian.Uint32(c5[12:])
		out.CRCErrors = &crc
		out.Collisions = &col
	}
	return out
}

// cipIfCountersReq — 네트워크 래퍼.
func cipIfCountersReq(ctx context.Context, ip string) *cipCounters {
	attrs := cipReadAttrs(ctx, ip, [][3]byte{{0xF6, 1, 4}, {0xF6, 1, 5}}, cipTimeout)
	return parseCIPIfCounters(attrs[0], attrs[1])
}

// trimSpaceASCII — Python str.strip() 과 같은 양끝 공백 제거(ASCII 범위).
func trimSpaceASCII(s string) string {
	start := 0
	for start < len(s) && isASCIISpace(s[start]) {
		start++
	}
	end := len(s)
	for end > start && isASCIISpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isASCIISpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f'
}
