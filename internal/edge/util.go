package edge

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// tsKST — Python _ts(epoch): epoch + 9h 를 UTC 로 포맷 = KST 로컬시각.
// 프런트 경보 시각이 KST 문자열로 고정돼 있어 동일 규칙을 유지한다.
func tsKST(epoch float64) string {
	return time.Unix(int64(epoch), 0).UTC().Add(kstOffset).Format("2006-01-02 15:04")
}

// iround — Python 3 의 round()(banker's rounding)와 같은 결과를 낸다.
// math.Round(반올림)와 .5 케이스에서 달라지므로 RoundToEven 을 쓴다.
func iround(x float64) int64 { return int64(math.RoundToEven(x)) }

// round1 — Python round(x, 1).
func round1(x float64) float64 { return math.RoundToEven(x*10) / 10 }

func itoa(i int) string { return strconv.Itoa(i) }

// cleanStr — Python _s(): UTF-8 replace 디코딩 후 "\x00 " 제거 + trim.
func cleanStr(s string) string {
	s = strings.ToValidUTF8(s, "�")
	return strings.TrimSpace(strings.Trim(s, "\x00 "))
}

// pyFloat — Python str(float) 형태(36.5 → "36.5", 36.0 → "36.0").
// 경보 사유 문자열이 Python 출력과 글자 단위로 같아야 해서 맞춘다.
func pyFloat(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".eE") && !strings.Contains(s, "Inf") && !strings.Contains(s, "NaN") {
		s += ".0"
	}
	return s
}

// ── JSON(map[string]any) 접근 헬퍼 — Python dict.get 관행을 Go 로 옮긴 것 ──

// jm — v 가 object 면 map, 아니면 nil.
func jm(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// jl — v 가 array 면 slice, 아니면 nil.
func jl(v any) []any {
	l, _ := v.([]any)
	return l
}

// js — v 가 string 이면 그 값, 아니면 "".
func js(v any) string {
	s, _ := v.(string)
	return s
}

// jf — Python float(v) 유사: JSON number 또는 숫자 문자열.
// (직접 만든 맵의 int/int64 도 받는다 — json.Unmarshal 은 float64 만 내지만
// 통합자가 손으로 만든 맵이 들어올 수 있어서.)
func jf(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f, err == nil
	}
	return 0, false
}

// ji — Python _num(v): int(v) 실패 시 int(float(v)) 까지 시도, 둘 다 아니면 실패.
// int() 는 0 방향 절단 — Go 의 float64→int64 변환과 동일.
func ji(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case string:
		s := strings.TrimSpace(n)
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), true
		}
	}
	return 0, false
}

// jiOr — Python `ji(v) or defv` 관행: 없거나 0 이면 defv (0 은 falsy).
func jiOr(v any, defv int64) int64 {
	if n, ok := ji(v); ok && n != 0 {
		return n
	}
	return defv
}

// jtruthy — Python 진리값: nil/false/0/"" /빈 컨테이너 → false.
func jtruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// numOrNil — *int64 를 JSON 값으로: nil 은 null 유지(Python None).
func numOrNil(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// i64p — int64 포인터.
func i64p(v int64) *int64 { return &v }

// asciiReplace — Python bytes.decode("ascii", "replace"): 비ASCII 는 U+FFFD.
func asciiReplace(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if c < utf8.RuneSelf {
			sb.WriteByte(c)
		} else {
			sb.WriteRune(utf8.RuneError)
		}
	}
	return sb.String()
}
