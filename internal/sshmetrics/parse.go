package sshmetrics

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// parse.go 는 MetricsScript 가 내는 `@섹션` 구분 출력의 파서다.
//
// Python 과의 의도적 차이 한 가지: Python 은 int()/float() 가 던지는 예외로
// 샘플 전체를 버린다(워커가 잡아 로그만 남김). 여기서는 **깨진 숫자 행만 건너뛰고**
// 나머지 섹션은 살린다 — NIC 한 줄 쓰레기 때문에 CPU/메모리/디스크 전부가
// 몇 분간 null 이 되는 것보다 낫다고 판단했다. 정상 입력에서의 결과는 동일하다.

var (
	memLineRe = regexp.MustCompile(`^(\w+):\s+(\d+)\s*kB`)
	tzRe      = regexp.MustCompile(`^([+-])(\d{2})(\d{2})$`)
	// A-Link 이름 휴리스틱은 spine 이 없을 때만 쓰는 폴백이다.
	// everRun 의 A-Link 네트워크 이름은 임의라(실장비: priv0, net_82) 이 규칙은
	// p38p2 같은 NIC 를 놓친다 — 그래서 spine role 이 있으면 항상 그쪽이 이긴다.
	interconnectNameRe = regexp.MustCompile(`^(priv|alink)`)
)

// parseMetrics 는 원격 출력을 파싱해 뷰(Metrics)와 델타 기준(rawSample)을 만든다.
// Python 의 parse_metrics + cur_sample 분리에 해당한다. 없는 섹션은 그냥 빠진다.
// now 는 T= 행이 없을 때 ts 대체값으로만 쓴다.
func parseMetrics(raw string, now time.Time) (*Metrics, *rawSample) {
	sections, ts := splitSections(raw)
	if ts == 0 {
		ts = now.Unix()
	}
	m := &Metrics{TS: ts, RawSections: sortedKeys(sections)}
	s := &rawSample{ts: ts}

	// --- @stat : cpu jiffies(누적값 — 사용률은 델타로만 계산)
	for _, ln := range sections["stat"] {
		f := strings.Fields(ln)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "cpu":
			var j []int64
			for _, x := range f[1:] {
				if isInt(x) {
					v, _ := strconv.ParseInt(x, 10, 64)
					j = append(j, v)
				}
			}
			s.cpuJiffies = j
		case "ctxt", "procs_running", "procs_blocked":
			if len(f) < 2 {
				continue
			}
			v, err := strconv.ParseInt(f[1], 10, 64)
			if err != nil {
				continue
			}
			switch f[0] {
			case "ctxt":
				m.Ctxt = &v
			case "procs_running":
				m.ProcsRunning = &v
			case "procs_blocked":
				m.ProcsBlocked = &v
			}
		}
	}

	// --- @load : 첫 행만 본다(Python 동일 — 첫 행이 짧으면 load 자체가 없다)
	if ls := sections["load"]; len(ls) > 0 {
		if f := strings.Fields(ls[0]); len(f) >= 3 {
			l1, ok1 := parseFloat(f[0])
			l5, ok2 := parseFloat(f[1])
			l15, ok3 := parseFloat(f[2])
			if ok1 && ok2 && ok3 {
				m.Load = []float64{l1, l5, l15}
			}
		}
	}

	// --- @up : 첫 행만
	if ls := sections["up"]; len(ls) > 0 {
		if f := strings.Fields(ls[0]); len(f) > 0 {
			if v, ok := parseFloat(f[0]); ok {
				secs := int64(v)
				m.UptimeSecs = &secs
				days := roundN(v/86400, 2)
				m.UptimeDays = &days
			}
		}
	}

	// --- @mem (kB → 바이트 환산)
	parseMemSection(sections["mem"], m)

	// --- @disk
	if disks := parseDiskSection(sections["disk"]); len(disks) > 0 {
		m.Filesystems = disks
		var mx int64
		for i, d := range disks {
			var v int64
			if d.UsedPct != nil {
				v = *d.UsedPct
			}
			if i == 0 || v > mx {
				mx = v
			}
		}
		m.FSMaxPct = &mx
	}

	// --- @diskio (누적 — 델타로만 사용)
	if dio := parseDiskIOSection(sections["diskio"]); len(dio) > 0 {
		s.diskRaw = dio
	}

	// --- @spine : NIC↔shared-network 확정 매핑. @net 보다 먼저 파싱해
	//     interconnect 판별에 쓴다.
	spine := parseSpine(sections["spine"])
	if spine != nil {
		m.Spine = spine
	}
	var spineRoles map[string]*string
	if spine != nil {
		spineRoles = spine.NICRoles
	}

	// --- @net (누적 — 델타로만 사용)
	if net := parseNetSection(sections["net"], spineRoles); len(net) > 0 {
		s.netRaw = net
	}

	// --- @temp
	if temps := parseTempSection(sections["temp"]); len(temps) > 0 {
		m.Temps = temps
		mx := temps[0].Celsius
		for _, t := range temps[1:] {
			if t.Celsius > mx {
				mx = t.Celsius
			}
		}
		m.TempMaxC = &mx
	}

	// --- @link
	if links := parseLinkSection(sections["link"]); len(links) > 0 {
		m.Links = links
	}

	// --- @md / @drbd
	if md := nonEmptyLines(sections["md"]); len(md) > 0 {
		m.MDStat = md
		degraded := false
		for _, ln := range md {
			// "[U_]" 처럼 브래킷 안의 밑줄이 디그레이드 표시다.
			if strings.Contains(ln, "[") && strings.Contains(ln, "]") && strings.Contains(ln, "_") {
				degraded = true
				break
			}
		}
		m.MDDegraded = &degraded
	}
	if drbd := nonEmptyLines(sections["drbd"]); len(drbd) > 0 {
		m.DRBD = drbd
		uptodate := true
		for _, ln := range drbd {
			if !strings.Contains(ln, "UpToDate/UpToDate") {
				uptodate = false
				break
			}
		}
		m.DRBDUpToDate = &uptodate
	}

	// --- @tz : "+0900" / "KST"
	if tz := nonEmptyLines(sections["tz"]); len(tz) > 0 {
		if mm := tzRe.FindStringSubmatch(tz[0]); mm != nil {
			sign := int64(1)
			if mm[1] == "-" {
				sign = -1
			}
			hh, _ := strconv.ParseInt(mm[2], 10, 64)
			mi, _ := strconv.ParseInt(mm[3], 10, 64)
			off := sign * (hh*3600 + mi*60)
			m.TZOffsetSecs = &off
		}
		if len(tz) > 1 {
			m.TZName = tz[1]
		}
	}

	// --- @vm / @plat
	if vms := nonEmptyLines(sections["vm"]); len(vms) > 0 {
		m.RunningDomains = vms
	}
	if plat := nonEmptyLines(sections["plat"]); len(plat) > 0 {
		m.OSPlatform = plat[0]
	}
	return m, s
}

// splitSections 는 `@이름` 행으로 출력을 나눈다. 같은 섹션이 두 번 나오면
// 마지막 것이 이긴다(Python 의 sections[cur]=[] 재할당과 동일).
// T= 행은 첫 섹션 전에만 타임스탬프로 인식한다(섹션 안의 "T=..." 는 데이터).
func splitSections(raw string) (map[string][]string, int64) {
	sections := map[string][]string{}
	var ts int64
	var cur string
	inSection := false
	// Python splitlines 는 마지막 개행 뒤에 빈 행을 만들지 않는다 — 맞춰 둔다.
	for _, ln := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		ln = strings.TrimSuffix(ln, "\r")
		if strings.HasPrefix(ln, "T=") && !inSection {
			if v, ok := parseInt(ln[2:]); ok {
				ts = v
			}
			continue
		}
		if strings.HasPrefix(ln, "@") {
			cur = strings.TrimSpace(ln[1:])
			sections[cur] = nil
			inSection = true
			continue
		}
		if inSection {
			sections[cur] = append(sections[cur], ln)
		}
	}
	return sections, ts
}

// parseMemSection 은 @mem 의 kB 값을 바이트로 환산한다.
// MemAvailable 이 없는 아주 오래된 커널이면 MemFree 로 대체한다(Python 동일).
func parseMemSection(lines []string, m *Metrics) {
	mem := map[string]int64{}
	for _, ln := range lines {
		if mm := memLineRe.FindStringSubmatch(ln); mm != nil {
			if v, err := strconv.ParseInt(mm[2], 10, 64); err == nil {
				mem[mm[1]] = v * 1024
			}
		}
	}
	total, ok := mem["MemTotal"]
	if !ok {
		return
	}
	avail, ok := mem["MemAvailable"]
	if !ok {
		avail = mem["MemFree"]
	}
	m.MemTotalBytes = &total
	m.MemAvailableBytes = &avail
	used := total - avail
	m.MemUsedBytes = &used
	m.MemPct = fptr(roundN(float64(used)/float64(total)*100, 1))
	if sw, ok := mem["SwapTotal"]; ok {
		m.SwapTotalBytes = &sw
		swUsed := sw - mem["SwapFree"]
		m.SwapUsedBytes = &swUsed
		m.SwapPct = fptr(roundN(float64(swUsed)/float64(sw)*100, 1))
	}
	if d, ok := mem["Dirty"]; ok {
		m.DirtyBytes = &d
	}
}

// parseDiskSection 은 df -PB1 출력을 파싱한다. 숫자가 깨진 행은 버린다.
func parseDiskSection(lines []string) []Disk {
	var disks []Disk
	for _, ln := range lines {
		f := strings.Fields(ln)
		if len(f) < 6 {
			continue
		}
		size, err1 := strconv.ParseInt(f[len(f)-5], 10, 64)
		used, err2 := strconv.ParseInt(f[len(f)-4], 10, 64)
		avail, err3 := strconv.ParseInt(f[len(f)-3], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		disks = append(disks, Disk{
			Device:     f[0],
			Mount:      f[len(f)-1],
			SizeBytes:  size,
			UsedBytes:  used,
			AvailBytes: avail,
			UsedPct:    intPtrFromStr(strings.TrimSuffix(f[len(f)-2], "%")),
		})
	}
	return disks
}

// parseDiskIOSection 은 /proc/diskstats 의 누적 카운터를 파싱한다.
// 필드: major minor name reads - read_sectors - writes - write_sectors - - io_ms -
func parseDiskIOSection(lines []string) map[string]diskCounters {
	dio := map[string]diskCounters{}
	for _, ln := range lines {
		f := strings.Fields(ln)
		if len(f) < 14 {
			continue
		}
		reads, e1 := strconv.ParseInt(f[3], 10, 64)
		rs, e2 := strconv.ParseInt(f[5], 10, 64)
		writes, e3 := strconv.ParseInt(f[7], 10, 64)
		ws, e4 := strconv.ParseInt(f[9], 10, 64)
		ioms, e5 := strconv.ParseInt(f[12], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil {
			continue
		}
		dio[f[2]] = diskCounters{reads: reads, readSectors: rs,
			writes: writes, writeSectors: ws, ioMS: ioms}
	}
	return dio
}

// parseNetSection 은 /proc/net/dev 누적 카운터를 파싱한다.
// lo 와 16필드 미만 행은 버린다. vnet*(게스트 tap)은 guest_tap 플래그만 달고
// 포함한다 — 뷰에서의 제외는 호출자 몫이다(Python 동일).
func parseNetSection(lines []string, spineRoles map[string]*string) map[string]nicCounters {
	net := map[string]nicCounters{}
	for _, ln := range lines {
		i := strings.Index(ln, ":")
		if i < 0 {
			continue
		}
		name := strings.TrimSpace(ln[:i])
		f := strings.Fields(ln[i+1:])
		if len(f) < 16 || name == "lo" {
			continue
		}
		// rx: 0=bytes 1=packets 2=errs 3=drop / tx: 8=bytes 9=packets 10=errs 11=drop
		idx := [8]int{0, 1, 2, 3, 8, 9, 10, 11}
		var v [8]int64
		ok := true
		for i, fi := range idx {
			val, err := strconv.ParseInt(f[fi], 10, 64)
			if err != nil {
				ok = false
				break
			}
			v[i] = val
		}
		if !ok {
			continue
		}
		c := nicCounters{
			rxBytes: v[0], rxPackets: v[1], rxErrs: v[2], rxDrop: v[3],
			txBytes: v[4], txPackets: v[5], txErrs: v[6], txDrop: v[7],
			guestTap: strings.HasPrefix(name, "vnet"),
		}
		// A-Link 여부는 이름이 아니라 spine 의 확정 role 로 판정한다.
		if role, present := spineRoles[name]; present && role != nil {
			c.interconnect = *role == "a-link"
			c.interconnectEvidence = "spine-config"
		} else {
			c.interconnect = interconnectNameRe.MatchString(name)
			c.interconnectEvidence = "name-heuristic"
		}
		net[name] = c
	}
	return net
}

// parseTempSection 은 "<hwmon>:<label>=<밀리℃>" 행을 ℃로 환산한다.
// label 에 '=' 가 들어갈 수 있어 마지막 '=' 로만 나눈다(Python rsplit 동일).
func parseTempSection(lines []string) []Temp {
	var temps []Temp
	for _, ln := range lines {
		i := strings.LastIndex(ln, "=")
		if i < 0 {
			continue
		}
		v, ok := parseFloat(ln[i+1:])
		if !ok {
			continue
		}
		chip, label, _ := strings.Cut(ln[:i], ":")
		if label == "" {
			label = chip
		}
		temps = append(temps, Temp{Chip: chip, Label: label, Celsius: roundN(v/1000.0, 1)})
	}
	return temps
}

// parseLinkSection 은 @link 의 5필드를 파싱한다. phy/virt 마커가 없으면
// Physical 을 nil 로 둔다 — 이름으로 추측해 브리지를 물리 NIC 로 오분류하지 않기 위해.
func parseLinkSection(lines []string) []Link {
	var links []Link
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		var f []string
		if strings.Contains(ln, "|") {
			f = strings.Split(ln, "|")
		} else {
			f = strings.Fields(ln)
		}
		name := strings.TrimSpace(f[0])
		if name == "" {
			continue
		}
		lk := Link{Name: name, GuestTap: strings.HasPrefix(name, "vnet")}
		if len(f) > 1 {
			if st := strings.TrimSpace(f[1]); st != "" {
				lk.State = &st
			}
		}
		lk.Up = lk.State != nil && *lk.State == "up"
		if len(f) > 2 {
			if v, ok := parseInt(strings.TrimSpace(f[2])); ok && v >= 0 {
				lk.SpeedMbps = &v
			}
		}
		marker := ""
		if len(f) > 3 {
			marker = strings.TrimSpace(f[3])
		}
		switch marker {
		case "phy":
			lk.Physical = bptr(true)
		case "virt":
			lk.Physical = bptr(false)
		}
		if len(f) > 4 {
			lk.MTU = intPtrFromStr(strings.TrimSpace(f[4]))
		}
		links = append(links, lk)
	}
	return links
}

// ==========================================================================
// @spine — NIC↔shared-network 확정 매핑
// ==========================================================================

var (
	spineNetRe  = regexp.MustCompile(`^/etc/opt/ft/spine/networks/([^/]+)/(\w+)$`)
	spineNodeRe = regexp.MustCompile(`^/etc/opt/ft/spine/nodes/([^/]+)/networks/([^/]+)/(\w+)$`)
	// spineRoleMap 은 Stratus role 표기를 폴리 납품 계약(a-link/business)으로 통일한다.
	spineRoleMap = map[string]string{
		"ALINK": "a-link", "A-LINK": "a-link", "PRIVATE": "a-link",
		"BUSINESS": "business", "MANAGEMENT": "business",
	}
)

// spineNodeNets 는 한 노드의 networks/* 항목을 **첫 등장 순서 보존**으로 담는다.
// 같은 ifn 이 두 번 나오면 첫 항목이 이기는 Python dict 순서 의미를 지키기 위해서다.
type spineNodeNets struct {
	order []string
	nets  map[string]map[string]string
}

// spineVal 은 설정 파일 첫 줄의 YAML 문서 표기("--- 값")와 따옴표를 벗긴다.
func spineVal(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "---") {
		v = strings.TrimSpace(v[3:])
	}
	return strings.Trim(v, "'\"")
}

// parseSpine 은 @spine 섹션(`F|<경로>|<첫줄>`)을 NIC↔네트워크 확정 매핑으로 만든다.
// /etc/opt/ft/spine/networks/<키>/{name,role,ordinal,mtu,bridge_name} 가 shared-network
// 정의이고, /etc/opt/ft/spine/nodes/<노드uuid>/networks/<uuid>/{name,parent_uuid} 가
// 물리 NIC → 네트워크 소속이다. 매핑이 하나도 없으면 nil 을 반환한다.
func parseSpine(lines []string) *Spine {
	nets := map[string]map[string]string{}
	nodeNets := map[string]*spineNodeNets{}
	var selfUUID string
	haveUUID := false
	for _, ln := range lines {
		p := strings.SplitN(ln, "|", 3)
		if len(p) < 3 || p[0] != "F" {
			continue
		}
		path, val := p[1], spineVal(p[2])
		if path == "/etc/opt/ft/node-config-uuid" {
			selfUUID, haveUUID = val, true
			continue
		}
		if mm := spineNetRe.FindStringSubmatch(path); mm != nil {
			d := nets[mm[1]]
			if d == nil {
				d = map[string]string{}
				nets[mm[1]] = d
			}
			d[mm[2]] = val
			continue
		}
		if mm := spineNodeRe.FindStringSubmatch(path); mm != nil {
			nn := nodeNets[mm[1]]
			if nn == nil {
				nn = &spineNodeNets{nets: map[string]map[string]string{}}
				nodeNets[mm[1]] = nn
			}
			e := nn.nets[mm[2]]
			if e == nil {
				e = map[string]string{}
				nn.nets[mm[2]] = e
				nn.order = append(nn.order, mm[2])
			}
			e[mm[3]] = val
		}
	}

	// 네트워크 정의는 uuid 디렉터리와 name 디렉터리에 중복 저장돼 있다. name 으로 통합.
	byKey := map[string]*SpineNetwork{}
	byName := map[string]*SpineNetwork{}
	for key, d := range nets {
		nm := d["name"]
		if nm == "" {
			continue
		}
		rec := &SpineNetwork{Name: nm}
		role := d["role"]
		if mapped, ok := spineRoleMap[strings.ToUpper(role)]; ok {
			rec.Role = &mapped
		} else if lr := strings.ToLower(role); lr != "" {
			rec.Role = &lr
		}
		rec.Ordinal = intPtrFromStr(d["ordinal"])
		rec.MTU = intPtrFromStr(d["mtu"])
		if b := d["bridge_name"]; b != "" {
			rec.Bridge = &b
		}
		byKey[key] = rec
		byName[nm] = rec
	}

	// 노드 디렉터리는 클러스터의 **모든** 노드 것을 담고 있다. 로컬 노드 것을 우선 쓴다.
	var order []string
	if haveUUID {
		if _, ok := nodeNets[selfUUID]; ok {
			order = append(order, selfUUID)
		}
	}
	for _, k := range sortedKeys(nodeNets) {
		if k != selfUUID {
			order = append(order, k)
		}
	}
	nicNet := map[string]*string{}
	nicRole := map[string]*string{}
	for _, nuuid := range order {
		nn := nodeNets[nuuid]
		for _, euuid := range nn.order {
			entry := nn.nets[euuid]
			ifn := entry["name"]
			if ifn == "" {
				continue
			}
			if _, dup := nicNet[ifn]; dup {
				continue
			}
			if net := byKey[entry["parent_uuid"]]; net != nil {
				nicNet[ifn] = &net.Name
				nicRole[ifn] = net.Role
			} else {
				nicNet[ifn] = nil
				nicRole[ifn] = nil
			}
		}
		if len(nicNet) > 0 && haveUUID && nuuid == selfUUID {
			break
		}
	}
	if len(byName) == 0 && len(nicNet) == 0 {
		return nil
	}
	networks := make([]SpineNetwork, 0, len(byName))
	for _, nm := range sortedKeys(byName) {
		networks = append(networks, *byName[nm])
	}
	var su *string
	if haveUUID {
		su = &selfUUID
	}
	return &Spine{SelfUUID: su, Networks: networks, NICNetworks: nicNet, NICRoles: nicRole}
}

// ==========================================================================
// 공용 헬퍼
// ==========================================================================

// parseFloat 는 Python ap.parse_float 에 해당한다: 공백·쉼표를 제거하고 float 로 읽는다.
func parseFloat(s string) (float64, bool) {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", ""))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

// parseInt 는 Python ap.parse_int 에 해당한다: float 경유 후 절삭(int() 동일).
func parseInt(s string) (int64, bool) {
	v, ok := parseFloat(s)
	if !ok {
		return 0, false
	}
	return int64(v), true
}

// intPtrFromStr 은 파싱 가능할 때만 포인터를 만든다(불가면 nil = Python None).
func intPtrFromStr(s string) *int64 {
	if v, ok := parseInt(s); ok {
		return &v
	}
	return nil
}

// roundN 은 Python round(x, n) 을 재현한다. strconv 의 정확한 10진 변환을 경유해
// float 직접 스케일링(2.675 → 2.68 같은 오차)과 half-away-from-zero 차이를 둘 다 피한다.
func roundN(x float64, n int) float64 {
	s := strconv.FormatFloat(x, 'f', n, 64)
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// isInt 는 Python 의 x.lstrip("-").isdigit() 필터에 해당한다.
func isInt(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// nonEmptyLines 는 앞뒤 공백을 제거한 비어있지 않은 행만 남긴다.
func nonEmptyLines(lines []string) []string {
	var out []string
	for _, ln := range lines {
		if s := strings.TrimSpace(ln); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sortedKeys 는 맵 키를 정렬해 돌려준다(출력 순서를 입력 순서와 무관하게 고정).
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func fptr(v float64) *float64 { return &v }
func bptr(v bool) *bool       { return &v }
