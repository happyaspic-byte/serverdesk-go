package poller

// 평면 토폴로지 그래프 빌더 (poller.py build_topology 포트).
// id 기반 그래프로, 프런트가 바로 그릴 수 있는 노드/엣지 형태다.
// 상태 어휘는 ok/warning/critical/unknown 로 고정(계약) — 상세 모델의 degraded 는
// 순위를 유지한 채 warning 으로 낮춘다.

import (
	"serverdesk/internal/avcli"
	"serverdesk/internal/topology"
)

// flatStatusRank 는 topology 패키지의 비공개 순위(ok<unknown<warning<degraded<
// critical)와 같은 값이다. 평면 모델의 감쇠 계산(_with_alert)에 필요한데 패키지
// 상수가 비공개라 여기서 같은 표를 유지한다.
var flatStatusRank = map[string]int{"ok": 0, "unknown": 1, "warning": 2, "degraded": 3, "critical": 4}

var flatStatusByRank = map[int]string{0: "ok", 1: "unknown", 2: "warning", 3: "degraded", 4: "critical"}

// flat 은 상세 모델의 상태를 평면 어휘로 내린다(degraded → warning).
func flat(status string) string {
	if status == "degraded" {
		return "warning"
	}
	if status == "" {
		return "unknown"
	}
	return status
}

func flatMax(a, b string) string {
	return flat(topology.StatusMax(a, b))
}

// withAlert 는 R11 감쇠다: 권위 상태가 ok 인데 알림만 심각하면 해소된 과거 알림일
// 수 있으니 알림 측을 warning 까지만 올린다(poller.py _with_alert).
func withAlert(base, alertSev string) string {
	if alertSev == "" {
		return base
	}
	if base == "ok" {
		// ALERT_DAMPED_CEILING = "warning"
		r := flatStatusRank[alertSev]
		if r > flatStatusRank["warning"] {
			r = flatStatusRank["warning"]
		}
		alertSev = flatStatusByRank[r]
	}
	return flatMax(base, flat(alertSev))
}

// alertStatusByTarget 은 알림 → {(대상타입, 이름): 최악 심각도} 맵이다.
// 분류/추출 규칙은 topology 패키지의 것을 재사용한다(두 구현이 어긋나면 안 된다).
func alertStatusByTarget(view map[string]any) map[[2]string]string {
	out := map[[2]string]string{}
	for _, av := range listVal(view["alerts"]) {
		a := dictVal(av)
		if a == nil {
			continue
		}
		al := topology.AlertInput{
			Name:        strVal(a["name"]),
			Description: strVal(a["description"]),
			Severity:    strVal(a["severity_raw"]),
		}
		sev, _ := topology.ClassifyAlert(al)
		targets := topology.ExtractAlertTargets(al)
		if sev != "critical" && sev != "degraded" && sev != "warning" {
			continue
		}
		for _, t := range targets {
			k := [2]string{t.Type, t.Name}
			prev := out[k]
			if prev == "" {
				prev = "ok"
			}
			out[k] = topology.StatusMax(prev, sev)
		}
	}
	return out
}

// networkStatusFlat 은 topology 패키지 비공개 networkStatus 와 같은 판정이다:
// fault-tolerant 가 ft/ha 가 아니면 degraded(평면에서는 warning).
func networkStatusFlat(net map[string]any) string {
	ft := lowerStr(strVal(net["fault_tolerant"]))
	if ft != "" && ft != "ft" && ft != "ha" {
		return "degraded"
	}
	return "ok"
}

// BuildFlatTopology 는 클러스터 뷰 1개의 평면 그래프({cluster, nodes, edges})를
// 만든다(poller.py build_topology).
func BuildFlatTopology(view map[string]any) map[string]any {
	nodes := []any{}
	edges := []any{}
	ckey := strVal(view["key"])
	croot := "cluster:" + ckey
	health := dictVal(view["health"])
	nodes = append(nodes, map[string]any{
		"id": croot, "type": "cluster", "label": view["name"],
		"status": strVal(health["level"]), "platform": view["platform"]})

	for _, nv := range listVal(view["nodes"]) {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		nid := strVal(n["id"])
		if nid == "" {
			nid = "host:" + strVal(n["name"])
		}
		// state!=running 은 노드 정지 → critical. 유지보수 모드/standing 이상은 warning.
		var nst string
		if boolVal(n["healthy"]) {
			nst = "ok"
		} else if strVal(n["state"]) != "running" {
			nst = "critical"
		} else {
			nst = "warning"
		}
		nodes = append(nodes, map[string]any{
			"id": nid, "type": "node", "label": n["name"],
			"status": nst, "state": n["state"], "mode": n["mode"],
			"primary": n["primary"], "ip": n["ip"],
			"reachable": n["reachable"],
			"cpu_pct":   n["cpu_pct"], "mem_pct": n["mem_pct"]})
		edges = append(edges, map[string]any{"from": croot, "to": nid, "kind": "member"})
	}

	// 네트워크 상태 채널 — 알림 맵을 얹어 실장비의 critical 네트워크 알림이 보이게.
	alertMap := alertStatusByTarget(view)
	for _, nv := range listVal(view["networks"]) {
		net := dictVal(nv)
		if net == nil {
			continue
		}
		nstat := flat(networkStatusFlat(net))
		nstat = withAlert(nstat, alertMap[[2]string{"sharednetwork", strVal(net["name"])}])
		nodes = append(nodes, map[string]any{
			"id": net["id"], "type": "network", "label": net["name"],
			"role": net["role"], "status": nstat,
			"fault_tolerant": net["fault_tolerant"],
			"bandwidth_bps":  net["bandwidth_bps"]})
		edges = append(edges, map[string]any{"from": croot, "to": net["id"], "kind": "network"})
	}

	for _, gv := range listVal(view["storage_groups"]) {
		g := dictVal(gv)
		if g == nil {
			continue
		}
		// /api/fleet 의 health 와 같은 임계값을 써야 두 엔드포인트가 어긋나지 않는다.
		up, hasUp := numVal(g["used_pct"])
		sgStatus := "unknown"
		if hasUp {
			switch {
			case up >= avcli.SGCritPct:
				sgStatus = "critical"
			case up >= avcli.SGWarnPct:
				sgStatus = "warning"
			default:
				sgStatus = "ok"
			}
		}
		nodes = append(nodes, map[string]any{
			"id": g["id"], "type": "storage_group", "label": g["name"],
			"used_pct": g["used_pct"], "size_bytes": g["size_bytes"],
			"used_bytes": g["used_bytes"], "status": sgStatus})
		edges = append(edges, map[string]any{"from": croot, "to": g["id"], "kind": "storage"})
		for _, dv := range listVal(g["disks"]) {
			d := dictVal(dv)
			if d == nil || strVal(d["id"]) == "" {
				continue
			}
			dstat := "warning"
			if ss := d["standing_state"]; ss == nil || strVal(ss) == "normal" {
				dstat = "ok"
			}
			nodes = append(nodes, map[string]any{
				"id": d["id"], "type": "disk", "label": d["name"], "status": dstat})
			edges = append(edges, map[string]any{"from": g["id"], "to": d["id"], "kind": "contains"})
		}
	}

	// 노드가 보고하는 '이 노드에서 구동 중인 VM' 역인덱스 → placed_on 의 active/standby.
	activeNodesByVM := map[string]map[string]bool{}
	for _, nv := range listVal(view["nodes"]) {
		n := dictVal(nv)
		if n == nil {
			continue
		}
		nid := strVal(n["id"])
		for _, pv := range listVal(n["vm_placements"]) {
			p := dictVal(pv)
			if p == nil {
				continue
			}
			if pid := strVal(p["id"]); pid != "" && nid != "" {
				s := activeNodesByVM[pid]
				if s == nil {
					s = map[string]bool{}
					activeNodesByVM[pid] = s
				}
				s[nid] = true
			}
		}
	}

	netByName := map[string]string{}
	for _, nv := range listVal(view["networks"]) {
		if n := dictVal(nv); n != nil && strVal(n["name"]) != "" {
			netByName[strVal(n["name"])] = strVal(n["id"])
		}
	}
	// Python 계약: storage_group_id 가 null 이면 맵 값도 null 로 유지한다
	// ("" 로 바꾸면 노드 속성이 null 대신 빈 문자열로 나가 스키마가 어긋난다).
	sgByVolume := map[string]any{}
	for _, vv := range listVal(view["volumes"]) {
		if v := dictVal(vv); v != nil && strVal(v["id"]) != "" {
			sgByVolume[strVal(v["id"])] = v["storage_group_id"]
		}
	}

	for _, vmv := range listVal(view["vms"]) {
		vm := dictVal(vmv)
		if vm == nil {
			continue
		}
		vid := strVal(vm["id"])
		if vid == "" {
			continue
		}
		status, ok := map[string]string{"redundant": "ok", "simplex": "warning", "down": "critical"}[strVal(vm["redundancy"])]
		if !ok {
			status = "unknown"
		}
		// 정지된 VM 을 초록으로 칠하면 안 된다. stopped/shutoff → warning,
		// running 이 아닌 그 밖의 상태 → degraded(평면에서는 warning).
		vstate := lowerStr(strVal(vm["state"]))
		if vstate == "stopped" || vstate == "shutoff" || vstate == "shut off" {
			status = flatMax(status, "warning")
		} else if vstate != "" && vstate != "running" {
			status = flatMax(status, flat("degraded"))
		}
		standing := lowerStr(strVal(vm["standing_state"]))
		if standing != "" && standing != "normal" {
			status = flatMax(status, flat("degraded"))
		}
		nodes = append(nodes, map[string]any{
			"id": vid, "type": "vm", "label": vm["name"],
			"status": status, "state": vm["state"],
			"ha_mode": vm["ha_mode"], "cpus": vm["cpus"],
			"memory_bytes": vm["memory_bytes"]})
		// VM → 노드 배치는 local-virtual-machines 기준. FT 는 양쪽 lockstep 이지만
		// HA 는 한쪽에서만 구동되므로 역할을 구분한다.
		isFT := lowerStr(strVal(vm["ha_mode"])) == "ft"
		actives := activeNodesByVM[vid]
		for _, iv := range listVal(vm["instances"]) {
			inst := dictVal(iv)
			if inst == nil {
				continue
			}
			nodeID := strVal(inst["node_id"])
			if nodeID == "" {
				continue
			}
			var role string
			if isFT {
				role = "lockstep"
			} else if len(actives) > 0 {
				if actives[nodeID] {
					role = "active"
				} else {
					role = "standby"
				}
			} else {
				role = "unknown"
			}
			edges = append(edges, map[string]any{
				"from": vid, "to": nodeID, "kind": "placed_on",
				"role": role, "enabled": inst["enabled"], "mtbf": inst["mtbf_status"]})
		}
		for _, iv := range listVal(vm["interfaces"]) {
			iface := dictVal(iv)
			if iface == nil {
				continue
			}
			if tgt, ok := netByName[strVal(iface["shared_network"])]; ok && tgt != "" {
				edges = append(edges, map[string]any{
					"from": vid, "to": tgt, "kind": "attached",
					"redundant": iface["redundant"], "mac": iface["mac"]})
			}
		}
		for _, vv := range listVal(vm["volumes"]) {
			v := dictVal(vv)
			if v == nil || boolVal(v["is_cdrom"]) || strVal(v["id"]) == "" {
				continue
			}
			volid := strVal(v["id"])
			volStatus := "warning"
			if boolVal(v["mirrored"]) {
				volStatus = "ok"
			}
			sgID := sgByVolume[volid] // null 보존
			nodes = append(nodes, map[string]any{
				"id": volid, "type": "volume", "label": v["name"],
				"size_bytes": v["size_bytes"], "storage_group_id": sgID,
				"status": volStatus})
			edges = append(edges, map[string]any{"from": vid, "to": volid, "kind": "uses"})
			if sg, ok := sgID.(string); ok && sg != "" {
				edges = append(edges, map[string]any{"from": volid, "to": sg, "kind": "stored_on"})
			}
			for _, dv := range listVal(v["disk_images"]) {
				di := dictVal(dv)
				if di == nil {
					continue
				}
				if nodeID := strVal(di["node_id"]); nodeID != "" {
					edges = append(edges, map[string]any{
						"from": volid, "to": nodeID, "kind": "mirror",
						"enabled": di["enabled"]})
				}
			}
		}
	}

	// volume-info 전체(시스템 볼륨 포함)를 등록한다 — VM 볼륨만 돌면 시스템 볼륨이
	// 그래프에 빠져 스토리지 그룹 용량 소비자를 추적할 수 없다.
	// 중복 id 는 아래 dedup 이 흡수한다(VM 볼륨 쪽 항목이 우선).
	for _, vv := range listVal(view["volumes"]) {
		v := dictVal(vv)
		if v == nil || strVal(v["id"]) == "" {
			continue
		}
		nodes = append(nodes, map[string]any{
			"id": v["id"], "type": "volume", "label": v["name"],
			"size_bytes": v["size_bytes"], "storage_group_id": v["storage_group_id"],
			"bootable": v["bootable"], "status": "unknown"})
		if sg := strVal(v["storage_group_id"]); sg != "" {
			edges = append(edges, map[string]any{
				"from": v["id"], "to": sg, "kind": "stored_on"})
		}
	}

	// 중복 제거(같은 id 가 여러 경로로 추가될 수 있음)
	seen := map[string]bool{}
	uniq := make([]any, 0, len(nodes))
	for _, nv := range nodes {
		n := dictVal(nv)
		id := strVal(n["id"])
		if seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, nv)
	}
	eseen := map[[3]string]bool{}
	euniq := make([]any, 0, len(edges))
	for _, ev := range edges {
		e := dictVal(ev)
		k := [3]string{strVal(e["from"]), strVal(e["to"]), strVal(e["kind"])}
		if eseen[k] {
			continue
		}
		eseen[k] = true
		euniq = append(euniq, ev)
	}
	return map[string]any{"cluster": ckey, "nodes": uniq, "edges": euniq}
}
