package edge

import (
	"fmt"
)

// finsReadTimeout — FINS 1회 왕복 타임아웃(Python _fins_req 의 3.0초).

// ── 옴론 PLC 폴(FINS/UDP + EtherNet/IP) ────────────────────────────────────

// plcCntSample — 트래픽 B/s 계산용 직전 인터페이스 카운터 샘플.
type plcCntSample struct {
	TS        float64
	InOctets  uint32
	OutOctets uint32
}

// plcStatic — PLC 의 라운드 간 유지 상태.
// Python poll_plc 는 refresh 플래그를 쓰지 않고 model 이 비었을 때만 정적
// 정보를 다시 읽는다 — FINS/CIP 정적 조회가 제어기에 부담을 주지 않도록
// 한번 잡히면 고정되는 관행을 그대로 유지한다.
type plcStatic struct {
	Model    string
	FW       string
	CIP      *cipIdentity
	Eth      map[string]any
	Hostname string
	// FINS 확장 명령 지원 여부 1회 판별(NX 시리즈는 0602/2101 이 0401=미지원).
	HasCycle  bool
	HasErrlog bool
	Cnt       *plcCntSample
}

var cipStateText = map[int]string{
	0: "Nonexistent", 1: "Self testing", 2: "Standby",
	3: "Operational", 4: "Major recoverable fault",
	5: "Major unrecoverable fault",
}

// pollPLC — Python poll_plc 이식.
func (w *Worker) pollPLC(pc *pollCtx, dev DeviceConfig, st *plcStatic) (map[string]any, *plcStatic) {
	ip := dev.IP
	port := dev.FinsPort
	if port == 0 {
		port = 9600
	}
	sa1 := dev.FinsSrcNode
	if sa1 == 0 {
		sa1 = 84
	}

	stat, rtt := finsReq(pc.ctx, ip, port, []byte{0x06, 0x01}, byte(sa1), finsReadTimeout)
	fst, ok := parseFINSStatus(stat)
	if !finsOK(stat) || !ok {
		d := baseDevice(dev, "PLC", "down", 0)
		m := d["meta"].(map[string]any)
		if a := stateAlert("down", pc.now); a != nil {
			m["alerts"] = []any{a}
		}
		model, fw := "", ""
		if st != nil {
			model, fw = st.Model, st.FW
		}
		m["plc"] = map[string]any{
			"runState": "", "hasError": false, "errSev": "", "errSince": "",
			"errorMessage": "", "protocol": "FINS", "port": port,
			"maker": "OMRON", "model": model,
			"detectedModel": model,
			"productName":   trimSpaceASCII("OMRON " + model),
			"fwVersion":     fw, "unitRev": "",
			"serial": "", "finsRttMs": nil,
			"cipStatus": "", "cipMajorFault": false, "cipMinorFault": false,
			"linkSpeedMbps": nil, "linkFullDuplex": nil,
			"clockSkewSec": nil, "procVars": []any{},
		}
		return d, st
	}

	runState := fst.RunState()
	hasError := fst.Fatal != 0
	errSev := ""
	if fst.Fatal != 0 {
		errSev = "major"
	} else if fst.NonFatal != 0 {
		errSev = "minor"
	}

	if st == nil || st.Model == "" {
		st = w.plcInitStatic(pc, ip, port, byte(sa1))
	}

	var cycle *finsCycle
	if st.HasCycle {
		if c, ok := parseFINSCycle(finsReqData(pc, ip, port, []byte{0x06, 0x02, 0x00}, byte(sa1))); ok {
			cycle = &c
		}
	}
	errlog := []finsErrEntry{}
	if st.HasErrlog {
		errlog = parseFINSErrlog(finsReqData(pc, ip, port, []byte{0x21, 0x01, 0x00, 0x00, 0x00, 0x05}, byte(sa1)), 5)
	}

	// 실측 트래픽 — 카운터 델타/시간 = B/s. 카운터 랩어라운드(32비트)면 그 라운드는 건드너뛴다.
	cnt := cipIfCountersReq(pc.ctx, ip)
	var inBps, outBps *int64
	if cnt != nil && st.Cnt != nil && pc.now > st.Cnt.TS {
		dt := pc.now - st.Cnt.TS
		din := int64(cnt.InOctets) - int64(st.Cnt.InOctets)
		dout := int64(cnt.OutOctets) - int64(st.Cnt.OutOctets)
		if din >= 0 && din < 1<<31 && dout >= 0 && dout < 1<<31 && dt < 600 {
			inBps = i64p(int64(float64(din) / dt))
			outBps = i64p(int64(float64(dout) / dt))
		}
	}
	if cnt != nil {
		st.Cnt = &plcCntSample{TS: pc.now, InOctets: cnt.InOctets, OutOctets: cnt.OutOctets}
	}

	var skewPtr *int64
	if epoch, ok := parseFINSClock(finsReqData(pc, ip, port, []byte{0x07, 0x01}, byte(sa1))); ok {
		skew := int64(float64(epoch) - (pc.now + kstOffset.Seconds()))
		skewPtr = i64p(skew)
	}

	status := "op"
	if hasError || errSev == "minor" {
		status = "deg"
	}
	if runState == "STOP" {
		status = "deg"
	}

	var cip cipIdentity
	if st.CIP != nil {
		cip = *st.CIP
	}
	eth := st.Eth
	if eth == nil {
		eth = map[string]any{}
	}
	errMsg := fst.Msg
	if hasError {
		errMsg = fmt.Sprintf("FAL %04X", fst.Fatal)
		if fst.Msg != "" {
			errMsg += " — " + fst.Msg
		}
	}
	errSince := ""
	if hasError || errSev != "" {
		errSince = tsKST(pc.now)
	}
	productName := cip.Name
	if productName == "" {
		productName = trimSpaceASCII("OMRON " + st.Model)
	}
	cipStatus := ""
	if st.CIP != nil {
		cipStatus = cipStateText[cip.State]
	}
	var ioConn any
	if st.CIP != nil {
		ioConn = cip.IOConn
	}
	fatalCode, nonFatalCode := "", ""
	if fst.Fatal != 0 {
		fatalCode = fmt.Sprintf("%04X", fst.Fatal)
	}
	if fst.NonFatal != 0 {
		nonFatalCode = fmt.Sprintf("%04X", fst.NonFatal)
	}
	var cycleAvg, cycleMax, cycleMin any
	if cycle != nil {
		cycleAvg, cycleMax, cycleMin = cycle.Avg, cycle.Max, cycle.Min
	}
	errlogOut := []any{}
	for _, e := range errlog {
		errlogOut = append(errlogOut, map[string]any{
			"code": e.Code, "detail": e.Detail, "time": e.Time,
		})
	}
	var crcErr, collisions any
	if cnt != nil {
		if cnt.CRCErrors != nil {
			crcErr = int64(*cnt.CRCErrors)
		}
		if cnt.Collisions != nil {
			collisions = int64(*cnt.Collisions)
		}
	}
	var inErrs, outErrs any
	if cnt != nil {
		inErrs = int64(cnt.InErrors)
		outErrs = int64(cnt.OutErrors)
	}

	d := baseDevice(dev, "PLC", status, 0)
	m := d["meta"].(map[string]any)
	m["vendor"] = "OMRON"
	m["version"] = st.FW
	m["plc"] = map[string]any{
		"runState":     runState,
		"hasError":     hasError,
		"errSev":       errSev,
		"errSince":     errSince,
		"errorMessage": errMsg,
		"protocol":     "FINS", "port": port,
		"maker": "OMRON", "model": st.Model,
		"detectedModel": st.Model,
		"productName":   productName,
		"fwVersion":     st.FW, "unitRev": cip.Rev,
		"serial": cip.Serial, "finsRttMs": rtt,
		"cipStatus":     cipStatus,
		"cipMajorFault": st.CIP != nil && cip.MajorFault,
		"cipMinorFault": st.CIP != nil && cip.MinorFault,
		"ioConn":        ioConn,
		"linkSpeedMbps": eth["speedMbps"], "linkFullDuplex": eth["fullDuplex"],
		"mac":      strOr(eth["mac"], ""),
		"hostname": st.Hostname,
		"netMask":  strOr(eth["netMask"], ""), "gateway": strOr(eth["gateway"], ""),
		"clockSkewSec": numOrNil(skewPtr), "procVars": []any{},
		"finsFatalCode": fatalCode, "finsNonFatalCode": nonFatalCode,
		"netInBps": numOrNil(inBps), "netOutBps": numOrNil(outBps),
		"netInErrors": inErrs, "netOutErrors": outErrs,
		"netCrcErrors": crcErr, "netCollisions": collisions,
		"cycleAvgMs": cycleAvg, "cycleMaxMs": cycleMax, "cycleMinMs": cycleMin,
		"errorLog": errlogOut,
	}

	alerts := []any{}
	if a := stateAlert(status, pc.now); a != nil {
		alerts = append(alerts, a)
	}
	if hasError {
		desc := errMsg
		if desc == "" {
			desc = "Controller error flag set"
		}
		alerts = append(alerts, map[string]any{
			"name": "PLC_ERROR", "desc": desc,
			"time": tsKST(pc.now), "severity": "critical", "sev": "critical",
		})
	} else if errSev == "minor" {
		alerts = append(alerts, map[string]any{
			"name": "PLC_WARNING", "desc": "Non-fatal error flag set",
			"time": tsKST(pc.now), "severity": "warning", "sev": "warning",
		})
	}
	m["alerts"] = capAlerts(alerts)
	return d, st
}

// plcInitStatic — model/FW(0501) + CIP identity + 이더넷 링크 + 확장 명령 지원 판별.
func (w *Worker) plcInitStatic(pc *pollCtx, ip string, port int, sa1 byte) *plcStatic {
	model, fw := "", ""
	if md, _ := finsReq(pc.ctx, ip, port, []byte{0x05, 0x01}, sa1, finsReadTimeout); finsOK(md) && len(md) >= 34 {
		model = cleanStr(string(md[14:34]))
		if len(md) >= 54 {
			fw = cleanStr(string(md[34:54]))
		}
	}
	eth := cipEthLink(pc.ctx, ip)
	st := &plcStatic{
		Model: model, FW: fw,
		CIP:      cipIdentityReq(pc.ctx, ip, cipIDTimeout),
		Eth:      eth,
		Hostname: strOr(eth["hostname"], ""),
	}
	// FINS 확장 명령 지원 여부 1회 판별(NX 시리즈는 0602/2101 이 0401=미지원).
	_, st.HasCycle = parseFINSCycle(finsReqData(pc, ip, port, []byte{0x06, 0x02, 0x00}, sa1))
	st.HasErrlog = len(parseFINSErrlog(finsReqData(pc, ip, port, []byte{0x21, 0x01, 0x00, 0x00, 0x00, 0x05}, sa1), 5)) > 0
	return st
}

// finsReqData — data 만 필요한 호출 축약.
func finsReqData(pc *pollCtx, ip string, port int, cmd []byte, sa1 byte) []byte {
	d, _ := finsReq(pc.ctx, ip, port, cmd, sa1, finsReadTimeout)
	return d
}

// strOr — any → string, 없으면 defv (Python dict.get(...) or "" 관행).
func strOr(v any, defv string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return defv
}
