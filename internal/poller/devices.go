package poller

// devices_adapter.py 포트 — 폴러 fleet(`clusters[]`) → serverdesk 프런트의
// 평면 `device[]` 스키마(serverdesk/device@1) 어댑터.
//
// 프런트는 화면 전부가 평면 device[] 를 소비하고 폴러는 계층형 clusters[] 를 낸다.
// /api/fleet 계약은 그대로 두고 프런트 전용 변환을 여기서 한 번 더 한다.
// 자격증명은 이 어댑터를 통과하지 않는다(입력 뷰에 없고, 출력은 화이트리스트).

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DisplayMeta 는 설정의 표시용 메타다(site/company/factory/asset_tag/floor_pos/label).
// 자격증명은 절대 들어가지 않는다. PUT /api/clusters 가 즉시 반영되는 통로다.
type DisplayMeta struct {
	Label    string
	Company  string
	Factory  string
	Site     string
	AssetTag string
	FloorPos string
}

// platformType 은 플랫폼 → 프런트 TYPES 레지스트리 키다. 미지 플랫폼은 FT 계열
// 기본값 'EV'.
var platformType = map[string]string{
	"everrun": "EV", "ztcedge": "EDGE", "ztc_edge": "EDGE",
	"endurance": "END", "ztcendurance": "END", "ftserver": "FTS",
}

var licMon = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun",
	"Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
var licWd = []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

const gib = 1024.0 * 1024.0 * 1024.0

// numOrNil 은 Python _num 에 해당한다(숫자만, bool 제외).
func numOrNil(v any) any {
	if f, ok := numVal(v); ok {
		return f
	}
	return nil
}

func pctOf(used, size any) any {
	u, uok := numVal(used)
	s, sok := numVal(size)
	if !uok || !sok || s == 0 {
		return nil
	}
	return round1(100.0 * u / s)
}

func gibOf(v any) any {
	f, ok := numVal(v)
	if !ok {
		return nil
	}
	return round1(f / gib)
}

// tsFmt 은 epoch → 'YYYY-MM-DD HH:MM:SS' (프런트 tsNorm/정렬 포맷).
func tsFmt(epoch float64, tzOff float64) string {
	if epoch == 0 {
		return ""
	}
	t := time.Unix(int64(epoch), 0).UTC().Add(time.Duration(tzOff) * time.Second)
	return t.Format("2006-01-02 15:04:05")
}

// licDateFmt 은 epoch → 'Mon Jul 06 07:35:25 KST 2026'
// (compute.js::parseLicDate 포맷 — Python 과 같이 리터럴 KST 를 쓴다).
func licDateFmt(epoch float64, tzOff float64) string {
	if epoch == 0 {
		return ""
	}
	t := time.Unix(int64(epoch), 0).UTC().Add(time.Duration(tzOff) * time.Second)
	wd := licWd[int(t.Weekday()+6)%7] // Go Sunday=0 → Python Mon=0
	return fmt.Sprintf("%s %s %02d %02d:%02d:%02d KST %d",
		wd, licMon[int(t.Month())-1], t.Day(), t.Hour(), t.Minute(), t.Second(), t.Year())
}

// cidr24 는 IP → 'a.b.c.0/24' (factory 폭백 표기).
func cidr24(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return ""
	}
	for _, p := range parts {
		if p == "" {
			return ""
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	return parts[0] + "." + parts[1] + "." + parts[2] + ".0/24"
}

// seriesOf 는 폴러 링버퍼 [{t,v}...] → 프런트 스파크라인용 숫자 배열(최근 48개).
func seriesOf(hist any) []any {
	out := []any{}
	for _, pv := range listVal(hist) {
		var v any
		if pm := dictVal(pv); pm != nil {
			v = pm["v"]
		} else {
			v = pv
		}
		if f, ok := numVal(v); ok {
			out = append(out, round1(f))
		}
	}
	if len(out) > 48 {
		out = out[len(out)-48:]
	}
	return out
}

// ---------------------------------------------------------------------------
// 상태 도출
// ---------------------------------------------------------------------------

func nodeRunning(n map[string]any) bool {
	return lowerStr(strVal(n["state"])) == "running"
}

func nodeNormal(n map[string]any) bool {
	st := strVal(n["standing_state"])
	if st == "" {
		st = "normal"
	}
	md := strVal(n["mode"])
	if md == "" {
		md = "normal"
	}
	st, md = lowerStr(st), lowerStr(md)
	return (st == "" || st == "normal") && (md == "" || md == "normal" || md == "production")
}

// DeriveStatus 는 노드 실상태(가용성) → 'op' | 'deg' | 'down' 이다
// (devices_adapter._derive_status 포트).
//
// status 는 순수하게 가용성(노드가 돌고 있는가)만 본다. health.level 은 반영하지
// 않는다 — 노드 2대가 멀쩡히 running 인 클러스터가 스토리지 사용률·과거 critical
// 알림 이력 때문에 "저하"로 표시되는 오보가 있었다(실장비). reachable(SSH/SNMP
// 도달성)도 down 판정에 쓰지 않는다 — avcli(관리 VIP)와 독립된 별도 채널이라
// reachable=false 를 down 으로 접으면 'running 노드 2대인데 오프라인' 오보가 난다.
func DeriveStatus(view map[string]any) string {
	nodes := listVal(view["nodes"])
	if len(nodes) == 0 {
		return "down"
	}
	running := 0
	anyAbnormal := false
	for _, nv := range nodes {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		if nodeRunning(n) {
			running++
		}
		if !nodeNormal(n) {
			anyAbnormal = true
		}
	}
	if running == 0 {
		return "down"
	}
	if running < len(nodes) || anyAbnormal {
		return "deg"
	}
	return "op"
}

// deriveSync 는 FT 미러/이중화 상태 → 'sync' | 'simplex' | 'offline' 이다
// (devices_adapter._derive_sync 포트).
func deriveSync(view map[string]any, status string) string {
	nodes := listVal(view["nodes"])
	running := 0
	for _, nv := range nodes {
		if n := dictVal(nv); n != nil && nodeRunning(n) {
			running++
		}
	}
	if status == "down" || running == 0 {
		return "offline"
	}
	if running < 2 || running < len(nodes) {
		return "simplex"
	}
	if boolVal(mapGet(view, "unit", "syncing")) {
		return "simplex"
	}
	for _, nv := range nodes {
		if n := dictVal(nv); n != nil && !nodeNormal(n) {
			return "simplex"
		}
	}
	for _, vmv := range listVal(view["vms"]) {
		vm := dictVal(vmv)
		if vm == nil {
			continue
		}
		for _, vv := range listVal(vm["volumes"]) {
			vol := dictVal(vv)
			if vol == nil {
				continue
			}
			imgs := listVal(vol["disk_images"])
			// cdrom·미러 대상이 아닌(디스크 이미지 1개뿐) 볼륨은 건너뛴다.
			// 'not mirrored' 로 거르면 한쪽이 DISABLED 된 '깨진 미러'가 skip 돼
			// 정작 잡아야 할 상태를 놓친다.
			if boolVal(vol["is_cdrom"]) || len(imgs) < 2 {
				continue
			}
			enabled := 0
			for _, iv := range imgs {
				if d := dictVal(iv); d != nil && boolVal(d["enabled"]) {
					enabled++
				}
			}
			if enabled < running {
				return "simplex"
			}
		}
	}
	for _, gv := range listVal(view["storage_groups"]) {
		g := dictVal(gv)
		if g == nil {
			continue
		}
		for _, dv := range listVal(g["disks"]) {
			d := dictVal(dv)
			if d == nil {
				continue
			}
			ss := strVal(d["standing_state"])
			if ss == "" {
				ss = "normal"
			}
			if ls := lowerStr(ss); ls != "" && ls != "normal" {
				return "simplex"
			}
		}
	}
	return "sync"
}

// availNominal 은 상태의 명목 가용성 환산이다(실측 트래커가 관측 충분 시 교체).
func availNominal(status string) float64 {
	switch status {
	case "op":
		return 99.99
	case "deg":
		return 99.9
	}
	return 99.0
}

// ---------------------------------------------------------------------------
// meta 조각
// ---------------------------------------------------------------------------

func metaNodes(view map[string]any) []any {
	out := []any{}
	for _, nv := range listVal(view["nodes"]) {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		osm := dictVal(n["os"])
		if osm == nil {
			osm = map[string]any{}
		}
		standing := strVal(n["standing_state"])
		if standing == "" {
			if nodeRunning(n) {
				standing = "normal"
			} else {
				standing = "unknown"
			}
		}
		cpus := ""
		if n["cpus"] != nil {
			if f, ok := numVal(n["cpus"]); ok {
				cpus = fmt.Sprintf("%v", f)
			}
		}
		memory := strVal(n["memory_raw"])
		memGiB := gibOf(osm["mem_total_bytes"])
		if memGiB == nil {
			memGiB = gibOf(n["memory_bytes"])
		}
		var cores any
		if v, ok := numVal(osm["cpu_cores"]); ok {
			cores = v
		}
		out = append(out, map[string]any{
			"name":          n["name"],
			"state":         n["state"],
			"standing":      standing,
			"mode":          n["mode"],
			"primary":       boolVal(n["primary"]),
			"manufacturer":  n["manufacturer"],
			"model":         n["model"],
			"cpus":          cpus,
			"memory":        memory,
			"ip":            n["ip"],
			"serial":        strVal(n["serial"]),
			"bios":          strVal(n["bios"]),
			"cpuModel":      strVal(osm["cpu_model"]),
			"cores":         cores,
			"memGiB":        memGiB,
			"modules":       nil,
			"reachable":     boolVal(n["reachable"]),
			"metricsSource": n["metrics_source"],
			"tempMaxC":      numOrNil(n["temp_max_c"]),
			"loadAvg":       loadOrNil(osm["load"]),
			"fsMaxPct":      numOrNil(osm["fs_max_pct"]),
			"vmCount":       len(listVal(n["vm_placements"])),
		})
	}
	return out
}

// loadOrNil 은 Python `osm.get("load") or None` 과 같다: 빈 배엷도 nil 이다.
func loadOrNil(v any) any {
	if l := listVal(v); len(l) > 0 {
		return l
	}
	return nil
}

// metaSNMP 는 meta.snmp[] — 프런트가 노드별 CPU/MEM/uptime 을 읽는 배열(이름만 SNMP).
func metaSNMP(view map[string]any, now float64) []any {
	out := []any{}
	for _, nv := range listVal(view["nodes"]) {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		osm := dictVal(n["os"])
		if osm == nil {
			osm = map[string]any{}
		}
		up := 0.0
		if f, ok := numVal(n["uptime_secs"]); ok {
			up = f
		}
		rec := map[string]any{
			"ip":          n["ip"],
			"reachable":   boolVal(n["reachable"]),
			"uptime_secs": int64(up),
			"uptime_days": int64(up) / 86400,
			"fresh":       up < 3600,
			"cpuModel":    strVal(osm["cpu_model"]),
			"memGiB":      gibOf(osm["mem_total_bytes"]),
			"serial":      strVal(n["serial"]),
			"bios":        strVal(n["bios"]),
			"source":      n["metrics_source"],
		}
		cpu, cpuOK := numVal(n["cpu_pct"])
		mem, memOK := numVal(n["mem_pct"])
		if rec["reachable"].(bool) {
			if cpuOK {
				rec["cpu"] = int64(roundHalfEven(cpu))
			}
			if memOK {
				rec["mem"] = int64(roundHalfEven(mem))
			}
		}
		// 최근 재부팅(24h 이내)은 프런트 detail 이 배지로 띄운다.
		if up > 0 && up < 86400 {
			rec["rebooted_at"] = int64(now - up)
			rec["reboot_ago"] = int64(up)
		}
		out = append(out, rec)
	}
	return out
}

// placements 는 VM 이름 → 현재 배치된 노드 목록이다.
//
// node.vm_placements(지금 그 VM 을 들고 있는 노드)와 vm.instances[].node(이중화로
// 구성된 인스턴스 노드)를 헷갈리면 안 된다. 화면의 "VM 배치"는 앞쪽이다.
func placements(view map[string]any) map[string][]string {
	out := map[string][]string{}
	for _, nv := range listVal(view["nodes"]) {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		for _, pv := range listVal(n["vm_placements"]) {
			p := dictVal(pv)
			if p == nil {
				continue
			}
			if name := strVal(p["name"]); name != "" {
				out[name] = append(out[name], strVal(n["name"]))
			}
		}
	}
	return out
}

func metaVMs(view map[string]any) []any {
	place := placements(view)
	out := []any{}
	for _, vmv := range listVal(view["vms"]) {
		vm := dictVal(vmv)
		if vm == nil {
			continue
		}
		name := strVal(vm["name"])
		inst := strListVal(vm["nodes"]) // 인스턴스가 구성된 노드(이중화 쌍)
		on := place[name]               // 현재 배치 노드
		diskBytes := 0.0
		for _, vv := range listVal(vm["volumes"]) {
			v := dictVal(vv)
			if v == nil || boolVal(v["is_cdrom"]) {
				continue
			}
			if f, ok := numVal(v["size_bytes"]); ok {
				diskBytes += f
			}
		}
		nets := []any{}
		for _, iv := range listVal(vm["interfaces"]) {
			if i := dictVal(iv); i != nil {
				if sn := strVal(i["shared_network"]); sn != "" {
					nets = append(nets, sn)
				}
			}
		}
		cpus := ""
		if vm["cpus"] != nil {
			if f, ok := numVal(vm["cpus"]); ok {
				cpus = fmt.Sprintf("%v", f)
			}
		}
		nodeStr := ""
		if len(on) > 0 {
			nodeStr = strings.Join(on, "·")
		} else if len(inst) > 0 {
			nodeStr = strings.Join(inst, "·")
		}
		nodesList := on
		if len(nodesList) == 0 {
			nodesList = inst
		}
		standby := []any{}
		onSet := map[string]bool{}
		for _, o := range on {
			onSet[o] = true
		}
		for _, x := range inst {
			if !onSet[x] {
				standby = append(standby, x)
			}
		}
		diskMB := int64(0)
		if diskBytes != 0 {
			diskMB = int64(diskBytes / (1024 * 1024))
		}
		out = append(out, map[string]any{
			"name":     vm["name"],
			"state":    vm["state"],
			"ft":       lowerStr(strVal(vm["ha_mode"])),
			"cpus":     cpus,
			"vcpu":     numOrNil(vm["cpus"]),
			"memory":   strVal(vm["memory_raw"]),
			"diskMB":   diskMB,
			"standing": strVal(vm["standing_state"]),
			// 배치 근거가 없으면(정지 VM 등) 인스턴스 노드로 폭백한다.
			"node":          nodeStr,
			"nodes":         strSliceAny(nodesList),
			"placedOn":      strSliceAny(on),
			"standbyNodes":  standby,
			"instanceNodes": strSliceAny(inst),
			"ip":            "", // avcli 는 게스트 IP 를 주지 않는다(허구 금지)
			"guest":         nil,
			"os":            strVal(vm["os_type"]),
			"uuid":          strVal(vm["uuid"]),
			"redundancy":    strVal(vm["redundancy"]),
			"diskMirrored":  vm["disk_mirrored"],
			"nicRedundant":  vm["nic_redundant"],
			"networks":      nets,
		})
	}
	return out
}

func metaUnit(view map[string]any) map[string]any {
	u := dictVal(view["unit"])
	if u == nil {
		u = map[string]any{}
	}
	res := dictVal(u["resources"])
	if res == nil {
		res = map[string]any{}
	}
	name := strVal(u["name"])
	if name == "" {
		name = strVal(view["mgmt_ip"])
	}
	version := strVal(u["version"])
	if version == "" {
		version = strVal(view["version"])
	}
	syncing := "false"
	if boolVal(u["syncing"]) {
		syncing = "true" // 프런트는 문자열 'true'/'false' 를 그대로 표시한다.
	}
	ntp := listVal(u["ntp"])
	if ntp == nil {
		ntp = []any{}
	}
	return map[string]any{
		"name":       name,
		"version":    version,
		"syncing":    syncing,
		"totVcpu":    numOrNil(res["total_vcpus"]),
		"usedVcpu":   numOrNil(res["used_vcpus"]),
		"totMem":     gibOf(res["total_memory_bytes"]),
		"usedMem":    gibOf(res["used_memory_bytes"]),
		"vcpuPct":    numOrNil(res["vcpu_pct"]),
		"memPct":     numOrNil(res["memory_pct"]),
		"uuid":       strVal(u["uuid"]),
		"ntp":        ntp,
		"configured": u["configured"],
	}
}

func metaLicense(view map[string]any, tzOff float64) any {
	lic := dictVal(view["license"])
	if len(lic) == 0 {
		return nil
	}
	install := strVal(lic["install_date"])
	if install == "" {
		if f, ok := numVal(lic["install_epoch"]); ok {
			install = licDateFmt(f, tzOff)
		}
	}
	expire := ""
	if boolVal(lic["expires"]) {
		expire = strVal(lic["expire_date"])
		if expire == "" {
			if f, ok := numVal(lic["expire_epoch"]); ok {
				expire = licDateFmt(f, tzOff)
			}
		}
	}
	return map[string]any{
		"name":      strVal(lic["name"]),
		"type":      strVal(lic["type"]),
		"edition":   strVal(lic["edition"]),
		"install":   install,
		"expire":    expire,
		"expires":   boolVal(lic["expires"]),
		"activated": boolVal(lic["activated"]),
		"daysLeft":  numOrNil(lic["days_left"]),
	}
}

// metaAlerts 는 최신순 상위 25건이다(프런트는 문자열 시각 비교로 정렬).
func metaAlerts(view map[string]any) []any {
	out := []any{}
	for _, av := range listVal(view["alerts"]) {
		a := dictVal(av)
		if a == nil {
			continue
		}
		sev := lowerStr(strVal(a["severity"]))
		switch sev {
		case "critical", "warning", "info":
		default:
			sev = "info"
		}
		desc := strVal(a["description"])
		if desc == "" {
			desc = strVal(a["name"])
		}
		out = append(out, map[string]any{
			"name": strVal(a["name"]), "desc": desc,
			"time": strVal(a["time"]), "severity": sev, "sev": sev,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strVal(out[i].(map[string]any)["time"]) > strVal(out[j].(map[string]any)["time"])
	})
	if len(out) > 25 {
		out = out[:25]
	}
	return out
}

// metaEvents 는 실제 알림 상위 6건이다. 시뮬레이터처럼 상태전이/재부팅을
// 지어내지 않는다(허구 금지).
func metaEvents(view map[string]any, alerts []any) []any {
	out := []any{}
	host := strVal(view["name"])
	if host == "" {
		host = strVal(view["key"])
	}
	for i, av := range alerts {
		if i >= 6 {
			break
		}
		a := dictVal(av)
		out = append(out, map[string]any{
			"ts": a["time"], "at": 0, "kind": "alert",
			"text": a["desc"], "sev": a["sev"], "host": host,
		})
	}
	return out
}

// lastReboot 는 가장 최근에 올라온 노드(uptime 최소)다 — 24h 이내일 때만.
func lastReboot(view map[string]any, now float64) any {
	var bestUp float64
	var best map[string]any
	for _, nv := range listVal(view["nodes"]) {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		up, ok := numVal(n["uptime_secs"])
		if !ok || up >= 86400 {
			continue
		}
		if best == nil || up < bestUp {
			bestUp, best = up, n
		}
	}
	if best == nil {
		return nil
	}
	return map[string]any{
		"ip": best["ip"], "node": best["name"],
		"at": int64(now - bestUp), "agoSecs": int64(bestUp),
	}
}

// ---------------------------------------------------------------------------
// meta.topo — 토폴로지 화면 전용 실관계 (devices_adapter._topo 포트)
// ---------------------------------------------------------------------------

func metaTopo(view map[string]any) map[string]any {
	nicMap := dictVal(view["nic_network_map"])
	byNet := map[string]map[string][]string{}
	for nodeName, nicsV := range nicMap {
		nics := dictVal(nicsV)
		if nics == nil {
			continue
		}
		for nic, val := range nics {
			var net string
			if m := dictVal(val); m != nil {
				net = strVal(m["network"])
			} else {
				net = strVal(val)
			}
			if net == "" {
				continue
			}
			bn := byNet[net]
			if bn == nil {
				bn = map[string][]string{}
				byNet[net] = bn
			}
			bn[nodeName] = append(bn[nodeName], nic)
		}
	}

	// 알림에 이름이 등장하는 네트워크/스토리지는 상태를 낮춘다(간이 전파).
	var alertTexts []string
	for _, av := range listVal(view["alerts"]) {
		a := dictVal(av)
		if a == nil {
			continue
		}
		sev := lowerStr(strVal(a["severity"]))
		if sev == "critical" || sev == "warning" {
			alertTexts = append(alertTexts, strVal(a["name"])+" "+strVal(a["description"]))
		}
	}
	alertText := lowerStr(strings.Join(alertTexts, " "))

	nets := []any{}
	for _, nv := range listVal(view["networks"]) {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		name := strVal(n["name"])
		nics := map[string]any{}
		if bn := byNet[name]; bn != nil {
			for node, ifs := range bn {
				nics[node] = strSliceAny(ifs)
			}
		}
		st := "op"
		if name != "" && strings.Contains(alertText, lowerStr(name)) {
			st = "deg"
		}
		nets = append(nets, map[string]any{
			"name":   name,
			"id":     n["id"],
			"role":   strVal(n["role"]),
			"ft":     strVal(n["fault_tolerant"]),
			"bw":     strVal(n["bandwidth_raw"]),
			"mtu":    numOrNil(n["mtu"]),
			"nics":   nics,
			"status": st,
		})
	}
	// a-link 먼저(FT 심장부), 그다음 business.
	sort.SliceStable(nets, func(i, j int) bool {
		a, b := nets[i].(map[string]any), nets[j].(map[string]any)
		ra, rb := 1, 1
		if a["role"] == "a-link" {
			ra = 0
		}
		if b["role"] == "a-link" {
			rb = 0
		}
		if ra != rb {
			return ra < rb
		}
		return a["name"].(string) < b["name"].(string)
	})

	volSG := map[string]string{}
	for _, vv := range listVal(view["volumes"]) {
		if v := dictVal(vv); v != nil && strVal(v["name"]) != "" {
			volSG[strVal(v["name"])] = strVal(v["storage_group_name"])
		}
	}

	groups := []any{}
	for _, gv := range listVal(view["storage_groups"]) {
		g := dictVal(gv)
		if g == nil {
			continue
		}
		disks := []any{}
		anyBad := false
		for _, dv := range listVal(g["disks"]) {
			d := dictVal(dv)
			if d == nil {
				continue
			}
			state := strVal(d["standing_state"])
			if state == "" {
				state = "normal"
			}
			state = lowerStr(state)
			okState := state == "" || state == "normal"
			if !okState {
				anyBad = true
			}
			disks = append(disks, map[string]any{
				"name":    strVal(d["name"]),
				"node":    strVal(d["node_name"]),
				"state":   state,
				"ok":      okState,
				"sizeRaw": strVal(d["size_raw"]),
			})
		}
		pct := pctOf(g["used_bytes"], g["size_bytes"])
		st := "op"
		if p, ok := numVal(pct); ok {
			warn, crit := UsageThresholds()
			if p >= crit {
				st = "down"
			} else if p >= warn {
				st = "deg"
			}
		}
		if anyBad && st == "op" {
			st = "deg"
		}
		mirrorNodes := map[string]bool{}
		for _, dv := range disks {
			dm := dv.(map[string]any)
			if nd := dm["node"].(string); nd != "" {
				mirrorNodes[nd] = true
			}
		}
		volCount := 0
		for _, vv := range listVal(view["volumes"]) {
			if v := dictVal(vv); v != nil && strVal(v["storage_group_name"]) == strVal(g["name"]) {
				volCount++
			}
		}
		groups = append(groups, map[string]any{
			"name":    strVal(g["name"]),
			"id":      g["id"],
			"pct":     pct,
			"usedRaw": strVal(g["used_raw"]),
			"sizeRaw": strVal(g["size_raw"]),
			"disks":   disks,
			// 노드마다 디스크가 하나씩 있으면 미러(everRun/ztC 의 기본 구성).
			"mirrored": len(mirrorNodes) >= 2,
			"volumes":  volCount,
			"status":   st,
		})
	}

	place := placements(view)
	vmNet := map[string]any{}
	vmSG := map[string]any{}
	vmNodes := map[string]any{}
	vmStandby := map[string]any{}
	for _, vmv := range listVal(view["vms"]) {
		vm := dictVal(vmv)
		if vm == nil {
			continue
		}
		nm := strVal(vm["name"])
		netSet := map[string]bool{}
		for _, iv := range listVal(vm["interfaces"]) {
			if i := dictVal(iv); i != nil {
				if sn := strVal(i["shared_network"]); sn != "" {
					netSet[sn] = true
				}
			}
		}
		vmNet[nm] = sortedKeys(netSet)
		sgSet := map[string]bool{}
		for _, vv := range listVal(vm["volumes"]) {
			v := dictVal(vv)
			if v == nil || boolVal(v["is_cdrom"]) || strVal(v["name"]) == "" {
				continue
			}
			if sg := volSG[strVal(v["name"])]; sg != "" {
				sgSet[sg] = true
			}
		}
		vmSG[nm] = sortedKeys(sgSet)
		inst := strListVal(vm["nodes"])
		on := place[nm]
		if len(on) > 0 {
			vmNodes[nm] = strSliceAny(on)
		} else {
			vmNodes[nm] = strSliceAny(inst)
		}
		onSet := map[string]bool{}
		for _, o := range on {
			onSet[o] = true
		}
		sb := []any{}
		for _, x := range inst {
			if !onSet[x] {
				sb = append(sb, x)
			}
		}
		vmStandby[nm] = sb
	}

	return map[string]any{
		"source":     "poller",
		"networks":   nets,
		"storage":    groups,
		"vmNetworks": vmNet,
		"vmStorage":  vmSG,
		"vmNodes":    vmNodes,
		"vmStandby":  vmStandby,
	}
}

func sortedKeys(set map[string]bool) []any {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strSliceAny(out)
}

func strListVal(v any) []string {
	var out []string
	for _, x := range listVal(v) {
		out = append(out, strVal(x))
	}
	return out
}

func strSliceAny(s []string) []any {
	out := make([]any, 0, len(s))
	for _, x := range s {
		out = append(out, x)
	}
	return out
}

// ---------------------------------------------------------------------------
// 본체
// ---------------------------------------------------------------------------

// BuildDevice 는 cluster view 1개 → 프런트 device 1개다(devices_adapter.build_device).
func BuildDevice(view map[string]any, cfg DisplayMeta, now float64) map[string]any {
	tzOff := 0.0
	if f, ok := numVal(view["tz_offset_secs"]); ok {
		tzOff = f
	}
	key := strVal(view["key"])
	if key == "" {
		key = "cluster"
	}
	platform := lowerStr(strVal(view["platform"]))
	dtype, ok := platformType[platform]
	if !ok {
		dtype = "EV"
	}
	mgmt := strVal(view["mgmt_ip"])

	status := DeriveStatus(view)
	sync := deriveSync(view, status)

	nodes := listVal(view["nodes"])
	var cpus, mems []float64
	for _, nv := range nodes {
		n := dictVal(nv)
		if n == nil || !boolVal(n["reachable"]) {
			continue
		}
		if f, ok := numVal(n["cpu_pct"]); ok {
			cpus = append(cpus, f)
		}
		if f, ok := numVal(n["mem_pct"]); ok {
			mems = append(mems, f)
		}
	}
	cpu0 := int64(-1)
	if len(cpus) > 0 {
		sum := 0.0
		for _, c := range cpus {
			sum += c
		}
		cpu0 = int64(roundHalfEven(sum / float64(len(cpus))))
	}
	mem0 := int64(-1)
	if len(mems) > 0 {
		sum := 0.0
		for _, m := range mems {
			sum += m
		}
		mem0 = int64(roundHalfEven(sum / float64(len(mems))))
	}
	uptimeDays := int64(0)
	for _, nv := range nodes {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		up := 0.0
		if f, ok := numVal(n["uptime_secs"]); ok {
			up = f
		}
		if d := int64(up) / 86400; d > uptimeDays {
			uptimeDays = d
		}
	}

	// 노드별 이력 → 클러스터 평균 이력(스파크라인). 길이가 다르면 짧은 쪽에 맞춘다.
	avgHist := func(field string) []any {
		var series [][]any
		for _, nv := range nodes {
			n := dictVal(nv)
			if n == nil {
				continue
			}
			s := seriesOf(mapGet(n, "history", field))
			if len(s) > 0 {
				series = append(series, s)
			}
		}
		if len(series) == 0 {
			return []any{}
		}
		ln := len(series[0])
		for _, s := range series {
			if len(s) < ln {
				ln = len(s)
			}
		}
		out := make([]any, 0, ln)
		for i := 0; i < ln; i++ {
			sum := 0.0
			for _, s := range series {
				f, _ := numVal(s[len(s)-ln+i])
				sum += f
			}
			out = append(out, int64(roundHalfEven(sum/float64(len(series)))))
		}
		return out
	}

	alerts := metaAlerts(view)
	health := dictVal(view["health"])
	if health == nil {
		health = map[string]any{}
	}

	label := cfg.Label
	if label == "" {
		label = strVal(view["name"])
	}
	if label == "" {
		label = key
	}
	company := cfg.Company
	if company == "" {
		company = "루비컴"
	}
	factory := cfg.Factory
	if factory == "" {
		factory = cidr24(mgmt)
	}
	if factory == "" {
		factory = "—"
	}
	vendor := ""
	if len(nodes) > 0 {
		if n := dictVal(nodes[0]); n != nil {
			vendor = strVal(n["manufacturer"])
		}
	}
	if vendor == "" {
		vendor = "Stratus Technologies"
	}
	site := cfg.Site
	if site == "" {
		site = mgmt
	}
	if site == "" {
		site = "—"
	}
	var errField any
	if e := mapGet(view, "collection", "errors"); e != nil {
		if m := dictVal(e); m != nil && len(m) > 0 {
			errField = m
		}
	}
	collection := dictVal(view["collection"])
	if collection == nil {
		collection = map[string]any{}
	}
	alertCounts := health["alert_counts"]
	if alertCounts == nil {
		alertCounts = map[string]any{}
	}
	healthReasons := health["reasons"]
	if healthReasons == nil {
		healthReasons = []any{}
	}
	healthLevel := strVal(health["level"])
	if healthLevel == "" {
		healthLevel = "unknown"
	}
	traps := view["traps"]
	if traps == nil {
		traps = []any{}
	}

	vms := listVal(view["vms"])
	vmRunning := 0
	for _, vv := range vms {
		if v := dictVal(vv); v != nil && lowerStr(strVal(v["state"])) == "running" {
			vmRunning++
		}
	}

	meta := map[string]any{
		"label":         label,
		"company":       company,
		"factory":       factory,
		"mgmt":          mgmt,
		"assetTag":      cfg.AssetTag,
		"floorPos":      cfg.FloorPos,
		"vendor":        vendor,
		"error":         errField,
		"pending":       false,
		"version":       strVal(view["version"]),
		"uuid":          strVal(view["uuid"]),
		"platform":      platform,
		"alerts":        alerts,
		"alertCounts":   alertCounts,
		"healthLevel":   healthLevel,
		"healthReasons": healthReasons,
		// SNMP 트랩(이벤트 피드) — 폴러 뷰가 이미 프런트 스키마로 정규화해 실어 준다.
		"traps":          traps,
		"snmp":           metaSNMP(view, now),
		"nodes":          metaNodes(view),
		"vmList":         metaVMs(view),
		"vms":            len(vms),
		"vmRunning":      vmRunning,
		"unit":           metaUnit(view),
		"license":        metaLicense(view, tzOff),
		"lastVmSwitch":   nil, // avcli 로는 스위치 이력을 못 얻는다
		"lastNodeSwitch": nil,
		"lastReboot":     lastReboot(view, now),
		"bmc":            nil,
		"events":         metaEvents(view, alerts),
		"topo":           metaTopo(view),
		"collection":     collection,
		"stale":          boolVal(view["stale"]),
		"tzName":         strVal(view["tz_name"]),
	}

	return map[string]any{
		"id":      key,
		"host":    key,
		"type":    dtype,
		"site":    site,
		"status":  status,
		"availN":  availNominal(status),
		"cpu0":    cpu0,
		"mem0":    mem0,
		"cpuNA":   cpu0 < 0,
		"memNA":   mem0 < 0,
		"sync":    sync,
		"uptime":  uptimeDays,
		"live":    true,
		"meta":    meta,
		"histCpu": avgHist("cpu"),
		"histMem": avgHist("mem"),
		"histRtt": []any{},
	}
}

// BuildDevices 는 /api/fleet 응답 → /api/devices 응답 변환이다
// (devices_adapter.build_devices). 시뮬레이션 장비는 포팅하지 않는다 —
// 운영 설정은 sim_devices=0 이라 실장비만 낸다.
func BuildDevices(fleet map[string]any, cfgByKey map[string]DisplayMeta, refreshSec int) map[string]any {
	now := nowFloat()
	devices := []any{}
	for _, cv := range listVal(fleet["clusters"]) {
		c := dictVal(cv)
		if c == nil {
			continue
		}
		devices = append(devices, BuildDevice(c, cfgByKey[strVal(c["key"])], now))
	}
	generatedAt := fleet["generated_at"]
	if generatedAt == nil {
		generatedAt = int64(now)
	}
	if refreshSec <= 0 {
		refreshSec = 30
	}
	return map[string]any{
		"schema":         "serverdesk/device@1",
		"generated_at":   generatedAt,
		"poller_version": fleet["poller_version"],
		"overall":        fleet["overall"],
		"stale":          boolVal(fleet["stale"]),
		// 프런트 isStale() 임계 = refreshSec * 3
		"refreshSec": int64(refreshSec),
		"count":      len(devices),
		"devices":    devices,
	}
}
