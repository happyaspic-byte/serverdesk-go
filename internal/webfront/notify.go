package webfront

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var notifyDeniedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // shared carrier-grade NAT
	netip.MustParsePrefix("198.18.0.0/15"), // benchmark/internal test networks
}

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

// ValidateNotifyTarget applies the same SSRF allowlist used by the interactive
// relay to server-resident notification delivery.
func (s *Server) ValidateNotifyTarget(target string) error {
	u, err := url.Parse(strings.TrimSpace(target))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("need an http(s) webhook URL without userinfo")
	}
	host := strings.ToLower(u.Hostname())
	if !notifyHostAllowed(host, s.notifyHosts) {
		return fmt.Errorf("notify target host not allowed (register it via SERVERDESK_NOTIFY_HOSTS)")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("notify target must use HTTPS unless it is loopback")
		}
	}
	return nil
}

func explicitLoopbackNotifyHost(host string) bool {
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func prohibitedNotifyIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	for _, prefix := range notifyDeniedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// validateNotifyResolution rejects the entire answer set when any A/AAAA
// result enters a local or special-use range. Rejecting mixed answers prevents
// a resolver from steering a later retry to an unchecked private address.
func validateNotifyResolution(host string, addrs []net.IPAddr) error {
	if len(addrs) == 0 {
		return fmt.Errorf("webhook destination has no addresses")
	}
	loopbackTarget := explicitLoopbackNotifyHost(host)
	for _, addr := range addrs {
		if loopbackTarget {
			if addr.IP == nil || !addr.IP.IsLoopback() {
				return fmt.Errorf("loopback webhook name resolved outside loopback")
			}
			continue
		}
		if prohibitedNotifyIP(addr.IP) {
			return fmt.Errorf("webhook destination resolved to a prohibited address")
		}
	}
	return nil
}

// dialNotifyContext resolves once, validates every answer, then dials one of
// those exact addresses. The HTTP transport still performs TLS with the
// original request hostname, preserving certificate hostname verification.
func (s *Server) dialNotifyContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid webhook destination")
	}
	lookup := s.notifyLookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupIPAddr
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("webhook destination resolution failed")
	}
	if err := validateNotifyResolution(host, addrs); err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: notifyTimeout}
	var lastErr error
	for _, addr := range addrs {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.IP.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("no validated webhook address")
	}
	return nil, lastErr
}

// SendWebhook sends one bounded, non-redirecting webhook request. deliveryID is
// stable across retries and is exposed as Idempotency-Key for webhook gateways
// that support deduplication. Errors intentionally omit the target URL because
// webhook paths commonly contain bearer secrets.
func (s *Server) SendWebhook(ctx context.Context, target, message, deliveryID string) (int, error) {
	if err := s.ValidateNotifyTarget(target); err != nil {
		return 0, err
	}
	message = truncateRunes(strings.TrimSpace(message), 1900)
	if message == "" {
		return 0, fmt.Errorf("webhook message is empty")
	}
	payload := marshalJSON(map[string]any{"text": message, "content": message})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("webhook request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	if deliveryID != "" {
		req.Header.Set("Idempotency-Key", deliveryID)
	}
	resp, err := s.notifyClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("webhook request failed")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("webhook returned HTTP %d: %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return resp.StatusCode, nil
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
	if err := s.ValidateNotifyTarget(target); err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	code, err := s.SendWebhook(r.Context(), target, text, "")
	// Python urllib 은 2xx 가 아니면(NoRedirectHandler 라 3xx 도) HTTPError 를 던지므로
	// 여기서도 2xx 외는 릴리 실패다 — 정상 웹훅 응답은 200/204 뿐이라 끊어도 무해하다.
	if err != nil {
		writeJSONError(w, http.StatusBadGateway,
			"webhook relay failed: "+err.Error())
		return
	}
	// 응답 모양은 Python 과 같게 유지한다({"ok":..., "status":...}) — 프런트가 이 필드를 읽는다.
	writeJSON(w, http.StatusOK, map[string]any{"ok": 200 <= code && code < 300, "status": code})
}
