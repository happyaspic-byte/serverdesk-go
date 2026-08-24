//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBinaryAuthenticationLifecycle exercises the shipped process boundary rather than an
// in-memory handler: credential initialization, startup, public health, login, authenticated API,
// cross-origin rejection, and graceful signal shutdown.
func TestBinaryAuthenticationLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("process integration test")
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "serverdesk")
	build := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-o", bin, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build product binary: %v\n%s", err, output)
	}

	authPath := filepath.Join(dir, "auth.json")
	initCmd := exec.Command(bin, "-auth", authPath, "-init-auth")
	initOutput, err := initCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("initialize authentication: %v\n%s", err, initOutput)
	}
	password := outputValue(string(initOutput), "ADMIN_PASSWORD")
	if password == "" {
		t.Fatalf("initializer did not return an administrator password: %q", initOutput)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	configPath := filepath.Join(dir, "config.json")
	configDoc := map[string]any{
		"listen":               addr,
		"secret_policy":        "require-references",
		"avcli_bin":            "/bin/true",
		"runtime_dir":          filepath.Join(dir, "runtime"),
		"snmp_enabled":         false,
		"trap":                 map[string]any{"enabled": false},
		"clusters":             []any{},
		"edge_devices":         []any{},
		"cache_refresh":        1,
		"cors_allowed_origins": []string{},
	}
	configBytes, err := json.Marshal(configDoc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	var processLog bytes.Buffer
	cmd := exec.Command(bin, "-c", configPath, "-auth", authPath, "-allow-argv-exposure")
	cmd.Stdout = &processLog
	cmd.Stderr = &processLog
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		_ = cmd.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	})

	baseURL := "http://" + addr
	if err := waitForHealthy(baseURL+"/api/health", exited, 10*time.Second); err != nil {
		t.Fatalf("server did not become healthy: %v\n%s", err, processLog.String())
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar:     jar,
		Timeout: 5 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	unauth, err := client.Get(baseURL + "/api/admin/health")
	if err != nil {
		t.Fatal(err)
	}
	unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin health = %d, want 401", unauth.StatusCode)
	}

	values := url.Values{"username": {"admin"}, "password": {password}, "next": {"https://evil.example/"}}
	loginReq, err := http.NewRequest(http.MethodPost, baseURL+"/login", strings.NewReader(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	loginReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loginReq.Header.Set("Origin", baseURL)
	login, err := client.Do(loginReq)
	if err != nil {
		t.Fatal(err)
	}
	login.Body.Close()
	if login.StatusCode != http.StatusSeeOther || login.Header.Get("Location") != "/" {
		t.Fatalf("login = %d location=%q, want 303 /", login.StatusCode, login.Header.Get("Location"))
	}

	authed, err := client.Get(baseURL + "/api/admin/health")
	if err != nil {
		t.Fatal(err)
	}
	authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("authenticated admin health = %d, want 200", authed.StatusCode)
	}

	root, err := client.Get(baseURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	root.Body.Close()
	if root.StatusCode != http.StatusOK || root.Header.Get("Content-Security-Policy") == "" {
		t.Fatalf("authenticated console = %d CSP=%q", root.StatusCode, root.Header.Get("Content-Security-Policy"))
	}

	notifications, err := client.Get(baseURL + "/api/admin/notifications")
	if err != nil {
		t.Fatal(err)
	}
	var notificationSettings map[string]any
	if err := json.NewDecoder(notifications.Body).Decode(&notificationSettings); err != nil {
		notifications.Body.Close()
		t.Fatal(err)
	}
	notifications.Body.Close()
	if notifications.StatusCode != http.StatusOK {
		t.Fatalf("notification settings = %d", notifications.StatusCode)
	}
	if _, exposed := notificationSettings["webhook_url"]; exposed {
		t.Fatalf("notification settings exposed the webhook secret: %#v", notificationSettings)
	}

	ackBody := []byte(`{"set":{"binary-e2e":{"ts":"2026-08-24T12:00:00Z","by":"e2e","reason":"verified process boundary"}}}`)
	ackReq, err := http.NewRequest(http.MethodPut, baseURL+"/ack", bytes.NewReader(ackBody))
	if err != nil {
		t.Fatal(err)
	}
	ackReq.Header.Set("Content-Type", "application/json")
	ackReq.Header.Set("Origin", baseURL)
	ackWrite, err := client.Do(ackReq)
	if err != nil {
		t.Fatal(err)
	}
	ackWrite.Body.Close()
	if ackWrite.StatusCode != http.StatusOK {
		t.Fatalf("same-origin structured ack = %d", ackWrite.StatusCode)
	}
	ackRead, err := client.Get(baseURL + "/ack")
	if err != nil {
		t.Fatal(err)
	}
	var ackState map[string]any
	if err := json.NewDecoder(ackRead.Body).Decode(&ackState); err != nil {
		ackRead.Body.Close()
		t.Fatal(err)
	}
	ackRead.Body.Close()
	ackValue, ok := ackState["binary-e2e"].(map[string]any)
	if ackRead.StatusCode != http.StatusOK || !ok || ackValue["reason"] != "verified process boundary" {
		t.Fatalf("durable structured ack = %d %#v", ackRead.StatusCode, ackState)
	}

	crossAckReq, err := http.NewRequest(http.MethodPut, baseURL+"/ack", bytes.NewReader([]byte(`{"del":["binary-e2e"]}`)))
	if err != nil {
		t.Fatal(err)
	}
	crossAckReq.Header.Set("Content-Type", "application/json")
	crossAckReq.Header.Set("Origin", "https://evil.example")
	crossAck, err := client.Do(crossAckReq)
	if err != nil {
		t.Fatal(err)
	}
	crossAck.Body.Close()
	if crossAck.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin ack = %d, want 403", crossAck.StatusCode)
	}

	logoutReq, err := http.NewRequest(http.MethodPost, baseURL+"/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutReq.Header.Set("Origin", "https://evil.example")
	crossOrigin, err := client.Do(logoutReq)
	if err != nil {
		t.Fatal(err)
	}
	crossOrigin.Body.Close()
	if crossOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin logout = %d, want 403", crossOrigin.StatusCode)
	}

	logoutReq, err = http.NewRequest(http.MethodPost, baseURL+"/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutReq.Header.Set("Origin", baseURL)
	logout, err := client.Do(logoutReq)
	if err != nil {
		t.Fatal(err)
	}
	logout.Body.Close()
	if logout.StatusCode != http.StatusSeeOther || logout.Header.Get("Location") != "/login" {
		t.Fatalf("same-origin logout = %d location=%q", logout.StatusCode, logout.Header.Get("Location"))
	}
	loggedOut, err := client.Get(baseURL + "/api/admin/health")
	if err != nil {
		t.Fatal(err)
	}
	loggedOut.Body.Close()
	if loggedOut.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session remained valid after logout: %d", loggedOut.StatusCode)
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		stopped = true
		if err != nil {
			t.Fatalf("graceful shutdown: %v\n%s", err, processLog.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("graceful shutdown timed out\n%s", processLog.String())
	}
}

func outputValue(output, key string) string {
	prefix := key + "="
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func waitForHealthy(endpoint string, exited chan error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			exited <- err
			return fmt.Errorf("process exited before health check: %w", err)
		default:
		}
		resp, err := client.Get(endpoint)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", endpoint)
}
