package poller

// NIC<->shared-network 확정 매핑 빌더 (poller.py 의 reconcile_spine_networks /
// build_nic_network_map 포트).
//
// NIC 와 네트워크의 관계는 추측하지 않고 노드의 spine 설정(/etc/opt/ft/spine,
// SSH 로 읽은 확정 소스)에서 읽는다. 설정 파일(config.local.json)의
// nic_network_map 이 있으면 그쪽이 최우선이다.

import (
	"sort"
	"strings"
	"time"

	"serverdesk/internal/avcli"
	"serverdesk/internal/sshmetrics"
	"serverdesk/internal/topology"
)

// spineRole 은 spine 네트워크의 role 을 소문자로 읽는다(nil 안전).
func spineRole(s sshmetrics.SpineNetwork) string {
	if s.Role == nil {
		return ""
	}
	return lowerStr(*s.Role)
}

// spineOrdinal 은 정렬 키용 ordinal 이다. 없으면 Python 과 같이 큰 값(1<<30)을 쓴다.
func spineOrdinal(s sshmetrics.SpineNetwork) int64 {
	if s.Ordinal == nil {
		return 1 << 30
	}
	return *s.Ordinal
}

func lowerStr(s string) string {
	return strings.ToLower(s)
}

// spineXlat 은 spine 네트워크 이름 -> (avcli shared-network 이름, evidence, confidence) 다.
type spineXlat struct {
	name       string
	evidence   string
	confidence float64
}

// reconcileSpineNetworks 는 spine 네트워크 이름을 avcli shared-network 이름으로 변환한다.
//
// everRun 은 두 이름이 같다(network0/priv0/net_82) → 그대로 매칭된다.
// ztC Edge 는 spine 이름(network0..2, alink_1)과 avcli 표시명(P1..P3, A1/A2)이 달라,
// 같은 role 안에서 spine ordinal 순위와 avcli 이름 순위를 맞춰 짝짓는다.
// 개수가 다르면 짝짓지 않는다(오매핑보다 미매핑이 낫다).
func reconcileSpineNetworks(spineNets []sshmetrics.SpineNetwork, avcliNets []avcli.SharedNetwork) map[string]spineXlat {
	avail := map[string]bool{}
	for _, n := range avcliNets {
		if n.Name != nil && *n.Name != "" {
			avail[*n.Name] = true
		}
	}
	out := map[string]spineXlat{}
	var pending []sshmetrics.SpineNetwork
	for _, sn := range spineNets {
		nm := sn.Name
		if nm == "" {
			continue
		}
		if avail[nm] {
			out[nm] = spineXlat{nm, "config", 1.0}
		} else {
			pending = append(pending, sn)
		}
	}
	used := map[string]bool{}
	for _, v := range out {
		used[v.name] = true
	}
	for _, role := range []string{"a-link", "business"} {
		var srcs []sshmetrics.SpineNetwork
		for _, s := range pending {
			if spineRole(s) == role {
				srcs = append(srcs, s)
			}
		}
		sort.SliceStable(srcs, func(i, j int) bool {
			oi, oj := spineOrdinal(srcs[i]), spineOrdinal(srcs[j])
			if oi != oj {
				return oi < oj
			}
			return srcs[i].Name < srcs[j].Name
		})
		var dsts []string
		for _, n := range avcliNets {
			nm := strp(n.Name)
			rl := ""
			if n.Role != nil {
				rl = *n.Role
			}
			if lowerStr(rl) == role && nm != "" && !used[nm] {
				dsts = append(dsts, nm)
			}
		}
		sort.Strings(dsts)
		if len(srcs) > 0 && len(srcs) == len(dsts) {
			for i, s := range srcs {
				out[s.Name] = spineXlat{dsts[i], "config-ordinal", 0.9}
			}
		}
	}
	return out
}

// BuildNICNetworkMap 은 {노드이름: {ifname: 매핑}} 을 만든다
// (poller.py build_nic_network_map). 값은 {network,evidence,confidence} 맵이거나
// (명시 설정 오버라이드이면) 네트워크 이름 문자열이다 — Python 과 같은 2형태 계약.
func BuildNICNetworkMap(nodeSpineByName map[string]*sshmetrics.Spine, avcliNets []avcli.SharedNetwork,
	explicit map[string]map[string]string) map[string]any {
	out := map[string]any{}
	// 결정적 출력을 위해 노드 이름 정렬 순회.
	nodeNames := make([]string, 0, len(nodeSpineByName))
	for name := range nodeSpineByName {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)
	for _, nodeName := range nodeNames {
		spine := nodeSpineByName[nodeName]
		if nodeName == "" || spine == nil {
			continue
		}
		xlat := reconcileSpineNetworks(spine.Networks, avcliNets)
		ifNames := make([]string, 0, len(spine.NICNetworks))
		for ifn := range spine.NICNetworks {
			ifNames = append(ifNames, ifn)
		}
		sort.Strings(ifNames)
		for _, ifName := range ifNames {
			spineNet := spine.NICNetworks[ifName] // *string: nil = '소속 없음' 확정
			var netName string
			if spineNet != nil {
				netName = *spineNet
			}
			if netName != "" {
				if hit, ok := xlat[netName]; ok {
					nodeMap(out, nodeName)[ifName] = map[string]any{
						"network": hit.name, "evidence": hit.evidence, "confidence": hit.confidence}
				}
				// xlat 에 없으면(avcli 네트워크 목록과 못 맞춤) 키를 남기지 않는다.
			} else {
				// spine 이 "소속 네트워크 없음"이라고 확정한 포트(케이블 미연결 예비
				// 포트). 키를 남겨야 소비자가 '미상'과 '확정된 미사용'을 구분한다.
				nodeMap(out, nodeName)[ifName] = map[string]any{
					"network": nil, "evidence": "config", "confidence": 1.0}
			}
		}
	}
	for nodeName, d := range explicit {
		for ifName, netName := range d {
			nodeMap(out, nodeName)[ifName] = netName
		}
	}
	return out
}

func nodeMap(m map[string]any, node string) map[string]any {
	if v, ok := m[node].(map[string]any); ok {
		return v
	}
	nm := map[string]any{}
	m[node] = nm
	return nm
}

// TypedNICNetworkMap 은 BuildNICNetworkMap 의 결과를 topology.NICNetworkMap 으로
// 변환한다. Evidence/Confidence 가 비어 있으면 topology 빌더가 "config"/1.0 으로
// 채우므로 명시 문자열 오버라이드는 제로값으로 둔다(Python 의 문자열 형태와 동치).
func TypedNICNetworkMap(m map[string]any) topology.NICNetworkMap {
	out := topology.NICNetworkMap{}
	for node, v := range m {
		d, ok := v.(map[string]any)
		if !ok {
			continue
		}
		nm := map[string]topology.NICMapping{}
		for ifName, mv := range d {
			switch val := mv.(type) {
			case string:
				s := val
				nm[ifName] = topology.NICMapping{Network: &s}
			case map[string]any:
				var netPtr *string
				if ns, ok := val["network"].(string); ok {
					netPtr = &ns
				}
				var conf *float64
				if c, ok := numVal(val["confidence"]); ok {
					conf = &c
				}
				nm[ifName] = topology.NICMapping{
					Network:    netPtr,
					Evidence:   strVal(val["evidence"]),
					Confidence: conf,
				}
			}
		}
		out[node] = nm
	}
	return out
}

// --- 트랩 뷰 (poller.py _fmt_trap_time / _trap_view 포트) ---------------------

// fmtTrapTime 은 트랩 수신 epoch → 알림과 같은 로컬시각 문자열이다
// (정렬 가능, 프런트가 문자열 비교).
func fmtTrapTime(ts float64, tzOff *int64) string {
	var off int64
	if tzOff != nil {
		off = *tzOff
	}
	t := time.Unix(int64(ts), 0).UTC().Add(time.Duration(off) * time.Second)
	return t.Format("2006-01-02 15:04:05")
}

// TrapView 는 ClusterState 트랩 버퍼(원시)를 프런트 device.meta.traps[] 스키마로
// 변환한다. 프런트 TrapsCard/normalizeTrap 이 소비하는 필드: desc, oid, time, src,
// sev. 추가로 name/severity/ts/varbinds 를 함께 실어 로그·디버깅에 쓴다. 최신순.
func TrapView(traps []map[string]any, tzOff *int64, limit int) []any {
	out := make([]any, 0, len(traps))
	for i, t := range traps {
		if i >= limit {
			break
		}
		sev := strVal(t["sev"])
		if sev == "" {
			sev = "info"
		}
		ts, _ := numVal(t["ts"])
		oid := strVal(t["trap_oid"])
		if oid == "" {
			// Go snmp.TrapStore 가 다시 쓴 항목은 "oid" 키를 쓴다(패키지 계약 차이
			// 흡수 — 둘 다 같은 트랩 식별 OID 다).
			oid = strVal(t["oid"])
		}
		desc := strVal(t["desc"])
		if desc == "" {
			desc = strVal(t["name"])
		}
		if desc == "" {
			desc = oid
		}
		if desc == "" {
			desc = "SNMP trap"
		}
		varbinds := make([]any, 0)
		for _, vb := range listVal(t["varbinds"]) {
			vm := dictVal(vb)
			if vm == nil {
				continue
			}
			value := vm["display"]
			if value == nil {
				value = vm["value"]
			}
			varbinds = append(varbinds, map[string]any{
				"oid": vm["oid"], "name": vm["name"], "value": value})
		}
		out = append(out, map[string]any{
			"time":     fmtTrapTime(ts, tzOff),
			"ts":       int64(ts),
			"src":      strVal(t["src"]),
			"oid":      oid,
			"name":     strVal(t["name"]),
			"desc":     desc,
			"sev":      sev,
			"severity": sev,
			"pdu":      strVal(t["pdu"]),
			"varbinds": varbinds,
		})
	}
	return out
}
