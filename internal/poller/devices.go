package poller

// devices.go — 수집 코어(poller)의 상태 도출 및 관련 기본 타입.
//
// 프레젠테이션 뷰 빌더 계층(BuildDevices, BuildDevice, meta* 함수군)은
// 수집 코어와 화면 어댑터의 책임을 분리하기 위해 신규 패키지 internal/deviceview 로 이동되었다.
// 본 파일은 노드 실상태/이중화 상태 판정(DeriveStatus, deriveSync) 및 수집 기본 메타를 담당한다.

import (
	"sort"
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

// ---------------------------------------------------------------------------
// 상태 도출 (수집 코어 책임)
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
	return DeriveSync(view, status)
}

// DeriveSync 는 FT 미러/이중화 상태 → 'sync' | 'simplex' | 'offline' 이다.
func DeriveSync(view map[string]any, status string) string {
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
	return AvailNominal(status)
}

// AvailNominal 은 상태의 명목 가용성 환산이다.
func AvailNominal(status string) float64 {
	switch status {
	case "op":
		return 99.99
	case "deg":
		return 99.9
	}
	return 99.0
}

// BuildDevices 는 EventWatcher 가 클러스터 스냅샷에서 장비 상태/알림을 추출할 때 사용하는 수집 레벨 어댑터다.
// 프런트엔드 서빙을 위한 완전한 뷰 변환은 internal/deviceview.BuildDevices 가 전담한다.
func BuildDevices(fleet map[string]any, cfgByKey map[string]DisplayMeta, refreshSec int) map[string]any {
	devices := []any{}
	for _, cv := range listVal(fleet["clusters"]) {
		c := dictVal(cv)
		if c == nil {
			continue
		}
		key := strVal(c["key"])
		if key == "" {
			key = "cluster"
		}
		status := DeriveStatus(c)
		cfg := cfgByKey[key]
		label := cfg.Label
		if label == "" {
			label = strVal(c["name"])
		}
		if label == "" {
			label = key
		}
		alerts := metaAlerts(c)
		devices = append(devices, map[string]any{
			"id":     key,
			"host":   key,
			"status": status,
			"meta": map[string]any{
				"label":  label,
				"alerts": alerts,
			},
		})
	}
	return map[string]any{
		"devices": devices,
	}
}

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
