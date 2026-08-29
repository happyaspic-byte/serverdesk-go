package poller

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockSink struct {
	mu       sync.Mutex
	received []AuditEvent
	closed   bool
}

func (m *mockSink) Send(ctx context.Context, ev AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.received = append(m.received, ev)
	return nil
}

func (m *mockSink) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func TestAuditSinkForwardingAndDrain(t *testing.T) {
	dir := t.TempDir()
	el := NewEventLog(filepath.Join(dir, "events.jsonl"), 10)
	defer el.StopAuditForwarder()

	sink := &mockSink{}
	el.RegisterAuditSink(sink)
	el.StartAuditForwarder(context.Background(), 100)

	el.Add("node0", "everRun", "alert", "critical", "Node disconnected")
	el.Add("node1", "everRun", "state", "info", "Node synchronized")

	// 비동기 전송 대기
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sink.mu.Lock()
		count := len(sink.received)
		sink.mu.Unlock()
		if count >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	sink.mu.Lock()
	if len(sink.received) != 2 {
		t.Fatalf("기대 이벤트 수 2, 실제 %d", len(sink.received))
	}
	if sink.received[0].Severity != "critical" || sink.received[0].Host != "node0" {
		t.Errorf("이벤트 0 필드 오류: %+v", sink.received[0])
	}
	sink.mu.Unlock()

	el.StopAuditForwarder()
	sink.mu.Lock()
	if !sink.closed {
		t.Errorf("StopAuditForwarder 호출 시 sink.Close()가 실행되어야 함")
	}
	sink.mu.Unlock()

	st := el.Status()
	fwd, ok := st["forwarder"].(map[string]any)
	if !ok || fwd["sent"].(int64) < 2 {
		t.Errorf("Status forwarder 정보 누락 또는 오작동: %+v", st)
	}
}

func TestStopAuditForwarderFlushesQueue(t *testing.T) {
	el := NewEventLog(filepath.Join(t.TempDir(), "events.jsonl"), 10)
	sink := &mockSink{}
	el.RegisterAuditSink(sink)
	el.StartAuditForwarder(context.Background(), 100)
	el.Add("node0", "everRun", "alert", "critical", "Node disconnected")
	el.Add("node1", "everRun", "state", "info", "Node synchronized")
	el.StopAuditForwarder()

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.received) != 2 {
		t.Fatalf("StopAuditForwarder flushed %d events, want 2", len(sink.received))
	}
	if !sink.closed {
		t.Error("StopAuditForwarder must close sinks after flushing")
	}
}

func TestSyslogSinkRFC5424Format(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()

	sink := NewSyslogSink("udp", pc.LocalAddr().String(), "serverdesk-test")
	defer sink.Close()

	ev := AuditEvent{
		Timestamp:   time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		Host:        "node0",
		Label:       "everRun",
		Kind:        "alert",
		Severity:    "critical",
		Description: "Disk mirror broke",
	}

	if err := sink.Send(context.Background(), ev); err != nil {
		t.Fatalf("Syslog 전송 실패: %v", err)
	}

	buf := make([]byte, 2048)
	_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatalf("Syslog 수신 대기 실패: %v", err)
	}

	msg := string(buf[:n])
	// <130>1 2026-08-27T10:00:00Z ... serverdesk-test ... alert - Disk mirror broke
	if !strings.HasPrefix(msg, "<130>1 2026-08-27T10:00:00Z") {
		t.Errorf("RFC 5424 PRI/Timestamp 헤더 불일치: %q", msg)
	}
	if !strings.Contains(msg, "serverdesk-test") || !strings.Contains(msg, "Disk mirror broke") {
		t.Errorf("Syslog 페이로드 불일치: %q", msg)
	}
}

func TestWebhookSinkJSONDelivery(t *testing.T) {
	var received AuditEvent
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("webhook body decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL, nil)
	defer sink.Close()

	ev := AuditEvent{
		Timestamp:   time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
		Host:        "node0",
		Label:       "everRun",
		Kind:        "alert",
		Severity:    "critical",
		Description: "Disk mirror broke",
	}
	if err := sink.Send(context.Background(), ev); err != nil {
		t.Fatalf("Webhook 전송 실패: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if received.Host != "node0" || received.Severity != "critical" || received.Description != "Disk mirror broke" {
		t.Errorf("webhook payload mismatch: %+v", received)
	}
}

func TestWebhookSinkNon2xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	sink := NewWebhookSink(srv.URL, nil)
	defer sink.Close()

	ev := AuditEvent{Timestamp: time.Now(), Host: "h", Kind: "k", Severity: "info", Description: "d"}
	if err := sink.Send(context.Background(), ev); err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestWebhookSinkRefusesRedirect(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer final.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redir.Close()

	sink := NewWebhookSink(redir.URL, nil)
	defer sink.Close()
	ev := AuditEvent{Timestamp: time.Now(), Host: "h", Kind: "k", Severity: "info", Description: "d"}
	if err := sink.Send(context.Background(), ev); err == nil {
		t.Error("expected error when webhook redirects")
	}
}
