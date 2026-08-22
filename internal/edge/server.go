package edge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"serverdesk/internal/snmp"
)

// ── 일반 랙서버(HPE/Dell/Lenovo …) — Redfish(BMC) + SNMP(OS) 하이브리드 ────
// Redfish 는 읽기전용 GET 만 사용한다(POST/PATCH 절대 금지).
// iLO5+/iDRAC9+/XCC 공통 표준 경로만 쓴다.

const redfishTimeout = 6 * time.Second

// rfSystem — /redfish/v1/Systems/<n> 요약 1건.
type rfSystem struct {
	Path      string
	Power     string // On | Off
	Health    string // OK | Warning | Critical
	Maker     string
	Model     string
	Serial    string
	BIOS      string
	Hostname  string
	CPUModel  string
	CPUCount  any // 원본 값 그대로(벤더별 int/float 편차)
	MemGiB    any // 원본 값 그대로
	MemHealth string
}

// thermalData — 섀시 온도·팬(정적 주기 수집).
type thermalData struct {
	Temps []map[string]any
	Fans  []map[string]any
}

// srvStatic — server kind 의 라운드 간 유지 상태.
type srvStatic struct {
	Thermal      *thermalData
	ThermalTried bool // Python `"thermal" in static` 마커 — 실패핮도 기록
}

// redfishGet — Redfish 읽기전용 GET. Basic 인증, 자가서명 무검증(폐쇄망 BMC 전용).
func redfishGet(ctx context.Context, cl *http.Client, host, user, pw, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+path, nil)
	if err != nil {
		return nil, err
	}
	if user != "" {
		tok := base64.StdEncoding.EncodeToString([]byte(user + ":" + pw))
		req.Header.Set("Authorization", "Basic "+tok)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &httpStatusError{Code: resp.StatusCode}
	}
	var v any
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, &jsonSyntaxError{Err: err}
	}
	return jm(v), nil
}

// redfishFirstMember — Members[0].@odata.id (컬렉션 첫 멤버 경로).
func redfishFirstMember(root map[string]any) string {
	members := jl(root["Members"])
	if len(members) == 0 {
		return ""
	}
	return js(jm(members[0])["@odata.id"])
}

// mapRedfishSystem — 시스템 JSON → 요약. 순수 함수(테스트 대상).
func mapRedfishSystem(s map[string]any, sysPath string) *rfSystem {
	st := jm(s["Status"])
	ps := jm(s["ProcessorSummary"])
	ms := jm(s["MemorySummary"])
	return &rfSystem{
		Path:      sysPath,
		Power:     js(s["PowerState"]),
		Health:    js(st["Health"]),
		Maker:     strings.TrimSpace(js(s["Manufacturer"])),
		Model:     strings.TrimSpace(js(s["Model"])),
		Serial:    strings.TrimSpace(js(s["SerialNumber"])),
		BIOS:      strings.TrimSpace(js(s["BiosVersion"])),
		Hostname:  strings.TrimSpace(js(s["HostName"])),
		CPUModel:  strings.TrimSpace(js(ps["Model"])),
		CPUCount:  ps["Count"],
		MemGiB:    ms["TotalSystemMemoryGiB"],
		MemHealth: js(jm(ms["Status"])["Health"]),
	}
}

// fetchRedfishSystem — Systems 컬렉션 첫 멤버의 요약. 멤버가 없으면 nil.
func fetchRedfishSystem(ctx context.Context, cl *http.Client, bmc, user, pw string) (*rfSystem, error) {
	root, err := redfishGet(ctx, cl, bmc, user, pw, "/redfish/v1/Systems")
	if err != nil {
		return nil, err
	}
	sysPath := redfishFirstMember(root)
	if sysPath == "" {
		return nil, nil
	}
	s, err := redfishGet(ctx, cl, bmc, user, pw, sysPath)
	if err != nil {
		return nil, err
	}
	return mapRedfishSystem(s, sysPath), nil
}

// mapRedfishThermal — Thermal JSON → 온도/팬(각 최대 12개). 순수 함수.
// ReadingCelsius 없는 온도 엔트리는 실장 안 된 슬롯이라 걸러낸다.
func mapRedfishThermal(t map[string]any) *thermalData {
	out := &thermalData{Temps: []map[string]any{}, Fans: []map[string]any{}}
	for i, x := range jl(t["Temperatures"]) {
		if i >= 12 {
			break
		}
		xm := jm(x)
		v, ok := jf(xm["ReadingCelsius"])
		if !ok {
			continue
		}
		out.Temps = append(out.Temps, map[string]any{
			"name": js(xm["Name"]), "c": round1(v),
			"health": js(jm(xm["Status"])["Health"]),
		})
	}
	for i, x := range jl(t["Fans"]) {
		if i >= 12 {
			break
		}
		xm := jm(x)
		name := js(xm["Name"])
		if name == "" {
			name = js(xm["FanName"])
		}
		out.Fans = append(out.Fans, map[string]any{
			"name": name, "rpm": xm["Reading"],
			"health": js(jm(xm["Status"])["Health"]),
		})
	}
	return out
}

// fetchRedfishThermal — Chassis 첫 멤버의 Thermal.
func fetchRedfishThermal(ctx context.Context, cl *http.Client, bmc, user, pw string) (*thermalData, error) {
	root, err := redfishGet(ctx, cl, bmc, user, pw, "/redfish/v1/Chassis")
	if err != nil {
		return nil, err
	}
	chPath := redfishFirstMember(root)
	if chPath == "" {
		return nil, nil
	}
	t, err := redfishGet(ctx, cl, bmc, user, pw, strings.TrimRight(chPath, "/")+"/Thermal")
	if err != nil {
		return nil, err
	}
	return mapRedfishThermal(t), nil
}

// serverStatus — 상태 판정 규칙:
//
//	SNMP 응답 = op · SNMP 무응답+전원 On = deg(OS 무응답) ·
//	전원 Off 또는 전부 무응답 = down. SNMP 미설정이면 BMC 단독=정상.
func serverStatus(aliveOS, rfOK, powerOff, useSNMP bool) string {
	if aliveOS {
		return "op"
	}
	if rfOK && !powerOff {
		if useSNMP {
			return "deg"
		}
		return "op"
	}
	return "down"
}

// serverKernel — sysDescr "Linux <host> <kernel> ..." 에서 커널 버전 조각.
// Python str.split(" ") 관행(연속 공백 보존)을 그대로 따른다.
func serverKernel(sysd string) string {
	if !strings.HasPrefix(sysd, "Linux") {
		return ""
	}
	parts := strings.Split(sysd, " ")
	if len(parts) > 2 {
		return parts[2]
	}
	return ""
}

// serverVersion — sysDescr 의 "#" 앞부분 48자.
func serverVersion(sysd string) string {
	v := strings.TrimSpace(strings.Split(sysd, "#")[0])
	r := []rune(v)
	if len(r) > 48 {
		r = r[:48]
	}
	return string(r)
}

// pollServer — Python poll_server 이식.
func (w *Worker) pollServer(pc *pollCtx, dev DeviceConfig, st *srvStatic) (map[string]any, *srvStatic) {
	if st == nil {
		st = &srvStatic{}
	}
	ip := dev.IP
	comm := dev.Community
	bmcIP, bmcUser, bmcPW := dev.BMCIP, dev.BMCUser, dev.BMCPassword
	useSNMP := comm != ""
	useRF := bmcIP != "" && bmcUser != ""

	// ---- SNMP(OS) ----
	var sn map[string]snmp.Value
	if useSNMP {
		sn = pc.snmp.call(pc.ctx, ip, comm, []string{
			oSysUptime, oSysDescr, oSysName,
			oUCDCPUIdle, oUCDLA1, oUCDLA5, oUCDLA15,
			oUCDMemTot, oUCDMemAvail, oHRMemKB}, 3*time.Second)
	}
	upVal, upOK := sn[oSysUptime]
	aliveOS := len(sn) > 0 && upOK && upVal.Kind != snmp.KindNull

	// ---- Redfish(BMC) ----
	var rf *rfSystem
	rfErr := ""
	if useRF {
		rfClient := pc.rf
		if dev.TLSFingerprint != "" {
			rfClient = DeviceHTTPClient(redfishTimeout, dev.TLSFingerprint)
		}
		var err error
		rf, err = fetchRedfishSystem(pc.ctx, rfClient, bmcIP, bmcUser, bmcPW)
		if err != nil {
			rfErr = errClass(err)
		} else {
			if pc.refresh || !st.ThermalTried {
				if th, terr := fetchRedfishThermal(pc.ctx, rfClient, bmcIP, bmcUser, bmcPW); terr == nil {
					st.Thermal = th
					st.ThermalTried = true
				} else if !st.ThermalTried {
					// 열 정보는 선택 — 실패핮도 시도 마커를 남겨 매 라운드 재시도를 막는다.
					st.ThermalTried = true
				}
			}
		}
	}
	th := st.Thermal

	powerOff := rf != nil && rf.Power != "" && !strings.EqualFold(rf.Power, "on")
	status := serverStatus(aliveOS, rf != nil, powerOff, useSNMP)

	// ---- 지표 ----
	var cpuPtr, memPtr *int64
	var la []string
	if aliveOS {
		if idle, ok := vnumStrict(sn[oUCDCPUIdle]); ok {
			c := 100 - idle
			if c < 0 {
				c = 0
			}
			if c > 100 {
				c = 100
			}
			cpuPtr = i64p(c)
		}
		mt, mtOK := vnumStrict(sn[oUCDMemTot])
		ma, maOK := vnumStrict(sn[oUCDMemAvail])
		if mtOK && maOK && mt > 0 {
			memPtr = i64p(iround(100.0 * float64(mt-ma) / float64(mt)))
		}
		for _, o := range []string{oUCDLA1, oUCDLA5, oUCDLA15} {
			if v, ok := sn[o]; ok && v.Kind != snmp.KindNull {
				la = append(la, vstr(v))
			}
		}
	}
	upDays := int64(0)
	if aliveOS {
		ticks, _ := vnum(upVal)
		upDays = int64(float64(ticks) / 100 / daySec)
	}

	var loadAvg any
	if len(la) > 0 {
		f := make([]float64, 0, len(la))
		for _, x := range la {
			fv, err := strconv.ParseFloat(x, 64)
			if err != nil {
				// Python 의 float(x) 실패 = ValueError → 워커 예외 경로와 같은 결과.
				return downBase(dev, pc.now), st
			}
			f = append(f, fv)
		}
		loadAvg = f
	}

	d := baseDevice(dev, "SRV", status, upDays)
	if cpuPtr != nil {
		d["cpu0"], d["cpuNA"] = *cpuPtr, false
	}
	if memPtr != nil {
		d["mem0"], d["memNA"] = *memPtr, false
	}
	m := d["meta"].(map[string]any)
	sysd := vstr(sn[oSysDescr])
	vendor := dev.Vendor
	if rf != nil && rf.Maker != "" {
		vendor = rf.Maker
	}
	m["vendor"] = vendor
	if sysd != "" {
		m["version"] = serverVersion(sysd)
	}
	var memGiB any
	if rf != nil && rf.MemGiB != nil {
		if f, ok := jf(rf.MemGiB); ok {
			memGiB = round1(f)
		}
	} else if aliveOS {
		if mt, ok := vnumStrict(sn[oUCDMemTot]); ok {
			memGiB = round1(float64(mt) / 1048576.0)
		} else if hr, ok := vnumStrict(sn[oHRMemKB]); ok {
			memGiB = round1(float64(hr) / 1048576.0)
		}
	}
	nodeName := vstr(sn[oSysName])
	if nodeName == "" && rf != nil {
		nodeName = rf.Hostname
	}
	if nodeName == "" {
		nodeName = dev.Name
	}
	if nodeName == "" {
		nodeName = dev.Key
	}
	nodeState := "running"
	standing := "normal"
	if status == "down" {
		nodeState, standing = "stopped", "unknown"
	} else if status == "deg" {
		standing = "warning"
	}
	metricsSource := ""
	if aliveOS {
		metricsSource = "snmp"
	} else if rf != nil {
		metricsSource = "redfish"
	}
	rfCPUModel, rfModel, rfSerial, rfBIOS := "", "", "", ""
	if rf != nil {
		rfCPUModel, rfModel, rfSerial, rfBIOS = rf.CPUModel, rf.Model, rf.Serial, rf.BIOS
	}
	m["nodes"] = []any{map[string]any{
		"name": nodeName, "state": nodeState, "standing": standing,
		"mode": "normal", "primary": true, "ip": ip,
		"reachable":     status != "down",
		"metricsSource": metricsSource,
		"cpu_pct":       numOrNil(cpuPtr), "mem_pct": numOrNil(memPtr),
		"loadAvg":  loadAvg,
		"memGiB":   memGiB,
		"cpuModel": rfCPUModel,
		"model":    rfModel, "serial": rfSerial, "bios": rfBIOS,
	}}
	load := []any{}
	for _, x := range la {
		load = append(load, x)
	}
	m["srv"] = map[string]any{
		"node": nodeName, "load": load,
		"cpuModel": rfCPUModel,
		"memGiB":   memGiB,
		"kernel":   serverKernel(sysd),
	}
	if useRF {
		m["bmc"] = map[string]any{"ip": bmcIP, "up": rf != nil}
	}

	// ---- 건강 판정 + 경보 ----
	reasons := []string{}
	level := "ok"
	if status == "deg" {
		level = "warning"
	} else if status == "down" {
		level = "critical"
	}
	alerts := []any{}
	if a := stateAlert(status, pc.now); a != nil {
		alerts = append(alerts, a)
	}
	if rf != nil && rf.Health != "" && !strings.EqualFold(rf.Health, "OK") {
		sev := "warning"
		if strings.EqualFold(rf.Health, "CRITICAL") {
			sev = "critical"
		}
		reasons = append(reasons, "Redfish 시스템 건강 "+rf.Health)
		if sev == "critical" {
			level = "critical"
		} else if level != "critical" {
			level = "warning"
		}
		alerts = append(alerts, map[string]any{
			"name": "HW_HEALTH", "desc": "System health " + rf.Health + " (Redfish)",
			"time": tsKST(pc.now), "severity": sev, "sev": sev,
		})
	}
	if th != nil {
		for _, f := range th.Fans {
			fh := js(f["health"])
			if fh != "" && !strings.EqualFold(fh, "OK") {
				reasons = append(reasons, "팬 "+js(f["name"])+" "+fh)
				if level != "critical" {
					level = "warning"
				}
			}
		}
		for _, t := range th.Temps {
			th2 := js(t["health"])
			if th2 != "" && !strings.EqualFold(th2, "OK") {
				reasons = append(reasons, "온도 "+js(t["name"])+" "+pyFloat(t["c"].(float64))+"°C "+th2)
				if level != "critical" {
					level = "warning"
				}
			}
		}
	}
	if powerOff {
		reasons = append(reasons, "전원 Off (Redfish)")
	}
	if useSNMP && !aliveOS && status != "down" {
		reasons = append(reasons, "OS SNMP 무응답")
	}
	if useRF && rf == nil {
		rsn := "BMC 접속 실패"
		if rfErr != "" {
			rsn += " (" + rfErr + ")"
		}
		reasons = append(reasons, rsn)
		if level != "critical" {
			level = "warning"
		}
	}
	m["healthLevel"] = level
	reasonsAny := make([]any, 0, len(reasons))
	for _, r := range reasons {
		reasonsAny = append(reasonsAny, r)
	}
	m["healthReasons"] = reasonsAny
	m["alerts"] = capAlerts(alerts)
	m["srvThermal"] = th
	return d, st
}
