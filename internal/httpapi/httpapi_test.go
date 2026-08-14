package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"serverdesk/internal/webfront"
)

func TestWriteGateRejectsEvilOriginFromLoopbackProxy(t *testing.T) {
	srv := &Server{Gate: webfront.New(nil, webfront.Options{StateDir: t.TempDir(), AllowWrites: true})}
	req := httptest.NewRequest(http.MethodPost, "http://noc.local:6001/api/clusters", nil)
	req.RemoteAddr = "127.0.0.1:4567"
	req.Header.Set("Origin", "https://evil.example")

	rec := httptest.NewRecorder()
	if srv.writeGate(rec, req) {
		t.Fatal("loopback proxy request with evil Origin passed write gate")
	}
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross-origin write rejected") {
		t.Fatalf("write gate response = %d: %s", rec.Code, rec.Body.String())
	}
}
