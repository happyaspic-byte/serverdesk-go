package webauth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuditMutationsNormalResponseAndBypasses(t *testing.T) {
	var messages []string
	handler := AuditMutations(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("accepted"))
	}), func(message string) { messages = append(messages, message) })

	req := httptest.NewRequest(http.MethodPost, "https://serverdesk.test/admin/device%20one?password=hidden", nil)
	req.RemoteAddr = "127.0.0.1:4242"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusAccepted || res.Body.String() != "accepted" {
		t.Fatalf("response = %d %q", res.Code, res.Body.String())
	}
	if len(messages) != 1 || !strings.Contains(messages[0], `method=POST path="/admin/device%20one" status=202 remote=127.0.0.1`) {
		t.Fatalf("audit messages = %q", messages)
	}
	if strings.Contains(messages[0], "password") {
		t.Fatalf("audit leaked query: %q", messages[0])
	}

	messages = nil
	get := httptest.NewRequest(http.MethodGet, "https://serverdesk.test/read-only", nil)
	handler.ServeHTTP(httptest.NewRecorder(), get)
	if len(messages) != 0 {
		t.Fatalf("read-only request was audited: %q", messages)
	}

	called := false
	withoutLogger := AuditMutations(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }), nil)
	withoutLogger.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/device", nil))
	if !called {
		t.Fatal("nil logger did not preserve downstream handler")
	}
}

func TestAuditResponseWriterContractsAndMutationMethods(t *testing.T) {
	base := httptest.NewRecorder()
	wrapper := &auditResponseWriter{ResponseWriter: base}
	if wrapper.Status() != http.StatusOK || wrapper.Unwrap() != base {
		t.Fatal("default status or unwrap contract is wrong")
	}
	if _, err := wrapper.Write([]byte("ok")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if wrapper.Status() != http.StatusOK || base.Body.String() != "ok" {
		t.Fatalf("write contract = status %d body %q", wrapper.Status(), base.Body.String())
	}

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if !isMutationMethod(method) {
			t.Fatalf("%s should be a mutation", method)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, "post"} {
		if isMutationMethod(method) {
			t.Fatalf("%s should not be a mutation", method)
		}
	}
}
