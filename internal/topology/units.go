package topology

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// 단위/값 정규화 유틸
// ---------------------------------------------------------------------------

// binUnits 는 크기 문자열의 단위 -> 배수다. 이진 단위가 기본이며,
// 혹시 십진 단위가 섞여 나올 경우를 대비해 KB/MB/GB/TB 도 받는다.
var binUnits = map[string]int64{
	"B":   1,
	"KIB": 1024,
	"MIB": 1024 * 1024,
	"GIB": 1024 * 1024 * 1024,
	"TIB": 1024 * 1024 * 1024 * 1024,
	"PIB": 1024 * 1024 * 1024 * 1024 * 1024,
	"KB":  1000,
	"MB":  1000 * 1000,
	"GB":  1000 * 1000 * 1000,
	"TB":  1000 * 1000 * 1000 * 1000,
}

var sizeRe = regexp.MustCompile(`^\s*([0-9]+(?:\.[0-9]+)?)\s*([A-Za-z]+)?\s*$`)

// ParseSize 는 "110.81 GiB" 같은 문자열을 바이트 수로 변환한다.
// 파싱 불가 시 nil. avcli 가 단위 표기를 흔들어도 그래프가 깨지지 않게
// 실패를 예외 대신 nil 로 흡수한다.
func ParseSize(text string) *int64 {
	m := sizeRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	unit := strings.ToUpper(m[2])
	if unit == "" {
		unit = "B"
	}
	mult, ok := binUnits[unit]
	if !ok {
		return nil
	}
	// Python int(round(x)) — round-half-even
	v := int64(math.RoundToEven(val * float64(mult)))
	return &v
}

var bwRe = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([KMGT]?)b/s\s*$`)

var bwMult = map[string]float64{
	"": 1, "K": 1e3, "M": 1e6, "G": 1e9, "T": 1e12,
}

// ParseBandwidth 는 "10 Gb/s" 를 bits/s 로 변환한다. 파싱 불가 시 nil.
// 원본과 같이 소수점 이하는 버린다(int() 캐스팅).
func ParseBandwidth(text string) *int64 {
	m := bwRe.FindStringSubmatch(text)
	if m == nil {
		return nil
	}
	val, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return nil
	}
	v := int64(val * bwMult[strings.ToUpper(m[2])])
	return &v
}

// ParseBool 은 avcli 텍스트("true"/"yes"/"1"/"enabled" 등)를 bool 로 해석한다.
// 알 수 없는 값이면 def 를 그대로 돌려준다.
func ParseBool(text string, def *bool) *bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "true", "yes", "1", "enabled":
		v := true
		return &v
	case "false", "no", "0", "disabled":
		v := false
		return &v
	}
	return def
}

// Pct 는 used/total 의 백분율(소수 2자리)을 반환한다.
// 원본 계약상 used 나 total 이 없거나 0 이면 nil 이다(0% 와 '정보 없음' 을
// 구분하지 않는 Python `if not used or not total` 을 그대로 따른다).
func Pct(used, total *int64) *float64 {
	if used == nil || total == nil || *used == 0 || *total == 0 {
		return nil
	}
	v := math.RoundToEven(float64(*used)*100.0/float64(*total)*100) / 100
	return &v
}

// HumanSize 는 바이트 수를 "110.81 GiB" 형태로 재구성한다 (UI 표시용).
func HumanSize(nbytes *int64) *string {
	if nbytes == nil {
		return nil
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB", "PiB"}
	i := 0
	v := float64(*nbytes)
	for v >= 1024 && i < len(units)-1 {
		v /= 1024.0
		i++
	}
	s := strconv.FormatFloat(v, 'f', 2, 64) + " " + units[i]
	return &s
}

// --- nil 가능 값 헬퍼 -------------------------------------------------------

// strOrNil 은 빈 문자열을 nil 로 변환한다. Python 의 None 과 맞추기 위함이다.
func strOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ptrOrNil 은 빈 문자열이면 nil, 아니면 *string 을 반환한다.
func ptrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// boolOrNil 은 *bool 입력을 JSON 값으로 바꾼다 (nil 보존).
func boolOrNil(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

// f64 는 float64 값의 포인터를 만든다 (테스트/빌더 편의).
func f64(v float64) *float64 { return &v }

// i64 는 int64 값의 포인터를 만든다.
func i64(v int64) *int64 { return &v }
