package avcli

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// 값 정규화 헬퍼 — avcli_parse.py 의 parse_* 계열 포트.
// 용량/대역폭/섹터크기 문자열("110.81 GiB", "10 Gb/s", "512 B")은 정수로 정규화하고,
// 실패하면 nil(Python 의 default=None). 원문은 별도 *_raw 필드가 보존한다.

var sizeUnits = map[string]int64{
	"B":  1,
	"KB": 1000, "MB": 1000 * 1000, "GB": 1000 * 1000 * 1000,
	"TB": 1000 * 1000 * 1000 * 1000, "PB": 1000 * 1000 * 1000 * 1000 * 1000,
	"KIB": 1024, "MIB": 1024 * 1024, "GIB": 1024 * 1024 * 1024,
	"TIB": 1024 * 1024 * 1024 * 1024, "PIB": 1024 * 1024 * 1024 * 1024 * 1024,
	"K": 1024, "M": 1024 * 1024, "G": 1024 * 1024 * 1024, "T": 1024 * 1024 * 1024 * 1024,
}

var (
	sizeRe = regexp.MustCompile(`^\s*([-+]?[\d.,]+)\s*([A-Za-z]*)\s*$`)
	bwRe   = regexp.MustCompile(`^\s*([-+]?[\d.,]+)\s*([A-Za-z]+/?[Ss]?)\s*$`)
)

var bwUnits = map[string]int64{
	"B/S": 1, "BPS": 1,
	"KB/S": 1e3, "MB/S": 1e6, "GB/S": 1e9, "TB/S": 1e12,
}

// roundHalfEven 은 Python round() 와 같은 짝수 반올림이다.
// math.Round(반올림 상한)와 0.5 경계에서 결과가 달라지므로 반드시 이걸 쓴다.
func roundHalfEven(x float64) float64 { return math.RoundToEven(x) }

// ParseSize 는 "110.81 GiB" / "512 B" / "1.74 TiB" 를 바이트로 정규화한다.
// 실패 시 nil. 이진 단위(KiB/GiB/TiB)와 십진 단위(KB/GB/TB)가 혼재한다(실측).
func ParseSize(s string) *int64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil
	}
	num, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return nil
	}
	unit := m[2]
	if unit == "" {
		unit = "B"
	}
	mult, ok := sizeUnits[strings.ToUpper(unit)]
	if !ok {
		return nil
	}
	v := int64(roundHalfEven(num * float64(mult)))
	return &v
}

// ParseBandwidth 는 "1 Gb/s" / "10 Gb/s" 를 bits/sec 로 정규화한다. 실패 시 nil.
//
// avcli 는 비트 단위(소문자 b)로 표기한다. 대소문자를 구분하지 않고 b/s 계열은
// 전부 비트로 간주한다(Bytes/s 표기는 실장비에서 관측되지 않음).
func ParseBandwidth(s string) *int64 {
	m := bwRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return nil
	}
	num, err := strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
	if err != nil {
		return nil
	}
	unit := strings.ToUpper(m[2])
	unit = strings.ReplaceAll(unit, "PS", "/S")
	if !strings.HasSuffix(unit, "/S") {
		unit += "/S"
	}
	mult, ok := bwUnits[unit]
	if !ok {
		return nil
	}
	v := int64(roundHalfEven(num * float64(mult)))
	return &v
}

// ParseBool 은 avcli 불리언(소문자 "true"/"false" 외 관용 표현)을 읽는다.
func ParseBool(s string) *bool {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "true", "yes", "1", "enabled", "on":
		b := true
		return &b
	case "false", "no", "0", "disabled", "off":
		b := false
		return &b
	}
	return nil
}

// ParseFloat 은 콤마를 제거한 float 파싱이다("6.00" 같은 vcpu 문자열 대응).
func ParseFloat(s string) *float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(t, ",", ""), 64)
	if err != nil {
		return nil
	}
	return &f
}

// ParseInt 는 ParseFloat 경유 정수 변환(Python parse_int: float 후 int() 절삭).
func ParseInt(s string) *int64 {
	f := ParseFloat(s)
	if f == nil {
		return nil
	}
	v := int64(*f)
	return &v
}

var months = map[string]time.Month{
	"Jan": time.January, "Feb": time.February, "Mar": time.March, "Apr": time.April,
	"May": time.May, "Jun": time.June, "Jul": time.July, "Aug": time.August,
	"Sep": time.September, "Oct": time.October, "Nov": time.November, "Dec": time.December,
}

var (
	javaDateRe = regexp.MustCompile(
		`^\s*\w{3}\s+(\w{3})\s+(\d{1,2})\s+(\d{2}):(\d{2}):(\d{2})\s+(\S+)\s+(\d{4})\s*$`)
	isoLikeRe = regexp.MustCompile(`^\s*(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})`)
)

// ParseJavaDate 는 "Mon Jul 06 07:35:25 UTC 2026"(java Date.toString)이나
// alert 의 "2026-07-14 16:02:24"(TZ 표기 없음) 형식을 epoch 초로 읽는다.
//
// TZ 표기가 없는 형식은 **UTC 로 읽은 naive epoch** 로 계산한다. 실제 보정은
// ApplyAlertTimezone 이 노드 오프셋을 빼서 수행하므로, 여기서 호스트 로컬 TZ 를
// 쓰면 같은 오프셋이 이중 적용된다(호스트가 KST 면 모든 알림이 9시간 오래돼 보인다).
func ParseJavaDate(s string) *int64 {
	txt := strings.TrimSpace(s)
	if txt == "" {
		return nil
	}
	if m := javaDateRe.FindStringSubmatch(txt); m != nil {
		mon, ok := months[m[1]]
		if !ok {
			return nil
		}
		day, _ := strconv.Atoi(m[2])
		hh, _ := strconv.Atoi(m[3])
		mm, _ := strconv.Atoi(m[4])
		ss, _ := strconv.Atoi(m[5])
		yr, _ := strconv.Atoi(m[7])
		tz := strings.ToUpper(m[6])
		var t time.Time
		if tz == "UTC" || tz == "GMT" || tz == "Z" {
			t = time.Date(yr, mon, day, hh, mm, ss, 0, time.UTC)
		} else {
			// UTC 외 TZ 표기는 호스트 로컬로 해석한다(Python time.mktime 과 동일).
			t = time.Date(yr, mon, day, hh, mm, ss, 0, time.Local)
		}
		v := t.Unix()
		return &v
	}
	if m := isoLikeRe.FindStringSubmatch(txt); m != nil {
		y, _ := strconv.Atoi(m[1])
		mo, _ := strconv.Atoi(m[2])
		d, _ := strconv.Atoi(m[3])
		hh, _ := strconv.Atoi(m[4])
		mi, _ := strconv.Atoi(m[5])
		ss, _ := strconv.Atoi(m[6])
		// 호스트 TZ 에 의존하지 않는다. 'UTC 로 읽은 naive' 값.
		v := time.Date(y, time.Month(mo), d, hh, mi, ss, 0, time.UTC).Unix()
		return &v
	}
	return nil
}
