package webauth

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const fixedTestPassword = "test-admin-password"

var testCredentials Credentials

// TestMain은 테스트 실행 속도 개선을 위해 PBKDF2 반복수를 1000으로 오버라이드한다. (프로덕션 기본값 600_000은 불변)
func TestMain(m *testing.M) {
	credentialIterations = 1000
	testCredentials = mustTestCredentials()
	os.Exit(m.Run())
}

func mustTestCredentials() Credentials {
	digest, err := pbkdf2.Key(sha256.New, fixedTestPassword, []byte("test-salt-123456"), credentialIterations, credentialDigestBytes)
	if err != nil {
		panic(err)
	}
	return Credentials{salt: []byte("test-salt-123456"), digest: digest}
}

func TestCredentials(t *testing.T) {
	manager := New(testCredentials)
	if !manager.validCredentials(Username, fixedTestPassword) {
		t.Fatal("administrator credentials were rejected")
	}
	if manager.validCredentials("Admin", fixedTestPassword) {
		t.Fatal("username must be case-sensitive")
	}
	if manager.validCredentials(Username, fixedTestPassword+"x") {
		t.Fatal("wrong password was accepted")
	}
}

func TestLoginSessionAndLogout(t *testing.T) {
	manager := New(testCredentials)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("protected:" + r.URL.Path))
	})
	handler := manager.Handler(next)

	root := serve(handler, http.MethodGet, "/", nil, "")
	if root.Code != http.StatusSeeOther || !strings.HasPrefix(root.Header().Get("Location"), "/login?next=") {
		t.Fatalf("unauthenticated root = %d location=%q", root.Code, root.Header().Get("Location"))
	}

	api := serve(handler, http.MethodGet, "/api/devices", nil, "")
	if api.Code != http.StatusUnauthorized || !strings.Contains(api.Body.String(), "authentication required") {
		t.Fatalf("unauthenticated API = %d %s", api.Code, api.Body.String())
	}
	if api.Header().Get("Location") == "" {
		t.Fatal("unauthenticated API did not advertise the login location")
	}

	health := serve(handler, http.MethodGet, "/api/health", nil, "")
	if health.Code != http.StatusOK || health.Body.String() != "protected:/api/health" {
		t.Fatalf("public health = %d %q", health.Code, health.Body.String())
	}

	healthHead := serve(handler, http.MethodHead, "/api/health", nil, "")
	if healthHead.Code != http.StatusOK {
		t.Fatalf("public HEAD health = %d, want 200", healthHead.Code)
	}

	detailedHealth := serve(handler, http.MethodGet, "/api/admin/health", nil, "")
	if detailedHealth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated detailed health = %d, want 401", detailedHealth.Code)
	}
	detailedHealthHead := serve(handler, http.MethodHead, "/api/admin/health", nil, "")
	if detailedHealthHead.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated detailed HEAD health = %d, want 401", detailedHealthHead.Code)
	}

	healthSlash := serve(handler, http.MethodGet, "/api/health/", nil, "")
	if healthSlash.Code != http.StatusUnauthorized {
		t.Fatalf("health trailing slash = %d, want 401", healthSlash.Code)
	}
	healthSlashHead := serve(handler, http.MethodHead, "/api/health/", nil, "")
	if healthSlashHead.Code != http.StatusUnauthorized {
		t.Fatalf("HEAD health trailing slash = %d, want 401", healthSlashHead.Code)
	}

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, encodedHealth := range []string{"/api%2fhealth", "/api/%68ealth", "/API/health"} {
			rec := serve(handler, method, encodedHealth, nil, "")
			if rec.Code == http.StatusOK {
				t.Errorf("%s non-exact health path %q reached the public handler", method, encodedHealth)
			}
		}
	}

	page := serve(handler, http.MethodGet, "/login", nil, "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "관리자 로그인") {
		t.Fatalf("login page = %d", page.Code)
	}
	if strings.Contains(page.Body.String(), fixedTestPassword) {
		t.Fatal("login page exposes the test password")
	}
	if got := page.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Fatalf("login Referrer-Policy = %q, want same-origin", got)
	}

	wrong := login(handler, Username, "wrong", "/")
	if wrong.Code != http.StatusUnauthorized || len(wrong.Result().Cookies()) != 0 {
		t.Fatalf("wrong login = %d cookies=%d", wrong.Code, len(wrong.Result().Cookies()))
	}

	loggedIn := login(handler, Username, fixedTestPassword, "/api/devices?format=full")
	if loggedIn.Code != http.StatusSeeOther || loggedIn.Header().Get("Location") != "/api/devices?format=full" {
		t.Fatalf("login = %d location=%q", loggedIn.Code, loggedIn.Header().Get("Location"))
	}
	cookies := loggedIn.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies = %d, want 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != cookieName || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge <= 0 {
		t.Fatalf("session cookie attributes = %#v", cookie)
	}

	authedReq := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	authedReq.AddCookie(cookie)
	authed := httptest.NewRecorder()
	handler.ServeHTTP(authed, authedReq)
	if authed.Code != http.StatusOK || authed.Body.String() != "protected:/api/devices" {
		t.Fatalf("authenticated API = %d %q", authed.Code, authed.Body.String())
	}

	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.AddCookie(cookie)
	logoutReq.Header.Set("Origin", "http://example.com")
	logout := httptest.NewRecorder()
	handler.ServeHTTP(logout, logoutReq)
	if logout.Code != http.StatusSeeOther || logout.Header().Get("Location") != "/login" {
		t.Fatalf("logout = %d location=%q", logout.Code, logout.Header().Get("Location"))
	}
	cleared := logout.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Fatalf("logout cookie = %#v", cleared)
	}

	reusedReq := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	reusedReq.AddCookie(cookie)
	reused := httptest.NewRecorder()
	handler.ServeHTTP(reused, reusedReq)
	if reused.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session reuse = %d, want 401", reused.Code)
	}
}

func TestLoginOriginRateLimitAndSafeNext(t *testing.T) {
	manager := New(testCredentials)
	handler := manager.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	crossValues := url.Values{"username": {Username}, "password": {fixedTestPassword}}
	crossReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(crossValues.Encode()))
	crossReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	crossReq.Header.Set("Origin", "http://evil.example")
	crossReq.Host = "serverdesk.local"
	cross := httptest.NewRecorder()
	handler.ServeHTTP(cross, crossReq)
	if cross.Code != http.StatusForbidden {
		t.Fatalf("cross-origin login = %d, want 403", cross.Code)
	}

	noOriginReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(crossValues.Encode()))
	noOriginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	noOrigin := httptest.NewRecorder()
	handler.ServeHTTP(noOrigin, noOriginReq)
	if noOrigin.Code != http.StatusForbidden {
		t.Fatalf("login without origin = %d, want 403", noOrigin.Code)
	}

	same := httptest.NewRequest(http.MethodPost, "http://serverdesk.local/login", nil)
	same.Header.Set("Origin", "http://serverdesk.local:80")
	if !sameOrigin(same) {
		t.Fatal("equivalent default HTTP port was rejected")
	}
	same.Header.Set("Origin", "https://serverdesk.local")
	if sameOrigin(same) {
		t.Fatal("cross-scheme origin was accepted")
	}
	same.Header.Set("Origin", "http://serverdesk.local/path")
	if sameOrigin(same) {
		t.Fatal("Origin containing a path was accepted")
	}

	for i := 0; i < maxFailures; i++ {
		manager.recordFailure("192.0.2.20")
	}
	if blocked, wait := manager.loginBlocked("192.0.2.20"); !blocked || wait <= 0 {
		t.Fatalf("rate limit = blocked %v wait %s", blocked, wait)
	}

	for _, candidate := range []string{
		"https://evil.example", "//evil.example", `/\evil.example`, "/%5cevil.example",
		"/%2f/evil.example", "/login", "/login/", "/logout", "bad",
	} {
		if got := safeNext(candidate); got != "/" {
			t.Errorf("safeNext(%q) = %q, want /", candidate, got)
		}
	}
	if got := safeNext("/nodes?q=a#top"); got != "/nodes?q=a#top" {
		t.Errorf("safeNext valid = %q", got)
	}

	limited := New(testCredentials)
	limitedNow := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	limited.now = func() time.Time { return limitedNow }
	for i := 0; i < maxFailureEntries+20; i++ {
		limited.recordFailure(fmt.Sprintf("2001:db8::%x", i))
	}
	if len(limited.failures) != maxFailureEntries {
		t.Fatalf("failure limiter entries = %d, want %d", len(limited.failures), maxFailureEntries)
	}
	limited.recordFailure("expiring")
	limitedNow = limitedNow.Add(failureWindow + time.Second)
	limited.recordFailure("current")
	if _, ok := limited.failures["expiring"]; ok {
		t.Fatal("expired failure entry was not purged globally")
	}
}

func TestSessionExpiresAndTLSCookieIsSecure(t *testing.T) {
	manager := New(testCredentials)
	now := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	tlsReq := httptest.NewRequest(http.MethodPost, "https://serverdesk.local/login", strings.NewReader(url.Values{
		"username": {Username}, "password": {fixedTestPassword},
	}.Encode()))
	tlsReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tlsReq.Header.Set("Origin", "https://serverdesk.local")
	tlsRec := httptest.NewRecorder()
	manager.Handler(http.NotFoundHandler()).ServeHTTP(tlsRec, tlsReq)
	cookies := tlsRec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure {
		t.Fatalf("TLS session cookie = %#v", cookies)
	}

	now = now.Add(sessionTTL)
	expiredReq := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	expiredReq.AddCookie(cookies[0])
	expired := httptest.NewRecorder()
	manager.Handler(http.NotFoundHandler()).ServeHTTP(expired, expiredReq)
	if expired.Code != http.StatusUnauthorized {
		t.Fatalf("expired session = %d, want 401", expired.Code)
	}

	proxyManager := New(testCredentials)
	proxyReq := httptest.NewRequest(http.MethodPost, "http://serverdesk.local/login", strings.NewReader(url.Values{
		"username": {Username}, "password": {fixedTestPassword},
	}.Encode()))
	proxyReq.RemoteAddr = "127.0.0.1:23456"
	proxyReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	proxyReq.Header.Set("Origin", "https://serverdesk.local")
	proxyReq.Header.Set("X-Forwarded-Proto", "https")
	proxyReq.Header.Set("X-Forwarded-For", "198.51.100.20")
	proxyRec := httptest.NewRecorder()
	proxyManager.Handler(http.NotFoundHandler()).ServeHTTP(proxyRec, proxyReq)
	proxyCookies := proxyRec.Result().Cookies()
	if len(proxyCookies) != 1 || !proxyCookies[0].Secure ||
		proxyRec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatalf("trusted TLS proxy response cookie=%#v hsts=%q",
			proxyCookies, proxyRec.Header().Get("Strict-Transport-Security"))
	}
}

func TestForwardedClientRateLimitKeys(t *testing.T) {
	proxied := func(forwarded string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://serverdesk.local/login", nil)
		req.RemoteAddr = "127.0.0.1:34567"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-For", "203.0.113.9, "+forwarded)
		return req
	}
	first := proxied("198.51.100.21")
	second := proxied("198.51.100.22")
	if clientKey(first) != "198.51.100.21" || clientKey(second) != "198.51.100.22" {
		t.Fatalf("proxied client keys = %q, %q", clientKey(first), clientKey(second))
	}
	manager := New(testCredentials)
	for i := 0; i < maxFailures; i++ {
		manager.recordFailure(clientKey(first))
	}
	if blocked, _ := manager.loginBlocked(clientKey(first)); !blocked {
		t.Fatal("first proxied client was not blocked")
	}
	if blocked, _ := manager.loginBlocked(clientKey(second)); blocked {
		t.Fatal("second proxied client inherited first client's block")
	}

	direct := httptest.NewRequest(http.MethodPost, "http://serverdesk.local/login", nil)
	direct.RemoteAddr = "192.0.2.44:45678"
	direct.Header.Set("X-Forwarded-Proto", "https")
	direct.Header.Set("X-Forwarded-For", "198.51.100.99")
	if clientKey(direct) != "192.0.2.44" || requestScheme(direct) != "http" {
		t.Fatalf("direct spoofed forwarding headers were trusted: key=%q scheme=%q",
			clientKey(direct), requestScheme(direct))
	}

	missingIP := httptest.NewRequest(http.MethodPost, "http://serverdesk.local/login", nil)
	missingIP.RemoteAddr = "127.0.0.1:45678"
	missingIP.Header.Set("X-Forwarded-Proto", "https")
	if requestScheme(missingIP) != "http" {
		t.Fatal("proxy scheme trusted without a validated forwarded client")
	}
}

func TestFileBackedManagerReloadsCredentials(t *testing.T) {
	path := t.TempDir() + "/auth.json"
	const originalPassword = "initial-password"
	const rotatedPassword = "rotated-password"
	if err := SetPassword(path, originalPassword); err != nil {
		t.Fatalf("SetPassword(initial): %v", err)
	}
	manager, err := NewFromFile(path)
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	handler := manager.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	initialLogin := login(handler, Username, originalPassword, "/")
	if initialLogin.Code != http.StatusSeeOther {
		t.Fatalf("initial login = %d", initialLogin.Code)
	}
	cookie := initialLogin.Result().Cookies()[0]
	unchanged := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	unchanged.AddCookie(cookie)
	unchangedRec := httptest.NewRecorder()
	handler.ServeHTTP(unchangedRec, unchanged)
	if unchangedRec.Code != http.StatusOK {
		t.Fatalf("session after unchanged credential file = %d, want 200", unchangedRec.Code)
	}

	manager.recordFailure("192.0.2.1")
	if err := SetPassword(path, rotatedPassword); err != nil {
		t.Fatalf("SetPassword(rotated): %v", err)
	}
	revoked := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
	revoked.AddCookie(cookie)
	revokedRec := httptest.NewRecorder()
	handler.ServeHTTP(revokedRec, revoked)
	if revokedRec.Code != http.StatusUnauthorized {
		t.Fatalf("session after credential rotation = %d, want 401", revokedRec.Code)
	}
	manager.mu.Lock()
	failures := len(manager.failures)
	manager.mu.Unlock()
	if failures != 0 {
		t.Fatalf("credential rotation retained %d login failure states", failures)
	}

	oldLogin := login(handler, Username, originalPassword, "/")
	if oldLogin.Code != http.StatusUnauthorized {
		t.Fatalf("old password login = %d, want 401", oldLogin.Code)
	}
	newLogin := login(handler, Username, rotatedPassword, "/")
	if newLogin.Code != http.StatusSeeOther {
		t.Fatalf("rotated password login = %d, want 303", newLogin.Code)
	}

	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatalf("write invalid credentials: %v", err)
	}
	unavailable := httptest.NewRecorder()
	handler.ServeHTTP(unavailable, httptest.NewRequest(http.MethodGet, "/api/devices", nil))
	if unavailable.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid credential file = %d, want 503", unavailable.Code)
	}
	if strings.Contains(unavailable.Body.String(), originalPassword) ||
		strings.Contains(unavailable.Body.String(), rotatedPassword) {
		t.Fatal("credential reload failure exposed a password")
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health during credential reload failure = %d, want 200", health.Code)
	}
}

func TestFileBackedManagerConcurrentHandling(t *testing.T) {
	path := t.TempDir() + "/auth.json"
	if err := SetPassword(path, fixedTestPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	manager, err := NewFromFile(path)
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	handler := manager.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	loggedIn := login(handler, Username, fixedTestPassword, "/")
	if loggedIn.Code != http.StatusSeeOther {
		t.Fatalf("login = %d", loggedIn.Code)
	}
	cookie := loggedIn.Result().Cookies()[0]

	const requests = 32
	results := make(chan int, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			req := httptest.NewRequest(http.MethodGet, "/api/devices", nil)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			results <- rec.Code
		}()
	}
	group.Wait()
	close(results)
	for code := range results {
		if code != http.StatusOK {
			t.Errorf("concurrent authenticated request = %d, want 200", code)
		}
	}
}

func login(handler http.Handler, username, password, next string) *httptest.ResponseRecorder {
	values := url.Values{"username": {username}, "password": {password}, "next": {next}}
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func serve(handler http.Handler, method, target string, body *strings.Reader, contentType string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, body)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
