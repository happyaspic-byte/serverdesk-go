package topology

import (
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// 네트워크 계층 빌더 (공유 네트워크 / 물리 NIC / vNIC)
// ---------------------------------------------------------------------------

// buildNetworks 는 공유 네트워크(level 5)를 만든다.
func (b *clusterBuild) buildNetworks() {
	for _, net := range b.c.Networks {
		ngid := gid(b.cid, net.ID)
		st, reasons := networkStatus(net)
		n := b.g.addNode(ngid, nodeInit{
			Type:    "sharednetwork",
			Label:   ptrOrNil(net.Name),
			Status:  st,
			Level:   levels["sharednetwork"],
			Parent:  &b.clusterGID,
			Cluster: ptrOrNil(b.cid),
			Meta: omap{
				{"raw_id", strOrNil(net.ID)},
				{"role", strOrNil(net.Role)},
				{"fault_tolerant", strOrNil(net.FaultTolerant)},
				{"bandwidth_label", strOrNil(net.Bandwidth)},
				{"bandwidth_bps", intPtrToAny(ParseBandwidth(net.Bandwidth))},
				{"mtu", intPtrToAny(net.MTU)},
				{"is_interconnect", strings.ToLower(net.Role) == "a-link"},
			},
		})
		n.Reasons = append(n.Reasons, reasons...)
		b.g.addEdge(b.clusterGID, ngid, "contains", "ok")
		b.netByName[net.Name] = ngid
		b.netRoleByName[net.Name] = strings.ToLower(net.Role)
	}
}

// buildNICs 는 물리 NIC(level 4)와 uplink 엣지를 만든다 (R9/R9b).
//
// NIC -> shared-network 업링크 근거 우선순위:
//  1. 확정 매핑(노드 spine 설정/설정파일)
//  2. 알림 문자열
//  3. 이름·MTU 휴리스틱
//  4. 개수 일치 짝짓기
//
// 노드 순회는 이름 정렬 순이다(원본은 node_metrics 삽입 순; 밴드 정렬 키가
// 유일하므로 레이아웃 결과는 동일하다).
func (b *clusterBuild) buildNICs() {
	explicit := b.c.NICNetworkMap
	alertNICMap := DeriveNICMapFromAlerts(b.c.Alerts)
	for _, nd := range b.sortedNodes() {
		nodeName := nd.Name
		hostGID, hasHost := b.nodeByName[nodeName]
		if !hasHost {
			continue
		}
		metrics := b.c.NodeMetrics[nodeName]
		if metrics == nil {
			continue
		}
		var physLinks []LinkInput
		for _, l := range metrics.Links {
			kind, _ := GuessNICRole(l.Name)
			if kind == nicKindPhysical {
				physLinks = append(physLinks, l)
			}
		}
		ordinal := ordinalNICMap(physLinks, b.c.Networks)
		for _, link := range metrics.Links {
			ifname := link.Name
			kind, role := GuessNICRole(ifname)
			if kind != nicKindPhysical {
				continue // 브리지 / 게스트 tap / 루프백은 물리 토폴로지에서 제외
			}
			ngid := gid(b.cid, fmt.Sprintf("nic:%s:%s", nodeName, ifname))
			oper := strings.ToLower(link.OperState)

			// 확정 소스(노드 spine 설정/설정파일/알림)가 이 NIC 을 '알고 있는가'.
			// Network 이 nil 이어도 키가 있으면 "소속 네트워크 없음" 이 확정된 것이다.
			nodeMap := explicit[nodeName]
			rawMap, mappingKnown := nodeMap[ifname]
			targetNet := ""
			var evidence string
			var conf *float64
			if mappingKnown {
				if rawMap.Network != nil {
					targetNet = *rawMap.Network
				}
				evidence = rawMap.Evidence
				if evidence == "" {
					evidence = "config"
				}
				conf = rawMap.Confidence
				if conf == nil {
					conf = f64(1.0)
				}
			}
			if targetNet == "" {
				if an, ok := alertNICMap[ifname]; ok && an != "" {
					targetNet = an
					evidence, conf = "alert-text", f64(0.8)
					mappingKnown = true
				}
			}
			if targetNet == "" {
				if hn := heuristicNetForNIC(ifname, role, b.c.Networks, link.MTU); hn != "" {
					targetNet = hn
					evidence, conf = "heuristic", f64(0.4)
				}
			}
			if targetNet == "" {
				if on, ok := ordinal[ifname]; ok {
					targetNet = on
					evidence, conf = "ordinal-guess", f64(0.25)
				}
			}
			_, netExists := b.netByName[targetNet]
			attached := targetNet != "" && netExists
			if !attached {
				targetNet = ""
				if !mappingKnown {
					evidence, conf = "", nil
				}
			}
			// 매핑을 확정 근거로 얻지 못했으면 '미사용 예비 포트' 라고 단정하면 안 된다.
			// 확정 근거 없이 not attached 를 미사용으로 읽으면, 업무 트래픽을 나르는
			// ibiz0 가 끊겨도 R9b(status=ok, 초록)로 분류돼 장애 탐지를 적극적으로 억누른다.
			mappingUnknown := !attached && !mappingKnown

			// R9: 다운된 포트라도 어떤 shared-network 에도 속하지 않으면 '미사용 포트'다.
			//     (실장비 Edge 에 케이블 미연결 ibiz3~5 가 상시 down 으로 존재)
			//     단 '소속 없음' 이 확정된 경우에만. 매핑 자체가 미상이면 unknown.
			var nst string
			switch oper {
			case "up":
				nst = "ok"
			case "down":
				switch {
				case attached:
					nst = "critical"
				case mappingUnknown:
					nst = "unknown"
				default:
					nst = "ok"
				}
			default:
				nst = "unknown"
			}
			unused := oper == "down" && !attached && mappingKnown
			var attachedNet, attachEvidence, mapEvidence any
			isInterconnect := role == "a-link"
			if attached {
				attachedNet = targetNet
				attachEvidence = evidence
				isInterconnect = b.netRoleByName[targetNet] == "a-link"
			}
			if mappingKnown {
				mapEvidence = evidence
			}
			nn := b.g.addNode(ngid, nodeInit{
				Type:    "nic",
				Label:   ptrOrNil(fmt.Sprintf("%s:%s", nodeName, ifname)),
				Status:  nst,
				Level:   levels["nic"],
				Parent:  &hostGID,
				Lane:    nodeName,
				Cluster: ptrOrNil(b.cid),
				Meta: omap{
					{"node", nodeName},
					{"ifname", ifname},
					{"operstate", strOrNil(link.OperState)},
					{"speed_mbps", intPtrToAny(link.Speed)},
					{"nic_kind", kind},
					{"role_guess", strOrNil(role)},
					{"mtu", intPtrToAny(link.MTU)},
					{"is_interconnect", isInterconnect},
					{"attached_network", attachedNet},
					{"attachment_evidence", attachEvidence},
					{"mapping_evidence", mapEvidence},
					{"mapping_unknown", mappingUnknown},
					// 확정 소스가 '소속 없음' 이라고 말해준 포트만 미사용으로 인정한다.
					{"unused", unused},
					{"rx_errors", intPtrToAny(link.RxErrors)},
					{"tx_errors", intPtrToAny(link.TxErrors)},
					{"drops_delta", intPtrToAny(link.DropsDelta)},
					{"source", "ssh:@link"},
				},
			})
			switch {
			case nst == "critical":
				nn.Reasons = append(nn.Reasons,
					fmt.Sprintf("물리 NIC operstate=%s (공유 네트워크 %s 소속)", oper, targetNet))
			case unused:
				nn.Reasons = append(nn.Reasons, "미사용 포트(링크 다운, 소속 공유 네트워크 없음 — 확정)")
			case mappingUnknown:
				op := oper
				if op == "" {
					op = "unknown"
				}
				nn.Reasons = append(nn.Reasons,
					fmt.Sprintf("NIC<->공유네트워크 매핑을 확정하지 못함(operstate=%s) — "+
						"미사용 예비 포트인지 장애인지 판정 불가", op))
			}
			b.g.addEdge(hostGID, ngid, "contains", "ok")
			b.nicKeys = append(b.nicKeys, nicKeyGID{nodeName, ifname, ngid})

			if attached {
				b.g.addEdge(ngid, b.netByName[targetNet], "uplink", nst,
					kv{"evidence", evidence}, kv{"confidence", *conf})
			}
		}
	}
}

// buildVMNICs 는 vNIC -> shared-network 엣지를 만든다.
// 이중화가 깨지면 VM 권위 상태도 함께 올린다(원본과 동일하게 status 를 직접 갱신).
func (b *clusterBuild) buildVMNICs(vm VMInput, vgid string, vmNode *Node) {
	for i, itf := range vm.Interfaces {
		netGID, ok := b.netByName[itf.SharedNetwork]
		if !ok {
			continue
		}
		n0 := strings.ToUpper(itf.Net0Status) == "ENABLED"
		n1 := strings.ToUpper(itf.Net1Status) == "ENABLED"
		var est, rstate string
		switch {
		case n0 && n1:
			est, rstate = "ok", "redundant"
		case n0 || n1:
			est, rstate = "degraded", "simplex"
		default:
			est, rstate = "critical", "down"
		}
		b.g.addEdge(vgid, netGID, "vnic", est,
			kv{"span", true},
			kv{"mac", strOrNil(itf.MAC)},
			kv{"index", i},
			kv{"net0_status", strOrNil(itf.Net0Status)},
			kv{"net1_status", strOrNil(itf.Net1Status)},
			kv{"redundancy", rstate},
		)
		if est != "ok" {
			vmNode.Reasons = append(vmNode.Reasons,
				fmt.Sprintf("vNIC(%s) 이중화 상태=%s", itf.SharedNetwork, rstate))
			vmNode.Status = StatusMax(vmNode.Status, est)
		}
	}
}
