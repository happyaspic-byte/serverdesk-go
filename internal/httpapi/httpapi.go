// Package httpapi 는 everrun-poller 의 HTTP 표면(poller.py Handler)의 Go 포트다.
//
// 최상위 webauth 미들웨어가 읽기·쓰기 API를 인증한다. 쓰기 API는 추가로
// webfront GateWrite의 동일 출처 검사를 거친다.
// 응답은 compact JSON(HTML 이스케이프 끔 — 한글 원문), Cache-Control: no-store,
// 1KB 초과 시 gzip(클로이언트 수용 시), CORS 는 설정 allowlist 에만 부여한다.
package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"serverdesk/internal/config"
	"serverdesk/internal/poller"
	"serverdesk/internal/webfront"
)

// Logf 는 호스트 로거 연결 훅이다. 기본 no-op.
var Logf = func(level, cluster, msg string) {}

func logf(level, cluster, msg string) { Logf(level, cluster, msg) }

// Server 는 /api 표면의 핸들러다.
type Server struct {
	Cache  *poller.FleetCache
	States []*poller.ClusterState
	Cfg    *config.Config
	Store  *config.Store
	Events *poller.EventLog
	Avail  *poller.AvailTracker
	Edge   *poller.EdgeManager
	// Gate 는 쓰기 경로의 동일출처 검사에 쓰는 webfront 서버다(GateWrite).
	Gate *webfront.Server

	StartedAt time.Time
	CORS      []string

	// displayOverlay 는 클러스터 표시 메타의 실행 중 사본이다.
	// config.ClusterConfig 에는 asset_tag/floor_pos 필드가 없어(읽기 전용 뷰 계약)
	// 이 두 키와 PUT 즉시 반영분을 여기서 들고 있는다.
	ovlMu          sync.Mutex
	displayOverlay map[string]map[string]string
}

// New 는 /api 핸들러를 만든다. overlay 는 NewDisplayOverlay 로 만든 것을 넘긴다.
func New(cache *poller.FleetCache, states []*poller.ClusterState, cfg *config.Config,
	store *config.Store, events *poller.EventLog, avail *poller.AvailTracker,
	edge *poller.EdgeManager, gate *webfront.Server, cors []string,
	overlay map[string]map[string]string) *Server {
	return &Server{
		Cache: cache, States: states, Cfg: cfg, Store: store, Events: events,
		Avail: avail, Edge: edge, Gate: gate, StartedAt: time.Now(),
		CORS: cors, displayOverlay: overlay,
	}
}

// NewDisplayOverlay 는 원본 config 파일에서 클러스터 표시 메타를 읽어 초기
// 오버레이를 만든다. 구조체에 없는 asset_tag/floor_pos 도 여기서 복구된다.
func NewDisplayOverlay(cfg *config.Config) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, c := range cfg.Clusters {
		m := map[string]string{}
		if c.Name != "" {
			m["label"] = c.Name
		}
		if c.Site != "" {
			m["site"] = c.Site
		}
		if c.Company != "" {
			m["company"] = c.Company
		}
		if c.Factory != "" {
			m["factory"] = c.Factory
		}
		out[c.Key] = m
	}
	// 구조체가 버리는 표시 키(asset_tag/floor_pos)를 원본 파일에서 복구한다.
	if cfg.Path != "" {
		if data, err := readFileQuiet(cfg.Path); err == nil {
			var doc map[string]json.RawMessage
			if json.Unmarshal(data, &doc) == nil {
				var arr []map[string]any
				if raw, ok := doc["clusters"]; ok && json.Unmarshal(raw, &arr) == nil {
					for _, e := range arr {
						key, _ := e["key"].(string)
						m := out[key]
						if m == nil {
							continue
						}
						for _, k := range []string{"asset_tag", "floor_pos"} {
							if v, ok := e[k].(string); ok && v != "" {
								m[k] = v
							}
						}
					}
				}
			}
		}
	}
	return out
}

func readFileQuiet(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// readCapped 는 요청 본문을 cap 까지 읽는다. cap 초과면 오류.
func readCapped(r *http.Request, cap int64) ([]byte, error) {
	if r.ContentLength < 0 {
		return io.ReadAll(io.LimitReader(r.Body, cap))
	}
	if r.ContentLength > cap {
		_, _ = io.Copy(io.Discard, r.Body)
		return nil, fmt.Errorf("본문이 너무 큼(%dB 초과)", cap)
	}
	return io.ReadAll(io.LimitReader(r.Body, cap+1))
}

// DisplayCfg 는 /api/devices 변환용 클러스터 표시 메타 맵이다
// (poller.py _display_cfg 포트 — 자격증명 제외, 표시 필드만).
func (s *Server) DisplayCfg() map[string]poller.DisplayMeta {
	s.ovlMu.Lock()
	defer s.ovlMu.Unlock()
	out := map[string]poller.DisplayMeta{}
	for key, m := range s.displayOverlay {
		out[key] = poller.DisplayMeta{
			Label:    m["label"],
			Company:  m["company"],
			Factory:  m["factory"],
			Site:     m["site"],
			AssetTag: m["asset_tag"],
			FloorPos: m["floor_pos"],
		}
	}
	return out
}

// refreshSec 은 프런트 stale 임계(refreshSec*3) 산출용이다 — 가장 빠른 fast 주기.
// (poller.py _refresh_sec)
func (s *Server) refreshSec() int {
	best := 0
	for _, st := range s.States {
		v := st.Cfg.Intervals.Fast
		if best == 0 || v < best {
			best = v
		}
	}
	if best == 0 {
		return 30
	}
	return best
}

// --- 응답 공통 ---------------------------------------------------------------

// corsHeaders 는 요청 Origin 이 allowlist 에 있을 때만 그 출처를 반향한다.
// 와일드카드 무조건 부착은 임의 웹사이트 JS 의 드라이브바이 읽기를 허용하므로 금지.
func (s *Server) corsHeaders(r *http.Request, h http.Header) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	allowed := false
	for _, o := range s.CORS {
		if o == "*" || o == origin {
			allowed = true
			break
		}
	}
	if !allowed {
		return
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	h.Set("Access-Control-Allow-Headers", "*")
	h.Set("Access-Control-Allow-Methods", "GET, PUT, DELETE, POST, OPTIONS")
}

// send 는 compact JSON 응답을 본낸다(Python _send 포트).
// HTML 이스케이프를 끈다(한글 원문, ensure_ascii=False 에 해당).
func (s *Server) send(w http.ResponseWriter, r *http.Request, code int, payload any) {
	var body []byte
	switch p := payload.(type) {
	case []byte:
		body = p
	default:
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(payload); err != nil {
			body = []byte(`{"error":"marshal"}`)
			code = 500
		} else {
			body = bytes.TrimRight(buf.Bytes(), "\n")
		}
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	s.corsHeaders(r, h)
	if len(body) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		var buf bytes.Buffer
		gz, _ := gzip.NewWriterLevel(&buf, 6)
		_, _ = gz.Write(body)
		_ = gz.Close()
		body = buf.Bytes()
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(code)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// writeGate 는 쓰기 공통 가드다. GateWrite는 Origin/Referer가 둘 다 없는
// 로컬 자동화 요청은 허용하되, 명시된 교차 출처는 RemoteAddr와 무관하게 거부한다.
func (s *Server) writeGate(w http.ResponseWriter, r *http.Request) bool {
	if s.Gate != nil {
		return s.Gate.GateWrite(w, r) // 응답은 GateWrite 가 작성
	}
	s.send(w, r, 403, map[string]any{"error": "쓰기는 비활성화되어 있습니다"})
	return false
}

// readJSONBody 는 최대 64KB 의 JSON 객체 본문을 읽는다(Python _read_json_body).
func (s *Server) readJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	body, err := readCapped(r, 64*1024)
	if err != nil {
		s.send(w, r, 400, map[string]any{"error": "JSON 본문이 필요합니다"})
		return nil, false
	}
	if len(body) == 0 {
		return nil, true // 본문 없음 — Python 의 None 과 같게 호출부에서 400 처리
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, true // 파싱 실패도 None 취급(Python 동일)
	}
	return m, true
}

// ServeHTTP 는 /api 라우팅이다. 패닉은 500 으로 변환한다(Python do_GET 의
// 맨 바깥 except 와 같은 격리).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			logf("error", "http", fmt.Sprintf("HTTP 핸들러 예외: %v\n%s", rec, debug.Stack()))
			s.send(w, r, 500, map[string]any{"error": "internal"})
		}
	}()
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "" {
		path = "/"
	}

	switch r.Method {
	case http.MethodOptions:
		s.send(w, r, 204, []byte{})
		return
	case http.MethodGet, http.MethodHead:
		s.doGet(w, r, path, r.URL.Query())
		return
	case http.MethodPut:
		s.doPut(w, r, path)
		return
	case http.MethodDelete:
		s.doDelete(w, r, path)
		return
	case http.MethodPost:
		s.doPost(w, r, path)
		return
	default:
		s.send(w, r, 404, map[string]any{"error": "not found", "path": path})
	}
}

// doGet 은 읽기 라우팅이다.
func (s *Server) doGet(w http.ResponseWriter, r *http.Request, path string, qs map[string][]string) {
	fleet, topo, ts := s.Cache.Snapshot()
	switch path {
	case "/api/fleet", "/api/fleet.json", "/api/devices":
		if fleet == nil {
			s.send(w, r, 503, map[string]any{
				"error": "no data yet", "stale": true,
				"clusters": []any{}, "devices": []any{},
				"generated_at": time.Now().Unix()})
			return
		}
		fmtQ := ""
		if v := qs["format"]; len(v) > 0 {
			fmtQ = lowerASCII(v[0])
		}
		if path == "/api/devices" || fmtQ == "devices" || fmtQ == "device" || fmtQ == "serverdesk" {
			out := poller.BuildDevices(fleet, s.DisplayCfg(), s.refreshSec())
			// 실 엣지 디바이스 — FT 클러스터 바로 뒤에 append.
			devices := []map[string]any{}
			for _, dv := range listAny(out["devices"]) {
				if dm, ok := dv.(map[string]any); ok {
					devices = append(devices, dm)
				}
			}
			if s.Edge != nil {
				devices = append(devices, s.Edge.Latest()...)
			}
			// 실측 가용성 주입(관측 충분한 장비만 명목값 대체).
			if s.Avail != nil {
				s.Avail.Apply(devices)
			}
			out["devices"] = devices
			out["count"] = len(devices)
			warnT, critT := poller.UsageThresholds()
			out["thresholds"] = map[string]any{"warn": warnT, "crit": critT}
			if s.Events != nil {
				out["events"] = s.Events.List(150)
			}
			out["cache_age_secs"] = round1(nowFloat() - ts)
			s.send(w, r, 200, out)
			return
		}
		// 캐시가 오래됐어도 stale 플래그만 덧붙여 그대로 제공(빈 응답 금지).
		out := shallowCopy(fleet)
		out["cache_age_secs"] = round1(nowFloat() - ts)
		s.send(w, r, 200, out)
	case "/api/topology", "/api/topology/full":
		wantFull := strings.HasSuffix(path, "/full")
		if v := qs["model"]; len(v) > 0 && (lowerASCII(v[0]) == "full" || lowerASCII(v[0]) == "detail") {
			wantFull = true
		}
		if wantFull {
			full, fts := s.Cache.SnapshotFull()
			if full == nil {
				s.send(w, r, 503, map[string]any{
					"error": "detailed topology unavailable",
					"hint":  "topology.py 로드 실패 또는 아직 수집 전",
					"nodes": []any{}, "edges": []any{}})
				return
			}
			out := shallowCopy(full)
			out["cache_age_secs"] = round1(nowFloat() - fts)
			s.send(w, r, 200, out)
			return
		}
		if topo == nil {
			s.send(w, r, 503, map[string]any{"error": "no data yet", "clusters": []any{}})
			return
		}
		out := shallowCopy(topo)
		out["cache_age_secs"] = round1(nowFloat() - ts)
		s.send(w, r, 200, out)
	case "/api/admin/config/export":
		s.handleConfigExport(w, r)
	case "/api/admin/health":
		s.send(w, r, 200, s.health(fleet, ts))
	case "/api/availability.csv":
		s.handleAvailabilityCSV(w, r)
	case "/api/health":
		s.send(w, r, 200, publicHealth(s.health(fleet, ts)))
	case "/api", "/":
		// Python 폴러는 / 에서 엔드포인트 인덱스를 줬다. 병합 바이너리에서는
		// / 가 프런트(index.html)에 할당되므로 인덱스는 /api 로 옮긴다.
		s.send(w, r, 200, map[string]any{
			"service": "everrun-poller", "version": poller.Version,
			"endpoints": []string{"/api/fleet", "/api/devices",
				"/api/fleet?format=devices", "/api/topology",
				"/api/topology?model=full", "/api/health", "/api/admin/health"}})
	default:
		s.send(w, r, 404, map[string]any{"error": "not found", "path": path})
	}
}

// health 는 인증된 /api/admin/health 상세 응답이다. 수집 티어, 캐시,
// edge worker, 이벤트 영속 저장의 신선도와 오류를 함께 판정한다.
func (s *Server) health(fleet map[string]any, ts float64) map[string]any {
	clusters := []any{}
	worst := "ok"
	rank := map[string]int{"ok": 0, "degraded": 1, "down": 2}
	bump := func(status string) {
		if rank[status] > rank[worst] {
			worst = status
		}
	}

	uptime := time.Since(s.StartedAt)
	if uptime < 0 {
		uptime = 0
	}
	refresh := 5
	if s.Cfg != nil && s.Cfg.CacheRefresh > 0 {
		refresh = s.Cfg.CacheRefresh
	}
	cacheLimit := float64(refresh * 3)
	cacheState := "ok"
	var cacheAge, cacheReason any
	if fleet == nil || ts == 0 {
		cacheState = "down"
		cacheReason = "캐시가 한 번도 생성되지 않음"
		bump(cacheState)
	} else {
		age := round1(nowFloat() - ts)
		cacheAge = age
		if age > cacheLimit {
			cacheState = "degraded"
			cacheReason = fmt.Sprintf("캐시 %.1fs 경과(임계 %.0fs)", age, cacheLimit)
			bump(cacheState)
		}
	}

	for _, st := range s.States {
		fastIV := st.Cfg.Intervals.Fast
		limit := float64(fastIV * 3)
		tiers := map[string]any{}
		var fastAge *float64
		errs := map[string]string{}
		for _, tier := range []string{"fast", "slow", "static"} {
			age := st.Age(tier)
			errStr := config.Mask(st.TierErr(tier))
			var errVal any
			if errStr != "" {
				errVal = errStr
				errs[tier] = errStr
			}
			tiers[tier] = map[string]any{"age_secs": age, "error": errVal}
			if tier == "fast" {
				fastAge = age
			}
		}
		var status, reason string
		if fastAge == nil {
			status, reason = "down", "fast 티어가 한 번도 성공하지 못함"
		} else if *fastAge > limit {
			status = "degraded"
			reason = fmt.Sprintf("fast 티어 %.0fs 경과(임계 %ds)", *fastAge, int64(limit))
		} else if len(errs) > 0 {
			status = "degraded"
			parts := []string{}
			for _, tier := range []string{"fast", "slow", "static"} {
				if value, ok := errs[tier]; ok {
					parts = append(parts, tier+"="+value)
				}
			}
			reason = "수집 오류: " + strings.Join(parts, "; ")
		} else {
			status = "ok"
		}
		bump(status)
		nodes, vms := st.NodeCounts()
		var reasonValue any
		if reason != "" {
			reasonValue = reason
		}
		clusters = append(clusters, map[string]any{
			"key":                  st.Key,
			"platform":             platformOr(st.GetPlatform(), "unknown"),
			"status":               status,
			"reason":               reasonValue,
			"stale_threshold_secs": int64(limit),
			"tiers":                tiers,
			"nodes_seen":           nodes,
			"vms_seen":             vms,
			"os_metrics":           osMetricsMap(st.OSReachable()),
		})
	}

	eventStatus := map[string]any{"enabled": false, "healthy": true}
	if s.Events != nil {
		eventStatus = s.Events.Status()
		eventStatus["enabled"] = true
		if value, ok := eventStatus["last_error"].(string); ok {
			eventStatus["last_error"] = config.Mask(value)
		}
		if healthy, _ := eventStatus["healthy"].(bool); !healthy {
			bump("degraded")
		}
	}

	edgeSnapshot := poller.EdgeCollectorStatus{}
	if s.Edge != nil {
		edgeSnapshot = s.Edge.CollectorStatus()
	} else if s.Cfg != nil {
		edgeSnapshot.Configured = len(s.Cfg.EdgeDevices)
	}
	edgeStatus, edgeSeverity := edgeCollectorHealth(uptime, edgeSnapshot)
	if edgeSeverity != "" {
		bump(edgeSeverity)
	}

	return map[string]any{
		"status":         worst,
		"version":        poller.Version,
		"uptime_secs":    int64(uptime.Seconds()),
		"cache_age_secs": cacheAge,
		"cache": map[string]any{
			"status":               cacheState,
			"reason":               cacheReason,
			"age_secs":             cacheAge,
			"stale_threshold_secs": int64(cacheLimit),
		},
		"event_store":    eventStatus,
		"edge_collector": edgeStatus,
		"clusters":       clusters,
	}
}

// publicHealth 는 인증 없이 공개되는 설치/감시용 최소 응답이다. 내부 주소,
// 클러스터 식별자, 수집 오류와 파일 경로는 상세 health에만 남긴다.
func publicHealth(detail map[string]any) map[string]any {
	return map[string]any{
		"status":      detail["status"],
		"version":     detail["version"],
		"uptime_secs": detail["uptime_secs"],
	}
}

const edgeCollectorStaleAfter = 130 * time.Second

func edgeCollectorHealth(uptime time.Duration, snapshot poller.EdgeCollectorStatus) (map[string]any, string) {
	component := map[string]any{
		"status":               "disabled",
		"reason":               nil,
		"configured":           snapshot.Configured,
		"observed":             snapshot.Observed,
		"last_round_at":        nil,
		"age_secs":             nil,
		"stale_threshold_secs": int64(edgeCollectorStaleAfter.Seconds()),
		"last_error":           nil,
	}
	if snapshot.Configured == 0 {
		return component, ""
	}

	component["status"] = "ok"
	if snapshot.LastError != "" {
		component["status"] = "degraded"
		component["reason"] = "edge 수집 라운드 실패"
		component["last_error"] = config.Mask(snapshot.LastError)
		return component, "degraded"
	}
	if snapshot.LastRoundAt.IsZero() {
		if uptime < edgeCollectorStaleAfter {
			component["status"] = "starting"
			return component, ""
		}
		component["status"] = "degraded"
		component["reason"] = "완료된 edge 수집 라운드 없음"
		return component, "degraded"
	}

	age := time.Since(snapshot.LastRoundAt)
	if age < 0 {
		age = 0
	}
	component["last_round_at"] = snapshot.LastRoundAt.UTC().Format(time.RFC3339)
	component["age_secs"] = round1(age.Seconds())
	if age > edgeCollectorStaleAfter {
		component["status"] = "degraded"
		component["reason"] = fmt.Sprintf("edge 수집 %.1fs 경과(임계 %.0fs)",
			age.Seconds(), edgeCollectorStaleAfter.Seconds())
		return component, "degraded"
	}
	if snapshot.Observed < snapshot.Configured {
		component["status"] = "degraded"
		component["reason"] = fmt.Sprintf("설정 %d대 중 %d대 스냅샷",
			snapshot.Configured, snapshot.Observed)
		return component, "degraded"
	}
	return component, ""
}

func platformOr(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

func osMetricsMap(m map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func shallowCopy(m map[string]any) map[string]any {
	out := make(map[string]any, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func listAny(v any) []any {
	l, _ := v.([]any)
	return l
}

func lowerASCII(s string) string { return strings.ToLower(s) }

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func round1(x float64) float64 {
	return math.RoundToEven(x*10) / 10
}
