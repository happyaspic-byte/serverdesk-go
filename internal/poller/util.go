package poller

import (
	"bytes"
	"encoding/json"
	"math"
)

// mathRoundToEven 은 math.RoundToEven 의 별칭이다(state.go 의 round1 에서 사용).
func mathRoundToEven(x float64) float64 { return math.RoundToEven(x) }

// toJSONAny 는 타입이 있는 수집 결과(avcli/sshmetrics 구조체)를 JSON 라운드트립으로
// 동적 뷰 값(map/slice/스칼라)으로 변환한다. Go 구조체의 JSON 태그가 Python dict 키와
// 1:1 로 맞춰져 있어 이 변환만으로 Python 의 dict 복사(dict(n) 등)와 같은 결과가 된다.
//
// 숫자는 float64 로 복원되지만 encoding/json 은 정수값 float64 를 "123" 처럼
// 소수점 없이 직렬화하므로 최종 응답 바이트는 Python 과 같다.
func toJSONAny(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil
	}
	return out
}

// toJSONMap 은 toJSONAny 의 map[string]any 전용 단축이다. 구조체가 nil 이거나
// 객체가 아니면 빈 map 을 돌려준다(Python 의 dict(None-가능-값) or {} 관용구).
func toJSONMap(v any) map[string]any {
	if m, ok := toJSONAny(v).(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// --- 동적 뷰 맵 접근 헬퍼(devices/토폴로지 빌더 전용) ------------------------

// mapGet 은 중첩 맵 조회다. 어느 단계든 map 이 아니면 nil.
func mapGet(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		cm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = cm[k]
	}
	return cur
}

// numVal 은 JSON 숫자(float64/int64/int)를 float64 로 읽는다.
// bool 이나 문자열은 숫자가 아니다(Python _num 과 같은 계약 — isinstance(bool) 배제).
func numVal(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// strVal 은 문자열 값을 읽는다. 문자열이 아니면 "".
func strVal(v any) string {
	s, _ := v.(string)
	return s
}

// listVal 은 []any 로 읽는다. 아니면 nil.
func listVal(v any) []any {
	l, _ := v.([]any)
	return l
}

// dictVal 은 map[string]any 로 읽는다. 아니면 nil.
func dictVal(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// jsonRoundTrip 은 이미 JSON 모양인 값(map/slice)을 구조체로 옮긴다.
// nodeOS 맵의 links/net/temps 서브트리를 topology 뷰 타입으로 변환할 때 쓴다.
func jsonRoundTrip(v any, dst any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
