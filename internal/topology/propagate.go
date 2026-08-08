package topology

import (
	"fmt"
	"math"
	"sort"
)

// ---------------------------------------------------------------------------
// 상태 전파 (롤업)
// ---------------------------------------------------------------------------
// 계산 순서: propagateUplinks → effective_status → 레벨 역순 롤업 →
// 클러스터 특례(R6/R7) → 레이아웃.

// effectiveStatus 는 권위 상태 + 알림 상태를 합쳐 자기 자신의 최종 상태를 만든다 (R11).
//
// alert-info 는 해소된 과거 알림도 함께 돌려주므로:
//  1. 알림은 status(권위 상태)를 절대 건드리지 않고 alert_status 에만 쌓는다.
//  2. 권위 상태가 ok 인 객체는 알림으로 최대 warning 까지만 올라간다(alertDampedCeiling).
//  3. 권위 상태가 이미 나쁘면 알림 심각도를 그대로 합친다.
//  4. 권위 상태 소스가 아예 없는 객체(quorum 등)는 알림 심각도를 그대로 쓴다.
func effectiveStatus(n *Node) string {
	a := n.AlertStatus
	if a == nil || *a == "" {
		return n.Status
	}
	if authoritativeTypes[n.Type] && n.Status == "ok" {
		// 실시간 필드가 정상인데 알림만 심각한 경우 = 해소된 과거 알림일 가능성이 높다
		ra := statusRankByName[*a]
		cap_ := statusRankByName[alertDampedCeiling]
		if ra > cap_ {
			ra = cap_
		}
		return StatusMax(n.Status, statusByRank[ra])
	}
	return StatusMax(n.Status, *a)
}

// propagateUplinks 는 R9 확장: 물리 NIC -> shared-network 전파다.
// 트리 부모가 아니라 uplink 엣지를 따라가므로 롤업 전에 별도로 계산한다.
// 포트가 전부 죽으면 critical, 일부만 죽으면 이중화 상실로 degraded.
func propagateUplinks(g *graph) {
	byNet := map[string][]*Edge{}
	var netOrder []string
	for _, e := range g.edges {
		if e.Kind != "uplink" {
			continue
		}
		if _, ok := byNet[e.Target]; !ok {
			netOrder = append(netOrder, e.Target)
		}
		byNet[e.Target] = append(byNet[e.Target], e)
	}
	for _, netGID := range netOrder {
		edges := byNet[netGID]
		net := g.get(netGID)
		if net == nil {
			continue
		}
		var down int
		for _, e := range edges {
			if e.Status == "critical" {
				down++
			}
		}
		if down == 0 {
			continue
		}
		if down == len(edges) {
			net.Status = StatusMax(net.Status, "critical")
			net.Reasons = append(net.Reasons,
				fmt.Sprintf("소속 물리 포트 전부 다운(%d/%d)", down, len(edges)))
		} else {
			net.Status = StatusMax(net.Status, "degraded")
			net.Reasons = append(net.Reasons,
				fmt.Sprintf("물리 포트 일부 다운(%d/%d) — 경로 이중화 상실", down, len(edges)))
		}
	}
}

// finalizeGraph 은 자식 수 집계 → 상태 롤업 → 레이아웃 좌표 계산을 수행한다.
func finalizeGraph(g *graph) {
	propagateUplinks(g)
	for _, n := range g.nodes {
		n.EffectiveStatus = effectiveStatus(n)
	}
	children := map[string][]*Node{}
	for _, n := range g.nodes {
		if n.Parent != nil && *n.Parent != "" {
			children[*n.Parent] = append(children[*n.Parent], n)
		}
	}
	for _, n := range g.nodes {
		n.ChildrenCount = len(children[n.ID])
	}

	// 위상 정렬 없이 level 역순(깊은 곳부터)으로 롤업
	order := make([]*Node, len(g.nodes))
	copy(order, g.nodes)
	sort.SliceStable(order, func(i, j int) bool { return order[i].Level > order[j].Level })
	desc := map[string]int{}
	for _, n := range order {
		kids := children[n.ID]
		dampTypes := damping[n.Type]
		healthyTypes := map[string]bool{}
		for _, k := range kids {
			if k.RollupStatus == "ok" {
				healthyTypes[k.Type] = true
			}
		}
		cnt := 0
		worst := n.EffectiveStatus
		for _, k := range kids {
			cnt += 1 + desc[k.ID]
			ks := k.RollupStatus
			if ks == "critical" && dampTypes[k.Type] && healthyTypes[k.Type] {
				ks = "degraded" // R12 이중화 감쇠
				n.Reasons = append(n.Reasons,
					fmt.Sprintf("%s 1개 장애를 이중화가 흡수 (critical -> degraded)", k.Type))
			}
			worst = StatusMax(worst, ks)
		}
		desc[n.ID] = cnt
		n.DescendantCount = cnt
		n.RollupStatus = worst
	}

	// 클러스터 특례: 모든 노드가 죽었으면 critical (R7)
	for _, n := range g.nodes {
		if n.Type != "cluster" {
			continue
		}
		var phys []*Node
		for _, x := range g.nodes {
			if x.Type == "node" && x.Parent != nil && *x.Parent == n.ID {
				phys = append(phys, x)
			}
		}
		if len(phys) == 0 {
			continue
		}
		allCrit, anyCrit := true, false
		for _, p := range phys {
			// R7: 모든 물리 노드 다운 (권위 상태 기준. 알림만으로는 발동하지 않는다)
			if p.Status == "critical" {
				anyCrit = true
			} else {
				allCrit = false
			}
		}
		switch {
		case allCrit:
			n.RollupStatus = "critical"
			n.Reasons = append(n.Reasons, "모든 물리 노드 다운 — 클러스터 전면 장애")
		case anyCrit:
			n.RollupStatus = StatusMax(n.RollupStatus, "degraded")
			n.Reasons = append(n.Reasons, "일부 노드 다운 — 이중화로 서비스 유지 중(degraded)")
		}
	}

	layoutGraph(g)
}

// ---------------------------------------------------------------------------
// 레이아웃 힌트
// ---------------------------------------------------------------------------

// laneOrder 는 lane 문자열을 좌우 정렬 키로 바꾼다.
// shared 는 중앙(0), 나머지는 index 기준 좌우 분산 (node0 -> -1, node1 -> +1).
func laneOrder(lane string, lanesSorted []string) float64 {
	if lane == LaneShared {
		return 0.0
	}
	idx := -1
	for i, l := range lanesSorted {
		if l == lane {
			idx = i
			break
		}
	}
	n := len(lanesSorted)
	if idx < 0 || n <= 1 {
		return 0.0
	}
	// 노드 3개 이상이면 -1..+1 균등 분포
	return (float64(idx)/float64(n-1))*2.0 - 1.0
}

// layoutGraph 는 밴드(레벨) 내 1차원 패킹 좌표를 계산한다.
//
// 레인은 클러스터 스코프다. 노드 이름(node0/node1)은 클러스터마다 반복되므로
// 전역 레인으로 다루면 밴드가 E,Z,E,Z 로 뒤섞이고, everRun 클러스터에서 자기
// node1 서브트리로 가는 엣지가 ztC Edge 서브트리 전체를 가로지른다.
func layoutGraph(g *graph) {
	lanesByCluster := map[string][]string{}
	seen := map[string]map[string]bool{}
	var clusterOrder []string
	allLanesSet := map[string]bool{}
	for _, n := range g.nodes {
		if n.Lane == LaneShared {
			continue
		}
		c := derefStr(n.Cluster)
		if seen[c] == nil {
			seen[c] = map[string]bool{}
			clusterOrder = append(clusterOrder, c)
		}
		if !seen[c][n.Lane] {
			seen[c][n.Lane] = true
			lanesByCluster[c] = append(lanesByCluster[c], n.Lane)
		}
		allLanesSet[n.Lane] = true
	}
	for _, c := range clusterOrder {
		sort.Strings(lanesByCluster[c])
	}
	var allLanes []string
	for l := range allLanesSet {
		allLanes = append(allLanes, l)
	}
	sort.Strings(allLanes)
	g.lanesByCluster = lanesByCluster
	g.laneClusterOrd = clusterOrder

	lanesOf := func(n *Node) []string {
		if l := lanesByCluster[derefStr(n.Cluster)]; len(l) > 0 {
			return l
		}
		return allLanes
	}

	bands := map[int][]*Node{}
	var bandLevels []int
	for _, n := range g.nodes {
		if _, ok := bands[n.Level]; !ok {
			bandLevels = append(bandLevels, n.Level)
		}
		bands[n.Level] = append(bands[n.Level], n)
	}
	for _, level := range bandLevels {
		members := bands[level]
		// 클러스터를 레인보다 먼저 본다 → 밴드 안에서 클러스터별로 뭉치고,
		// 그 안에서만 좌(node0)-중앙(shared)-우(node1) 구도가 성립한다.
		sort.SliceStable(members, func(i, j int) bool {
			a, c := members[i], members[j]
			ca, cc := derefStr(a.Cluster), derefStr(c.Cluster)
			if ca != cc {
				return ca < cc
			}
			la, lc := laneOrder(a.Lane, lanesOf(a)), laneOrder(c.Lane, lanesOf(c))
			if la != lc {
				return la < lc
			}
			if a.Type != c.Type {
				return a.Type < c.Type
			}
			return derefStr(a.Label) < derefStr(c.Label)
		})
		size := len(members)
		laneCounter := map[[2]string]int{}
		for i, n := range members {
			key := [2]string{derefStr(n.Cluster), n.Lane}
			li := laneCounter[key]
			laneCounter[key] = li + 1
			cluster := derefStr(n.Cluster)
			laneKey := cluster + "/" + n.Lane
			if cluster == "" {
				laneKey = "-/" + n.Lane
			}
			x := (float64(i) - float64(size-1)/2.0) * NodeGapX
			x = math.RoundToEven(x*10) / 10 // Python round(x, 1)
			n.Layout = &NodeLayout{
				Level:      level,
				LevelLabel: levelLabel(level),
				BandIndex:  i,
				BandSize:   size,
				Lane:       n.Lane,
				LaneKey:    laneKey,
				LaneOffset: laneOrder(n.Lane, lanesOf(n)),
				LaneIndex:  li,
				Order:      i,
				X:          x,
				Y:          level * LevelGapY,
			}
		}
		for _, n := range members {
			n.Layout.LaneSize = laneCounter[[2]string{derefStr(n.Cluster), n.Lane}]
		}
	}
}

// levelLabel 은 밴드 헤더용 한글 라벨을 돌려준다. 없는 레벨은 숫자 문자열.
func levelLabel(level int) string {
	for _, li := range levelLabelsKO {
		if li.Level == level {
			return li.Label
		}
	}
	return fmt.Sprintf("%d", level)
}
