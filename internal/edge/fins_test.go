package edge

import (
	"bytes"
	"testing"
	"time"
)

func TestBuildFINSFrame(t *testing.T) {
	got := buildFINSFrame(113, 84, []byte{0x06, 0x01})
	want := []byte{0x80, 0x00, 0x02, 0x00, 113, 0x00, 0x00, 84, 0x00, 0x00, 0x06, 0x01}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = % X, want % X", got, want)
	}
}

func TestFinsDA1(t *testing.T) {
	if got := finsDA1("192.168.250.1"); got != 1 {
		t.Fatalf("da1 = %d", got)
	}
	if got := finsDA1("172.30.1.113"); got != 113 {
		t.Fatalf("da1 = %d", got)
	}
}

func TestFINSOK(t *testing.T) {
	if finsOK(nil) {
		t.Fatal("nil must not be ok")
	}
	if finsOK(make([]byte, 13)) {
		t.Fatal("short must not be ok")
	}
	d := make([]byte, 14)
	if !finsOK(d) {
		t.Fatal("14B zero end-code must be ok")
	}
	d[13] = 1
	if finsOK(d) {
		t.Fatal("nonzero SRES must not be ok")
	}
}

func TestBCD(t *testing.T) {
	cases := map[byte]int{0x00: 0, 0x09: 9, 0x10: 10, 0x59: 59, 0x99: 99, 0x24: 24}
	for in, want := range cases {
		if got := bcd(in); got != want {
			t.Fatalf("bcd(%02X) = %d, want %d", in, got, want)
		}
	}
}

// finsResp — 헤더 14B(12,13 = 종료코드 0) + body 로 테스트 응답 생성.
func finsResp(body []byte) []byte {
	d := make([]byte, 14+len(body))
	copy(d[14:], body)
	return d
}

func TestParseFINSStatus(t *testing.T) {
	body := make([]byte, 26) // 14..39
	body[0] = 0x01           // d[14] run bit
	body[1] = 0x04           // d[15] mode RUN
	body[2], body[3] = 0x00, 0x2A
	body[4], body[5] = 0x00, 0x07
	copy(body[10:], []byte{'E', 'R', 'R', 0x01, 0x7F, ' ', '!'}) // d[24..] 메시지
	st, ok := parseFINSStatus(finsResp(body))
	if !ok {
		t.Fatal("not ok")
	}
	if st.RunState() != "RUN" {
		t.Fatalf("runState = %q", st.RunState())
	}
	if st.Fatal != 0x002A || st.NonFatal != 0x0007 {
		t.Fatalf("fatal=%04X nonfatal=%04X", st.Fatal, st.NonFatal)
	}
	if st.Msg != "ERR !" { // 비인쇄문자 제거 + trim
		t.Fatalf("msg = %q", st.Msg)
	}
}

func TestParseFINSStatusModes(t *testing.T) {
	mk := func(runbit, mode byte) finsStatus {
		st, ok := parseFINSStatus(finsResp([]byte{runbit, mode}))
		if !ok {
			t.Fatal("not ok")
		}
		return st
	}
	if got := mk(0, 0).RunState(); got != "PROGRAM" {
		t.Fatalf("mode0 = %q", got)
	}
	if got := mk(0, 2).RunState(); got != "MONITOR" {
		t.Fatalf("mode2 = %q", got)
	}
	// 모드 바이트 없음(len 15) — 운전 비트로 추정.
	st, ok := parseFINSStatus(finsResp([]byte{0x00}))
	if !ok || st.RunState() != "STOP" {
		t.Fatalf("no-mode stop = %q ok=%v", st.RunState(), ok)
	}
	st, _ = parseFINSStatus(finsResp([]byte{0x01}))
	if st.RunState() != "RUN" {
		t.Fatalf("no-mode run = %q", st.RunState())
	}
	// 알 수 없는 모드 값.
	if got := mk(1, 9).RunState(); got != "RUN" {
		t.Fatalf("mode9 = %q", got)
	}
	// 길이 부족.
	if _, ok := parseFINSStatus(finsResp(nil)); ok {
		t.Fatal("14B frame must fail")
	}
}

func TestParseFINSCycle(t *testing.T) {
	body := make([]byte, 12)
	put := func(i int, v uint32) {
		body[i*4+0] = byte(v >> 24)
		body[i*4+1] = byte(v >> 16)
		body[i*4+2] = byte(v >> 8)
		body[i*4+3] = byte(v)
	}
	put(0, 3000) // 300.0ms
	put(1, 1000)
	put(2, 2000)
	c, ok := parseFINSCycle(finsResp(body))
	if !ok {
		t.Fatal("not ok")
	}
	if c.Min != 100.0 || c.Avg != 200.0 || c.Max != 300.0 {
		t.Fatalf("cycle = %+v", c)
	}
	// 10분 초과 = 파싱 이상.
	put(2, 6000001)
	if _, ok := parseFINSCycle(finsResp(body)); ok {
		t.Fatal(">600000ms must be rejected")
	}
	// 경계값 600000.0ms 는 허용.
	put(2, 6000000)
	if _, ok := parseFINSCycle(finsResp(body)); !ok {
		t.Fatal("600000.0ms boundary must be accepted")
	}
	// 길이 부족.
	if _, ok := parseFINSCycle(finsResp(body[:10])); ok {
		t.Fatal("short must fail")
	}
}

func TestParseFINSErrlog(t *testing.T) {
	body := []byte{0x00, 0x00, 0x00, 0x00, 0x00, 0x02} // d[14:20]: len 4 + n=2
	rec := func(code, det uint16, mi, se, da, ho, yr, mo byte) []byte {
		return []byte{byte(code >> 8), byte(code), byte(det >> 8), byte(det), mi, se, da, ho, yr, mo}
	}
	body = append(body, rec(0x0080, 0x0001, 0x35, 0x59, 0x15, 0x09, 0x24, 0x11)...)
	body = append(body, rec(0x00C0, 0x0002, 0x00, 0x00, 0x01, 0x00, 0x24, 0x13)...) // 월 13 → 무효
	out := parseFINSErrlog(finsResp(body), 5)
	if len(out) != 2 {
		t.Fatalf("entries = %d", len(out))
	}
	if out[0].Code != "0080" || out[0].Detail != "0001" || out[0].Time != "2024-11-15 09:35" {
		t.Fatalf("entry0 = %+v", out[0])
	}
	if out[1].Time != "" {
		t.Fatalf("invalid month time = %q", out[1].Time)
	}
	// count 제한.
	if n := len(parseFINSErrlog(finsResp(body), 1)); n != 1 {
		t.Fatalf("count cap = %d", n)
	}
	// 무효 프레임.
	if n := len(parseFINSErrlog(nil, 5)); n != 0 {
		t.Fatalf("nil frame = %d", n)
	}
}

func TestParseFINSClock(t *testing.T) {
	// 2024-11-15 09:35:59
	body := []byte{0x24, 0x11, 0x15, 0x09, 0x35, 0x59}
	epoch, ok := parseFINSClock(finsResp(body))
	if !ok {
		t.Fatal("not ok")
	}
	want := time.Date(2024, 11, 15, 9, 35, 59, 0, time.UTC).Unix()
	if epoch != want {
		t.Fatalf("epoch = %d, want %d", epoch, want)
	}
	// yy >= 70 은 19xx.
	body[0] = 0x99
	epoch, _ = parseFINSClock(finsResp(body))
	if epoch != time.Date(1999, 11, 15, 9, 35, 59, 0, time.UTC).Unix() {
		t.Fatalf("1999 epoch = %d", epoch)
	}
}

func TestTimegmLike(t *testing.T) {
	if _, ok := timegmLike(2024, 13, 1, 0, 0, 0); ok {
		t.Fatal("month 13 must fail")
	}
	if _, ok := timegmLike(2024, 2, 30, 0, 0, 0); ok {
		t.Fatal("Feb 30 must fail")
	}
	if _, ok := timegmLike(2024, 2, 29, 0, 0, 0); !ok {
		t.Fatal("leap Feb 29 must pass")
	}
	if _, ok := timegmLike(2023, 2, 29, 0, 0, 0); ok {
		t.Fatal("non-leap Feb 29 must fail")
	}
	// Python calendar.timegm 은 시·분·초를 검증하지 않는다.
	if _, ok := timegmLike(2024, 1, 1, 25, 0, 0); !ok {
		t.Fatal("hour 25 must pass (timegm semantics)")
	}
}
