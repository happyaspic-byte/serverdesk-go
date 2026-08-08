package edge

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── FINS/UDP 읽기전용 클라이언트 (옴론 PLC) ──────────────────────────────
// 읽기전용 계약: FINS 는 READ 명령만 사용 — 0501(기종), 0601(상태), 0602(사이클,
// 파라미터 0x00=읽기), 0701(시계), 2101(에러 로그 읽기). 쓰기 계열(메모리 쓰기
// 0102, 모드 변경 0402, 사이클 초기화 0602/0x01, 로그 클리어 2103, 강제 세트
// 2301 등)은 절대 금지 — 모니터링이 설비를 건드리면 안 된다.

// finsReadTimeout — FINS 1회 왕복 타임아웃(Python _fins_req 의 3.0초).
const finsReadTimeout = 3 * time.Second

// finsDA1 — FINS DA1(대상 노드)은 관행적으로 대상 IP 의 마지막 옥텟.
func finsDA1(ip string) byte {
	var last int
	for i := len(ip) - 1; i >= 0; i-- {
		if ip[i] == '.' {
			last, _ = strconv.Atoi(ip[i+1:])
			break
		}
	}
	return byte(last)
}

// buildFINSFrame — 10바이트 FINS/UDP 헤더 + 명령.
// ICF=0x80, RSV=0, GCT=0x02, DNA=0, DA1, DA2=0, SNA=0, SA1, SA2=0, SID=0.
func buildFINSFrame(da1, sa1 byte, cmd []byte) []byte {
	hdr := []byte{0x80, 0x00, 0x02, 0x00, da1, 0x00, 0x00, sa1, 0x00, 0x00}
	return append(hdr, cmd...)
}

// finsOK — 응답 유효: 최소 길이 + MRES/SRES(12,13바이트) 0.
func finsOK(d []byte) bool {
	return d != nil && len(d) >= 14 && d[12] == 0 && d[13] == 0
}

// bcd — BCD 바이트→정수.
func bcd(b byte) int { return int(b>>4)*10 + int(b&0x0F) }

// finsReq — FINS 명령 1회 송수신(UDP). 실패 시 data=nil.
// rtt 는 밀리초(소수 1자리) — 링크 상태 감각용으로 Python 과 같은 스케일.
func finsReq(ctx context.Context, ip string, port int, cmd []byte, sa1 byte, timeout time.Duration) ([]byte, float64) {
	frame := buildFINSFrame(finsDA1(ip), sa1, cmd)
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(ip, strconv.Itoa(port)))
	if err != nil {
		return nil, 0
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	t0 := time.Now()
	if _, err := conn.Write(frame); err != nil {
		return nil, 0
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, 0
	}
	rtt := round1(float64(time.Since(t0)) / float64(time.Millisecond))
	return buf[:n], rtt
}

// finsStatus — CONTROLLER STATUS READ(0601) 응답 해석 결과.
type finsStatus struct {
	RunBit   bool   // 운전 비트(14번 바이트 bit0)
	Mode     int    // 15번 바이트 (0=PROGRAM 2=MONITOR 4=RUN), 없으면 -1
	Fatal    uint16 // 치명 에러 워드(BE, 16:18)
	NonFatal uint16 // 비치명 에러 워드(BE, 18:20)
	Msg      string // 24:40 ASCII 메시지(인쇄가능문자만)
}

// parseFINSStatus — 0601 응답 해석. finsOK 가 참인 프레임만 넣을 것.
// Python 은 길이 부족 시 IndexError 로 라운드가 죽었으나, Go 는 ok=false 로
// 낮춰 같은 "응답 이상 → down" 결과를 만든다.
func parseFINSStatus(d []byte) (st finsStatus, ok bool) {
	if len(d) < 15 {
		return st, false
	}
	st.RunBit = d[14]&0x01 != 0
	st.Mode = -1
	if len(d) > 15 {
		st.Mode = int(d[15])
	}
	if len(d) >= 18 {
		st.Fatal = binary.BigEndian.Uint16(d[16:18])
	}
	if len(d) >= 20 {
		st.NonFatal = binary.BigEndian.Uint16(d[18:20])
	}
	if len(d) > 24 {
		end := 40
		if len(d) < end {
			end = len(d)
		}
		for _, c := range d[24:end] {
			if c >= 32 && c < 127 {
				st.Msg += string(c)
			}
		}
		st.Msg = strings.TrimSpace(st.Msg)
	}
	return st, true
}

// RunState — 모드 바이트→운전 상태 문자열.
// 모드가 없으면 운전 비트로 RUN/STOP 을 추정한다(Python 과 동일).
func (st finsStatus) RunState() string {
	switch st.Mode {
	case 0:
		return "PROGRAM"
	case 2:
		return "MONITOR"
	case 4:
		return "RUN"
	}
	if st.RunBit {
		return "RUN"
	}
	return "STOP"
}

// finsCycle — CYCLE TIME READ(0602) 결과(0.1ms 단위 min/avg/max).
type finsCycle struct {
	Min, Avg, Max float64
}

// parseFINSCycle — 0602(파라미터 0x00=읽기) 응답 해석.
// 3개의 BE uint32(0.1ms 단위)를 정렬해 min/avg/max 로 쓴다.
// 10분(600000ms) 초과 사이클은 파싱 이상으로 간주해 버린다(Python 과 동일 기준).
func parseFINSCycle(d []byte) (finsCycle, bool) {
	var c finsCycle
	if !finsOK(d) || len(d) < 26 {
		return c, false
	}
	vals := make([]float64, 3)
	for i := 0; i < 3; i++ {
		vals[i] = float64(binary.BigEndian.Uint32(d[14+i*4:18+i*4])) / 10.0
	}
	sort.Float64s(vals)
	if vals[2] > 600000 {
		return c, false
	}
	return finsCycle{Min: round1(vals[0]), Avg: round1(vals[1]), Max: round1(vals[2])}, true
}

// finsErrEntry — ERROR LOG READ(2101) 1건. 읽기 전용(클리어 2103 은 금지).
type finsErrEntry struct {
	Code   string // 에러 코드(대문자 hex 4자리)
	Detail string // 상세 코드(대문자 hex 4자리)
	Time   string // "20YY-MM-DD HH:MM" (PLC 로컬시각), 유효하지 않으면 ""
}

// parseFINSErrlog — 2101 응답 해석. 최대 count 건.
// 레코드: code(2,BE) detail(2,BE) 분·초·일·시·년·월 BCD 6바이트 = 10바이트.
func parseFINSErrlog(d []byte, count int) []finsErrEntry {
	out := []finsErrEntry{}
	if !finsOK(d) || len(d) < 20 {
		return out
	}
	n := int(binary.BigEndian.Uint16(d[18:20]))
	if n > count {
		n = count
	}
	off := 20
	for i := 0; i < n; i++ {
		if len(d) < off+10 {
			break
		}
		code := binary.BigEndian.Uint16(d[off : off+2])
		det := binary.BigEndian.Uint16(d[off+2 : off+4])
		mi, se, da, ho, yr, mo := bcd(d[off+4]), bcd(d[off+5]), bcd(d[off+6]), bcd(d[off+7]), bcd(d[off+8]), bcd(d[off+9])
		ts := ""
		if mo >= 1 && mo <= 12 && da >= 1 && da <= 31 {
			ts = fmt.Sprintf("20%02d-%02d-%02d %02d:%02d", yr, mo, da, ho, mi)
		}
		_ = se
		out = append(out, finsErrEntry{
			Code:   fmt.Sprintf("%04X", code),
			Detail: fmt.Sprintf("%04X", det),
			Time:   ts,
		})
		off += 10
	}
	return out
}

// parseFINSClock — CLOCK READ(0701) 응답의 BCD 시계를 epoch(UTC 환산)으로.
// PLC 시계는 KST 로컬시각이므로 naive UTC 환산 후 서버 UTC+9h 와 비교한다.
// Python calendar.timegm 은 월/일만 검증하고 시·분·초는 검증하지 않는다 —
// 같은 규칙을 따른다.
func parseFINSClock(d []byte) (int64, bool) {
	if !finsOK(d) || len(d) < 20 {
		return 0, false
	}
	yy, mm, dd := bcd(d[14]), bcd(d[15]), bcd(d[16])
	hh, mi, ss := bcd(d[17]), bcd(d[18]), bcd(d[19])
	year := 2000 + yy
	if yy >= 70 {
		year = 1900 + yy
	}
	return timegmLike(year, mm, dd, hh, mi, ss)
}

// timegmLike — Python calendar.timegm 과 같은 검증 규칙의 UTC 환산:
// datetime.date 가 월(1..12)·일(1..그 달의 말일)만 검증하고,
// 시·분·초는 범위 검증 없이 산술로만 반영한다.
func timegmLike(year, month, day, hh, mi, ss int) (int64, bool) {
	if month < 1 || month > 12 {
		return 0, false
	}
	// 그 달의 말일: 다음 달 0일 = 이번 달 마지막 날.
	last := time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day < 1 || day > last {
		return 0, false
	}
	base := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC).Unix()
	return base + int64(hh)*3600 + int64(mi)*60 + int64(ss), true
}
