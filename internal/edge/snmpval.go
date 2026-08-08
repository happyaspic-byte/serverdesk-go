package edge

import (
	"context"
	"strconv"
	"time"

	"serverdesk/internal/snmp"
)

// SNMPGetFunc — 고정된 sibling API serverdesk/internal/snmp.Get 과 같은 시그니처.
// 테스트에서는 가짜 구현을 주입하고, 운영에서는 기본값 snmp.Get 이 쓰인다.
// 읽기전용 계약(절대 규칙): SNMP 는 GET 만 사용한다 — SET 은 어떤 경로로도 부르지 않는다.
type SNMPGetFunc func(ctx context.Context, ip string, port int, community string, oids []string, timeout time.Duration) (map[string]snmp.Value, error)

// snmpValue — 패키지 낶부 약칭.
type snmpValue = snmp.Value

// isNumKind — Python snmp_get 이 int 로 매핑하던 태그들
// (int/counter32/gauge32/timeticks/counter64)에 해당하는 Kind 집합.
func isNumKind(k snmp.ValueKind) bool {
	switch k {
	case snmp.KindInt, snmp.KindCounter, snmp.KindGauge,
		snmp.KindTimeticks, snmp.KindCounter64:
		return true
	}
	return false
}

// call — Python EdgeWorker._safe 와 같은 의미: 타임아웃/네트워크 오류는
// "무응답"(nil)으로 환산한다. 예외가 라운드를 죽이면 안 되기 때문이다.
func (f SNMPGetFunc) call(ctx context.Context, ip, community string, oids []string, timeout time.Duration) map[string]snmp.Value {
	r, err := f(ctx, ip, 161, community, oids, timeout)
	if err != nil {
		return nil
	}
	return r
}

// vnum — Python _num(): SNMP 값을 정수로. 문자열/옥텟도 숫자 파싱을 시도한다.
func vnum(v snmp.Value) (int64, bool) {
	if isNumKind(v.Kind) {
		return v.Int, true
	}
	switch v.Kind {
	case snmp.KindString, snmp.KindOID:
		return ji(v.Str)
	case snmp.KindBytes:
		return ji(string(v.Bytes))
	}
	return 0, false
}

// vnumStrict — Python isinstance(x, int) 검사와 같은 엄격 버전.
// Python 의 snmp_get 은 int 계열 태그를 전부 int 로 돌려줬으므로
// 게이지/타임틱/카운터64 도 "int" 다 (server kind CPU/메모리 판정용).
func vnumStrict(v snmp.Value) (int64, bool) {
	if isNumKind(v.Kind) {
		return v.Int, true
	}
	return 0, false
}

// vnumOr — Python `_num(v) or defv`: 없거나 0 이면 defv (0 은 falsy).
func vnumOr(v snmp.Value, defv int64) int64 {
	if n, ok := vnum(v); ok && n != 0 {
		return n
	}
	return defv
}

// vstr — Python _s(): 문자열화 + NUL/공백 정리.
func vstr(v snmp.Value) string {
	switch {
	case v.Kind == snmp.KindBytes:
		return cleanStr(string(v.Bytes))
	case v.Kind == snmp.KindString || v.Kind == snmp.KindOID:
		return cleanStr(v.Str)
	case isNumKind(v.Kind):
		return strconv.FormatInt(v.Int, 10)
	}
	return ""
}

// vbyte0 — Python errbits 첫 바이트 해석(bytes/str/int 3형 모두 수용).
func vbyte0(v snmp.Value) (int64, bool) {
	switch {
	case v.Kind == snmp.KindBytes:
		if len(v.Bytes) > 0 {
			return int64(v.Bytes[0]), true
		}
	case v.Kind == snmp.KindString:
		if v.Str != "" {
			return int64(v.Str[0]), true
		}
	case isNumKind(v.Kind):
		return v.Int, true
	}
	return 0, false
}
