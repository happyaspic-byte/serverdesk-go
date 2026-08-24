package webfront

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGateWrite(t *testing.T) {
	mkreq := func(hdr map[string]string) *http.Request {
		req := httptest.NewRequest("DELETE", "/api/clusters/x", nil)
		req.Host = "noc.local:6001"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return req
	}

	// AllowWrites 꺼짐 → 403.
	s := New(testFS(), Options{StateDir: t.TempDir()})
	rec := httptest.NewRecorder()
	if s.GateWrite(rec, mkreq(nil)) {
		t.Errorf("GateWrite with AllowWrites=false = true, want false")
	}
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "writes are disabled") {
		t.Errorf("GateWrite off = %d %s", rec.Code, rec.Body.String())
	}

	// 켜짐 + 출처 일치 → 통과.
	s = New(testFS(), Options{StateDir: t.TempDir(), AllowWrites: true})
	rec = httptest.NewRecorder()
	if !s.GateWrite(rec, mkreq(map[string]string{"Origin": "http://noc.local:6001"})) {
		t.Errorf("GateWrite same-origin = false, want true: %s", rec.Body.String())
	}

	// 켜짐 + 교차 출처 → 403.
	rec = httptest.NewRecorder()
	if s.GateWrite(rec, mkreq(map[string]string{"Origin": "http://evil.example"})) {
		t.Errorf("GateWrite cross-origin = true, want false")
	}
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "cross-origin write rejected") {
		t.Errorf("GateWrite cross = %d %s", rec.Code, rec.Body.String())
	}

}

func TestCheckSameOrigin(t *testing.T) {
	cases := []struct {
		name            string
		origin, referer string
		want            bool
	}{
		{"no headers", "", "", true},
		{"origin match", "http://noc.local:6001", "", true},
		{"origin case-insensitive", "http://NOC.LOCAL:6001", "", true},
		{"origin mismatch", "http://evil.example", "", false},
		{"origin null", "null", "", false},
		{"origin file", "file://", "", false},
		{"origin garbage", "ht tp://x", "", false},
		{"referer match", "", "http://noc.local:6001/ui/", true},
		{"referer mismatch", "", "http://evil.example/ui/", false},
		{"both, referer bad", "http://noc.local:6001", "http://evil.example/", false},
		{"origin with userinfo", "http://u@noc.local:6001", "", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest("PUT", "/ack", nil)
		req.Host = "noc.local:6001"
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		if c.referer != "" {
			req.Header.Set("Referer", c.referer)
		}
		if got := CheckSameOrigin(req); got != c.want {
			t.Errorf("%s: CheckSameOrigin = %v, want %v", c.name, got, c.want)
		}
	}

	httpsReq := httptest.NewRequest("PUT", "https://noc.local/ack", nil)
	httpsReq.Host = "noc.local"
	httpsReq.Header.Set("Origin", "http://noc.local")
	if CheckSameOrigin(httpsReq) {
		t.Fatal("HTTPS request accepted an HTTP origin with the same authority")
	}
	httpsReq.Header.Set("Origin", "https://noc.local:443")
	if !CheckSameOrigin(httpsReq) {
		t.Fatal("HTTPS request rejected the canonical default port")
	}

	proxyReq := httptest.NewRequest("PUT", "http://noc.local/ack", nil)
	proxyReq.Host = "noc.local"
	proxyReq.RemoteAddr = "127.0.0.1:4321"
	proxyReq.Header.Set("X-Forwarded-For", "203.0.113.10")
	proxyReq.Header.Set("X-Forwarded-Proto", "https")
	proxyReq.Header.Set("Origin", "https://noc.local")
	if !CheckSameOrigin(proxyReq) {
		t.Fatal("validated loopback proxy scheme was not honored")
	}
	proxyReq.Header.Del("X-Forwarded-For")
	if CheckSameOrigin(proxyReq) {
		t.Fatal("proxy scheme was trusted without a validated client address")
	}
}

func TestApplyHardening(t *testing.T) {
	srv := &http.Server{}
	ApplyHardening(srv)
	if srv.ReadHeaderTimeout != 30*time.Second || srv.ReadTimeout != 30*time.Second ||
		srv.WriteTimeout != 30*time.Second || srv.IdleTimeout != 30*time.Second {
		t.Errorf("timeouts = %v/%v/%v/%v, want 30s each",
			srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout, srv.IdleTimeout)
	}
	if srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("MaxHeaderBytes = %d, want 64KiB", srv.MaxHeaderBytes)
	}
}
