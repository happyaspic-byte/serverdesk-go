package snmp

import (
	"fmt"
	"unicode/utf8"
)

// ValueKind 는 Get 응답 값의 해석 방식을 나타낸다.
// 운영에서 어떤 메트릭이 수치(게이지/카운터)이고 어떤 것이 표시 문자열인지
// 구분해야 프런트가 단위 환산(예: timeticks → 초)을 올바르게 할 수 있다.
type ValueKind int

const (
	// KindNull 은 noSuchObject/noSuchInstance/endOfMibView/NULL — everRun 은
	// MIB view 제약으로 미지원 OID 에 이 값을 자주 돌려주므로 정상 흐름이다.
	KindNull ValueKind = iota
	KindInt
	KindString
	KindBytes
	KindCounter
	KindGauge
	KindTimeticks
	KindCounter64
	KindOID
)

// String 은 디버그 로그용 Kind 이름이다.
func (k ValueKind) String() string {
	switch k {
	case KindNull:
		return "null"
	case KindInt:
		return "int"
	case KindString:
		return "string"
	case KindBytes:
		return "bytes"
	case KindCounter:
		return "counter32"
	case KindGauge:
		return "gauge32"
	case KindTimeticks:
		return "timeticks"
	case KindCounter64:
		return "counter64"
	case KindOID:
		return "oid"
	}
	return fmt.Sprintf("kind(%d)", int(k))
}

// Value 는 SNMP GET 으로 받은 값 1개다. 파이썬 snmp_get 의
// {oid: int|str|None} 을 타입 안전하게 옮긴 것이다.
type Value struct {
	Kind  ValueKind
	Int   int64  // KindInt/KindCounter/KindGauge/KindTimeticks/KindCounter64
	Str   string // KindString/KindOID
	Bytes []byte // KindBytes (UTF-8 아닌 OCTET STRING, 미지원 태그 원본)
}

// decGetValue 는 GET 응답 varbind 값 1개를 Value 로 디코드한다.
// 파이썬 snmp_get 의 태그 분기(0x02/0x41/0x42/0x43/0x46 → int, 0x04 → str,
// 0x06 → OID 문자열, 그 외 → None)와 동치가 되게 매핑한다.
// 차이: UTF-8 이 아닌 OCTET STRING 과 미지원 태그는 None 대신 원본 바이트를
// KindBytes 로 보존한다 — 모니터링에서 '값이 있었는데 버려졌다'와 '없다'를
// 구분해야 하기 때문이다.
func decGetValue(tag byte, val []byte) Value {
	switch tag {
	case tagInt:
		return Value{Kind: KindInt, Int: int64(decUint(val))}
	case tagCounter32:
		return Value{Kind: KindCounter, Int: int64(decUint(val))}
	case tagGauge32:
		return Value{Kind: KindGauge, Int: int64(decUint(val))}
	case tagTimeTicks:
		return Value{Kind: KindTimeticks, Int: int64(decUint(val))}
	case tagCounter64:
		return Value{Kind: KindCounter64, Int: int64(decUint(val))}
	case tagOctets:
		if utf8.Valid(val) {
			return Value{Kind: KindString, Str: string(val)}
		}
		return Value{Kind: KindBytes, Bytes: append([]byte(nil), val...)}
	case tagOID:
		s, err := decodeOID(val)
		if err != nil {
			return Value{Kind: KindBytes, Bytes: append([]byte(nil), val...)}
		}
		return Value{Kind: KindOID, Str: s}
	default: // 0x80/0x81/0x82(noSuch*), NULL, Opaque 등
		if tag == tagNull || tag == 0x80 || tag == 0x81 || tag == 0x82 {
			return Value{Kind: KindNull}
		}
		return Value{Kind: KindBytes, Bytes: append([]byte(nil), val...)}
	}
}

// decVarbindValue 는 트랩 varbind 값 1개를 (Go 값, kind 문자열, 표시 문자열)로
// 디코드한다. 파이썬 _dec_value 와 동일 규칙 — kind 어휘(string/hex/oid/int/
// timeticks/ipaddress/null)는 프런트가 그대로 소비하므로 바꾸지 않는다.
func decVarbindValue(tag byte, val []byte) (any, string, string) {
	switch tag {
	case tagOctets:
		// DisplayString 은 대개 UTF-8. 비출력 바이트가 섞이면 hex 로.
		if utf8.Valid(val) {
			s := string(val)
			printable := true
			for _, r := range s {
				if r > 31 || r == '\t' || r == '\r' || r == '\n' {
					continue
				}
				printable = false
				break
			}
			if printable {
				return s, "string", s
			}
		}
		h := fmt.Sprintf("%x", val)
		return h, "hex", h
	case tagOID:
		o, err := decodeOID(val)
		if err != nil {
			h := fmt.Sprintf("%x", val)
			return h, "hex", h
		}
		return o, "oid", o
	case tagInt, tagCounter32, tagGauge32, tagCounter64:
		n := int64(decUint(val))
		return n, "int", fmt.Sprintf("%d", n)
	case tagTimeTicks:
		n := decUint(val)
		// 1/100초 단위 → 초로 표시
		return int64(n), "timeticks", fmt.Sprintf("%.2fs", float64(n)/100.0)
	case tagIPAddress:
		if len(val) == 4 {
			ip := fmt.Sprintf("%d.%d.%d.%d", val[0], val[1], val[2], val[3])
			return ip, "ipaddress", ip
		}
		h := fmt.Sprintf("%x", val)
		return h, "hex", h
	case tagNull:
		return nil, "null", ""
	default: // Opaque / 기타
		h := fmt.Sprintf("%x", val)
		return h, "hex", h
	}
}
