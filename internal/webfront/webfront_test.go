package webfront

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
)

// testFS 는 차단/허용 경로를 고루 담은 가짜 정적 트리다.
func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":          {Data: []byte("<!doctype html><html><body>app</body></html>")},
		"js/app.js":           {Data: []byte(strings.Repeat("console.log('x');\n", 100))}, // >1KB
		"css/site.css":        {Data: []byte("body{color:#111}")},                         // <1KB
		"fonts/f.woff2":       {Data: bytes.Repeat([]byte{1, 2, 3}, 600)},                 // >1KB 바이너리
		"img/logo.svg":        {Data: []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)},
		"sub/page.txt":        {Data: []byte("hello")},
		"nodirindex/a.txt":    {Data: []byte("x")},
		"serve.py":            {Data: []byte("# python source")},
		"README.md":           {Data: []byte("# readme")},
		"debug.log":           {Data: []byte("log")},
		"debug.log.1":         {Data: []byte("log1")},
		"ack-state.json":      {Data: []byte("{}")},
		"ack-state.json.lock": {Data: []byte("")},
		".git/config":         {Data: []byte("x")},
		"tests/x.mjs":         {Data: []byte("x")},
		"tools/run.sh":        {Data: []byte("x")},
		"docs/design/x.html":  {Data: []byte("x")},
		"systemd/u.service":   {Data: []byte("x")},
		"sub/.hidden":         {Data: []byte("x")},
		"__pycache__/m.pyc":   {Data: []byte("x")},
	}
}

func newTestServer(t *testing.T, opts Options) *Server {
	t.Helper()
	if opts.StateDir == "" {
		opts.StateDir = t.TempDir()
	}
	return New(testFS(), opts)
}

func do(s *Server, method, target string, body io.Reader, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestStaticIndexAndSecurityHeaders(t *testing.T) {
	s := newTestServer(t, Options{})
	rec := do(s, "GET", "/", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	// CSP 의 인라인 테마 스크립트 해시는 실제 web/index.html 의 <script> 본문에서
	// 계산한다 — 하드코딩하면 스크립트 수정 시 테스트가 아니라 브라우저가 먼저 깨진다.
	hash := inlineThemeScriptSHA256
	if b, err := os.ReadFile("../../web/index.html"); err == nil {
		if m := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindSubmatch(b); m != nil {
			sum := sha256.Sum256(m[1])
			hash = "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
		}
	}
	wantCSP := "default-src 'self'; " +
		"script-src 'self' '" + hash + "'; " +
		"img-src 'self' data:; font-src 'self'; style-src 'self' 'unsafe-inline'; " +
		"object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Errorf("CSP = %q, want %q", got, wantCSP)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-cache, must-revalidate" {
		t.Errorf("index Cache-Control = %q, want no-cache, must-revalidate", got)
	}
	if !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("index body missing content: %q", rec.Body.String())
	}
}

func TestStaticBlacklist(t *testing.T) {
	s := newTestServer(t, Options{})
	blocked := []string{
		"/serve.py", "/README.md", "/debug.log", "/debug.log.1",
		"/ack-state.json", "/ack-state.json.lock",
		"/.git/config", "/tests/x.mjs", "/tools/run.sh",
		"/docs/design/x.html", "/systemd/u.service", "/sub/.hidden",
		"/__pycache__/m.pyc",
		// 인코딩/경로 우회 시도 — 디코딩·정규화 뒤에도 막혀야 한다.
		"/%2egit/config", "/js/../serve.py", "/js/%2E%2E/serve.py",
		"/nodirindex/", // 디렉터리 목록 대신 404
	}
	for _, p := range blocked {
		rec := do(s, "GET", p, nil, nil)
		if rec.Code != 404 {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	// 디렉터리 목록이 아니라 404 라 파일명이 새면 안 된다.
	rec := do(s, "GET", "/nodirindex/", nil, nil)
	if strings.Contains(rec.Body.String(), "a.txt") {
		t.Errorf("directory listing leaked file names: %q", rec.Body.String())
	}
	// NUL 바이트 경로(cron 판 하드닝).
	req := httptest.NewRequest("GET", "/", nil)
	req.URL.Path = "/a\x00b.js"
	rec2 := httptest.NewRecorder()
	s.ServeHTTP(rec2, req)
	if rec2.Code != 404 {
		t.Errorf("NUL path = %d, want 404", rec2.Code)
	}
}

func TestStaticMIMEAndCache(t *testing.T) {
	s := newTestServer(t, Options{})
	cases := []struct {
		path, mime, cache string
	}{
		// css/js 는 콘텐츠 해시 없는 파일명이라 재검증(배포 직후 구버전 캐시 혼선 방지),
		// 폰트·svg 만 장기 캐시.
		{"/js/app.js", "text/javascript", "no-cache, must-revalidate"},
		{"/css/site.css", "text/css", "no-cache, must-revalidate"},
		{"/fonts/f.woff2", "font/woff2", "public, max-age=3600"},
		{"/img/logo.svg", "image/svg+xml", "public, max-age=3600"},
		{"/sub/page.txt", "text/plain", "no-cache, must-revalidate"},
	}
	for _, c := range cases {
		rec := do(s, "GET", c.path, nil, nil)
		if rec.Code != 200 {
			t.Errorf("GET %s = %d", c.path, rec.Code)
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != c.mime {
			t.Errorf("GET %s Content-Type = %q, want %q", c.path, got, c.mime)
		}
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.cache)
		}
	}
}

func TestStaticGzip(t *testing.T) {
	s := newTestServer(t, Options{})
	orig := []byte(strings.Repeat("console.log('x');\n", 100))

	// >1KB + gzip 수용 → 압축.
	rec := do(s, "GET", "/js/app.js", nil, map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Errorf("Vary = %q, want Accept-Encoding", got)
	}
	gz, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	plain, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	if !bytes.Equal(plain, orig) {
		t.Errorf("gunzipped body mismatch")
	}

	// >1KB 이어도 클라이언트가 받지 않으면 identity.
	rec = do(s, "GET", "/js/app.js", nil, nil)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("no-AE Content-Encoding = %q, want empty", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), orig) {
		t.Errorf("identity body mismatch")
	}

	// q=0 은 수용이 아니다.
	rec = do(s, "GET", "/js/app.js", nil, map[string]string{"Accept-Encoding": "gzip;q=0"})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("q=0 Content-Encoding = %q, want empty", got)
	}

	// <1KB 는 gzip 하지 않는다.
	rec = do(s, "GET", "/css/site.css", nil, map[string]string{"Accept-Encoding": "gzip"})
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Errorf("small file Content-Encoding = %q, want empty", got)
	}
}

func TestStaticDirRedirectAndHEAD(t *testing.T) {
	s := newTestServer(t, Options{})

	// 트레일링 슬래시 없는 디렉터리는 301 (Python SimpleHTTPRequestHandler 와 같다).
	rec := do(s, "GET", "/nodirindex", nil, nil)
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /nodirindex = %d, want 301", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/nodirindex/" {
		t.Errorf("Location = %q, want /nodirindex/", loc)
	}

	// HEAD: 헤더만, 바디 없음.
	rec = do(s, "HEAD", "/js/app.js", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("HEAD = %d", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body len = %d, want 0", rec.Body.Len())
	}
	if got := rec.Header().Get("Content-Type"); got != "text/javascript" {
		t.Errorf("HEAD Content-Type = %q", got)
	}
}

func TestMethodRouting(t *testing.T) {
	s := newTestServer(t, Options{})
	cases := []struct {
		method, path string
		want         int
	}{
		{"POST", "/ack", 405},   // POST 는 /notify 뿐
		{"PUT", "/notify", 405}, // PUT 은 상태 엔드포인트뿐
		{"DELETE", "/index.html", 405},
		{"PATCH", "/", 501}, // Python 은 미구현 메서드에 501
		{"OPTIONS", "/js/app.js", 204},
		{"GET", "/notify", 404}, // GET /notify 는 정적으로 떨어져 404
	}
	for _, c := range cases {
		rec := do(s, c.method, c.path, nil, nil)
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
}

// TestStaticLastModified — embed.FS 는 ModTime 이 zero 라 Last-Modified 를 본내지 않고,
// 실제 파일시스템(os.DirFS)에서는 본낸다.
func TestStaticLastModified(t *testing.T) {
	s := newTestServer(t, Options{})
	rec := do(s, "GET", "/sub/page.txt", nil, nil)
	if got := rec.Header().Get("Last-Modified"); got != "" {
		t.Errorf("MapFS Last-Modified = %q, want empty (embed parity)", got)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	s2 := New(os.DirFS(dir), Options{StateDir: t.TempDir()})
	rec = do(s2, "GET", "/a.txt", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("DirFS GET = %d", rec.Code)
	}
	if got := rec.Header().Get("Last-Modified"); got == "" {
		t.Errorf("DirFS Last-Modified missing")
	}
}

// gateFS 는 지정 파일의 Open 을 release 까지 붙잡는 테스트용 FS 래퍼다.
type gateFS struct {
	fs.FS
	gate    string
	enter   chan struct{}
	release chan struct{}
}

func (g *gateFS) Open(name string) (fs.File, error) {
	if name == g.gate {
		g.enter <- struct{}{}
		<-g.release
	}
	return g.FS.Open(name)
}

func TestConcurrencyLimit(t *testing.T) {
	s := newTestServer(t, Options{})
	s.sem = make(chan struct{}, 1) // 슬롯 하나로 좁혀 경합을 만든다.
	gate := &gateFS{FS: testFS(), gate: "js/app.js",
		enter: make(chan struct{}, 1), release: make(chan struct{})}
	s.static = gate

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- do(s, "GET", "/js/app.js", nil, nil)
	}()
	<-gate.enter // 첫 요청이 유일한 슬롯을 쥐고 Open 안에서 잠겨 있다.

	// 슬롯이 없으면 기다리지 않고 503(cron 판 non-blocking 세마포어).
	rec := do(s, "GET", "/index.html", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("busy = %d, want 503", rec.Code)
	}

	close(gate.release)
	rec1 := <-done
	if rec1.Code != 200 {
		t.Errorf("gated request = %d, want 200", rec1.Code)
	}
}
