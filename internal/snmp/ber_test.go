package snmp

import (
	"bytes"
	"encoding/hex"
	"testing"
)

// BER 인코딩 골든 테스트 — 파이썬 poller.py/trap_receiver.py 의 _ber_* 와
// 바이트 수준으로 같아야 한다(상호운용이 목적이므로 구현끼리만 맞으면 안 된다).

func TestBERLenGolden(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "00"},
		{1, "01"},
		{0x7F, "7f"},
		{0x80, "8180"},
		{0xFF, "81ff"},
		{0x100, "820100"},
		{0xFFFF, "82ffff"},
		{0x10000, "83010000"},
	}
	for _, c := range cases {
		if got := hex.EncodeToString(berLen(c.n)); got != c.want {
			t.Errorf("berLen(%d) = %s, want %s", c.n, got, c.want)
		}
	}
}

func TestBERIntRoundTrip(t *testing.T) {
	// (값, 파이썬 _ber_int 결과 바이트) 골든 쌍
	cases := []struct {
		n    int64
		want string
	}{
		{0, "020100"},
		{1, "020101"},
		{127, "02017f"},
		{128, "02020080"}, // 최상위 비트 보호용 0x00 접두
		{255, "020200ff"},
		{256, "02020100"},
		{32768, "0203008000"},
		{1 << 30, "020440000000"},
		{-1, "0201ff"},
		{-128, "020180"},
		{-129, "0202ff7f"},
	}
	for _, c := range cases {
		got := berInt(c.n)
		if hex.EncodeToString(got) != c.want {
			t.Errorf("berInt(%d) = %x, want %s", c.n, got, c.want)
			continue
		}
		// 라운드트립: reader 로 읽어 부호 복원
		r := &reader{b: got}
		tag, val, err := r.read()
		if err != nil || tag != tagInt {
			t.Fatalf("berInt(%d) 리더 실패: %v tag=%x", c.n, err, tag)
		}
		var back int64
		if len(val) > 0 {
			if val[0]&0x80 != 0 {
				back = -1
			}
			for _, b := range val {
				back = (back << 8) | int64(b)
			}
		}
		if back != c.n {
			t.Errorf("berInt(%d) 라운드트립 = %d", c.n, back)
		}
		if r.more() {
			t.Errorf("berInt(%d) 잔여 바이트", c.n)
		}
	}
}

func TestOIDGolden(t *testing.T) {
	// sysUpTime.0 인코딩 골든 — 실제 SNMP 패킷에서 검증된 바이트열
	got, err := encodeOID("1.3.6.1.2.1.1.3.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := "06082b06010201010300"; hex.EncodeToString(got) != want {
		t.Errorf("encodeOID(sysUpTime.0) = %x, want %s", got, want)
	}
	// ssCpuIdle.0 — 2021 처럼 128 이상 서브식별자(2바이트 base-128) 포함
	got, err = encodeOID("1.3.6.1.4.1.2021.11.11.0")
	if err != nil {
		t.Fatal(err)
	}
	if want := "060a2b060104018f650b0b00"; hex.EncodeToString(got) != want {
		t.Errorf("encodeOID(ssCpuIdle.0) = %x, want %s", got, want)
	}
}

func TestOIDRoundTrip(t *testing.T) {
	oids := []string{
		"1.3.6.1.2.1.1.3.0",
		"1.3.6.1.4.1.2021.11.11.0",
		"1.3.6.1.4.1.458.115.2.0.2",
		"1.3.6.1.4.1.458.116.2.0.3",
		"1.3.6.1.6.3.1.1.4.1.0",
		"0.0",
		"2.39.3", // 2.x 에서 x>39 는 이 단순 코덱(파이썬 동일)으로 왕복 불가 — fleet OID 는 전부 1.3 아래
		"1.3.6.1.4.1.2021.10.1.3.1",
	}
	for _, o := range oids {
		enc, err := encodeOID(o)
		if err != nil {
			t.Fatalf("encodeOID(%s): %v", o, err)
		}
		r := &reader{b: enc}
		tag, val, err := r.read()
		if err != nil || tag != tagOID {
			t.Fatalf("OID %s 리더 실패: %v tag=%x", o, err, tag)
		}
		back, err := decodeOID(val)
		if err != nil {
			t.Fatalf("decodeOID(%s): %v", o, err)
		}
		if back != o {
			t.Errorf("OID 라운드트립: %s → %s", o, back)
		}
	}
}

func TestOIDEncodeErrors(t *testing.T) {
	for _, bad := range []string{"", "1", "x.3.6", "3.1.4", "1.x"} {
		if _, err := encodeOID(bad); err == nil {
			t.Errorf("encodeOID(%q) 오류 기대", bad)
		}
	}
}

// TestReaderTruncated — 손상 패킷이 panic 없이 오류로 보고되는지.
func TestReaderTruncated(t *testing.T) {
	for _, hexs := range []string{
		"", "30", "3005", "3082ff", "30030102", "300282", "300a0201",
	} {
		b, _ := hex.DecodeString(hexs)
		r := &reader{b: b}
		if _, _, err := r.read(); err == nil {
			t.Errorf("read(%s) 오류 기대", hexs)
		}
	}
}

// TestTLVRoundTrip — tlv + reader 가 긴 값도 왕복하는지.
func TestTLVRoundTrip(t *testing.T) {
	val := bytes.Repeat([]byte("ab"), 200) // 400바이트 — long-form length
	enc := tlv(0x30, val)
	r := &reader{b: enc}
	tag, got, err := r.read()
	if err != nil || tag != 0x30 {
		t.Fatalf("tlv roundtrip: %v tag=%x", err, tag)
	}
	if !bytes.Equal(got, val) {
		t.Errorf("tlv roundtrip 값 불일치: %d바이트", len(got))
	}
	if r.more() {
		t.Error("tlv roundtrip 잔여 바이트")
	}
}
