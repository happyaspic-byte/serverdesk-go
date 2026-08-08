package topology

import (
	"bytes"
	"encoding/json"
	"sort"
)

// ---------------------------------------------------------------------------
// 출력 조립 (스키마 §3.1)
// ---------------------------------------------------------------------------

// LevelInfo 는 levels 배열의 한 항목이다 (밴드 번호 + 한글 라벨).
type LevelInfo struct {
	Level int    `json:"level"`
	Label string `json:"label"`
}

// RuleInfo 는 상태 전파 규칙 하나다 (UI 툴팁/문서화용).
type RuleInfo struct {
	ID     string `json:"id"`
	When   string `json:"when"`
	Effect string `json:"effect"`
}

// LayoutSummary 는 최상위 layout 객체다.
type LayoutSummary struct {
	Mode           string   `json:"mode"`
	LevelGapY      int      `json:"level_gap_y"`
	NodeGapX       int      `json:"node_gap_x"`
	Lanes          []string `json:"lanes"`            // 하위 호환용 플랫 레인 목록
	LanesByCluster omap     `json:"lanes_by_cluster"` // {cluster: [lane…]} — 컬럼은 이걸로 그린다
}

// Summary 는 개수 집계와 최악 상태다.
type Summary struct {
	Clusters    []*string `json:"clusters"`
	NodeCount   int       `json:"node_count"`
	EdgeCount   int       `json:"edge_count"`
	ByType      omap      `json:"by_type"`
	ByStatus    omap      `json:"by_status"`
	WorstStatus string    `json:"worst_status"`
}

// AlertRecord 는 알림 원본 + 분류 결과 + 해석된 대상 노드 id 다.
type AlertRecord struct {
	ID             *string  `json:"id"`
	Name           *string  `json:"name"`
	Description    *string  `json:"description"`
	Time           *string  `json:"time"`
	RawSeverity    *string  `json:"raw_severity"`
	Severity       string   `json:"severity"`
	ClassifiedBy   string   `json:"classified_by"`
	Targets        []string `json:"targets"`
	TargetEvidence string   `json:"target_evidence"`
}

// FullTopology 는 토폴로지 그래프 JSON 의 최상위 객체다.
// json.Marshal 로 그대로 직렬화하면 스키마 1.0.0 문서가 된다.
type FullTopology struct {
	SchemaVersion    string         `json:"schema_version"`
	GeneratedBy      string         `json:"generated_by"`
	Roots            []string       `json:"roots"`
	Levels           []LevelInfo    `json:"levels"`
	EdgeKinds        omap           `json:"edge_kinds"`
	StatusRank       omap           `json:"status_rank"`
	PropagationRules []RuleInfo     `json:"propagation_rules"`
	Layout           LayoutSummary  `json:"layout"`
	Summary          Summary        `json:"summary"`
	Alerts           []*AlertRecord `json:"alerts"`
	Nodes            []*Node        `json:"nodes"`
	Edges            []*Edge        `json:"edges"`
}

// emitGraph 는 빌드가 끝난 그래프를 출력 스냅샷으로 조립한다.
func emitGraph(g *graph, clusters []ClusterInput) *FullTopology {
	byType := omap{}
	typeIdx := map[string]int{}
	for _, n := range g.nodes {
		if v, ok := byType.get(n.Type); ok {
			byType[typeIdx[n.Type]].v = v.(int) + 1
		} else {
			typeIdx[n.Type] = len(byType)
			byType = append(byType, kv{n.Type, 1})
		}
	}
	byStatus := omap{}
	statusIdx := map[string]int{}
	for _, n := range g.nodes {
		if v, ok := byStatus.get(n.RollupStatus); ok {
			byStatus[statusIdx[n.RollupStatus]].v = v.(int) + 1
		} else {
			statusIdx[n.RollupStatus] = len(byStatus)
			byStatus = append(byStatus, kv{n.RollupStatus, 1})
		}
	}

	var roots []string
	for _, n := range g.nodes {
		if n.Parent == nil || *n.Parent == "" {
			roots = append(roots, n.ID)
		}
	}
	if roots == nil {
		roots = []string{}
	}

	laneSet := map[string]bool{}
	for _, n := range g.nodes {
		laneSet[n.Lane] = true
	}
	lanes := make([]string, 0, len(laneSet))
	for l := range laneSet {
		lanes = append(lanes, l)
	}
	sort.Strings(lanes)

	lanesByCluster := omap{}
	for _, c := range g.laneClusterOrd {
		lst := g.lanesByCluster[c]
		if lst == nil {
			lst = []string{}
		}
		lanesByCluster = append(lanesByCluster, kv{c, lst})
	}

	clusterIDs := make([]*string, 0, len(clusters))
	for i := range clusters {
		clusterIDs = append(clusterIDs, ptrOrNil(clusters[i].ClusterID))
	}

	var worst string
	if len(g.nodes) == 0 {
		worst = "unknown"
	} else {
		rollups := make([]string, 0, len(g.nodes))
		for _, n := range g.nodes {
			rollups = append(rollups, n.RollupStatus)
		}
		worst = StatusMax(rollups...)
	}

	alerts := g.alertRecords
	if alerts == nil {
		alerts = []*AlertRecord{}
	}
	nodes := g.nodes
	if nodes == nil {
		nodes = []*Node{}
	}
	edges := g.edges
	if edges == nil {
		edges = []*Edge{}
	}

	return &FullTopology{
		SchemaVersion:    SchemaVersion,
		GeneratedBy:      generatedBy,
		Roots:            roots,
		Levels:           levelLabelsKO,
		EdgeKinds:        edgeKinds,
		StatusRank:       statusRank,
		PropagationRules: propagationRules,
		Layout: LayoutSummary{
			Mode:           "layered",
			LevelGapY:      LevelGapY,
			NodeGapX:       NodeGapX,
			Lanes:          lanes,
			LanesByCluster: lanesByCluster,
		},
		Summary: Summary{
			Clusters:    clusterIDs,
			NodeCount:   len(g.nodes),
			EdgeCount:   len(g.edges),
			ByType:      byType,
			ByStatus:    byStatus,
			WorstStatus: worst,
		},
		Alerts: alerts,
		Nodes:  nodes,
		Edges:  edges,
	}
}

// ToJSON 은 그래프를 JSON 바이트로 직렬화한다.
// json.Marshal 과 달리 HTML 이스케이프(<, >, &)를 끄므로 한글 reason 문장이
// 원본 Python json.dumps(ensure_ascii=False) 와 같은 바이트로 나온다.
func (t *FullTopology) ToJSON(indent bool) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// FindNode 는 id 로 노드를 찾는다 (소비자/테스트 편의).
func (t *FullTopology) FindNode(id string) *Node {
	for _, n := range t.Nodes {
		if n.ID == id {
			return n
		}
	}
	return nil
}

// FindEdge 는 id 로 엣지를 찾는다 (소비자/테스트 편의).
func (t *FullTopology) FindEdge(id string) *Edge {
	for _, e := range t.Edges {
		if e.ID == id {
			return e
		}
	}
	return nil
}
