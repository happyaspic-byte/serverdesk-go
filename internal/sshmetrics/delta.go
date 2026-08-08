package sshmetrics

import (
	"math"
	"time"
)

// delta.go 는 누적 카운터 두 개의 차이로 비율/증분을 만드는 계산들이다.
// 전부 "직전 샘플이 있을 때만" 의미가 있다 — 첫 샘플은 델타 기준이 없어
// 결과를 내지 않는다(Python: m["cpu_pct"] = None, net/diskio 키 자체를 생략).

// cpuPctFromDelta 는 /proc/stat cpu 행의 폴 간 델타로 사용률(%)을 계산한다.
// jiffies 는 누적값이라 단일 샘플로는 계산할 수 없고, idle+iowait(d[3]+d[4])를
// 유휴로 본다. total 이 0 이하면(카운터 리셋 직후 등) nil 을 반환한다.
func cpuPctFromDelta(cur, prev []int64) *float64 {
	if len(cur) < 8 || len(prev) < 8 {
		return nil
	}
	var total, idle int64
	for i := 0; i < 8; i++ {
		d := cur[i] - prev[i]
		total += d
		if i == 3 || i == 4 {
			idle += d
		}
	}
	if total <= 0 {
		return nil
	}
	pct := roundN(clamp(float64(total-idle)/float64(total)*100, 0, 100), 1)
	return &pct
}

// rate 는 초당 증분을 소수 1자리로 반올림해 돌려준다.
// 카운터가 뒤로 감겼으면(리셋/랩) nil — 음수 속도를 0 으로 뭉개지 않고
// 호출자가 "없음" 으로 처리하게 한다(Python: rate() -> None).
func rate(cur, prev int64, dt float64) *float64 {
	if dt <= 0 || cur < prev {
		return nil
	}
	v := roundN(float64(cur-prev)/dt, 1)
	return &v
}

// rateOr0 는 rate 의 nil 을 0 으로 바꾼다. Python 의 `(rate(...) or 0)` 에 해당한다.
func rateOr0(cur, prev int64, dt float64) float64 {
	if r := rate(cur, prev, dt); r != nil {
		return *r
	}
	return 0
}

// applyDeltasLocked 은 직전 샘플(h.prev)과 현재 샘플(s)로 파생 메트릭을 채운다.
// Runner.mu 를 쥔 상태에서만 호출한다. 첫 샘플이면 CPUPct 는 nil 로 두고
// Net/DiskIO 는 만들지 않는다 — 누적값(이미 수천만 건인 drop 등)을 절대값으로
// 보여주는 사고를 막기 위해 Python 과 같은 방식으로 아예 생략한다.
func applyDeltasLocked(h *hostState, m *Metrics, s *rawSample) {
	prev := h.prev
	if prev == nil {
		return
	}
	// dt 는 노드 시각끼리의 차이(T= 행)라 폴-노드 시계 오차와 무관하다.
	dt := math.Max(1e-6, float64(s.ts-prev.ts))
	m.CPUPct = cpuPctFromDelta(s.cpuJiffies, prev.cpuJiffies)

	var nets []NetRate
	for _, name := range sortedKeys(s.netRaw) {
		cur := s.netRaw[name]
		item := NetRate{
			Name:                 name,
			GuestTap:             cur.guestTap,
			Interconnect:         cur.interconnect,
			InterconnectEvidence: cur.interconnectEvidence,
		}
		if p, ok := prev.netRaw[name]; ok {
			item.RxBps = fptr(rateOr0(cur.rxBytes, p.rxBytes, dt) * 8)
			item.TxBps = fptr(rateOr0(cur.txBytes, p.txBytes, dt) * 8)
			item.RxDropDelta = i64ptr(max(cur.rxDrop-p.rxDrop, 0))
			item.TxDropDelta = i64ptr(max(cur.txDrop-p.txDrop, 0))
			item.RxErrDelta = i64ptr(max(cur.rxErrs-p.rxErrs, 0))
			item.TxErrDelta = i64ptr(max(cur.txErrs-p.txErrs, 0))
		}
		nets = append(nets, item)
	}
	if len(nets) > 0 {
		m.Net = nets
		var drops int64
		for _, x := range nets {
			if x.Interconnect {
				drops += derefI64(x.RxDropDelta) + derefI64(x.TxDropDelta)
			}
		}
		m.InterconnectDrops = &drops
	}

	var dios []DiskIO
	for _, name := range sortedKeys(s.diskRaw) {
		cur := s.diskRaw[name]
		item := DiskIO{Name: name}
		if p, ok := prev.diskRaw[name]; ok {
			item.ReadBps = fptr(rateOr0(cur.readSectors, p.readSectors, dt) * 512)
			item.WriteBps = fptr(rateOr0(cur.writeSectors, p.writeSectors, dt) * 512)
			busy := float64(cur.ioMS-p.ioMS) / (dt * 1000.0) * 100
			item.BusyPct = fptr(roundN(clamp(busy, 0, 100), 1))
		}
		dios = append(dios, item)
	}
	if len(dios) > 0 {
		m.DiskIO = dios
	}
}

// rebootCheckLocked 은 uptime 이 뒤로 감기면 리부트로 간주하고 24시간 동안 표시한다.
// 120초 여유는 collect 주기 내 uptime 오차·지연을 흡수한다(Python: secs + 120 < prev).
// 리부트 시각(rebootAt)은 SSH 실패와 무관하게 호스트 상태에 남는다 — 실패 동안의
// 리부트도 다음 성공 수집에서 잡아내기 위해서다.
func rebootCheckLocked(h *hostState, m *Metrics, now time.Time) {
	if m.UptimeSecs == nil {
		return
	}
	secs := *m.UptimeSecs
	if h.uptimeLast != nil && secs+120 < *h.uptimeLast {
		at := now
		h.rebootAt = &at
	}
	h.uptimeLast = &secs
	if h.rebootAt != nil {
		ago := now.Sub(*h.rebootAt)
		if ago > 24*time.Hour {
			h.rebootAt = nil
		} else {
			at := h.rebootAt.Unix()
			m.RebootedAt = &at
			agoSecs := int64(ago.Seconds())
			m.RebootAgoSecs = &agoSecs
		}
	}
	m.RecentlyBooted = bptr(secs < 3600)
}

func clamp(x, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, x))
}

func i64ptr(v int64) *int64 { return &v }

func derefI64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
