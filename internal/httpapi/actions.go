package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

const (
	clusterActionsCapability = "cluster_actions"
	clusterActionsReason     = "cluster mutations are not implemented by this server"
	clusterActionsReasonKO   = "이 서버는 클러스터 제어 변경 작업을 아직 구현하지 않았습니다"
)

// capabilities 는 프런트가 추측으로 변경 API 를 노출하지 않도록 서버가 지원하는
// 기능을 명시한다. actions 는 allowlist 계약이며 빈 배열은 지원 액션이 없다는 뜻이다.
func (s *Server) capabilities() map[string]any {
	return map[string]any{
		clusterActionsCapability: map[string]any{
			"supported": false,
			"actions":   []string{},
			"endpoint":  "/api/clusters/{id}/action",
			"reason":    clusterActionsReason,
			"reason_ko": clusterActionsReasonKO,
		},
	}
}

// clusterActionTarget 는 정확히 /api/clusters/<id>/action 형태만 인식한다.
// 장비 key 는 설정 API 와 같은 URL path segment 계약을 따른다.
func clusterActionTarget(path string) (string, bool) {
	const prefix = "/api/clusters/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/action") {
		return "", false
	}
	middle := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/action")
	if middle == "" || strings.Contains(middle, "/") {
		return "", false
	}
	id, err := url.PathUnescape(middle)
	if err != nil || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// clusterActionUnsupported 는 존재하지 않는 구현을 일반 404 로 위장하지 않는다.
// capability와 안정적인 code를 함께 보내 UI·자동화가 명시적으로 분기할 수 있게 한다.
func (s *Server) clusterActionUnsupported(w http.ResponseWriter, r *http.Request, clusterID string) {
	s.send(w, r, http.StatusNotImplemented, map[string]any{
		"error":      clusterActionsReason,
		"code":       "capability_not_supported",
		"capability": clusterActionsCapability,
		"supported":  false,
		"cluster_id": clusterID,
		"reason_ko":  clusterActionsReasonKO,
	})
}

func (s *Server) clusterActionMethodNotAllowed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST, OPTIONS")
	s.send(w, r, http.StatusMethodNotAllowed, map[string]any{
		"error":           "method not allowed for cluster action endpoint",
		"code":            "method_not_allowed",
		"capability":      clusterActionsCapability,
		"supported":       false,
		"allowed_methods": []string{http.MethodPost, http.MethodOptions},
	})
}
