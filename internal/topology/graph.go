package topology

import (
	"bytes"
	"encoding/json"
)

// ---------------------------------------------------------------------------
// 그래프 노드/엣지 (출력 스키마 §3.2, §3.3)
// ---------------------------------------------------------------------------

// Node 는 그래프 노드 하나다. JSON 필드 이름/순서는 원본 스키마와 동일하다.
//
// Status          : 권위 상태 (avcli/SSH 의 실시간 필드에서 직접 판정)
// AlertStatus     : 알림 기반 상태 (과거 이력 포함 가능 → 감쇠 대상)
// EffectiveStatus : 위 둘을 합친 자기 자신의 상태
// RollupStatus    : EffectiveStatus + 하위 자식 롤업 (UI 색칠은 이 값)
type Node struct {
	ID              string      `json:"id"`
	Type            string      `json:"type"`
	Label           *string     `json:"label"`
	Status          string      `json:"status"`
	AlertStatus     *string     `json:"alert_status"`
	EffectiveStatus string      `json:"effective_status"`
	RollupStatus    string      `json:"rollup_status"`
	Level           int         `json:"level"`
	Parent          *string     `json:"parent"`
	Lane            string      `json:"lane"`
	Cluster         *string     `json:"cluster"`
	ChildrenCount   int         `json:"children_count"`
	DescendantCount int         `json:"descendant_count"`
	Collapsible     bool        `json:"collapsible"`
	Meta            omap        `json:"meta"`
	Reasons         []string    `json:"reasons"`
	Alerts          []*string   `json:"alerts"`
	Layout          *NodeLayout `json:"layout,omitempty"`
}

// NodeLayout 은 프런트 렌더링 힌트다. x/y 를 그대로 SVG 좌표로 써도 된다.
// 필드 순서는 원본과 같게 lane_size 가 마지막이다(원본은 2차 루프에서 뒤에 추가된다).
type NodeLayout struct {
	Level      int     `json:"level"`
	LevelLabel string  `json:"level_label"`
	BandIndex  int     `json:"band_index"`
	BandSize   int     `json:"band_size"`
	Lane       string  `json:"lane"`
	LaneKey    string  `json:"lane_key"` // "<cluster>/<lane>" — 레인은 클러스터 스코프
	LaneOffset float64 `json:"lane_offset"`
	LaneIndex  int     `json:"lane_index"` // 같은 레벨·같은 클러스터·같은 레인 안 순번
	Order      int     `json:"order"`
	X          float64 `json:"x"`
	Y          int     `json:"y"`
	LaneSize   int     `json:"lane_size"`
}

// Edge 는 그래프 엣지다. 고정 5개 키(id/source/target/kind/status) 뒤에
// kind 별 추가 속성이 따라붙는다 (스키마 §3.3 표 참조).
type Edge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Attrs  omap   `json:"-"`
}

// MarshalJSON 은 고정 키를 먼저, 추가 속성을 삽입 순서대로 직렬화한다.
func (e *Edge) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	fixed := omap{
		{"id", e.ID},
		{"source", e.Source},
		{"target", e.Target},
		{"kind", e.Kind},
		{"status", e.Status},
	}
	for i, pair := range append(fixed, e.Attrs...) {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := json.Marshal(pair.k)
		if err != nil {
			return nil, err
		}
		vb, err := json.Marshal(pair.v)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(vb)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// 그래프 빌더 (가변 작업대)
// ---------------------------------------------------------------------------

// graph 는 빌드 중인 토폴로지의 가변 작업대다. id 색인과 엣지 중복 제거만 제공하고,
// 최종 스냅샷(FullTopology)과는 분리한다.
type graph struct {
	nodes          []*Node
	byID           map[string]*Node
	edges          []*Edge
	edgeKeys       map[[3]string]bool
	alertRecords   []*AlertRecord
	lanesByCluster map[string][]string // finalize 의 layout 이 채운다
	laneClusterOrd []string            // lanesByCluster 의 삽입 순서
}

func newGraph() *graph {
	return &graph{
		byID:     map[string]*Node{},
		edgeKeys: map[[3]string]bool{},
	}
}

// nodeInit 은 addNode 의 초기화 인자다 (Python add_node 의 kwargs 에 해당).
type nodeInit struct {
	Type    string
	Label   *string
	Status  string // "" 이면 "unknown"
	Level   int
	Parent  *string
	Lane    string // "" 이면 LaneShared
	Cluster *string
	Meta    omap
}

// addNode 는 노드를 추가한다. 같은 id 가 이미 있으면 기존 노드를 돌려준다
// (원본 add_node 와 같은 중복 흡수 동작).
func (g *graph) addNode(id string, in nodeInit) *Node {
	if n, ok := g.byID[id]; ok {
		return n
	}
	st := in.Status
	if st == "" {
		st = "unknown"
	}
	lane := in.Lane
	if lane == "" {
		lane = LaneShared
	}
	meta := in.Meta
	if meta == nil {
		meta = omap{}
	}
	n := &Node{
		ID:              id,
		Type:            in.Type,
		Label:           in.Label,
		Status:          st,
		EffectiveStatus: st,
		RollupStatus:    st,
		Level:           in.Level,
		Parent:          in.Parent,
		Lane:            lane,
		Cluster:         in.Cluster,
		Collapsible:     true,
		Meta:            meta,
		Reasons:         []string{},
		Alerts:          []*string{},
	}
	g.nodes = append(g.nodes, n)
	g.byID[id] = n
	return n
}

func (g *graph) get(id string) *Node { return g.byID[id] }

func (g *graph) has(id string) bool {
	_, ok := g.byID[id]
	return ok
}

// addEdge 는 엣지를 추가한다. 양 끝점 노드가 없거나 같은 (src,dst,kind) 가
// 이미 있으면 nil 을 돌려주고 아무것도 하지 않는다.
func (g *graph) addEdge(src, dst, kind, status string, attrs ...kv) *Edge {
	if !g.has(src) || !g.has(dst) {
		return nil
	}
	key := [3]string{src, dst, kind}
	if g.edgeKeys[key] {
		return nil
	}
	g.edgeKeys[key] = true
	if status == "" {
		status = "ok"
	}
	e := &Edge{
		ID:     kind + "|" + src + "|" + dst,
		Source: src,
		Target: dst,
		Kind:   kind,
		Status: status,
		Attrs:  attrs,
	}
	g.edges = append(g.edges, e)
	return e
}

// gid 는 그래프 전역 고유 id 를 만든다. 클러스터 시퀀스는 클러스터마다 겹치므로
// 반드시 접두한다.
func gid(clusterID, rawID string) string {
	return clusterID + "/" + rawID
}
