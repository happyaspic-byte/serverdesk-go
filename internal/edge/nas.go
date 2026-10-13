package edge

import (
	"strconv"
	"strings"
	"time"
)

// ── Synology NAS 폴 ────────────────────────────────────────────────────────

type nasDiskStatic struct {
	Idx   int
	Name  string
	Model string
}

type nasRaidStatic struct {
	Idx  int
	Name string
}

// nasStatic — NAS 의 라운드 간 유지 정적 정보.
type nasStatic struct {
	Model   string
	Serial  string
	DSM     string
	Sysname string
	Disks   []nasDiskStatic
	Raids   []nasRaidStatic
	Faults  map[string]map[string]any // Retained until this OID explicitly recovers.
}

// ucdCPU — ssCpuIdle(%) → 사용률. 범위 밖 값은 파싱 이상으로 버린다.
func ucdCPU(idle int64) (int64, bool) {
	if idle < 0 || idle > 100 {
		return 0, false
	}
	return 100 - idle, true
}

// ucdMemPct — UCD 메모리 사용률.
// 리눅스 캐시 관행: memAvailReal 은 순수 free 라 캐시가 사용량으로 잡힌다.
// buffer+cache 를 가용으로 되돌려 실사용률을 만든다.
func ucdMemPct(total, avail, buf, cache int64) (int64, bool) {
	if total <= 0 {
		return 0, false
	}
	free := avail + buf + cache
	pct := iround(float64(total-free) * 100.0 / float64(total))
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct, true
}

// synDiskOK — 디스크 상태 1(normal)/2(initialized) 만 정상.
func synDiskOK(st int64) bool { return st == 1 || st == 2 }

// synRaidText — Unrecognized RAID states are not evidence of normal health.
func synRaidText(st int64) string {
	if s, ok := synRaidStatusText[st]; ok {
		return s
	}
	return "unknown"
}

// pollNAS — Python poll_nas 이식.
func (w *Worker) pollNAS(pc *pollCtx, dev DeviceConfig, st *nasStatic) (map[string]any, *nasStatic) {
	ip := dev.IP
	comm := dev.Community
	if comm == "" {
		comm = "public"
	}
	r := pc.snmp.call(pc.ctx, ip, comm, []string{
		oSysUptime, oSynStatus, oSynTemp, oSynPower,
		oSynFanSys, oSynFanCPU, oSynUpgrade,
		oUCDCPUIdle, oUCDMemTot, oUCDMemAvail, oUCDMemBuf, oUCDMemCache,
	}, 3*time.Second)
	if len(r) == 0 {
		d := baseDevice(dev, "NAS", "down", 0)
		m := d["meta"].(map[string]any)
		if a := stateAlert("down", pc.now); a != nil {
			m["alerts"] = []any{a}
		}
		if st != nil {
			m["alerts"] = capAlerts(append(m["alerts"].([]any), nasFaultAlerts(st, nil)...))
		}
		m["nas"] = nil
		return d, st
	}

	upTicks, _ := vnum(r[oSysUptime])
	upDays := upTicks / 100 / daySec

	if pc.refresh || st == nil || st.Model == "" {
		st = w.nasRefreshStatic(pc, ip, comm, st)
	}

	if st.Faults == nil {
		st.Faults = map[string]map[string]any{}
	}
	// An undiscovered inventory cannot establish disk/RAID health.
	complete := len(st.Disks) > 0 && len(st.Raids) > 0
	badHealth := false
	observed := map[string]bool{}
	observe := func(oid string, known, healthy bool, name, desc string) {
		if !known {
			complete = false
			return
		}
		if healthy {
			delete(st.Faults, oid)
			return
		}
		badHealth = true
		observed[oid] = true
		if name != "" && st.Faults[oid] == nil {
			st.Faults[oid] = map[string]any{
				"name": name, "desc": desc, "time": tsKST(pc.now),
				"severity": "critical", "sev": "critical",
			}
		}
	}
	disks := []any{}
	for _, dk := range st.Disks {
		so := oSynDiskStat + strconv.Itoa(dk.Idx)
		to := oSynDiskTemp + strconv.Itoa(dk.Idx)
		di := pc.snmp.call(pc.ctx, ip, comm, []string{so, to}, 2*time.Second)
		dst, parsed := vnum(di[so])
		known := parsed && dst >= 1 && dst <= 5
		var ok any
		status := "unknown"
		if known {
			ok = synDiskOK(dst)
			status = "warning"
			if ok == true {
				status = "normal"
			}
		}
		observe(so, known, ok == true, "DISK_FAULT", dk.Name+" abnormal")
		disks = append(disks, map[string]any{
			"name": dk.Name, "status": status, "ok": ok,
			"model": dk.Model, "tempC": numOrNil(vnumPtr(di[to])),
		})
	}
	raids := []any{}
	for _, rd := range st.Raids {
		ro := oSynRaidStat + strconv.Itoa(rd.Idx)
		ri := pc.snmp.call(pc.ctx, ip, comm, []string{ro}, 2*time.Second)
		rst, parsed := vnum(ri[ro])
		stx := synRaidText(rst)
		known := parsed && stx != "unknown"
		var ok any
		if known {
			ok = rst == 1
		}
		name := ""
		if rst == 11 || rst == 12 {
			name = "RAID_" + strings.ToUpper(stx)
		}
		observe(ro, known, rst == 1, name, rd.Name+" "+stx)
		raids = append(raids, map[string]any{"name": rd.Name, "status": stx, "ok": ok})
	}

	states := map[string]string{}
	for _, oid := range []string{oSynStatus, oSynPower, oSynFanSys, oSynFanCPU} {
		n, parsed := vnum(r[oid])
		known := parsed && (n == 1 || n == 2)
		states[oid] = "unknown"
		if known {
			states[oid] = normFail(n)
		}
		observe(oid, known, n == 1, "NAS_HARDWARE_FAULT", oid+" failed")
	}
	var fansOK any
	if states[oSynFanSys] != "unknown" && states[oSynFanCPU] != "unknown" {
		fansOK = states[oSynFanSys] == "normal" && states[oSynFanCPU] == "normal"
	}

	status := "op"
	if !complete || badHealth || len(st.Faults) > 0 {
		status = "deg"
	}

	var cpuPtr, memPtr *int64
	if idle, ok := vnum(r[oUCDCPUIdle]); ok {
		if c, ok2 := ucdCPU(idle); ok2 {
			cpuPtr = i64p(c)
		}
	}
	mt, mtOK := vnum(r[oUCDMemTot])
	ma, maOK := vnum(r[oUCDMemAvail])
	buf, _ := vnum(r[oUCDMemBuf])
	cache, _ := vnum(r[oUCDMemCache])
	if mtOK && mt > 0 && maOK {
		if m2, ok := ucdMemPct(mt, ma, buf, cache); ok {
			memPtr = i64p(m2)
		}
	}

	d := baseDevice(dev, "NAS", status, upDays)
	m := d["meta"].(map[string]any)
	m["stale"] = !complete
	m["collection"] = map[string]any{"healthComplete": complete}
	m["vendor"] = "Synology"
	m["version"] = st.DSM
	if cpuPtr != nil {
		d["cpu0"], d["cpuNA"] = *cpuPtr, false
	}
	if memPtr != nil {
		d["mem0"], d["memNA"] = *memPtr, false
	}
	m["snmp"] = []any{map[string]any{
		"ip": ip, "reachable": true, "uptime_days": upDays,
		"uptime_secs": upTicks / 100,
		"cpu":         numOrNil(cpuPtr), "mem": numOrNil(memPtr),
		"serial": st.Serial, "source": "snmp",
	}}

	// 랜포트 상태 — extra_ips(제2 포트 등)를 짧은 SNMP 프로브로 확인.
	// 포트 다운은 상태(deg) 사유가 아니라 정보(미연결일 수 있음)로만 표기.
	lanPorts := []any{map[string]any{"ip": ip, "up": true}}
	for _, e := range dev.ExtraIPs {
		up := len(pc.snmp.call(pc.ctx, e, comm, []string{oSysUptime}, 1500*time.Millisecond)) > 0
		lanPorts = append(lanPorts, map[string]any{"ip": e, "up": up})
	}

	m["nas"] = map[string]any{
		"model": st.Model, "serial": st.Serial, "dsmVersion": st.DSM,
		"tempC":        numOrNil(vnumPtr(r[oSynTemp])),
		"systemStatus": states[oSynStatus],
		"powerStatus":  states[oSynPower],
		"systemFan":    states[oSynFanSys],
		"cpuFan":       states[oSynFanCPU],
		"fansOk":       fansOK,
		"upgradeAvailable": func() bool {
			n, ok := vnum(r[oSynUpgrade])
			return ok && n == 1
		}(),
		"disks": disks, "raid": raids, "volumes": []any{},
		"lanPorts": lanPorts,
	}

	alerts := []any{}
	if a := stateAlert(status, pc.now); a != nil {
		alerts = append(alerts, a)
	}
	alerts = append(alerts, nasFaultAlerts(st, observed)...)
	m["alerts"] = capAlerts(alerts)
	return d, st
}

// observedAlert copies retained evidence; current-round authority never mutates
// activation history or any previously published snapshot.
func observedAlert(alert map[string]any, observed bool) map[string]any {
	out := make(map[string]any, len(alert)+1)
	for key, value := range alert {
		out[key] = value
	}
	out["observed"] = observed
	return out
}

// nasFaultAlerts returns unresolved evidence in stable inventory order, even when down.
func nasFaultAlerts(st *nasStatic, observed map[string]bool) []any {
	alerts := []any{}
	for _, dk := range st.Disks {
		if a := st.Faults[oSynDiskStat+strconv.Itoa(dk.Idx)]; a != nil {
			alerts = append(alerts, observedAlert(a, observed[oSynDiskStat+strconv.Itoa(dk.Idx)]))
		}
	}
	for _, rd := range st.Raids {
		if a := st.Faults[oSynRaidStat+strconv.Itoa(rd.Idx)]; a != nil {
			alerts = append(alerts, observedAlert(a, observed[oSynRaidStat+strconv.Itoa(rd.Idx)]))
		}
	}
	for _, oid := range []string{oSynStatus, oSynPower, oSynFanSys, oSynFanCPU} {
		if a := st.Faults[oid]; a != nil {
			alerts = append(alerts, observedAlert(a, observed[oid]))
		}
	}
	return alerts
}

// normFail — 1=normal / 그 외 failed (Synology MIB 2치 상태).
func normFail(v int64) string {
	if v == 1 {
		return "normal"
	}
	return "failed"
}

// nasRefreshStatic — 모델·시리얼·DSM·디스크/RAID 인벤토리 재조회.
// 재조회가 일시 실패로 쪼그라들면 기존 목록 유지(프린터 sup 가드와 동일 원칙).
func (w *Worker) nasRefreshStatic(pc *pollCtx, ip, comm string, prev *nasStatic) *nasStatic {
	sr := pc.snmp.call(pc.ctx, ip, comm,
		[]string{oSynModel, oSynSerial, oSynDSM, oSysName}, 3*time.Second)
	disks := []nasDiskStatic{}
	for i := 0; i < 12; i++ {
		ido := oSynDiskID + strconv.Itoa(i)
		mo := oSynDiskModel + strconv.Itoa(i)
		di := pc.snmp.call(pc.ctx, ip, comm, []string{ido, mo}, 2*time.Second)
		nm := vstr(di[ido])
		if nm == "" {
			break
		}
		disks = append(disks, nasDiskStatic{Idx: i, Name: nm, Model: vstr(di[mo])})
	}
	raids := []nasRaidStatic{}
	for i := 0; i < 4; i++ {
		no := oSynRaidName + strconv.Itoa(i)
		ri := pc.snmp.call(pc.ctx, ip, comm, []string{no}, 2*time.Second)
		nm := vstr(ri[no])
		if nm == "" {
			break
		}
		raids = append(raids, nasRaidStatic{Idx: i, Name: nm})
	}
	n := &nasStatic{
		Model: vstr(sr[oSynModel]), Serial: vstr(sr[oSynSerial]),
		DSM: vstr(sr[oSynDSM]), Sysname: vstr(sr[oSysName]),
		Disks: disks, Raids: raids,
	}
	if prev != nil {
		n.Faults = prev.Faults
		if len(n.Disks) < len(prev.Disks) {
			n.Disks = prev.Disks
		}
		if len(n.Raids) < len(prev.Raids) {
			n.Raids = prev.Raids
		}
		if n.Model == "" {
			n.Model = prev.Model
		}
		if n.Serial == "" {
			n.Serial = prev.Serial
		}
		if n.DSM == "" {
			n.DSM = prev.DSM
		}
		if n.Sysname == "" {
			n.Sysname = prev.Sysname
		}
	}
	return n
}
