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

// plcStatic caches immutable identity, explicit unsupported commands and fault activation.
// Live CIP status, network configuration and link observations are read each round.
type plcStatic struct {
	Model             string
	FW                string
	CIP               *cipIdentity
	CycleUnsupported  bool
	ErrlogUnsupported bool
	CIPFault          map[string]any
	FINSFault         map[string]any
	Cnt               *plcCntSample
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
		model, fw, serial, rev := "", "", "", ""
		if st != nil {
			model, fw = st.Model, st.FW
			if st.CIP != nil {
				serial, rev = st.CIP.Serial, st.CIP.Rev
			}
			if st.FINSFault != nil {
				m["alerts"] = append(m["alerts"].([]any), observedAlert(st.FINSFault, false))
			}
			if st.CIPFault != nil {
				m["alerts"] = append(m["alerts"].([]any), observedAlert(st.CIPFault, false))
			}
		}
		m["plc"] = map[string]any{
			"runState": "", "hasError": false, "errSev": "", "errSince": "",
			"errorMessage": "", "protocol": "FINS", "port": port,
			"maker": "OMRON", "model": model,
			"detectedModel": model,
			"productName":   trimSpaceASCII("OMRON " + model),
			"fwVersion":     fw, "unitRev": rev,
			"serial": serial, "finsRttMs": nil,
			"cipStatus": "", "cipMajorFault": nil, "cipMinorFault": nil,
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

	if st == nil {
		st = w.plcInitStatic(pc, ip, port, byte(sa1))
	} else if st.Model == "" {
		identity := w.plcInitStatic(pc, ip, port, byte(sa1))
		st.Model, st.FW = identity.Model, identity.FW
	}

	var cycle *finsCycle
	if !st.CycleUnsupported {
		data := finsReqData(pc, ip, port, []byte{0x06, 0x02, 0x00}, byte(sa1))
		st.CycleUnsupported = finsUnsupported(data)
		if c, ok := parseFINSCycle(data); ok {
			cycle = &c
		}
	}
	errlog := []finsErrEntry{}
	if !st.ErrlogUnsupported {
		data := finsReqData(pc, ip, port, []byte{0x21, 0x01, 0x00, 0x00, 0x00, 0x05}, byte(sa1))
		st.ErrlogUnsupported = finsUnsupported(data)
		errlog = parseFINSErrlog(data, 5)
	}

	// 실측 트래픽 — 카운터 델타/시간 = B/s. 카운터 랩어라운드(32비트)면 그 라운드는 건드너뛴다.
	cnt := w.cipCounters(pc.ctx, ip)
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

	currentCIP := w.cipIdentity(pc.ctx, ip, cipIDTimeout)
	if currentCIP != nil && st.CIP == nil {
		identity := *currentCIP
		identity.Status, identity.State, identity.IOConn = 0, -1, 0
		identity.MajorFault, identity.MinorFault = false, false
		st.CIP = &identity
	}
	var cip cipIdentity
	if st.CIP != nil {
		cip = *st.CIP
	}
	eth := w.cipLink(pc.ctx, ip)
	_, speedKnown := eth["speedMbps"].(int64)
	_, duplexKnown := eth["fullDuplex"].(bool)
	linkUp, linkKnown := eth["linkUp"].(bool)
	complete := currentCIP != nil && speedKnown && duplexKnown && linkKnown
	if currentCIP != nil {
		if currentCIP.MajorFault {
			if st.CIPFault == nil {
				st.CIPFault = map[string]any{"name": "PLC_CIP_FAULT", "desc": "CIP major fault", "time": tsKST(pc.now), "severity": "critical", "sev": "critical"}
			}
		} else {
			st.CIPFault = nil
		}
	}
	if !complete || st.CIPFault != nil || (currentCIP != nil && currentCIP.MinorFault) || (linkKnown && !linkUp) {
		status = "deg"
	}
	errMsg := fst.Msg
	if hasError {
		errMsg = fmt.Sprintf("FAL %04X", fst.Fatal)
		if fst.Msg != "" {
			errMsg += " — " + fst.Msg
		}
	}
	errSince := ""
	if hasError {
		if st.FINSFault == nil {
			st.FINSFault = map[string]any{"name": "PLC_ERROR", "desc": errMsg, "time": tsKST(pc.now), "severity": "critical", "sev": "critical"}
		}
		errSince = st.FINSFault["time"].(string)
	} else {
		st.FINSFault = nil
		if errSev != "" {
			errSince = tsKST(pc.now)
		}
	}
	productName := cip.Name
	if productName == "" {
		productName = trimSpaceASCII("OMRON " + st.Model)
	}
	cipStatus := ""
	var ioConn, cipMajor, cipMinor any
	if currentCIP != nil {
		cipStatus = cipStateText[currentCIP.State]
		ioConn = currentCIP.IOConn
		cipMajor, cipMinor = currentCIP.MajorFault, currentCIP.MinorFault
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
	m["stale"] = !complete
	m["collection"] = map[string]any{"healthComplete": complete}
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
		"cipMajorFault": cipMajor,
		"cipMinorFault": cipMinor,
		"ioConn":        ioConn,
		"linkSpeedMbps": eth["speedMbps"], "linkFullDuplex": eth["fullDuplex"],
		"mac":      strOr(eth["mac"], ""),
		"hostname": strOr(eth["hostname"], ""),
		"linkUp":   eth["linkUp"],
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
	if st.FINSFault != nil {
		alerts = append(alerts, observedAlert(st.FINSFault, true))
	} else if errSev == "minor" {
		alerts = append(alerts, map[string]any{
			"name": "PLC_WARNING", "desc": "Non-fatal error flag set",
			"time": tsKST(pc.now), "severity": "warning", "sev": "warning",
		})
	}
	if st.CIPFault != nil {
		alerts = append(alerts, observedAlert(st.CIPFault, currentCIP != nil))
	}
	m["alerts"] = capAlerts(alerts)
	return d, st
}

// plcInitStatic — Only model/FW(0501); dynamic observations are never initialized once.
func (w *Worker) plcInitStatic(pc *pollCtx, ip string, port int, sa1 byte) *plcStatic {
	model, fw := "", ""
	if md, _ := finsReq(pc.ctx, ip, port, []byte{0x05, 0x01}, sa1, finsReadTimeout); finsOK(md) && len(md) >= 34 {
		model = cleanStr(string(md[14:34]))
		if len(md) >= 54 {
			fw = cleanStr(string(md[34:54]))
		}
	}
	return &plcStatic{Model: model, FW: fw}
}

// finsUnsupported distinguishes explicit 0401 from timeout, malformed and transient errors.
func finsUnsupported(data []byte) bool {
	return len(data) >= 14 && data[12] == 0x04 && data[13] == 0x01
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
