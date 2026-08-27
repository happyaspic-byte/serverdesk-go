package poller

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// AuditEvent 는 외부 SIEM/Syslog로 전달되는 이벤트다.
type AuditEvent struct {
	Timestamp   time.Time `json:"timestamp"`
	Host        string    `json:"host"`
	Label       string    `json:"label"`
	Kind        string    `json:"kind"`
	Severity    string    `json:"severity"`
	Description string    `json:"description"`
}

// AuditSink 는 외부 감사/이벤트 수신처다.
type AuditSink interface {
	Send(ctx context.Context, ev AuditEvent) error
	Close() error
}

// SyslogSink 는 RFC 5424 형식의 UDP/TCP Syslog 포워더다.
type SyslogSink struct {
	network string
	address string
	app     string

	mu   sync.Mutex
	conn net.Conn
}

// NewSyslogSink 는 Syslog 포워더를 생성한다.
func NewSyslogSink(network, address, app string) *SyslogSink {
	if app == "" {
		app = "serverdesk"
	}
	return &SyslogSink{network: network, address: address, app: app}
}

// Send 는 RFC 5424 메시지를 전송한다.
func (s *SyslogSink) Send(ctx context.Context, ev AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.conn == nil {
		var d net.Dialer
		conn, err := d.DialContext(ctx, s.network, s.address)
		if err != nil {
			return fmt.Errorf("syslog dial failed: %w", err)
		}
		s.conn = conn
	}

	pri := 134 // local0.info
	switch ev.Severity {
	case "critical":
		pri = 130 // local0.crit
	case "warning", "warn":
		pri = 132 // local0.warn
	}

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "serverdesk"
	}
	ts := ev.Timestamp.UTC().Format(time.RFC3339)
	msg := fmt.Sprintf("<%d>1 %s %s %s %d %s - %s\n",
		pri, ts, hostname, s.app, os.Getpid(), ev.Kind, ev.Description)

	_ = s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := s.conn.Write([]byte(msg))
	if err != nil {
		_ = s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

// Close 는 연결을 닫는다.
func (s *SyslogSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		err := s.conn.Close()
		s.conn = nil
		return err
	}
	return nil
}

// WebhookSink 는 SIEM HTTP/JSON 포워더다.
type WebhookSink struct {
	url    string
	client *http.Client
}

// NewWebhookSink 는 Webhook 포워더를 생성한다.
func NewWebhookSink(url string, client *http.Client) *WebhookSink {
	if client == nil {
		client = &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
			},
		}
	}
	return &WebhookSink{url: url, client: client}
}

// Send 는 이벤트를 JSON으로 POST 전송한다.
func (w *WebhookSink) Send(ctx context.Context, ev AuditEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
	return nil
}

// Close 는 유휴 연결을 정리한다.
func (w *WebhookSink) Close() error {
	w.client.CloseIdleConnections()
	return nil
}
