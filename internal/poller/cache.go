package poller

// FleetCache — 마지막 성공 응답 유지(poller.py 의 FleetCache 포트). 빈 응답 금지:
// 클러스터별 뷰 빌드 실패 시 직전 스냅샷을 stale 표식으로 유지하고, 최초 수집
// 전에만 503 을 허용한다.

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"serverdesk/internal/topology"
)

// Version 은 /api 응답의 poller_version 이다. Python 폴러와 같은 "1.0.0" 을
// 유지한다 — 프런트·외부 감시 도구가 이 값을 비교할 수 있기 때문이다.
const Version = "1.0.0"

// FleetCache 는 fleet/평면 토폴로지/상세 토폴로지의 마지막 성공 스냅샷이다.
type FleetCache struct {
	mu         sync.Mutex
	fleet      map[string]any
	topology   map[string]any
	topoFull   map[string]any
	typedViews []topology.ClusterView // 직전 갱신의 타입 뷰(상세 토폴로지 입력)
	ts         float64
}

// NewFleetCache 는 빈 캐시를 만든다.
func NewFleetCache() *FleetCache { return &FleetCache{} }

// Update 는 현재 상태에서 뷰를 다시 구워 캐시를 갱신한다(poller.py FleetCache.update).
//
// 클러스터별 격리: 1대의 뷰 빌드가 패닉해도 전체 캐시가 얼어붙지 않게 각
// 클러스터를 개별 recover 로 감싼다. 실패한 클러스터는 직전 스냅샷(있으면)을
// stale 로 표시해 유지하고, 최초 실패라 이전 데이터가 없으면 생략한다.
func (c *FleetCache) Update(states []*ClusterState) {
	c.mu.Lock()
	prevClusters := map[string]map[string]any{}
	if c.fleet != nil {
		for _, cv := range listVal(c.fleet["clusters"]) {
			if cm := dictVal(cv); cm != nil {
				prevClusters[strVal(cm["key"])] = cm
			}
		}
	}
	prevTopo := map[string]any{}
	if c.topology != nil {
		for _, tv := range listVal(c.topology["clusters"]) {
			if tm := dictVal(tv); tm != nil {
				prevTopo[strVal(tm["cluster"])] = tv
			}
		}
	}
	c.mu.Unlock()

	clusters := make([]any, 0, len(states))
	typed := make([]topology.ClusterView, 0, len(states))
	now := time.Now()
	for _, s := range states {
		view, tv, err := safeBuildViews(s, now)
		if err != nil {
			logf("error", s.Key, fmt.Sprintf("클러스터 뷰 빌드 실패: %v", err))
			if prev := staleCluster(prevClusters[s.Key]); prev != nil {
				clusters = append(clusters, prev)
			}
			continue
		}
		clusters = append(clusters, view)
		typed = append(typed, tv)
	}
	if len(clusters) == 0 {
		logf("error", "-", "fleet 빌드 실패: 갱신 가능한 클러스터가 없음")
		return
	}
	// overall: ok<warning=unknown<critical 최악 단계.
	rank := map[string]int{"ok": 0, "warning": 1, "critical": 2, "unknown": 1}
	overall := "unknown"
	anyStale := false
	for i, cv := range clusters {
		cm := dictVal(cv)
		lvl := strVal(mapGet(cm, "health", "level"))
		if i == 0 || rank[lvl] > rank[overall] {
			overall = lvl
		}
		if boolVal(cm["stale"]) {
			anyStale = true
		}
	}
	generatedAt := time.Now().Unix()
	fleet := map[string]any{
		"generated_at":   generatedAt,
		"poller_version": Version,
		"overall":        overall,
		"stale":          anyStale,
		"clusters":       clusters,
	}

	topoClusters := make([]any, 0, len(clusters))
	for _, cv := range clusters {
		cm := dictVal(cv)
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("error", strVal(cm["key"]), fmt.Sprintf("토폴로지 빌드 실패: %v\n%s", r, debug.Stack()))
					if pt, ok := prevTopo[strVal(cm["key"])]; ok {
						topoClusters = append(topoClusters, pt)
					}
				}
			}()
			topoClusters = append(topoClusters, BuildFlatTopology(cm))
		}()
	}
	topo := map[string]any{"generated_at": generatedAt, "clusters": topoClusters}

	// 상세 모델은 계산량이 크고 선택 기능이라 실패해도 평면 그래프에 영향을 주지
	// 않도록 격리한다(poller.py 와 같은 판단).
	var topoFull map[string]any
	func() {
		defer func() {
			if r := recover(); r != nil {
				logf("warn", "-", fmt.Sprintf("상세 토폴로지 빌드 실패(평면 그래프는 정상): %v\n%s", r, debug.Stack()))
				topoFull = nil
			}
		}()
		topoFull = buildFullTopologyMap(typed, generatedAt)
	}()

	c.mu.Lock()
	c.fleet = fleet
	c.topology = topo
	// 상세 빌드 실패 시 직전본 유지(Python: 새 값이 None 이고 이전도 None 일 때만 덮음).
	if topoFull != nil || c.topoFull == nil {
		c.topoFull = topoFull
	}
	c.typedViews = typed
	c.ts = nowFloat()
	c.mu.Unlock()
}

// safeBuildViews 는 클러스터 1대의 뷰 빌드를 recover 로 감싼다.
func safeBuildViews(st *ClusterState, now time.Time) (view map[string]any, tv topology.ClusterView, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v\n%s", r, debug.Stack())
		}
	}()
	view, tv = BuildClusterViews(st, now)
	return view, tv, nil
}

// staleCluster 는 뷰 빌드에 실패한 클러스터용 폭백이다. 직전 스냅샷을 stale +
// 오류 표식으로 복제해 돌린다(이전 데이터가 없으면 nil → 이번 갱신에서 생략).
func staleCluster(prev map[string]any) map[string]any {
	if prev == nil {
		return nil
	}
	c := make(map[string]any, len(prev))
	for k, v := range prev {
		c[k] = v
	}
	c["stale"] = true
	col := map[string]any{}
	if pc := dictVal(prev["collection"]); pc != nil {
		for k, v := range pc {
			col[k] = v
		}
	}
	errs := map[string]any{}
	if pe := dictVal(col["errors"]); pe != nil {
		for k, v := range pe {
			errs[k] = v
		}
	}
	errs["view_build"] = "뷰 빌드 실패(직전 스냅샷 유지)"
	col["errors"] = errs
	c["collection"] = col
	return c
}

// Snapshot 은 (fleet, 평면 topology, 갱신 시각)을 돌려준다.
// fleet 이 nil 이면 아직 한 번도 수집되지 않은 것이다.
func (c *FleetCache) Snapshot() (fleet, topo map[string]any, ts float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fleet == nil {
		return nil, nil, 0
	}
	return c.fleet, c.topology, c.ts
}

// SnapshotFull 은 (상세 topology, 갱신 시각)을 돌려준다.
func (c *FleetCache) SnapshotFull() (map[string]any, float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.topoFull, c.ts
}

// buildFullTopologyMap 은 topology.BuildFullTopology 결과를 맵으로 변환하고
// generated_at 을 싣는다(poller.py 는 dict 에 키를 추가했다).
func buildFullTopologyMap(typed []topology.ClusterView, generatedAt int64) map[string]any {
	full := topology.BuildFullTopology(topology.FleetInput{
		Clusters:   typed,
		Sites:      nil,
		NICMaps:    nil, // 뷰의 nic_network_map 을 쓴다(AdaptCluster 폭백 계약)
		FleetLabel: "",  // 빈 값이면 패키지 기본 "전체 플릿"(Python 기본값과 동일)
	})
	if full == nil {
		return nil
	}
	b, err := full.ToJSON(false)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	m["generated_at"] = generatedAt
	return m
}
