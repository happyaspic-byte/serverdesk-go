package snmp

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// BER 태그 상수. 파이썬 trap_receiver.py 의 _T_* / _PDU_* 와 동일 값.
const (
	tagInt       = 0x02
	tagOctets    = 0x04
	tagNull      = 0x05
	tagOID       = 0x06
	tagIPAddress = 0x40
	tagCounter32 = 0x41
	tagGauge32   = 0x42
	tagTimeTicks = 0x43
	tagOpaque    = 0x44
	tagCounter64 = 0x46

	pduGet      = 0xA0
	pduResponse = 0xA2
	pduV1Trap   = 0xA4
	pduInform   = 0xA6
	pduV2Trap   = 0xA7
)

// errBERTruncated 는 길이 접두와 실제 바이트 수가 어긋난 손상 패킷을 뜻한다.
// 손상 데이터그램 하나가 수신 루프를 죽이면 안 되므로 panic 대신 오류로 돌린다.
var errBERTruncated = errors.New("snmp: BER 길이가 버퍼를 초과")

// berLen 은 BER 길이 옥텛을 인코딩한다(파이썬 _ber_len 과 동일).
func berLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(n))
	i := 0
	for i < 7 && tmp[i] == 0 {
		i++
	}
	body := tmp[i:]
	out := make([]byte, 0, len(body)+1)
	out = append(out, 0x80|byte(len(body)))
	return append(out, body...)
}

// tlv 는 tag + length + value 를 이어 붙인다(파이썬 _tlv 와 동일).
func tlv(tag byte, val []byte) []byte {
	out := make([]byte, 0, len(val)+9)
	out = append(out, tag)
	out = append(out, berLen(len(val))...)
	return append(out, val...)
}

// signedBytes 는 DER 최소 길이 2의 보수 바이트열을 돌려준다.
// 파이썬 (n.bit_length()+8)//8 signed 인코딩과 동일 결과.
func signedBytes(n int64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(n))
	i := 0
	for i < 7 {
		switch {
		case b[i] == 0x00 && b[i+1]&0x80 == 0: // 양수 상위 0 제거
			i++
		case b[i] == 0xff && b[i+1]&0x80 == 0x80: // 음수 상위 sign-extension 제거
			i++
		default:
			return b[i:]
		}
	}
	return b[7:]
}

// berInt 는 INTEGER TLV 를 인코딩한다(파이썬 _ber_int 와 동일).
func berInt(n int64) []byte {
	if n == 0 {
		return tlv(tagInt, []byte{0x00})
	}
	return tlv(tagInt, signedBytes(n))
}

// berUintRaw 는 Counter/TimeTicks 같은 애플리케이션 태그용 최소 길이
// 부호 없는 바이트열을 돌려준다(파이썬 build_v2c_trap 의 timeticks 인코딩과 동일).
func berUintRaw(n uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], n)
	i := 0
	for i < 7 && b[i] == 0 {
		i++
	}
	return b[i:]
}

// encodeOID 는 점 표기 OID 를 OID TLV 로 인코딩한다(파이썬 _ber_oid 와 동일).
// 상위 2개 서브식별자는 첫 바이트(p0*40+p1)에 합쳐진다 — p0*40+p1 이 255 를
// 넘는 OID(예: 2.999)는 이 단순 방식으로 표현 못 하므로 오류를 돌린다.
func encodeOID(oid string) ([]byte, error) {
	s := strings.Trim(oid, ".")
	if s == "" {
		return nil, fmt.Errorf("snmp: 빈 OID")
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("snmp: OID 서브식별자 부족: %q", oid)
	}
	p := make([]uint64, len(parts))
	for i, tok := range parts {
		v, err := strconv.ParseUint(tok, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("snmp: OID %q 파싱 실패: %w", oid, err)
		}
		p[i] = v
	}
	if p[0] > 2 {
		return nil, fmt.Errorf("snmp: 지원하지 않는 OID 루트: %q", oid)
	}
	first := p[0]*40 + p[1]
	if first > 255 { // 파이썬도 bytes([n]) 에서 같은 경우 ValueError 로 실패한다
		return nil, fmt.Errorf("snmp: OID 루트가 1바이트 초과: %q", oid)
	}
	out := []byte{byte(first)}
	for _, v := range p[2:] {
		if v < 128 {
			out = append(out, byte(v))
			continue
		}
		var chunk [10]byte // uint64 는 7bit 그룹 최대 10개
		i := len(chunk)
		for v > 0 {
			i--
			chunk[i] = byte(v & 0x7F)
			v >>= 7
		}
		for j := i; j < len(chunk)-1; j++ {
			out = append(out, chunk[j]|0x80)
		}
		out = append(out, chunk[len(chunk)-1])
	}
	return tlv(tagOID, out), nil
}

// decodeOID 는 OID 값 바이트열을 점 표기로 디코드한다(파이썬 _dec_oid 와 동일).
func decodeOID(b []byte) (string, error) {
	if len(b) == 0 {
		return "", errBERTruncated
	}
	var sb strings.Builder
	sb.WriteString(strconv.Itoa(int(b[0]) / 40))
	sb.WriteByte('.')
	sb.WriteString(strconv.Itoa(int(b[0]) % 40))
	var v uint64
	for _, c := range b[1:] {
		v = (v << 7) | uint64(c&0x7F)
		if c&0x80 == 0 {
			sb.WriteByte('.')
			sb.WriteString(strconv.FormatUint(v, 10))
			v = 0
		}
	}
	return sb.String(), nil
}

// decUint 는 값 바이트열을 부호 없는 정수로 읽는다.
// 파이썬 int.from_bytes(val, "big") 과 동일하게 부호 없이 해석한다.
// 8바이트를 넘는 값은 상위 바이트를 버린다(실측 fleet 에서 발생하지 않음).
func decUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = (v << 8) | uint64(c)
	}
	return v
}

// reader 는 길이-접두 BER 스트림 순차 판독기다(파이썬 _Rdr 와 동일 역할).
// 파이썬은 IndexError 로 실패하지만 Go 는 손상 패킷이 수신 루프를 죽이지
// 못하게 모든 경계 검사를 오류로 돌린다.
type reader struct {
	b []byte
	i int
}

func (r *reader) more() bool { return r.i < len(r.b) }

// read 는 TLV 한 조각의 태그와 값을 돌려준다.
func (r *reader) read() (byte, []byte, error) {
	if r.i+2 > len(r.b) {
		return 0, nil, errBERTruncated
	}
	t := r.b[r.i]
	r.i++
	n := int(r.b[r.i])
	r.i++
	if n&0x80 != 0 {
		k := n & 0x7F
		if k == 0 || k > 4 { // 불정 형(0x80)과 4GB 초과 길이는 UDP SNMP 에서 비정상
			return 0, nil, fmt.Errorf("snmp: 비정상 BER 길이 옥텛 %d", k)
		}
		if r.i+k > len(r.b) {
			return 0, nil, errBERTruncated
		}
		n = 0
		for _, c := range r.b[r.i : r.i+k] {
			n = (n << 8) | int(c)
		}
		r.i += k
	}
	if r.i+n > len(r.b) {
		return 0, nil, errBERTruncated
	}
	v := r.b[r.i : r.i+n]
	r.i += n
	return t, v, nil
}

// mustRead 는 값이 꼭 필요한 위치에서 읽는다. 실패 시 오류.
func (r *reader) mustRead() (byte, []byte, error) {
	if !r.more() {
		return 0, nil, errBERTruncated
	}
	return r.read()
}
