package edge

import (
	"encoding/binary"
	"testing"
)

// cipIDFrame — 테스트용 ListIdentity 응답 생성.
func cipIDFrame(status uint16, serial uint32, name string, state int) []byte {
	b := make([]byte, 24+2+4+18)
	b[24], b[25] = 1, 0 // item count
	var u2 [2]byte
	binary.LittleEndian.PutUint16(u2[:], 5) // vendor
	b = append(b, u2[:]...)
	binary.LittleEndian.PutUint16(u2[:], 12) // device type
	b = append(b, u2[:]...)
	binary.LittleEndian.PutUint16(u2[:], 342) // product code
	b = append(b, u2[:]...)
	b = append(b, 2, 30) // revision
	binary.LittleEndian.PutUint16(u2[:], status)
	b = append(b, u2[:]...)
	var u4 [4]byte
	binary.LittleEndian.PutUint32(u4[:], serial)
	b = append(b, u4[:]...)
	b = append(b, byte(len(name)))
	b = append(b, []byte(name)...)
	if state >= 0 {
		b = append(b, byte(state))
	}
	return b
}

func TestParseCIPIdentity(t *testing.T) {
	status := uint16(0x0200 | 0x0400 | 6<<4) // minor rec + major rec + ioConn 6
	d := cipIDFrame(status, 0x00001B4D, "NX1P2-1140DT", 3)
	id, ok := parseCIPIdentity(d)
	if !ok {
		t.Fatal("not ok")
	}
	if id.Vendor != 5 || id.DeviceType != 12 || id.ProductCode != 342 {
		t.Fatalf("ids = %+v", id)
	}
	if id.Rev != "2.30" {
		t.Fatalf("rev = %q", id.Rev)
	}
	if id.Serial != "00001B4D" {
		t.Fatalf("serial = %q", id.Serial)
	}
	if id.Name != "NX1P2-1140DT" {
		t.Fatalf("name = %q", id.Name)
	}
	if id.State != 3 {
		t.Fatalf("state = %d", id.State)
	}
	if !id.MinorFault || !id.MajorFault {
		t.Fatalf("faults minor=%v major=%v", id.MinorFault, id.MajorFault)
	}
	if id.IOConn != 6 {
		t.Fatalf("ioConn = %d", id.IOConn)
	}
	// state 바이트 없음 → -1.
	d2 := cipIDFrame(0, 1, "X", -1)
	id2, ok := parseCIPIdentity(d2)
	if !ok || id2.State != -1 {
		t.Fatalf("no-state = %d ok=%v", id2.State, ok)
	}
	if id2.MinorFault || id2.MajorFault {
		t.Fatal("status 0 must have no faults")
	}
	// 짧은 프레임.
	if _, ok := parseCIPIdentity(make([]byte, 62)); ok {
		t.Fatal("62B must fail")
	}
}

// cipAttrResp — SendRRData 응답 생성(service, status, extsize + payload).
func cipAttrResp(status, extSize byte, payload []byte) []byte {
	cip := append([]byte{0x8E, 0x00, status, extSize}, payload...)
	r := make([]byte, 0, 44+len(cip))
	r = append(r, make([]byte, 24)...) // encap 헤더
	r = append(r, make([]byte, 4)...)  // interface handle
	r = append(r, make([]byte, 2)...)  // timeout
	r = append(r, 2, 0)                // item count
	r = append(r, 0, 0, 0, 0)          // null address item
	var u2 [2]byte
	binary.LittleEndian.PutUint16(u2[:], 0xB2)
	r = append(r, u2[:]...)
	binary.LittleEndian.PutUint16(u2[:], uint16(len(cip)))
	r = append(r, u2[:]...)
	return append(r, cip...)
}

func TestParseCIPAttrData(t *testing.T) {
	payload := []byte{0xE8, 0x03, 0x00, 0x00} // speed 1000 LE
	got := parseCIPAttrData(cipAttrResp(0, 0, payload))
	if len(got) != 4 || binary.LittleEndian.Uint32(got) != 1000 {
		t.Fatalf("data = % X", got)
	}
	// 확장 상태 워드 skip.
	got = parseCIPAttrData(cipAttrResp(0, 1, append([]byte{0xAA, 0xBB}, payload...)))
	if len(got) != 4 || binary.LittleEndian.Uint32(got) != 1000 {
		t.Fatalf("ext data = % X", got)
	}
	// general status != 0 → nil.
	if got := parseCIPAttrData(cipAttrResp(0x08, 0, payload)); got != nil {
		t.Fatalf("status!=0 = % X", got)
	}
	// 짧은 응답.
	if got := parseCIPAttrData(make([]byte, 43)); got != nil {
		t.Fatal("43B must fail")
	}
}

func TestBuildCIPGetAttr(t *testing.T) {
	got := buildCIPGetAttr(0xF6, 1, 4)
	want := []byte{0x0E, 0x03, 0x20, 0xF6, 0x24, 0x01, 0x30, 0x04}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attr path = % X", got)
		}
	}
	// SendRRData payload: ifh(4) timeout(2) count(2) item1(4) item2hdr(4) cip.
	p := buildCIPSendRRData(got)
	if len(p) != 16+8 {
		t.Fatalf("payload len = %d", len(p))
	}
	if binary.LittleEndian.Uint16(p[6:8]) != 2 {
		t.Fatal("item count != 2")
	}
	if binary.LittleEndian.Uint16(p[12:14]) != 0xB2 {
		t.Fatal("item2 type != 0xB2")
	}
}

func TestIP4FromLE32(t *testing.T) {
	if got := ip4FromLE32(0xC0A80101); got != "192.168.1.1" {
		t.Fatalf("ip = %q", got)
	}
	if got := ip4FromLE32(0); got != "0.0.0.0" {
		t.Fatalf("zero = %q", got)
	}
}

func le32bytes(v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b[:]
}

func TestMapCIPEthLink(t *testing.T) {
	mac := []byte{0xD0, 0x00, 0x06, 0x13, 0x27, 0x3E}
	host := append([]byte{5, 0}, []byte("nx1p2")...)
	ipc := append(le32bytes(0xC0A8FA01), le32bytes(0xFFFFFF00)...) // 192.168.250.1 / 255.255.255.0
	ipc = append(ipc, le32bytes(0)...)                             // gateway 0 → ""
	m := mapCIPEthLink([][]byte{le32bytes(1000), le32bytes(3), mac, host, ipc})
	if m["speedMbps"] != int64(1000) {
		t.Fatalf("speed = %v", m["speedMbps"])
	}
	if m["linkUp"] != true || m["fullDuplex"] != true {
		t.Fatalf("flags = %v %v", m["linkUp"], m["fullDuplex"])
	}
	if m["mac"] != "D0:00:06:13:27:3E" {
		t.Fatalf("mac = %v", m["mac"])
	}
	if m["hostname"] != "nx1p2" {
		t.Fatalf("host = %v", m["hostname"])
	}
	if m["netMask"] != "255.255.255.0" || m["gateway"] != "" {
		t.Fatalf("net = %v gw=%v", m["netMask"], m["gateway"])
	}
	// 속도 무효 → 키는 있되 nil.
	m2 := mapCIPEthLink([][]byte{le32bytes(0), nil, nil, nil, nil})
	if v, ok := m2["speedMbps"]; !ok || v != nil {
		t.Fatalf("invalid speed = %v ok=%v", v, ok)
	}
	// 전부 실패 → nil.
	if m3 := mapCIPEthLink([][]byte{nil, nil, nil, nil, nil}); m3 != nil {
		t.Fatalf("all-fail = %v", m3)
	}
}

func TestParseCIPIfCounters(t *testing.T) {
	c4 := make([]byte, 44)
	binary.LittleEndian.PutUint32(c4[0:], 1000)  // inOctets
	binary.LittleEndian.PutUint32(c4[16:], 2)    // inErrors
	binary.LittleEndian.PutUint32(c4[24:], 2000) // outOctets
	binary.LittleEndian.PutUint32(c4[40:], 3)    // outErrors
	c5 := make([]byte, 48)
	binary.LittleEndian.PutUint32(c5[4:], 7)  // FCS/CRC
	binary.LittleEndian.PutUint32(c5[8:], 1)  // single collision
	binary.LittleEndian.PutUint32(c5[12:], 2) // multiple collision
	c := parseCIPIfCounters(c4, c5)
	if c == nil {
		t.Fatal("nil")
	}
	if c.InOctets != 1000 || c.OutOctets != 2000 || c.InErrors != 2 || c.OutErrors != 3 {
		t.Fatalf("counters = %+v", c)
	}
	if c.CRCErrors == nil || *c.CRCErrors != 7 {
		t.Fatalf("crc = %v", c.CRCErrors)
	}
	if c.Collisions == nil || *c.Collisions != 3 {
		t.Fatalf("collisions = %v", c.Collisions)
	}
	// attr5 없음 → 포인터 nil.
	c2 := parseCIPIfCounters(c4, nil)
	if c2.CRCErrors != nil || c2.Collisions != nil {
		t.Fatal("no-c5 must leave pointers nil")
	}
	// attr4 짧음 → nil.
	if c3 := parseCIPIfCounters(c4[:43], c5); c3 != nil {
		t.Fatal("short c4 must fail")
	}
}
