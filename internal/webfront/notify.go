package webfront

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultNotifyHosts 는 /notify 릴리의 대상 호스트 허용 목록 기본값이다.
// /notify 는 LAN 의 누구든 {url, text} 만 보내면 서버가 대신 POST 해 주는 브리지라,
// 호스트를 제한하지 않으면 루프백 전용 폴러(:9890)나 사내 임의 서비스로의 blind POST
// (SSRF)가 가능하다. 기본값은 기존 사용 경로(Slack/Discord 웹훅)가 무설정으로 계속
// 동작하도록 둔 것이고, 사내 게이트웨이 등 다른 웹훅 호스트는 Options.NotifyHosts 나
// SERVERDESK_NOTIFY_HOSTS 환경변수(쉼표 구분)로 추가한다. 서브도메인은 허용 호스트에
// 포함된다.
var defaultNotifyHosts = []string{"hooks.slack.com", "discord.com", "discordapp.com"}

// notifyTimeout — 웹훅 릴리는 5초 안에 끝낸다(Python _NOTIFY_OPENER.open timeout=5).
const notifyTimeout = 5 * time.Second

// splitHosts 는 쉼표 구분 호스트 목록 문자열을 소문자·trim 정규화해 나눈다.
func splitHosts(csv string) []string {
	var out []string
	for _, h := range strings.Split(csv, ",") {
		if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// notifyHostAllowed 는 host 가 허용 목록 안이면 true. 서브도메인도 허용 호스트에 포함된다.
func notifyHostAllowed(host string, allowed []string) bool {
	for _, h := range allowed {
		if host == h || strings.HasSuffix(host, "."+h) {
			return true
		}
	}
	return false
}

// handleNotify 는 웹훅 중계다: {url, text} → 대상 URL 로 POST. Slack 은 text, Discord 는
// content 를 읽으므로 둘 다 싣는다(서로 모르는 필드는 무시). 서버가 쏘므로 브라우저
// CORS 와 무관하다. URL 은 http/https 만 — file:// 같은 스킴 오용을 막는다.
// 대상 호스트는 defaultNotifyHosts + SERVERDESK_NOTIFY_HOSTS + Options.NotifyHosts 허용
// 목록 안이어야 한다. 상위 로그인 미들웨어 인증과 이 제한을 함께 적용해 SSRF를 막는다.
func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if !CheckSameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "cross-origin notify rejected")
		return
	}
	raw, ok := readCappedBody(w, r, maxNotifyBody, "notify body too large")
	if !ok {
		return
	}
	var body any
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		trimmed = "{}"
	}
	if err := json.Unmarshal([]byte(trimmed), &body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	obj, isObj := body.(map[string]any)
	if !isObj {
		writeJSONError(w, http.StatusBadRequest, "notify body must be an object")
		return
	}
	target := strings.TrimSpace(pyStr(obj["url"]))
	text := truncateRunes(pyStr(obj["text"]), 1900) // Discord 2000자 제한의 안전 마진
	lower := strings.ToLower(target)
	if (!strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://")) || text == "" {
		writeJSONError(w, http.StatusBadRequest, "need http(s) url and text")
		return
	}
	host := ""
	if u, err := url.Parse(target); err == nil {
		host = strings.ToLower(u.Hostname())
	}
	if host == "" || !notifyHostAllowed(host, s.notifyHosts) {
		writeJSONError(w, http.StatusForbidden,
			"notify target host not allowed (register it via --notify-hosts)")
		return
	}
	payload := marshalJSON(map[string]any{"text": text, "content": text})
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "webhook relay failed: "+err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.notifyClient.Do(req)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "webhook relay failed: "+err.Error())
		return
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	code := resp.StatusCode
	// Python urllib 은 2xx 가 아니면(NoRedirectHandler 라 3xx 도) HTTPError 를 던지므로
	// 여기서도 2xx 외는 릴리 실패다 — 정상 웹훅 응답은 200/204 뿐이라 끊어도 무해하다.
	if code < 200 || code >= 300 {
		writeJSONError(w, http.StatusBadGateway,
			fmt.Sprintf("webhook relay failed: HTTP Error %d: %s", code, http.StatusText(code)))
		return
	}
	// 응답 모양은 Python 과 같게 유지한다({"ok":..., "status":...}) — 프런트가 이 필드를 읽는다.
	writeJSON(w, http.StatusOK, map[string]any{"ok": 200 <= code && code < 300, "status": code})
}
