package webfront

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// notifyTestServer 는 127.0.0.1(httptest) 대상을 허용한 서버를 만든다.
func notifyTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServer(t, Options{NotifyHosts: []string{"127.0.0.1"}})
}

func TestNotifyRelay(t *testing.T) {
	got := make(chan map[string]any, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("relay Content-Type = %q", ct)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("relay body decode: %v", err)
		}
		got <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	s := notifyTestServer(t)
	rec := do(s, "POST", "/notify",
		strings.NewReader(`{"url":"`+hook.URL+`","text":"경보 발생"}`), nil)
	if rec.Code != 200 {
		t.Fatalf("POST /notify = %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Status int  `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v", err)
	}
	if !resp.OK || resp.Status != 200 {
		t.Fatalf("resp = %+v, want ok=true status=200", resp)
	}
	// Slack 은 text, Discord 는 content 를 읽으므로 둘 다 실려 있어야 한다.
	payload := <-got
	if payload["text"] != "경보 발생" || payload["content"] != "경보 발생" {
		t.Fatalf("relay payload = %v", payload)
	}
}

func TestNotifyTextTruncation(t *testing.T) {
	got := make(chan map[string]any, 1)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		got <- payload
		w.WriteHeader(http.StatusOK)
	}))
	defer hook.Close()

	s := notifyTestServer(t)
	long := strings.Repeat("가", 2000)
	body, _ := json.Marshal(map[string]string{"url": hook.URL, "text": long})
	rec := do(s, "POST", "/notify", strings.NewReader(string(body)), nil)
	if rec.Code != 200 {
		t.Fatalf("POST /notify = %d", rec.Code)
	}
	payload := <-got
	if n := len([]rune(payload["text"].(string))); n != 1900 {
		t.Fatalf("relayed text = %d runes, want 1900 (문자 단위 절단)", n)
	}
}

func TestNotifySSRFGuards(t *testing.T) {
	s := notifyTestServer(t)
	cases := []struct {
		body string
		want int
	}{
		{`{"url":"http://169.254.169.254/latest","text":"x"}`, 403}, // 허용 목록 외 호스트
		{`{"url":"http://evil.example/hook","text":"x"}`, 403},
		{`{"url":"ftp://hooks.slack.com/x","text":"x"}`, 400}, // http(s) 만
		{`{"url":"hooks.slack.com/x","text":"x"}`, 400},       // 스킴 없음
		{`{"url":"http://hooks.slack.com/x","text":""}`, 400}, // text 필수
		{`{"url":"","text":"x"}`, 400},
		{`[1,2]`, 400}, // 객체 아님
		{`{bad`, 400},  // 깨진 JSON
	}
	for _, c := range cases {
		rec := do(s, "POST", "/notify", strings.NewReader(c.body), nil)
		if rec.Code != c.want {
			t.Errorf("POST /notify %s = %d, want %d", c.body, rec.Code, c.want)
		}
	}
	// 기본 허용 호스트의 서브도메인 규칙은 유닛으로 검증한다(실제 외부 연결은 하지 않는다).
	for _, tc := range []struct {
		host string
		want bool
	}{
		{"hooks.slack.com", true},
		{"x.hooks.slack.com", true},    // 서브도메인 포함
		{"evilhooks.slack.com", false}, // 접미 사칭은 안 된다
		{"slack.com", false},
		{"discord.com", true},
		{"cdn.discordapp.com", true},
	} {
		if got := notifyHostAllowed(tc.host, defaultNotifyHosts); got != tc.want {
			t.Errorf("notifyHostAllowed(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestNotifyRedirectNotFollowed(t *testing.T) {
	var targetHit atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()

	s := notifyTestServer(t)
	rec := do(s, "POST", "/notify",
		strings.NewReader(`{"url":"`+redir.URL+`","text":"x"}`), nil)
	// 리다이렉트를 추종하지 않는다 — 허용 호스트가 302 로 사설망을 가리키는 우회 차단.
	// Python urllib 은 3xx 를 HTTPError 로 던지므로 릴리 실패(502)가 된다.
	if rec.Code != 502 {
		t.Errorf("redirect relay = %d, want 502", rec.Code)
	}
	if targetHit.Load() {
		t.Errorf("redirect target was hit — redirect must not be followed")
	}
}

func TestNotifyWebhookError(t *testing.T) {
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer hook.Close()
	s := notifyTestServer(t)
	rec := do(s, "POST", "/notify", strings.NewReader(`{"url":"`+hook.URL+`","text":"x"}`), nil)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "webhook relay failed") {
		t.Errorf("webhook 500 = %d %s, want 502 relay failed", rec.Code, rec.Body.String())
	}
}

func TestNotifyTokenGate(t *testing.T) {
	s := newTestServer(t, Options{Token: "sek", NotifyHosts: []string{"127.0.0.1"}})
	rec := do(s, "POST", "/notify/test",
		strings.NewReader(`{"url":"http://127.0.0.1:1/x","text":"x"}`), nil)
	if rec.Code != 403 {
		t.Errorf("notify without token = %d, want 403", rec.Code)
	}
}
