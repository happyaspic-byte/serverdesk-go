package webfront

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// putJSON 은 JSON 바디 PUT 을 본낸다.
func putJSON(s *Server, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	return do(s, "PUT", path, strings.NewReader(body), hdr)
}

// getJSON 은 GET 하고 응답 바디를 map 으로 파싱한다.
func getJSON(t *testing.T, s *Server, path string) map[string]any {
	t.Helper()
	rec := do(s, "GET", path, nil, nil)
	if rec.Code != 200 {
		t.Fatalf("GET %s = %d", path, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("GET %s Content-Type = %q", path, ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", path, cc)
	}
	// Python 의 end_headers 와 같이 상태 JSON 응답에도 보안 헤더가 붙는다.
	if rec.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("GET %s missing CSP header", path)
	}
	var obj map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &obj); err != nil {
		t.Fatalf("GET %s invalid JSON: %v", path, err)
	}
	return obj
}

func TestAckDeltaMerge(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, Options{StateDir: dir})

	// 델타 병합: A 의 확인을 B 의 이후 쓰기가 지우지 않는다(두 콘솔 시나리오).
	rec := putJSON(s, "/ack", `{"set":{"a1":"2026-01-01T00:00:00Z"}}`, nil)
	assertOK(t, rec, 1, true)
	rec = putJSON(s, "/ack", `{"set":{"a2":"2026-01-02T00:00:00Z"}}`, nil)
	assertOK(t, rec, 2, true)

	got := getJSON(t, s, "/ack")
	if got["a1"] != "2026-01-01T00:00:00Z" || got["a2"] != "2026-01-02T00:00:00Z" {
		t.Fatalf("ack state = %v", got)
	}

	// del 델타.
	rec = putJSON(s, "/ack", `{"del":["a1"]}`, nil)
	assertOK(t, rec, 1, true)
	got = getJSON(t, s, "/ack")
	if _, has := got["a1"]; has || got["a2"] == nil {
		t.Fatalf("after del ack state = %v", got)
	}

	// 파일은 tmp+rename 으로 남고 .tmp 잔여물은 없어야 한다.
	if _, err := os.Stat(filepath.Join(dir, "ack-state.json")); err != nil {
		t.Fatalf("ack-state.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ack-state.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("tmp file left behind")
	}

	// 명시적 전체 교체.
	rec = putJSON(s, "/ack", `{"replace":{"b1":"2026-02-01T00:00:00Z"}}`, nil)
	assertOK(t, rec, 1, false)
	got = getJSON(t, s, "/ack")
	if len(got) != 1 || got["b1"] == nil {
		t.Fatalf("after replace ack state = %v", got)
	}

	// 구형 호환: 맵 통째로 본낸면 전체 교체.
	rec = putJSON(s, "/ack", `{"c1":"2026-03-01T00:00:00Z"}`, nil)
	assertOK(t, rec, 1, false)
	got = getJSON(t, s, "/ack")
	if len(got) != 1 || got["c1"] == nil {
		t.Fatalf("legacy replace ack state = %v", got)
	}

	// 트레일링 슬래시도 같은 엔드포인트다(Python rstrip("/")).
	rec = putJSON(s, "/ack/", `{"set":{"d1":"2026-04-01T00:00:00Z"}}`, nil)
	assertOK(t, rec, 2, true)
}

func assertOK(t *testing.T, rec *httptest.ResponseRecorder, wantCount int, wantMerged bool) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("PUT = %d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Count  int  `json:"count"`
		Merged bool `json:"merged"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if !resp.OK || resp.Count != wantCount || resp.Merged != wantMerged {
		t.Fatalf("resp = %+v, want ok=true count=%d merged=%v", resp, wantCount, wantMerged)
	}
}

func TestAckBadRequests(t *testing.T) {
	s := newTestServer(t, Options{})
	cases := []struct {
		body, wantMsg string
	}{
		{"", "empty ack body: send set/del delta or explicit replace"},
		{"{}", "empty ack body: send set/del delta or explicit replace"},
		{"[1,2]", "ack state must be an object"},
		{"{bad", "invalid JSON"},
		{`{"replace":"x"}`, "ack replace must be an object"},
		{`{"replace":null}`, "ack replace must be an object"},
	}
	for _, c := range cases {
		rec := putJSON(s, "/ack", c.body, nil)
		if rec.Code != 400 {
			t.Errorf("PUT /ack %q = %d, want 400", c.body, rec.Code)
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Errorf("PUT /ack %q non-JSON error body", c.body)
			continue
		}
		if resp["error"] != c.wantMsg {
			t.Errorf("PUT /ack %q error = %v, want %q", c.body, resp["error"], c.wantMsg)
		}
	}
}

func TestAckKeyCapKeepsNewest(t *testing.T) {
	s := newTestServer(t, Options{})
	// 5001개(상한+1)를 넣으면 ts 가 가장 오래된 것이 빠지고 최근 5000개만 남는다.
	var b strings.Builder
	b.WriteString(`{"replace":{`)
	for i := 0; i < 5001; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `"k%04d":"2026-01-01T00:%02d:%02dZ"`, i, i/60, i%60)
	}
	b.WriteString("}}")
	rec := putJSON(s, "/ack", b.String(), nil)
	assertOK(t, rec, 5000, false)
	got := getJSON(t, s, "/ack")
	if len(got) != 5000 {
		t.Fatalf("len = %d, want 5000", len(got))
	}
	if _, has := got["k0000"]; has {
		t.Errorf("oldest key k0000 should have been evicted")
	}
	if _, has := got["k5000"]; !has {
		t.Errorf("newest key k5000 should have been kept")
	}
}

func TestMaintShape(t *testing.T) {
	s := newTestServer(t, Options{})
	rec := putJSON(s, "/maint",
		`{"set":{"eq1":{"until":"2026-12-31T00:00:00Z","note":"점검 중","by":"kim","ts":"2026-08-01T00:00:00Z"},`+
			`"bad":{"note":"no until"},"junk":"not-a-dict"}}`, nil)
	assertOK(t, rec, 1, true)
	got := getJSON(t, s, "/maint")
	m, ok := got["eq1"].(map[string]any)
	if !ok {
		t.Fatalf("maint eq1 missing: %v", got)
	}
	if m["note"] != "점검 중" || m["by"] != "kim" || m["until"] != "2026-12-31T00:00:00Z" {
		t.Fatalf("maint eq1 = %v", m)
	}
	if _, has := got["bad"]; has {
		t.Errorf("entry without until must be dropped")
	}
	rec = putJSON(s, "/maint", `{"del":["eq1"]}`, nil)
	assertOK(t, rec, 0, true)

	// 빈 바디 규약은 ack 와 같다.
	rec = putJSON(s, "/maint", `{}`, nil)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "empty maint body") {
		t.Errorf("empty maint = %d %s", rec.Code, rec.Body.String())
	}
}

func TestNotesShape(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, Options{StateDir: dir})
	rec := putJSON(s, "/notes",
		`{"set":{"n1":{"text":"<b>장비</b> 인수인계","ts":"2026-08-01T00:00:00Z","by":"kim"},`+
			`"blank":{"text":"   "}}}`, nil)
	assertOK(t, rec, 1, true)
	got := getJSON(t, s, "/notes")
	m, ok := got["n1"].(map[string]any)
	if !ok || m["text"] != "<b>장비</b> 인수인계" {
		t.Fatalf("notes = %v", got)
	}
	if _, has := got["blank"]; has {
		t.Errorf("blank note must be dropped")
	}
	// Python json.dumps(ensure_ascii=False) 와 같이 HTML 이스케이프 없이 저장된다.
	raw, err := os.ReadFile(filepath.Join(dir, "notes-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "<b>장비</b>") {
		t.Errorf("state file escaped non-ASCII/HTML: %s", raw)
	}
}

func TestEscalClaimLifecycle(t *testing.T) {
	dir := t.TempDir()
	s := newTestServer(t, Options{StateDir: dir})

	// add-if-absent: 첫 클레임만 added 에 든다 — 두 번째 콘솔은 웹훅을 쏘지 않는다.
	rec := putJSON(s, "/escal", `{"set":{"k1":"client-ts-ignored"}}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"added":["k1"]`) {
		t.Fatalf("first claim = %d %s", rec.Code, rec.Body.String())
	}
	rec = putJSON(s, "/escal", `{"set":{"k1":"x"}}`, nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"added":[]`) {
		t.Fatalf("second claim = %d %s", rec.Code, rec.Body.String())
	}

	// 값은 클라이언트 ISO 가 아니라 서버 시각 UTC 스탬프다.
	got := getJSON(t, s, "/escal")
	stamp, _ := got["k1"].(string)
	if stamp == "client-ts-ignored" || !strings.HasSuffix(stamp, "+00:00") {
		t.Fatalf("claim stamp = %q, want server UTC ISO", stamp)
	}

	// TTL 지난 클레임은 GET 에서 걸러지고, 다시 클레임할 수 있다(선점 영구화 방지).
	seed := `{"old":"2020-01-01T00:00:00+00:00"}`
	if err := os.WriteFile(filepath.Join(dir, "escal-state.json"), []byte(seed), 0644); err != nil {
		t.Fatal(err)
	}
	got = getJSON(t, s, "/escal")
	if _, has := got["old"]; has {
		t.Fatalf("expired claim must be filtered from GET: %v", got)
	}
	rec = putJSON(s, "/escal", `{"set":{"old":""}}`, nil)
	if !strings.Contains(rec.Body.String(), `"added":["old"]`) {
		t.Fatalf("re-claim after expiry = %s", rec.Body.String())
	}

	// 바디 규약.
	for _, bad := range []string{`{}`, `{"set":"x"}`, `{"set":null}`} {
		rec = putJSON(s, "/escal", bad, nil)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "escal body must be {set: {...}}") {
			t.Errorf("PUT /escal %s = %d %s", bad, rec.Code, rec.Body.String())
		}
	}
}

func TestBodyCapsAndChunked(t *testing.T) {
	s := newTestServer(t, Options{})
	cases := []struct {
		path string
		size int
	}{
		{"/ack", 256*1024 + 1},
		{"/maint", 128*1024 + 1},
		{"/notes", 128*1024 + 1},
		{"/escal", 128*1024 + 1},
		{"/notify", 64*1024 + 1},
	}
	for _, c := range cases {
		method := "PUT"
		if c.path == "/notify" {
			method = "POST"
		}
		rec := do(s, method, c.path, strings.NewReader(strings.Repeat("a", c.size)), nil)
		if rec.Code != 413 {
			t.Errorf("%s %s (%dB) = %d, want 413", method, c.path, c.size, rec.Code)
		}
	}

	// 청크 인코딩(Content-Length 없음)은 411 — 캡을 적용할 수 없으므로 명시 거부.
	req := httptest.NewRequest("PUT", "/ack", strings.NewReader(`{"set":{}}`))
	req.ContentLength = -1
	req.TransferEncoding = []string{"chunked"}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != 411 {
		t.Errorf("chunked PUT = %d, want 411", rec.Code)
	}
}

func TestOriginAndTokenGates(t *testing.T) {
	s := newTestServer(t, Options{Token: "sek"})

	put := func(hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("PUT", "/ack", strings.NewReader(`{"set":{"x":"2026-01-01T00:00:00Z"}}`))
		req.Host = "noc.local:6001"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	// 토큰 없음/틀림 → 403. 맞으면 통과.
	if rec := put(nil); rec.Code != 403 {
		t.Errorf("no token = %d, want 403", rec.Code)
	}
	if rec := put(map[string]string{"X-Serverdesk-Token": "wrong"}); rec.Code != 403 {
		t.Errorf("wrong token = %d, want 403", rec.Code)
	}
	if rec := put(map[string]string{"X-Serverdesk-Token": "sek"}); rec.Code != 200 {
		t.Errorf("right token = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// 교차 출처는 토큰이 맞아도 403.
	cross := map[string]string{"X-Serverdesk-Token": "sek", "Origin": "http://evil.example"}
	if rec := put(cross); rec.Code != 403 {
		t.Errorf("cross-origin = %d, want 403", rec.Code)
	}
	// 같은 출처(대소문자 무관)는 통과.
	same := map[string]string{"X-Serverdesk-Token": "sek", "Origin": "http://NOC.local:6001"}
	if rec := put(same); rec.Code != 200 {
		t.Errorf("same-origin = %d, want 200", rec.Code)
	}
	// 'Origin: null'·'file://' 는 fail-close.
	if rec := put(map[string]string{"X-Serverdesk-Token": "sek", "Origin": "null"}); rec.Code != 403 {
		t.Errorf("Origin null = %d, want 403", rec.Code)
	}
	// Referer 도 같은 규칙.
	if rec := put(map[string]string{"X-Serverdesk-Token": "sek", "Referer": "http://evil.example/x"}); rec.Code != 403 {
		t.Errorf("cross referer = %d, want 403", rec.Code)
	}
	if rec := put(map[string]string{"X-Serverdesk-Token": "sek", "Referer": "http://noc.local:6001/ui/"}); rec.Code != 200 {
		t.Errorf("same referer = %d, want 200", rec.Code)
	}

	// GET 은 토큰/Origin 게이트 대상이 아니다.
	if rec := do(s, "GET", "/ack", nil, nil); rec.Code != 200 {
		t.Errorf("GET /ack = %d, want 200", rec.Code)
	}
}

func TestTokenFromEnv(t *testing.T) {
	t.Setenv("SERVERDESK_TOKEN", "envtok")
	s := newTestServer(t, Options{}) // Options.Token 비어 있으면 env 를 읽는다.
	if rec := putJSON(s, "/ack", `{"set":{"x":"2026-01-01T00:00:00Z"}}`, nil); rec.Code != 403 {
		t.Errorf("no token with env set = %d, want 403", rec.Code)
	}
	rec := putJSON(s, "/ack", `{"set":{"x":"2026-01-01T00:00:00Z"}}`,
		map[string]string{"X-Serverdesk-Token": "envtok"})
	if rec.Code != 200 {
		t.Errorf("env token = %d, want 200", rec.Code)
	}
}
