// Package webauth provides administrator login and server-side sessions.
package webauth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// Username is the only interactive account supported by serverdesk.
	Username = "admin"

	cookieName         = "serverdesk_session"
	sessionTTL         = 8 * time.Hour
	maxSessions        = 64
	maxLoginBody       = 8 << 10
	failureWindow      = 10 * time.Minute
	maxFailures        = 5
	maxFailureEntries  = 1024
	loginBlockDuration = 15 * time.Minute
)

type loginFailure struct {
	count        int
	windowStart  time.Time
	blockedUntil time.Time
}

// Manager owns short-lived in-memory sessions. A restart intentionally signs out every browser.
type Manager struct {
	credentials     Credentials
	credentialsPath string
	mu              sync.Mutex
	reloadMu        sync.Mutex
	sessions        map[[sha256.Size]byte]time.Time
	failures        map[string]loginFailure
	now             func() time.Time
	random          io.Reader
}

// New creates an authentication manager for the supplied administrator credentials.
func New(credentials Credentials) *Manager {
	if len(credentials.salt) != credentialSaltBytes || len(credentials.digest) != credentialDigestBytes {
		panic("webauth: invalid administrator credentials")
	}
	return &Manager{
		credentials: Credentials{
			salt:   append([]byte(nil), credentials.salt...),
			digest: append([]byte(nil), credentials.digest...),
		},
		sessions: make(map[[sha256.Size]byte]time.Time),
		failures: make(map[string]loginFailure),
		now:      time.Now,
		random:   rand.Reader,
	}
}

// NewFromFile creates an authentication manager backed by path. The credential
// store is reloaded before each authentication-protected request.
func NewFromFile(path string) (*Manager, error) {
	credentials, err := LoadCredentials(path)
	if err != nil {
		return nil, err
	}
	manager := New(credentials)
	manager.credentialsPath = path
	return manager, nil
}

// Handler protects the console and every data/mutation endpoint. Only the login routes and
// GET/HEAD /api/health remain public so installers and service supervisors can verify startup.
func (m *Manager) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w, r)
		path := strings.TrimRight(r.URL.Path, "/")
		if path == "" {
			path = "/"
		}

		// CORS Preflight(OPTIONS) 요청과 헬스체크는 인증 검사 전에 통과시켜 정당한 브라우저 API 호출 및 시동 확인이 차단되지 않게 한다.
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/health" && r.URL.EscapedPath() == "/api/health" &&
			(r.Method == http.MethodGet || r.Method == http.MethodHead) {
			next.ServeHTTP(w, r)
			return
		}
		if m.credentialsPath != "" {
			if err := m.reloadCredentials(); err != nil {
				m.authenticationUnavailable(w)
				return
			}
		}
		switch path {
		case "/login":
			m.handleLogin(w, r)
			return
		case "/logout":
			m.handleLogout(w, r)
			return
		}

		if !m.authenticated(r) {
			m.requireLogin(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Manager) handleLogin(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if m.authenticated(r) {
			http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
			return
		}
		m.writeLoginPage(w, r, http.StatusOK, "", safeNext(r.URL.Query().Get("next")))
	case http.MethodPost:
		if !sameOrigin(r) {
			http.Error(w, "cross-origin login rejected", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxLoginBody)
		if err := r.ParseForm(); err != nil {
			m.writeLoginPage(w, r, http.StatusBadRequest, "로그인 요청을 읽을 수 없습니다.", "/")
			return
		}
		next := safeNext(r.PostForm.Get("next"))
		client := clientKey(r)
		if blocked, wait := m.loginBlocked(client); blocked {
			w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second).Seconds())))
			m.writeLoginPage(w, r, http.StatusTooManyRequests, "로그인 시도가 너무 많습니다. 잠시 후 다시 시도하세요.", next)
			return
		}
		if !m.validCredentials(r.PostForm.Get("username"), r.PostForm.Get("password")) {
			m.recordFailure(client)
			m.writeLoginPage(w, r, http.StatusUnauthorized, "아이디 또는 비밀번호가 올바르지 않습니다.", next)
			return
		}
		m.clearFailures(client)
		if err := m.startSession(w, r); err != nil {
			http.Error(w, "세션을 만들 수 없습니다.", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, next, http.StatusSeeOther)
	default:
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (m *Manager) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if !sameOrigin(r) {
		http.Error(w, "cross-origin logout rejected", http.StatusForbidden)
		return
	}
	m.endSession(r)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(1, 0),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   requestScheme(r) == "https",
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (m *Manager) requireLogin(w http.ResponseWriter, r *http.Request) {
	loginURL := "/login?next=" + url.QueryEscape(safeNext(r.URL.RequestURI()))
	w.Header().Set("Location", loginURL)
	if !isDataEndpoint(r.URL.Path) && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		http.Redirect(w, r, loginURL, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": "authentication required"})
}

func isDataEndpoint(path string) bool {
	path = strings.TrimRight(path, "/")
	if path == "/api" || strings.HasPrefix(path, "/api/") {
		return true
	}
	switch path {
	case "/ack", "/maint", "/notes", "/escal", "/notify":
		return true
	default:
		return false
	}
}

func (m *Manager) startSession(w http.ResponseWriter, r *http.Request) error {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(m.random, raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	key := sha256.Sum256([]byte(token))
	now := m.now()
	expires := now.Add(sessionTTL)

	m.mu.Lock()
	m.purgeSessionsLocked(now)
	if len(m.sessions) >= maxSessions {
		m.removeOldestSessionLocked()
	}
	m.sessions[key] = expires
	m.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(sessionTTL.Seconds()),
		HttpOnly: true,
		Secure:   requestScheme(r) == "https",
		SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (m *Manager) authenticated(r *http.Request) bool {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32 {
		return false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	now := m.now()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeSessionsLocked(now)
	expires, ok := m.sessions[key]
	return ok && now.Before(expires)
}

func (m *Manager) endSession(r *http.Request) {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return
	}
	key := sha256.Sum256([]byte(cookie.Value))
	m.mu.Lock()
	delete(m.sessions, key)
	m.mu.Unlock()
}

func (m *Manager) purgeSessionsLocked(now time.Time) {
	for key, expires := range m.sessions {
		if !now.Before(expires) {
			delete(m.sessions, key)
		}
	}
}

func (m *Manager) removeOldestSessionLocked() {
	var oldestKey [sha256.Size]byte
	var oldest time.Time
	for key, expires := range m.sessions {
		if oldest.IsZero() || expires.Before(oldest) {
			oldestKey, oldest = key, expires
		}
	}
	if !oldest.IsZero() {
		delete(m.sessions, oldestKey)
	}
}

func (m *Manager) loginBlocked(client string) (bool, time.Duration) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeFailuresLocked(now)
	state, ok := m.failures[client]
	if !ok || !now.Before(state.blockedUntil) {
		return false, 0
	}
	return true, state.blockedUntil.Sub(now)
}

func (m *Manager) recordFailure(client string) {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeFailuresLocked(now)
	state, exists := m.failures[client]
	if !exists && len(m.failures) >= maxFailureEntries {
		m.removeOldestFailureLocked()
	}
	if state.windowStart.IsZero() || now.Sub(state.windowStart) > failureWindow {
		state = loginFailure{windowStart: now}
	}
	state.count++
	if state.count >= maxFailures {
		state.blockedUntil = now.Add(loginBlockDuration)
	}
	m.failures[client] = state
}

func (m *Manager) purgeFailuresLocked(now time.Time) {
	for client, state := range m.failures {
		expired := !state.blockedUntil.IsZero() && !now.Before(state.blockedUntil)
		expired = expired || (state.blockedUntil.IsZero() && now.Sub(state.windowStart) > failureWindow)
		if expired {
			delete(m.failures, client)
		}
	}
}

func (m *Manager) removeOldestFailureLocked() {
	var oldestClient string
	var oldestTime time.Time
	for client, state := range m.failures {
		at := state.windowStart
		if !state.blockedUntil.IsZero() {
			at = state.blockedUntil
		}
		if oldestTime.IsZero() || at.Before(oldestTime) || (at.Equal(oldestTime) && client < oldestClient) {
			oldestClient, oldestTime = client, at
		}
	}
	if oldestClient != "" {
		delete(m.failures, oldestClient)
	}
}

func (m *Manager) clearFailures(client string) {
	m.mu.Lock()
	delete(m.failures, client)
	m.mu.Unlock()
}

func (m *Manager) validCredentials(username, password string) bool {
	m.mu.Lock()
	salt := append([]byte(nil), m.credentials.salt...)
	digest := append([]byte(nil), m.credentials.digest...)
	m.mu.Unlock()

	derived, err := pbkdf2.Key(sha256.New, password, salt, credentialIterations, len(digest))
	if err != nil {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(Username))
	passwordOK := subtle.ConstantTimeCompare(derived, digest)
	return userOK&passwordOK == 1
}

func (m *Manager) reloadCredentials() error {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	credentials, err := LoadCredentials(m.credentialsPath)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if subtle.ConstantTimeCompare(m.credentials.salt, credentials.salt) == 1 &&
		subtle.ConstantTimeCompare(m.credentials.digest, credentials.digest) == 1 {
		return nil
	}
	m.credentials = Credentials{
		salt:   append([]byte(nil), credentials.salt...),
		digest: append([]byte(nil), credentials.digest...),
	}
	m.sessions = make(map[[sha256.Size]byte]time.Time)
	m.failures = make(map[string]loginFailure)
	return nil
}

func (m *Manager) authenticationUnavailable(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "authentication temporarily unavailable", http.StatusServiceUnavailable)
}

func clientKey(r *http.Request) string {
	if forwarded := forwardedClientIP(r); forwarded != "" {
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "unknown"
}

func sameOrigin(r *http.Request) bool {
	scheme := requestScheme(r)
	own, err := url.Parse(scheme + "://" + r.Host)
	if err != nil || own.Host == "" || own.User != nil {
		return false
	}
	ownAuthority := canonicalAuthority(own)
	found := false
	for _, name := range []string{"Origin", "Referer"} {
		value := r.Header.Get(name)
		if value == "" {
			continue
		}
		found = true
		u, err := url.Parse(value)
		if err != nil || u.User != nil || strings.ToLower(u.Scheme) != scheme ||
			canonicalAuthority(u) == "" || canonicalAuthority(u) != ownAuthority {
			return false
		}
		if name == "Origin" && (u.Path != "" || u.RawQuery != "" || u.Fragment != "") {
			return false
		}
	}
	return found
}

func canonicalAuthority(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return ""
		}
	}
	return net.JoinHostPort(host, port)
}

func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if forwardedClientIP(r) != "" {
		switch strings.ToLower(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto"))) {
		case "http":
			return "http"
		case "https":
			return "https"
		}
	}
	return "http"
}

func forwardedClientIP(r *http.Request) string {
	if !loopbackPeer(r.RemoteAddr) {
		return ""
	}
	values := r.Header.Values("X-Forwarded-For")
	if len(values) > 0 {
		parts := strings.Split(values[len(values)-1], ",")
		candidate := strings.TrimSpace(parts[len(parts)-1])
		if ip := net.ParseIP(candidate); ip != nil {
			return ip.String()
		}
		return ""
	}
	if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
		return ip.String()
	}
	return ""
}

func loopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func safeNext(next string) string {
	if next == "" || strings.Contains(next, "\\") || !strings.HasPrefix(next, "/") ||
		strings.HasPrefix(next, "//") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.IsAbs() || u.Opaque != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	escapedPath := strings.ToLower(u.EscapedPath())
	decodedPath, err := url.PathUnescape(u.EscapedPath())
	if err != nil || strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") ||
		!strings.HasPrefix(decodedPath, "/") || strings.HasPrefix(decodedPath, "//") ||
		strings.Contains(decodedPath, "\\") {
		return "/"
	}
	switch strings.TrimRight(decodedPath, "/") {
	case "/login", "/logout":
		return "/"
	}
	return u.String()
}

func setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if requestScheme(r) == "https" {
		h.Set("Strict-Transport-Security", "max-age=31536000")
	}
}
