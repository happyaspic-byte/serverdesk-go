package edge

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── 프린터 폴(static 캐시 + 용지 래치 + 토너 센티널) ──────────────────────

type supplyInfo struct {
	Name string
	Max  *int64
	Lvl  *int64
}

type trayInfo struct {
	Name  string
	Max   *int64
	Level *int64
}

// printerStatic — 프린터의 라운드 간 유지 상태.
// 절전 중에는 SNMP 응답이 느리고 일부 테이블이 쪼그라들어 있으므로
// 정적 정보는 캐시해 두고 shrink-guard 로 보호한다.
type printerStatic struct {
	Model  string
	Serial string
	Sup    map[int]*supplyInfo
	Trays  []*trayInfo
	Web    *syncThruData

	// AlertSince — 지속 경보의 '최초 발생 시각'. 매 라운드 time 이 갱신되면
	// 로그 화면에 같은 경보가 1분마다 새 이벤트처럼 쌓인다(스팸).
	AlertSince map[string]string

	// PaperLatch — 용지 소진 래치. hrPrinterDetectedErrorState 비트는 절전
	// 진입 시 풀려 휘발되므로(실측), 한번 관측되면 명시적 해제 조건까지 유지.
	PaperLatch bool
	LastPages  *int64

	// TrayLevelSuspect — 이 기종은 용지가 있어도 트레이 잔량을 0 으로 보고한다
	// (실측: level 0 인 채 인쇄 성공). '잔량 0 인데 인쇄 성공'이 관측되면
	// 이 장비의 잔량 센서는 무의미하다고 학습한다.
	TrayLevelSuspect bool
}

// prtErrNames — hrPrinterDetectedErrorState 첫 옥텟(MSB first) → 이름 목록.
func prtErrNames(b0 byte) []string {
	out := []string{}
	for i := 0; i < 8; i++ {
		if b0&(0x80>>uint(i)) != 0 {
			out = append(out, prtErrBits[i])
		}
	}
	return out
}

func hasPaperBit(errors []string) bool {
	for _, e := range errors {
		if e == "No paper" || e == "Low paper" {
			return true
		}
	}
	return false
}

// paperLatchStep — 용지 소진 래치 상태기계(순수 함수).
//
//	세트: no/low-paper 비트 관측(깨어있는 동안은 정확)
//	해제: 비트 꺼진 상태에서 인쇄 성공(페이지 증가) 또는 잔량>0 관측
//
// 절전으로 비트가 사라져도 경보가 유지되고, 용지 보충 후 첫 인쇄로 자동 해제된다.
// 반환: 새 래치 상태, 표시용 "No paper" 합성 여부.
func paperLatchStep(prevLatch, bitPaper, trayHasPositive, printed bool) (latched, synthErr bool) {
	latched = prevLatch || bitPaper
	if latched && !bitPaper && (trayHasPositive || printed) {
		latched = false
	}
	return latched, latched && !bitPaper
}

// supplyPct — 소모품 잔량 %. 센티널: -2=미보고/알수없음, 음수 그대로(-3=잔량
// 있음 OK 센티널) — 프런트가 이 관행으로 렌더링한다.
func supplyPct(lvl, mx *int64) int64 {
	if lvl == nil {
		return -2
	}
	if *lvl < 0 {
		return *lvl
	}
	if mx != nil && *mx > 0 {
		return iround(float64(*lvl) * 100.0 / float64(*mx))
	}
	return -2
}

// lowTonerName — "toner" 가 이름에 있고 0..10% 면 저토너 경보 대상.
func lowTonerName(name string, pct int64) bool {
	return pct >= 0 && pct <= 10 && strings.Contains(strings.ToLower(name), "toner")
}

// traysOut — 표출용 트레이 목록. 학습된 무의미 센서(trayLevelSuspect)의
// 0/음수 잔량은 -2(미보고) 센티널로 낸다. 원본(static)은 건드리지 않는다 —
// 용지 래치 판정이 원본 잔량을 쓰므로. 프런트는 -2 를 '—' 로 표기한다.
func traysOut(st *printerStatic) []any {
	out := []any{}
	if st == nil {
		return out
	}
	for _, t := range st.Trays {
		var lv any
		if t.Level != nil {
			lv = *t.Level
			if st.TrayLevelSuspect && *t.Level <= 0 {
				lv = int64(-2)
			}
		}
		out = append(out, map[string]any{
			"name": t.Name, "max": numOrNil(t.Max), "level": lv,
		})
	}
	return out
}

// printerStatus — 상태 판정: hrDeviceStatus 3(warning)/5(down) 이나 에러 비트,
// 저토너가 있으면 deg. (down 은 SNMP 무응답 경로에서만 나온다.)
func printerStatus(dv int64, errors []string, lowToner bool) string {
	status := "op"
	if dv == 3 {
		status = "deg"
	}
	if dv == 5 || containsStr(errors, "Offline") {
		status = "deg"
	}
	if (len(errors) > 0 || lowToner) && status == "op" {
		status = "deg"
	}
	return status
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// pollPrinter — Python poll_printer 이식.
func (w *Worker) pollPrinter(pc *pollCtx, dev DeviceConfig, st *printerStatic) (map[string]any, *printerStatic) {
	ip := dev.IP
	comm := dev.Community
	if comm == "" {
		comm = "public"
	}
	r := pc.snmp.call(pc.ctx, ip, comm, []string{
		oSysUptime, oPrtDevStatus, oPrtStatus, oPrtErrState, oPrtPages}, 3*time.Second)
	if len(r) == 0 {
		d := baseDevice(dev, "PRN", "down", 0)
		if a := stateAlert("down", pc.now); a != nil {
			d["meta"].(map[string]any)["alerts"] = []any{a}
		}
		return d, st
	}

	upTicks, _ := vnum(r[oSysUptime])
	upDays := upTicks / 100 / daySec
	dv := vnumOr(r[oPrtDevStatus], 2)
	pst := vnumOr(r[oPrtStatus], 1)

	errors := []string{}
	if b0, ok := vbyte0(r[oPrtErrState]); ok && b0 != 0 {
		errors = prtErrNames(byte(b0))
	}

	if pc.refresh || st == nil || st.Model == "" {
		st = w.printerRefreshStatic(pc, dev, comm, st)
	} else {
		// 토너 잔량만 fast 갱신.
		for i, si := range st.Sup {
			si2 := pc.snmp.call(pc.ctx, ip, comm,
				[]string{oPrtSupLvl + strconv.Itoa(i)}, 2*time.Second)
			if v, ok := vnum(si2[oPrtSupLvl+strconv.Itoa(i)]); ok {
				si.Lvl = i64p(v)
			}
		}
		// 트레이 '레벨'도 fast 갱신 — 용지 소진/보충이 1분 내 반영돼야 경보가
		// 즉시 뜨고 걷힌다. (이름·용량은 정적 주기 유지. OID 인덱스 = 수집 순서 1..N.)
		for i, tr := range st.Trays {
			oid := oPrtTrayLvl + strconv.Itoa(i+1)
			ti := pc.snmp.call(pc.ctx, ip, comm, []string{oid}, 2*time.Second)
			if v, ok := vnum(ti[oid]); ok {
				tr.Level = i64p(v)
			}
		}
	}

	supplies := []any{}
	lowToner := false
	keys := make([]int, 0, len(st.Sup))
	for k := range st.Sup {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		si := st.Sup[k]
		pct := supplyPct(si.Lvl, si.Max)
		supplies = append(supplies, map[string]any{"name": si.Name, "pct": pct})
		if lowTonerName(si.Name, pct) {
			lowToner = true
		}
	}

	// 용지 소진 — 래치 방식(상태기계는 paperLatchStep 참조).
	pagesNow, pagesOK := vnum(r[oPrtPages])
	var pagesPtr *int64
	if pagesOK {
		pagesPtr = i64p(pagesNow)
	}
	bitPaper := hasPaperBit(errors)
	trayHas := false
	for _, t := range st.Trays {
		if t.Level != nil && *t.Level > 0 {
			trayHas = true
			break
		}
	}
	printed := st.LastPages != nil && pagesPtr != nil && *pagesPtr > *st.LastPages
	latched, synthErr := paperLatchStep(st.PaperLatch, bitPaper, trayHas, printed)
	if synthErr {
		errors = append(errors, "No paper") // 래치 유지분 — 비트 휘발 후에도 표시
	}
	st.PaperLatch = latched
	if pagesPtr != nil {
		st.LastPages = pagesPtr
	}

	// 트레이 잔량 센서 학습(구조체 주석 참조). 양수 잔량이 다시 보이면 해제.
	// Python 의 `(t.get("level") or 0) == 0` 은 미보고(None)도 0 으로 본다.
	zeroSeen := false
	for _, t := range st.Trays {
		if t.Level == nil || *t.Level == 0 {
			zeroSeen = true
			break
		}
	}
	if printed && zeroSeen {
		st.TrayLevelSuspect = true
	}
	if trayHas {
		st.TrayLevelSuspect = false
	}

	status := printerStatus(dv, errors, lowToner)

	d := baseDevice(dev, "PRN", status, upDays)
	m := d["meta"].(map[string]any)
	m["vendor"] = "Samsung Electronics"
	m["version"] = st.Model
	m["snmp"] = []any{map[string]any{
		"ip": ip, "reachable": true, "uptime_days": upDays,
		"uptime_secs": upTicks / 100,
		"serial":      st.Serial, "source": "snmp",
	}}
	var web syncThruData
	if st.Web != nil {
		web = *st.Web
	}
	var monoTotal, colorTotal any
	if st.Web != nil {
		monoTotal, colorTotal = web.MonoTotal, web.ColorTotal
	}
	pstText := "idle"
	switch pst {
	case 3:
		pstText = "idle"
	case 4:
		pstText = "printing"
	case 5:
		pstText = "warmup"
	}
	m["printer"] = map[string]any{
		"status":   pstText,
		"pages":    numOrNil(pagesPtr),
		"serial":   st.Serial,
		"model":    st.Model,
		"errors":   errors,
		"supplies": supplies,
		"trays":    traysOut(st),
		// SyncThru 웹(있으면) — 상태 텍스트·정체·카운터 분해.
		"statusText": web.StatusText,
		"webModel":   web.WebModel,
		"productNum": web.ProductNum,
		"hostName":   web.HostName,
		"mac":        web.MAC,
		"location":   web.Location,
		"monoTotal":  monoTotal,
		"colorTotal": colorTotal,
		"tonerCnt":   tonerCntOrEmpty(st.Web),
	}

	// 지속 경보는 '최초 발생 시각'을 유지한다(AlertSince 주석 참조).
	since := map[string]string{}
	firstTS := func(k string) string {
		if t := st.AlertSince[k]; t != "" {
			since[k] = t
		} else {
			since[k] = tsKST(pc.now)
		}
		return since[k]
	}
	alerts := []any{}
	if a := stateAlert(status, pc.now); a != nil {
		alerts = append(alerts, a)
	}
	for _, e := range errors {
		sev := "warning"
		if prtErrCrit[e] {
			sev = "critical"
		}
		alerts = append(alerts, map[string]any{
			"name": "PRINTER_ERROR", "desc": e, "time": firstTS(e),
			"severity": sev, "sev": sev,
		})
	}
	if lowToner {
		alerts = append(alerts, map[string]any{
			"name": "TONER_LOW", "desc": "Toner level ≤ 10%",
			"time": firstTS("TONER_LOW"), "severity": "warning", "sev": "warning",
		})
	}
	st.AlertSince = since
	m["alerts"] = capAlerts(alerts)
	return d, st
}

// printerRefreshStatic — 정적 정보 재조회(모델·시리얼·소모품·트레이·웹).
// 절전 중 재조회는 느려서 목록이 쪼그라들 수 있다 — 기존 캐시보다 빈약하면
// 기존 목록을 유지한다(shrink-guard). AlertSince 등 래치 상태는 Python 이
// 정적 재조회 라운드에 들고 가지 않는 것과 같은 집합만 보존한다.
func (w *Worker) printerRefreshStatic(pc *pollCtx, dev DeviceConfig, comm string, prev *printerStatic) *printerStatic {
	ip := dev.IP
	sr := pc.snmp.call(pc.ctx, ip, comm, []string{oPrtModel, oPrtSerial}, 3*time.Second)
	sup := map[int]*supplyInfo{}
	for i := 1; i <= 8; i++ {
		d := oPrtSupDesc + strconv.Itoa(i)
		mx := oPrtSupMax + strconv.Itoa(i)
		lv := oPrtSupLvl + strconv.Itoa(i)
		si := pc.snmp.call(pc.ctx, ip, comm, []string{d, mx, lv}, 2*time.Second)
		name := vstr(si[d])
		if name == "" {
			break
		}
		sup[i] = &supplyInfo{Name: name, Max: vnumPtr(si[mx]), Lvl: vnumPtr(si[lv])}
	}
	trays := []*trayInfo{}
	for i := 1; i <= 4; i++ {
		nm := oPrtTrayName + strconv.Itoa(i)
		mx := oPrtTrayMax + strconv.Itoa(i)
		lv := oPrtTrayLvl + strconv.Itoa(i)
		ti := pc.snmp.call(pc.ctx, ip, comm, []string{nm, mx, lv}, 2*time.Second)
		name := vstr(ti[nm])
		if name == "" {
			break
		}
		trays = append(trays, &trayInfo{Name: name, Max: vnumPtr(ti[mx]), Level: vnumPtr(ti[lv])})
	}
	swsClient := pc.sws
	if dev.TLSFingerprint != "" {
		swsClient = DeviceHTTPClient(5*time.Second, dev.TLSFingerprint)
	}
	n := &printerStatic{
		Model:  vstr(sr[oPrtModel]),
		Serial: vstr(sr[oPrtSerial]),
		Sup:    sup,
		Trays:  trays,
		Web:    fetchSyncThru(pc.ctx, swsClient, ip),
	}
	// shrink-guard: 재조회가 일시 실패로 빈약하면 기존 유지.
	if prev != nil {
		if len(n.Sup) < len(prev.Sup) {
			n.Sup = prev.Sup
		}
		if len(n.Trays) < len(prev.Trays) {
			n.Trays = prev.Trays
		}
		if n.Model == "" {
			n.Model = prev.Model
		}
		if n.Serial == "" {
			n.Serial = prev.Serial
		}
		if n.Web == nil {
			n.Web = prev.Web
		}
		n.AlertSince = prev.AlertSince // 지속 경보 최초시각 보존
	}
	if n.AlertSince == nil {
		n.AlertSince = map[string]string{}
	}
	return n
}

func tonerCntOrEmpty(web *syncThruData) map[string]int64 {
	if web == nil || web.TonerCnt == nil {
		return map[string]int64{}
	}
	return web.TonerCnt
}

// vnumPtr — SNMP 값을 *int64 로(없으면 nil = Python None).
func vnumPtr(v snmpValue) *int64 {
	if n, ok := vnum(v); ok {
		return i64p(n)
	}
	return nil
}

// capAlerts — Python alerts[:25].
func capAlerts(alerts []any) []any {
	if len(alerts) > 25 {
		return alerts[:25]
	}
	return alerts
}
