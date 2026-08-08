package edge

import "strings"

// dtypeForKind — Python POLLERS 매핑의 프런트 type 코드.
// proxmox/server 는 둘 다 SRV 로 낸다(프런트는 type 으로 렌더링을 고른다).
func dtypeForKind(kind string) string {
	switch kind {
	case "printer":
		return "PRN"
	case "nas":
		return "NAS"
	case "plc":
		return "PLC"
	default:
		return "SRV"
	}
}

// baseDevice — Python _base(): 모든 kind 공통의 플랫 device dict 골격.
// 프런트는 이 스키마(serverdesk/device@1)를 그대로 소비하므로 키 집합과
// "빈 값은 null 이 아니라 빈 배열/객체" 관행을 정확히 유지한다.
func baseDevice(dev DeviceConfig, dtype, status string, uptimeDays int64) map[string]any {
	key := dev.Key
	ip := dev.IP
	syncState := "offline"
	avail := 99.0
	switch status {
	case "op":
		syncState, avail = "sync", 99.99
	case "deg":
		syncState, avail = "simplex", 99.9
	}
	factory := dev.Factory
	if factory == "" {
		parts := strings.Split(ip, ".")
		if len(parts) > 3 {
			parts = parts[:3]
		}
		factory = strings.Join(parts, ".") + ".0/24"
	}
	company := dev.Company
	if company == "" {
		company = "루비컴"
	}
	label := dev.Name
	if label == "" {
		label = key
	}
	meta := map[string]any{
		"label": label, "company": company, "factory": factory,
		"mgmt": ip, "assetTag": dev.AssetTag, "floorPos": dev.FloorPos,
		"vendor": dev.Vendor,
		"error":  nil, "pending": false,
		"version": "", "uuid": "", "platform": dev.kind(),
		"alerts": []any{}, "alertCounts": map[string]any{},
		"healthLevel": "unknown", "healthReasons": []any{},
		"traps": []any{}, "snmp": []any{}, "nodes": []any{},
		"vmList": []any{}, "vms": 0, "vmRunning": 0,
		"unit": nil, "license": nil,
		"lastVmSwitch": nil, "lastNodeSwitch": nil, "lastReboot": nil,
		"bmc": nil, "events": []any{}, "topo": nil,
		"collection": map[string]any{}, "stale": false, "tzName": "KST",
	}
	return map[string]any{
		"id": key, "host": key, "type": dtype,
		"site":   siteOrIP(dev),
		"status": status,
		// 가용성 %(상태의 순수 함수) — devices_adapter._avail_n 과 동일 스케일.
		"availN": avail,
		"cpu0":   int64(-1), "mem0": int64(-1),
		"cpuNA": true, "memNA": true,
		"sync": syncState, "uptime": uptimeDays, "live": true,
		"meta":    meta,
		"histCpu": []int64{}, "histMem": []int64{}, "histRtt": []any{},
	}
}

func siteOrIP(dev DeviceConfig) string {
	if dev.Site != "" {
		return dev.Site
	}
	return dev.IP
}

// stateAlert — Python _state_alert(): 비정상 상태 자체를 경보로 노출.
// 수집기 무응답(down)은 critical, 부분 이상(deg)은 warning.
func stateAlert(status string, now float64) map[string]any {
	if status == "op" {
		return nil
	}
	desc := "Device degraded — check hardware status"
	sev := "warning"
	if status == "down" {
		desc = "Device offline — no response to the collector"
		sev = "critical"
	}
	return map[string]any{
		"name": "DEVICE_STATE", "desc": desc, "time": tsKST(now),
		"severity": sev, "sev": sev,
	}
}

// downBase — 폴로 예외/무응답 시 낼 down 골격. Python 의 워커 예외 경로는
// asset_tag/floor_pos/vendor 를 떨군 부분 dict 로 _base 를 호출한다 — 그 관행을
// 그대로 따라 예외 경로 산출물이 Python 과 같아지게 한다.
func downBase(dev DeviceConfig, now float64) map[string]any {
	dev.AssetTag, dev.FloorPos, dev.Vendor = "", "", ""
	d := baseDevice(dev, dtypeForKind(dev.kind()), "down", 0)
	if a := stateAlert("down", now); a != nil {
		d["meta"].(map[string]any)["alerts"] = []any{a}
	}
	return d
}
