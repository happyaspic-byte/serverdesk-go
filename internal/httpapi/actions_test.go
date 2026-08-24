package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"serverdesk/internal/webauth"
)

func TestMutationAuditLogContainsNoBody(t *testing.T) {
	f := newAdminTestFixture(t, "")
	var messages []string
	handler := webauth.AuditMutations(f.srv, func(message string) { messages = append(messages, message) })
	req := httptest.NewRequest(http.MethodPost, "/api/clusters/cl-ft1/action",
		strings.NewReader(`{"action":"node-reboot","password":"must-not-be-logged"}`))
	req.RemoteAddr = "127.0.0.1:4321"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("action status = %d", rec.Code)
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "operator=admin") || !strings.Contains(joined, "status=501") ||
		strings.Contains(joined, "must-not-be-logged") {
		t.Fatalf("audit log contract = %q", joined)
	}
}

func TestClusterActionCapabilityContract(t *testing.T) {
	f := newAdminTestFixture(t, "")

	t.Run("capabilities endpoint advertises fail-closed action support", func(t *testing.T) {
		rec, res := execRequest(f.srv, http.MethodGet, "/api/capabilities", nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET capabilities = %d: %s", rec.Code, rec.Body.String())
		}
		caps, ok := res["capabilities"].(map[string]any)
		if !ok {
			t.Fatalf("capabilities missing or malformed: %#v", res)
		}
		actions, ok := caps[clusterActionsCapability].(map[string]any)
		if !ok || actions["supported"] != false {
			t.Fatalf("cluster action capability must be explicitly unsupported: %#v", caps)
		}
		allow, ok := actions["actions"].([]any)
		if !ok || len(allow) != 0 {
			t.Fatalf("unsupported cluster action allowlist = %#v, want empty", actions["actions"])
		}
		if strings.TrimSpace(actions["reason"].(string)) == "" {
			t.Fatal("unsupported capability must explain why")
		}

		// 프런트가 이미 폴링하는 /api/devices에도 같은 계약을 실어 별도 요청 없이 게이트한다.
		f.srv.Cache.Update(f.srv.States)
		devRec, devRes := execRequest(f.srv, http.MethodGet, "/api/devices", nil, "")
		if devRec.Code != http.StatusOK || devRes["devices"] == nil || devRes["capabilities"] == nil {
			t.Fatalf("GET /api/devices capability contract = %d: %#v", devRec.Code, devRes)
		}
	})

	t.Run("POST returns structured 501 instead of generic 404", func(t *testing.T) {
		rec, res := execRequest(f.srv, http.MethodPost, "/api/clusters/cl-ft1/action",
			map[string]any{"action": "node-reboot", "target": "node0"}, "")
		if rec.Code != http.StatusNotImplemented {
			t.Fatalf("POST cluster action = %d: %s", rec.Code, rec.Body.String())
		}
		if res["code"] != "capability_not_supported" || res["capability"] != clusterActionsCapability ||
			res["supported"] != false || res["cluster_id"] != "cl-ft1" {
			t.Fatalf("unexpected structured error: %#v", res)
		}
		if res["error"] == "not found" {
			t.Fatalf("unsupported action fell back to generic 404 contract: %#v", res)
		}
	})

	t.Run("wrong method returns structured 405", func(t *testing.T) {
		rec, res := execRequest(f.srv, http.MethodGet, "/api/clusters/cl-ft1/action", nil, "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET cluster action = %d: %s", rec.Code, rec.Body.String())
		}
		if res["code"] != "method_not_allowed" || !strings.Contains(rec.Header().Get("Allow"), http.MethodPost) {
			t.Fatalf("unexpected 405 contract: headers=%v body=%#v", rec.Header(), res)
		}
	})

	t.Run("write gate still rejects cross-origin mutation first", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://noc.local/api/clusters/cl-ft1/action",
			strings.NewReader(`{"action":"node-reboot"}`))
		req.Host = "noc.local"
		req.Header.Set("Origin", "http://evil.example")
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross-origin") {
			t.Fatalf("cross-origin action = %d: %s", rec.Code, rec.Body.String())
		}
	})
}

func TestClusterActionTarget(t *testing.T) {
	tests := []struct {
		path string
		id   string
		ok   bool
	}{
		{"/api/clusters/cl-ft1/action", "cl-ft1", true},
		{"/api/clusters/%25edge/action", "%edge", true},
		{"/api/clusters//action", "", false},
		{"/api/clusters/cl-ft1/action/extra", "", false},
		{"/api/clusters/cl-ft1", "", false},
	}
	for _, tc := range tests {
		id, ok := clusterActionTarget(tc.path)
		if id != tc.id || ok != tc.ok {
			t.Errorf("clusterActionTarget(%q) = (%q,%v), want (%q,%v)", tc.path, id, ok, tc.id, tc.ok)
		}
	}
}
