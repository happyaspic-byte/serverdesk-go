// Package demo provides an explicitly enabled, read-only sample inventory.
//
// The records in this package are presentation fixtures only. They are never
// converted to config.ClusterConfig or edge.DeviceConfig, so enabling demo mode
// cannot start AVCLI, SSH, SNMP, HTTP, or FINS collection against these targets.
package demo

// Source is the API source marker used for every sample-mode response and
// sample device. Keep this distinct from the production "live" source.
const Source = "sample"

// Devices returns a fresh, independent copy of the three synthetic devices.
// All addresses are RFC 5737 documentation ranges and are never polled.
func Devices() []map[string]any {
	return []map[string]any{
		{
			"id": "sample-everrun-01", "host": "sample-everrun-01", "type": "EV",
			"site": "오프라인 데모", "status": "op", "availN": 99.99,
			"cpu0": 34, "mem0": 52, "cpuNA": false, "memNA": false,
			"sync": "sync", "uptime": 187, "live": false, "source": Source,
			"histCpu": []any{29, 31, 33, 30, 35, 34},
			"histMem": []any{49, 50, 50, 51, 52, 52},
			"histRtt": []any{1.2, 1.1, 1.3, 1.1, 1.2, 1.1},
			"meta": map[string]any{
				"demo": true, "sample": true,
				"label":   "[샘플] everRun 정상 클러스터",
				"company": "샘플 고객사", "factory": "데모 공장",
				"mgmt": "192.0.2.10", "assetTag": "SAMPLE-EV-001",
				"floorPos": "1,1", "vendor": "Stratus", "platform": "everRun",
				"version": "샘플 버전", "pending": false, "error": nil,
				"nodes": []any{
					map[string]any{
						"name": "sample-ev-node-a", "state": "running", "standing": "normal",
						"mode": "active", "primary": true, "manufacturer": "Stratus",
						"model": "Sample node", "cpus": "16", "memory": "64 GiB",
						"ip": "192.0.2.11", "reachable": true, "metricsSource": Source,
						"cpuModel": "Sample CPU", "cores": 16, "memGiB": 64, "vmCount": 1,
					},
					map[string]any{
						"name": "sample-ev-node-b", "state": "running", "standing": "normal",
						"mode": "standby", "primary": false, "manufacturer": "Stratus",
						"model": "Sample node", "cpus": "16", "memory": "64 GiB",
						"ip": "192.0.2.12", "reachable": true, "metricsSource": Source,
						"cpuModel": "Sample CPU", "cores": 16, "memGiB": 64, "vmCount": 1,
					},
				},
				"snmp": []any{
					map[string]any{"ip": "192.0.2.11", "reachable": true, "uptime_days": 187, "cpu": 32, "mem": 50, "source": Source},
					map[string]any{"ip": "192.0.2.12", "reachable": true, "uptime_days": 187, "cpu": 36, "mem": 54, "source": Source},
				},
				"vmList": []any{
					map[string]any{"name": "sample-app-01", "state": "running", "ft": "ft", "vcpu": 4, "memory": "8 GiB", "node": "sample-ev-node-a", "nodes": []any{"sample-ev-node-a", "sample-ev-node-b"}, "standbyNodes": []any{"sample-ev-node-b"}},
					map[string]any{"name": "sample-db-01", "state": "running", "ft": "ft", "vcpu": 8, "memory": "16 GiB", "node": "sample-ev-node-b", "nodes": []any{"sample-ev-node-a", "sample-ev-node-b"}, "standbyNodes": []any{"sample-ev-node-a"}},
				},
				"vms": 2, "vmRunning": 2,
				"unit":    map[string]any{"name": "sample-everrun", "version": "샘플", "syncing": "false", "totVcpu": 32, "usedVcpu": 12, "totMem": 128, "usedMem": 66},
				"license": map[string]any{"name": "SAMPLE LICENSE — NOT VALID", "type": "sample", "edition": "Synthetic", "expires": false, "activated": false},
				"alerts":  []any{}, "traps": []any{}, "events": []any{},
				"topo":        map[string]any{"source": Source, "networks": []any{"sample-lan"}, "storage": []any{}},
				"healthLevel": "ok", "healthReasons": []any{}, "alertCounts": map[string]any{},
			},
		},
		{
			"id": "sample-edge-01", "host": "sample-edge-01", "type": "EDGE",
			"site": "오프라인 데모", "status": "deg", "availN": 99.9,
			"cpu0": 43, "mem0": 61, "cpuNA": false, "memNA": false,
			"sync": "simplex", "uptime": 92, "live": false, "source": Source,
			"histCpu": []any{37, 40, 39, 42, 44, 43},
			"histMem": []any{57, 58, 59, 60, 61, 61},
			"histRtt": []any{2.2, 2.0, 2.5, 2.4, 2.1, 2.3},
			"meta": map[string]any{
				"demo": true, "sample": true,
				"label":   "[샘플] ztC Edge 점검 필요",
				"company": "샘플 고객사", "factory": "데모 공장",
				"mgmt": "198.51.100.10", "assetTag": "SAMPLE-EDGE-001",
				"floorPos": "1,2", "vendor": "Stratus", "platform": "ztC Edge",
				"version": "샘플 버전", "pending": false, "error": nil,
				"issueSince": "2026-01-15 09:30:00",
				"nodes": []any{
					map[string]any{
						"name": "sample-edge-node-a", "state": "running", "standing": "normal",
						"mode": "active", "primary": true, "manufacturer": "Stratus",
						"model": "Sample edge node", "cpus": "8", "memory": "32 GiB",
						"ip": "198.51.100.11", "reachable": true, "metricsSource": Source,
						"cpuModel": "Sample CPU", "cores": 8, "memGiB": 32, "vmCount": 1,
					},
					map[string]any{
						"name": "sample-edge-node-b", "state": "running", "standing": "maintenance",
						"mode": "standby", "primary": false, "manufacturer": "Stratus",
						"model": "Sample edge node", "cpus": "8", "memory": "32 GiB",
						"ip": "198.51.100.12", "reachable": true, "metricsSource": Source,
						"cpuModel": "Sample CPU", "cores": 8, "memGiB": 32, "vmCount": 0,
					},
				},
				"snmp": []any{
					map[string]any{"ip": "198.51.100.11", "reachable": true, "uptime_days": 92, "cpu": 43, "mem": 61, "source": Source},
					map[string]any{"ip": "198.51.100.12", "reachable": true, "uptime_days": 92, "cpu": 12, "mem": 28, "source": Source},
				},
				"vmList": []any{
					map[string]any{"name": "sample-control-01", "state": "running", "ft": "ha", "vcpu": 4, "memory": "8 GiB", "node": "sample-edge-node-a", "nodes": []any{"sample-edge-node-a", "sample-edge-node-b"}, "standbyNodes": []any{"sample-edge-node-b"}},
				},
				"vms": 1, "vmRunning": 1,
				"unit":    map[string]any{"name": "sample-edge", "version": "샘플", "syncing": "false", "totVcpu": 16, "usedVcpu": 4, "totMem": 64, "usedMem": 39},
				"license": map[string]any{"name": "SAMPLE LICENSE — NOT VALID", "type": "sample", "edition": "Synthetic", "expires": false, "activated": false},
				"alerts": []any{
					map[string]any{"name": "SAMPLE_MAINTENANCE", "desc": "SAMPLE: 샘플 노드가 유지보수 상태입니다", "severity": "warning", "sev": "warning", "time": "2026-01-15 09:30:00"},
				},
				"traps": []any{}, "events": []any{},
				"topo":        map[string]any{"source": Source, "networks": []any{"sample-edge-lan"}, "storage": []any{}},
				"healthLevel": "warning", "healthReasons": []any{"sample maintenance"}, "alertCounts": map[string]any{"warning": 1},
			},
		},
		{
			"id": "sample-nas-01", "host": "sample-nas-01", "type": "NAS",
			"site": "오프라인 데모", "status": "op", "availN": 99.99,
			"cpu0": 27, "mem0": 48, "cpuNA": false, "memNA": false,
			"sync": "sync", "uptime": 64, "live": false, "source": Source,
			"histCpu": []any{22, 24, 23, 26, 28, 27},
			"histMem": []any{45, 46, 46, 47, 48, 48},
			"histRtt": []any{0.9, 1.0, 0.8, 0.9, 1.1, 0.9},
			"meta": map[string]any{
				"demo": true, "sample": true,
				"label":   "[샘플] 백업 NAS",
				"company": "샘플 고객사", "factory": "데모 공장",
				"mgmt": "203.0.113.10", "assetTag": "SAMPLE-NAS-001",
				"floorPos": "2,1", "vendor": "Sample Storage", "pending": false, "error": nil,
				"nodes": []any{}, "snmp": []any{
					map[string]any{"ip": "203.0.113.10", "reachable": true, "uptime_days": 64, "cpu": 27, "mem": 48, "source": Source},
				},
				"vmList": []any{}, "vms": 0, "vmRunning": 0,
				"alerts": []any{}, "traps": []any{}, "events": []any{},
				"nas": map[string]any{
					"model": "Sample NAS", "dsmVersion": "샘플", "serial": "SAMPLE-NAS-001",
					"tempC": 39, "systemStatus": "normal", "fansOk": true, "powerStatus": "normal", "upgradeAvailable": false,
					"disks": []any{
						map[string]any{"name": "Drive 1", "model": "SAMPLE-DISK", "status": "normal", "ok": true, "tempC": 35},
						map[string]any{"name": "Drive 2", "model": "SAMPLE-DISK", "status": "normal", "ok": true, "tempC": 36},
						map[string]any{"name": "Drive 3", "model": "SAMPLE-DISK", "status": "normal", "ok": true, "tempC": 37},
						map[string]any{"name": "Drive 4", "model": "SAMPLE-DISK", "status": "normal", "ok": true, "tempC": 38},
					},
					"lanPorts": []any{
						map[string]any{"name": "LAN 1", "ip": "203.0.113.10", "status": "up", "up": true},
						map[string]any{"name": "LAN 2", "ip": "203.0.113.11", "status": "up", "up": true},
					},
					"volumes": []any{
						map[string]any{"name": "sample-backup", "pct": 46, "status": "normal", "usedGiB": 3768, "sizeGiB": 8192},
					},
				},
				"healthLevel": "ok", "healthReasons": []any{}, "alertCounts": map[string]any{},
			},
		},
	}
}
