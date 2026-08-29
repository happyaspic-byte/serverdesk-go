package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"serverdesk/internal/config"
	"serverdesk/internal/poller"
)

func newHealthTestServer(t *testing.T, events *poller.EventLog) *Server {
	t.Helper()
	cache := poller.NewFleetCache()
	cache.Update(nil)
	cfg := &config.Config{CacheRefresh: 5}
	if events == nil {
		events = poller.NewEventLog(filepath.Join(t.TempDir(), "events.jsonl"), 20)
	}
	return New(cache, nil, cfg, nil, events, nil, nil, nil, nil, map[string]map[string]string{})
}

func healthRequest(t *testing.T, srv *Server, method, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var body map[string]any
	if method != http.MethodHead {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v (%s)", path, err, rec.Body.String())
		}
	}
	return rec, body
}

func TestPublicHealthIsMinimalAndSupportsHead(t *testing.T) {
	srv := newHealthTestServer(t, nil)
	rec, body := healthRequest(t, srv, http.MethodGet, "/api/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET health = %d", rec.Code)
	}
	if len(body) != 3 || body["status"] != "ok" || body["version"] == nil || body["uptime_secs"] == nil {
		t.Fatalf("public health = %#v", body)
	}
	for _, sensitive := range []string{"clusters", "cache_age_secs", "cache", "event_store", "edge_collector"} {
		if _, ok := body[sensitive]; ok {
			t.Errorf("public health exposed %q: %#v", sensitive, body[sensitive])
		}
	}

	head, _ := healthRequest(t, srv, http.MethodHead, "/api/health")
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") == "" {
		t.Fatalf("HEAD health = code %d body %d length %q", head.Code, head.Body.Len(), head.Header().Get("Content-Length"))
	}
}

func TestDetailedHealthIncludesCollectorPersistence(t *testing.T) {
	srv := newHealthTestServer(t, nil)
	rec, body := healthRequest(t, srv, http.MethodGet, "/api/admin/health")
	if rec.Code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("detailed health = %d %#v", rec.Code, body)
	}
	store, ok := body["event_store"].(map[string]any)
	if !ok || store["enabled"] != true || store["healthy"] != true || store["last_error"] != nil {
		t.Fatalf("event store = %#v", body["event_store"])
	}
	cache, ok := body["cache"].(map[string]any)
	if !ok || cache["status"] != "ok" {
		t.Fatalf("cache = %#v", body["cache"])
	}
	edge, ok := body["edge_collector"].(map[string]any)
	if !ok || edge["status"] != "disabled" || edge["configured"] != float64(0) {
		t.Fatalf("edge collector = %#v", body["edge_collector"])
	}
}

func TestEventPersistenceFailureDegradesOnlyDetailedOutput(t *testing.T) {
	root := t.TempDir()
	badPath := filepath.Join(root, "missing", "events.jsonl")
	events := poller.NewEventLog(badPath, 20)
	events.Add("internal-host", "label", "state", "critical", "offline")
	srv := newHealthTestServer(t, events)

	_, detailed := healthRequest(t, srv, http.MethodGet, "/api/admin/health")
	if detailed["status"] != "degraded" {
		t.Fatalf("event persistence failure status = %#v", detailed)
	}
	store := detailed["event_store"].(map[string]any)
	if store["healthy"] != false || store["last_error"] == nil {
		t.Fatalf("failed event store = %#v", store)
	}

	_, public := healthRequest(t, srv, http.MethodGet, "/api/health")
	if len(public) != 3 || public["status"] != "degraded" {
		t.Fatalf("public degraded health = %#v", public)
	}
}

func TestDetailedHealthDetectsStaleCacheAndMissingEdgeSnapshot(t *testing.T) {
	srv := newHealthTestServer(t, nil)
	srv.Cfg.EdgeDevices = []config.EdgeDevice{{Key: "edge-1"}}
	srv.StartedAt = time.Now().Add(-3 * time.Minute)

	detail := srv.health(map[string]any{"clusters": []any{}}, nowFloat()-60)
	if detail["status"] != "degraded" {
		t.Fatalf("stale collector status = %#v", detail)
	}
	cache := detail["cache"].(map[string]any)
	if cache["status"] != "degraded" {
		t.Fatalf("stale cache = %#v", cache)
	}
	edge := detail["edge_collector"].(map[string]any)
	if edge["status"] != "degraded" || edge["configured"] != 1 || edge["observed"] != 0 {
		t.Fatalf("missing edge snapshot = %#v", edge)
	}
}

func TestDetailedHealthMasksAuditForwarderError(t *testing.T) {
	secret := "siem-token-abcdef"
	config.RegisterSecret(secret)
	events := poller.NewEventLog(filepath.Join(t.TempDir(), "events.jsonl"), 20)
	events.RegisterAuditSink(healthErrorAuditSink{err: "webhook failed for " + secret})
	events.StartAuditForwarder(t.Context(), 8)
	t.Cleanup(events.StopAuditForwarder)
	events.Add("node0", "everRun", "alert", "critical", "offline")
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := events.Status()
		fwd, _ := status["forwarder"].(map[string]any)
		if n, ok := fwd["errors"].(int64); ok && n > 0 {
			break
		}
		if !time.Now().Before(deadline) {
			t.Fatal("audit forwarder did not record an error")
		}
		time.Sleep(10 * time.Millisecond)
	}
	srv := newHealthTestServer(t, events)
	_, body := healthRequest(t, srv, http.MethodGet, "/api/admin/health")
	if body["status"] != "degraded" {
		t.Fatalf("audit forwarder failure status = %#v", body["status"])
	}
	store, _ := body["event_store"].(map[string]any)
	fwd, _ := store["forwarder"].(map[string]any)
	if fwd["healthy"] != false {
		t.Fatalf("audit forwarder health = %#v", fwd["healthy"])
	}
	last, _ := fwd["last_error"].(string)
	if last == "" {
		t.Fatalf("expected masked forwarder last_error, got %#v", store)
	}
	if strings.Contains(last, secret) {
		t.Fatalf("forwarder last_error leaked secret: %q", last)
	}
	if !strings.Contains(last, "***") {
		t.Fatalf("forwarder last_error was not masked: %q", last)
	}
}

type healthErrorAuditSink struct{ err string }

func (s healthErrorAuditSink) Send(ctx context.Context, ev poller.AuditEvent) error {
	return errors.New(s.err)
}

func (s healthErrorAuditSink) Close() error { return nil }

func TestEdgeCollectorCompleteButStaleDegrades(t *testing.T) {
	fresh, severity := edgeCollectorHealth(3*time.Minute, poller.EdgeCollectorStatus{
		Configured: 1, Observed: 1, LastRoundAt: time.Now().Add(-10 * time.Second),
	})
	if fresh["status"] != "ok" || severity != "" {
		t.Fatalf("fresh edge collector = %#v severity=%q", fresh, severity)
	}

	stale, severity := edgeCollectorHealth(3*time.Minute, poller.EdgeCollectorStatus{
		Configured: 1, Observed: 1, LastRoundAt: time.Now().Add(-3 * time.Minute),
	})
	if stale["status"] != "degraded" || severity != "degraded" || stale["age_secs"] == nil {
		t.Fatalf("stale edge collector = %#v severity=%q", stale, severity)
	}
}
