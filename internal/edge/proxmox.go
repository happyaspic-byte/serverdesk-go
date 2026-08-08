package edge

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ── Proxmox VE 폴(HTTPS API 읽기전용 GET 수집) ─────────────────────────────
// 인증: POST /access/ticket (2시간 유효) → static 에 캐시, 90분 지나면 재발급.
// 매 라운드: /nodes/<n>/status · /qemu · /lxc.
// 정적(5라운드마다): /cluster/status · /version · /network · /disks/list · /storage.
// 쓰기 엔드포인트는 절대 호출하지 않는다. 자가서명 인증서 전제(폐쇄망 낮부 장비).

const (
	pveTicketMaxAge = 5400 // 초 — 티켓 재사용 상한(90분). 라운드당 인증 왕복 제거.
	pveHTTPTimeout  = 5 * time.Second
)

// pveStatic — PVE 의 라운드 간 유지 상태(티켓 + 정적 인벤토리).
type pveStatic struct {
	Ticket   string
	TicketTS float64
	Node     string
	Version  string

	Net        []map[string]any
	Disks      []map[string]any
	Storage    []map[string]any
	HasNet     bool // Python `"net" in static` 마커 — 실패핮도 시도했음을 기록
	HasDisks   bool
	HasStorage bool
}

// pveRaw — 매 라운드 API 에서 읽는 동적 데이터.
type pveRaw struct {
	NodeStatus map[string]any
	Qemu       []any
	Lxc        []any
}

// pveAPI — /api2/json GET/POST 1회. ticket 이 있으면 PVEAuthCookie 쿠키.
// "data" 필드만 꺼내 돌린다. 4xx/5xx 는 httpStatusError.
func pveAPI(ctx context.Context, cl *http.Client, ip, path string, form url.Values, ticket string) (any, error) {
	u := "https://" + ip + ":8006/api2/json" + path
	method := http.MethodGet
	var body *strings.Reader
	if form != nil {
		method = http.MethodPost
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if ticket != "" {
		req.Header.Set("Cookie", "PVEAuthCookie="+ticket)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &httpStatusError{Code: resp.StatusCode}
	}
	var v map[string]any
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&v); err != nil {
		return nil, &jsonSyntaxError{Err: err}
	}
	return v["data"], nil
}

// pveMAC — 네트워크 항목 altnames 의 enx<12hex> 형식에서 MAC 을 복원한다.
//
// PVE /nodes/<n>/network 응답에는 MAC 필드가 없다. 대신 systemd 의 MAC 기반
// 대체 이름(enxd0000613273e)이 altnames 로 오므로 그걸 역파싱한다.
func pveMAC(entry map[string]any) string {
	for _, a := range jl(entry["altnames"]) {
		s := strings.ToLower(js(a))
		if strings.HasPrefix(s, "enx") && len(s) == 15 && isHex12(s[3:]) {
			h := s[3:]
			parts := make([]string, 6)
			for i := 0; i < 6; i++ {
				parts[i] = h[i*2 : i*2+2]
			}
			return strings.Join(parts, ":")
		}
	}
	return ""
}

func isHex12(s string) bool {
	if len(s) != 12 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// pveNet — 이더넷/본드/브리지만 남겨 프런트용으로 축약. eth 먼저, 브리지 나중.
func pveNet(rows []any) []map[string]any {
	out := []map[string]any{}
	for _, e := range rows {
		em := jm(e)
		kind := js(em["type"])
		if kind != "eth" && kind != "bond" && kind != "bridge" {
			continue
		}
		ip := js(em["cidr"])
		if ip == "" {
			ip = js(em["address"])
		}
		out = append(out, map[string]any{
			"name": js(em["iface"]), "kind": kind,
			"up":    jtruthy(em["active"]),
			"mac":   pveMAC(em),
			"ip":    ip,
			"gw":    js(em["gateway"]),
			"ports": js(em["bridge_ports"]),
		})
	}
	order := map[string]int{"eth": 0, "bond": 1, "bridge": 2}
	sort.SliceStable(out, func(i, j int) bool {
		oi, oj := order[out[i]["kind"].(string)], order[out[j]["kind"].(string)]
		if oi != oj {
			return oi < oj
		}
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out
}

// pveDisks — 물리 디스크 목록: 모델·시리얼·SMART 건강·수명(wearout=남은 수명 %).
func pveDisks(rows []any) []map[string]any {
	out := []map[string]any{}
	for _, e := range rows {
		em := jm(e)
		size, _ := jf(em["size"])
		var wearout any
		if wv, ok := jf(em["wearout"]); ok {
			wearout = int64(wv)
		}
		var rpm any
		if rv, ok := jf(em["rpm"]); ok && rv == float64(int64(rv)) && int64(rv) > 0 {
			rpm = int64(rv)
		}
		out = append(out, map[string]any{
			"dev":     strings.ReplaceAll(js(em["devpath"]), "/dev/", ""),
			"model":   strings.TrimSpace(strings.ReplaceAll(js(em["model"]), "_", " ")),
			"serial":  js(em["serial"]),
			"sizeGB":  iround(size / 1e9),
			"kind":    js(em["type"]), // ssd | hdd | usb | nvme
			"health":  js(em["health"]),
			"wearout": wearout,
			"rpm":     rpm,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i]["dev"].(string) < out[j]["dev"].(string)
	})
	return out
}

// pveStorage — 스토리지 풀(활성만): 용량/사용률.
func pveStorage(rows []any) []map[string]any {
	out := []map[string]any{}
	for _, e := range rows {
		em := jm(e)
		if !jtruthy(em["active"]) {
			continue
		}
		tot, _ := jf(em["total"])
		used, _ := jf(em["used"])
		pct := int64(0)
		if tot != 0 {
			frac, _ := jf(em["used_fraction"])
			if frac == 0 {
				frac = used / tot
			}
			pct = iround(100.0 * frac)
		}
		out = append(out, map[string]any{
			"name": js(em["storage"]), "type": js(em["type"]),
			"totalGiB": round1(tot / 1073741824.0),
			"usedGiB":  round1(used / 1073741824.0),
			"pct":      pct,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out
}

// pveVM — qemu/lxc 1건을 프런트 VM 행으로.
func pveVM(v map[string]any, kind, node string) map[string]any {
	vmid := v["vmid"]
	name := js(v["name"])
	if name == "" {
		if n, ok := ji(vmid); ok {
			name = fmt.Sprintf("%d", n)
		}
	}
	mm, _ := ji(v["maxmem"])
	cpu, _ := jf(v["cpu"])
	var memPct any
	if mm != 0 {
		used, _ := jf(v["mem"])
		memPct = iround(100.0 * used / float64(mm))
	}
	uptime, _ := ji(v["uptime"])
	maxdisk, _ := jf(v["maxdisk"])
	return map[string]any{
		"name": name, "state": js(v["status"]),
		"node": node, "vmid": vmid, "kind": kind,
		"cpus":    v["cpus"],
		"memMiB":  mm / 1048576,
		"cpuPct":  iround(cpu * 100),
		"memPct":  memPct,
		"upDays":  uptime / daySec,
		"diskGiB": round1(maxdisk / 1073741824.0),
	}
}

var reCPUTM = regexp.MustCompile(`\((R|TM|r|tm)\)`)

// pveCPUModel — "(R)"/"(TM)" 제거 + 이중 공백 정리.
func pveCPUModel(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(reCPUTM.ReplaceAllString(s, ""), "  ", " "))
}

// pveHealth — 건강 판정: 디스크 SMART 실패는 크리티컬, 수명/용량 임박은 경고.
// 수명(wearout)은 남은 수명 % 라 10% 이하면 교체 검토 시점이다.
func pveHealth(disks, storage []map[string]any) (string, []string) {
	reasons := []string{}
	level := "ok"
	for _, dk := range disks {
		h := strings.ToUpper(dk["health"].(string))
		if dk["kind"].(string) != "usb" && h != "" && h != "PASSED" && h != "OK" && h != "UNKNOWN" {
			reasons = append(reasons, fmt.Sprintf("%s SMART %s", dk["dev"], dk["health"]))
			level = "critical"
		} else if w, ok := dk["wearout"].(int64); ok && w <= 10 {
			reasons = append(reasons, fmt.Sprintf("%s 수명 %d%%", dk["dev"], w))
			if level != "critical" {
				level = "warning"
			}
		}
	}
	for _, sp := range storage {
		if sp["pct"].(int64) >= 90 {
			reasons = append(reasons, fmt.Sprintf("%s %d%%", sp["name"], sp["pct"]))
			if level != "critical" {
				level = "warning"
			}
		}
	}
	return level, reasons
}

// pollProxmox — Python poll_proxmox 이식.
func (w *Worker) pollProxmox(pc *pollCtx, dev DeviceConfig, st *pveStatic) (map[string]any, *pveStatic) {
	if st == nil {
		st = &pveStatic{}
	}
	raw, err := w.pveFetch(pc, dev, st)
	if err != nil {
		// 죽은 티켓 폐기 — 다음 라운드 재인증.
		st.Ticket, st.TicketTS = "", 0
		d := baseDevice(dev, "SRV", "down", 0)
		m := d["meta"].(map[string]any)
		alerts := []any{}
		if a := stateAlert("down", pc.now); a != nil {
			alerts = append(alerts, a)
		}
		if authFailed(err) {
			alerts = append([]any{map[string]any{
				"name": "AUTH_FAIL", "desc": "Proxmox API authentication failed",
				"time": tsKST(pc.now), "severity": "critical", "sev": "critical",
			}}, alerts...)
			m["healthLevel"] = "critical"
			m["healthReasons"] = []any{"Proxmox API 인증 실패"}
		}
		m["alerts"] = alerts
		m["error"] = errClass(err)
		m["srv"] = nil
		return d, st
	}
	d, err := pveMap(pc, dev, st, raw)
	if err != nil {
		// Python 은 매핑 단계 예외가 워커까지 전파돼 down 골격으로 대첸다.
		return downBase(dev, pc.now), st
	}
	return d, st
}

// pveFetch — 티켓 + 정적 인벤토리 + 라운드 동적 데이터. API 단계 전용.
func (w *Worker) pveFetch(pc *pollCtx, dev DeviceConfig, st *pveStatic) (pveRaw, error) {
	var raw pveRaw
	ip := dev.IP
	user := dev.User
	if user == "" {
		user = "root@pam"
	}
	api := func(path string, form url.Values, ticket string) (any, error) {
		return pveAPI(pc.ctx, pc.pve, ip, path, form, ticket)
	}
	// 티켓 재사용(90분) — 만료 전 재발급으로 폴싱 라운드당 인증 왕복 제거.
	if st.Ticket == "" || pc.now-st.TicketTS > pveTicketMaxAge {
		t, err := api("/access/ticket", url.Values{
			"username": {user}, "password": {dev.Password},
		}, "")
		if err != nil {
			return raw, err
		}
		tk := js(jm(t)["ticket"])
		if tk == "" {
			return raw, &valueError{Msg: "edge: pve ticket 응답에 ticket 없음"}
		}
		st.Ticket, st.TicketTS = tk, pc.now
	}
	tk := st.Ticket

	if pc.refresh || st.Node == "" {
		csv, err := api("/cluster/status", nil, tk)
		if err != nil {
			return raw, err
		}
		node := ""
		for _, n := range jl(csv) {
			nm := jm(n)
			if js(nm["type"]) == "node" && jtruthy(nm["local"]) {
				node = js(nm["name"])
				break
			}
		}
		if node == "" {
			node = "localhost"
		}
		st.Node = node
		ver, err := api("/version", nil, tk)
		if err != nil {
			return raw, err
		}
		st.Version = js(jm(ver)["version"])
	}
	node := st.Node

	// 정적 인벤토리 — 실패핮도 라운드는 계속(각각 독립 try, 기존 값 유지).
	// Python 의 게이트 조건은 `"net" in static` 하나다.
	if pc.refresh || !st.HasNet {
		if v, err := api("/nodes/"+node+"/network", nil, tk); err == nil {
			st.Net = pveNet(jl(v))
		} else if !st.HasNet {
			st.Net = []map[string]any{}
		}
		st.HasNet = true
		if v, err := api("/nodes/"+node+"/disks/list", nil, tk); err == nil {
			st.Disks = pveDisks(jl(v))
		} else if !st.HasDisks {
			st.Disks = []map[string]any{}
		}
		st.HasDisks = true
		if v, err := api("/nodes/"+node+"/storage", nil, tk); err == nil {
			st.Storage = pveStorage(jl(v))
		} else if !st.HasStorage {
			st.Storage = []map[string]any{}
		}
		st.HasStorage = true
	}

	stt, err := api("/nodes/"+node+"/status", nil, tk)
	if err != nil {
		return raw, err
	}
	raw.NodeStatus = jm(stt)
	if raw.NodeStatus == nil {
		raw.NodeStatus = map[string]any{}
	}
	q, err := api("/nodes/"+node+"/qemu", nil, tk)
	if err != nil {
		return raw, err
	}
	raw.Qemu = jl(q)
	lxc, err := api("/nodes/"+node+"/lxc", nil, tk)
	if err == nil {
		raw.Lxc = jl(lxc)
	}
	return raw, nil
}

// pveMap — 동적 데이터 + 정적 캐시 → 프런트 device. 순수 매핑(테스트 대상).
func pveMap(pc *pollCtx, dev DeviceConfig, st *pveStatic, raw pveRaw) (map[string]any, error) {
	ip := dev.IP
	node := st.Node
	stt := raw.NodeStatus

	mem := jm(stt["memory"])
	memTot := jiOr(mem["total"], 1)
	if memTot < 1 {
		memTot = 1
	}
	memUsed, _ := jf(mem["used"])
	memPct := iround(100.0 * memUsed / float64(memTot))
	cpuF, _ := jf(stt["cpu"])
	cpuPct := iround(cpuF * 100)
	upSec, _ := ji(stt["uptime"])
	upDays := upSec / daySec

	vms := []map[string]any{}
	for _, v := range raw.Qemu {
		vms = append(vms, pveVM(jm(v), "qemu", node))
	}
	for _, v := range raw.Lxc {
		vms = append(vms, pveVM(jm(v), "lxc", node))
	}
	running := 0
	for _, v := range vms {
		if strings.EqualFold(v["state"].(string), "running") {
			running++
		}
	}

	ci := jm(stt["cpuinfo"])
	sw := jm(stt["swap"])
	rf := jm(stt["rootfs"])
	boot := jm(stt["boot-info"])
	kver := js(stt["kversion"])
	kshort := kver
	if parts := strings.Fields(kver); len(parts) > 1 {
		kshort = parts[1]
	}
	cpuModel := pveCPUModel(js(ci["model"]))

	la := jl(stt["loadavg"])
	var loadAvg any
	if len(la) > 0 {
		f := make([]float64, 0, 3)
		for _, x := range la {
			if len(f) >= 3 {
				break
			}
			fv, ok := jf(x)
			if !ok {
				return nil, &valueError{Msg: "edge: pve loadavg parse: " + js(x)}
			}
			f = append(f, fv)
		}
		loadAvg = f
	}

	d := baseDevice(dev, "SRV", "op", upDays)
	d["cpu0"], d["mem0"], d["cpuNA"], d["memNA"] = cpuPct, memPct, false, false
	m := d["meta"].(map[string]any)
	vendor := dev.Vendor
	if vendor == "" {
		vendor = "Proxmox"
	}
	m["vendor"] = vendor
	pveVer := st.Version
	if pveVer == "" {
		pveVer = "?"
	}
	m["version"] = "PVE " + pveVer
	m["platform"] = "proxmox"
	cpuPct1 := round1(cpuF * 100) // 유휴 서버 0% 불신 방지 — '<1%' 표시용
	memTotF, _ := jf(mem["total"])
	m["nodes"] = []any{map[string]any{
		"name": node, "state": "running", "standing": "normal", "mode": "normal",
		"primary": true, "ip": ip, "reachable": true, "metricsSource": "pve-api",
		"cpu_pct": cpuPct, "cpu_pct1": cpuPct1, "mem_pct": memPct,
		"loadAvg":  loadAvg,
		"memGiB":   round1(memTotF / 1073741824.0),
		"cpuModel": cpuModel,
		"vmCount":  running,
	}}
	vmList := vms
	if len(vmList) > 40 {
		vmList = vmList[:40]
	}
	listAny := make([]any, 0, len(vmList))
	for _, v := range vmList {
		listAny = append(listAny, v)
	}
	m["vmList"] = listAny
	m["vms"], m["vmRunning"] = len(vms), running

	swTot, _ := jf(sw["total"])
	swUsed, _ := jf(sw["used"])
	var swapUsedPct any
	if swTot != 0 {
		swapUsedPct = iround(100.0 * swUsed / swTot)
	}
	rfTot, _ := jf(rf["total"])
	rfUsed, _ := jf(rf["used"])
	var rootfsPct any
	if rfTot != 0 {
		rootfsPct = iround(100.0 * rfUsed / rfTot)
	}
	bootMode := js(boot["mode"])
	bootStr := ""
	if bootMode == "efi" {
		bootStr = "UEFI"
		if jtruthy(boot["secureboot"]) {
			bootStr += " · Secure Boot"
		}
	} else if bootMode != "" {
		bootStr = "BIOS"
	}
	waitF, _ := jf(stt["wait"])
	m["srv"] = map[string]any{
		"node": node, "load": la, "pve": st.Version,
		"cpuPct1":  cpuPct1,
		"kernel":   kshort,
		"cpuModel": cpuModel,
		"sockets":  ci["sockets"], "cores": ci["cores"], "threads": ci["cpus"],
		"memGiB":        round1(memTotF / 1073741824.0),
		"swapGiB":       round1(swTot / 1073741824.0),
		"swapUsedPct":   swapUsedPct,
		"rootfsGiB":     round1(rfTot / 1073741824.0),
		"rootfsUsedGiB": round1(rfUsed / 1073741824.0),
		"rootfsPct":     rootfsPct,
		"boot":          bootStr,
		"iowaitPct":     round1(waitF * 100),
	}
	net := st.Net
	if net == nil {
		net = []map[string]any{}
	}
	disks := st.Disks
	if disks == nil {
		disks = []map[string]any{}
	}
	storage := st.Storage
	if storage == nil {
		storage = []map[string]any{}
	}
	m["srvNet"] = net
	m["srvDisks"] = disks
	m["srvStorage"] = storage

	level, reasons := pveHealth(disks, storage)
	m["healthLevel"] = level
	reasonsAny := make([]any, 0, len(reasons))
	for _, r := range reasons {
		reasonsAny = append(reasonsAny, r)
	}
	m["healthReasons"] = reasonsAny
	alerts := []any{}
	if level == "critical" {
		alerts = append(alerts, map[string]any{
			"name": "DISK_SMART", "desc": strings.Join(reasons, "; "),
			"time": tsKST(pc.now), "severity": "critical", "sev": "critical",
		})
	}
	m["alerts"] = alerts
	return d, nil
}
