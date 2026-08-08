package snmp

import (
	"fmt"
)

// V2cVarbind 는 BuildV2cTrap 에 넘기는 varbind 지정이다.
// Kind: "str"/"string" | "oid" | "int" | "timeticks" — 파이썬 build_v2c_trap 의
// 태그 어휘와 동일. Value 는 Kind 에 맞는 Go 값(string / int64 등).
type V2cVarbind struct {
	OID   string
	Kind  string
	Value any
}

// BuildV2cTrap 은 SNMPv2c 트랩 UDP 페이로드를 만든다(파이썬 build_v2c_trap 포팅).
// net-snmp 없이 루프백·단위테스트에서 디코더/수신기를 검증하기 위한 도구다.
// sysUpTime + snmpTrapOID varbind 는 RFC 3584 규약대로 항상 앞에 붙는다.
func BuildV2cTrap(community, trapOID string, varbinds []V2cVarbind, sysUptime uint64) ([]byte, error) {
	enc := func(oid, kind string, value any) ([]byte, error) {
		var v []byte
		switch kind {
		case "str", "string":
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("snmp: str varbind 값이 string 아님: %v", value)
			}
			v = tlv(tagOctets, []byte(s))
		case "oid":
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("snmp: oid varbind 값이 string 아님: %v", value)
			}
			var err error
			v, err = encodeOID(s)
			if err != nil {
				return nil, err
			}
		case "int":
			n, ok := toInt64(value)
			if !ok {
				return nil, fmt.Errorf("snmp: int varbind 값이 정수 아님: %v", value)
			}
			v = berInt(n)
		case "timeticks":
			n, ok := toInt64(value)
			if !ok {
				return nil, fmt.Errorf("snmp: timeticks varbind 값이 정수 아님: %v", value)
			}
			v = tlv(tagTimeTicks, berUintRaw(uint64(n)))
		default:
			return nil, fmt.Errorf("snmp: 지원하지 않는 varbind kind %q", kind)
		}
		o, err := encodeOID(oid)
		if err != nil {
			return nil, err
		}
		return tlv(0x30, concat(o, v)), nil
	}

	up, err := enc(OIDSysUpTime, "timeticks", sysUptime)
	if err != nil {
		return nil, err
	}
	ton, err := enc(OIDSNMPTrapOID, "oid", trapOID)
	if err != nil {
		return nil, err
	}
	vbs := concat(up, ton)
	for _, vb := range varbinds {
		b, err := enc(vb.OID, vb.Kind, vb.Value)
		if err != nil {
			return nil, err
		}
		vbs = concat(vbs, b)
	}
	pdu := tlv(pduV2Trap, concat(berInt(1), berInt(0), berInt(0), tlv(0x30, vbs)))
	return tlv(0x30, concat(berInt(1), tlv(tagOctets, []byte(community)), pdu)), nil
}

// toInt64 는 테스트 지정값의 숫자형을 int64 로 정규화한다.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint32:
		return int64(n), true
	case uint64:
		return int64(n), true
	}
	return 0, false
}
