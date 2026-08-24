package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"serverdesk/internal/config"
)

type fakeNotificationController struct {
	mu             sync.Mutex
	cfg            config.NotificationConfig
	failNextUpdate bool
	testTarget     string
	status         map[string]any
}

func (f *fakeNotificationController) ValidateConfig(cfg config.NotificationConfig) error {
	if cfg.Enabled && !strings.HasPrefix(cfg.WebhookURL, "https://allowed.example/") {
		return errors.New("target is not in the server allowlist")
	}
	return nil
}

func (f *fakeNotificationController) UpdateConfig(cfg config.NotificationConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Deliberately mutate before the injected error. The HTTP layer's defensive
	// rollback must restore even a non-transactional controller implementation.
	f.cfg = cfg
	if f.failNextUpdate {
		f.failNextUpdate = false
		return errors.New("injected runtime persistence failure")
	}
	return nil
}

func (f *fakeNotificationController) Config() config.NotificationConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cfg
}

func (f *fakeNotificationController) Test(_ context.Context, target string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if target == "" {
		target = f.cfg.WebhookURL
	}
	f.testTarget = target
	if !strings.HasPrefix(target, "https://allowed.example/") {
		return 0, errors.New("target rejected")
	}
	return http.StatusNoContent, nil
}

func (f *fakeNotificationController) Status() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.status != nil {
		out := make(map[string]any, len(f.status))
		for key, value := range f.status {
			out[key] = value
		}
		return out
	}
	return map[string]any{"enabled": f.cfg.Enabled, "configured": f.cfg.WebhookURL != "", "healthy": true}
}

func notificationFixture(t *testing.T) (*adminTestFixture, *fakeNotificationController) {
	t.Helper()
	f := newAdminTestFixture(t, `{"secret_policy":"require-references","clusters":[],"edge_devices":[]}`)
	n := &fakeNotificationController{cfg: config.NotificationConfig{RetryMax: 5, RetryBaseSeconds: 5}}
	f.srv.Notifier = n
	return f, n
}

func TestNotificationAPIProtectsURLAndNeverReturnsIt(t *testing.T) {
	f, notifier := notificationFixture(t)
	const webhook = "https://allowed.example/hooks/private-token"
	rec, response := execRequest(f.srv, http.MethodPut, "/api/admin/notifications", map[string]any{
		"enabled": true, "webhook_url": webhook, "escalation_hours": 4,
		"retry_max": 5, "retry_base_seconds": 2,
	}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT notifications = %d %s", rec.Code, rec.Body.String())
	}
	if response["configured"] != true || strings.Contains(rec.Body.String(), "private-token") || strings.Contains(rec.Body.String(), "webhook_url") {
		t.Fatalf("secret-bearing response = %s", rec.Body.String())
	}
	stored, err := os.ReadFile(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "private-token") || !strings.Contains(string(stored), "secret://serverdesk.managed.notifications.webhook_url.") {
		t.Fatalf("stored notifications = %s", stored)
	}
	if got := notifier.Config(); got.WebhookURL != webhook || !got.Enabled || got.EscalationHours != 4 {
		t.Fatalf("runtime config = %#v", got)
	}

	// A masked/blank input preserves the central secret while changing only
	// non-secret policy fields.
	beforeRef, err := notificationWebhookFromDoc(mustReadConfigDoc(t, f))
	if err != nil {
		t.Fatal(err)
	}
	rec, _ = execRequest(f.srv, http.MethodPut, "/api/admin/notifications", map[string]any{
		"webhook_url": "", "retry_max": 7,
	}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("retry-only PUT = %d %s", rec.Code, rec.Body.String())
	}
	afterRef, err := notificationWebhookFromDoc(mustReadConfigDoc(t, f))
	if err != nil || afterRef != beforeRef || notifier.Config().WebhookURL != webhook || notifier.Config().RetryMax != 7 {
		t.Fatalf("secret was not preserved before=%q after=%q cfg=%#v err=%v", beforeRef, afterRef, notifier.Config(), err)
	}

	rec, _ = execRequest(f.srv, http.MethodGet, "/api/admin/notifications", nil, "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "private-token") || strings.Contains(rec.Body.String(), "webhook_url") {
		t.Fatalf("GET leaked URL: %d %s", rec.Code, rec.Body.String())
	}
}

func mustReadConfigDoc(t *testing.T, f *adminTestFixture) map[string]json.RawMessage {
	t.Helper()
	doc, err := f.srv.Store.ReadDoc()
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestNotificationRuntimeFailureRollsBackConfigAndRuntime(t *testing.T) {
	f, notifier := notificationFixture(t)
	const oldURL = "https://allowed.example/hooks/old-token"
	rec, _ := execRequest(f.srv, http.MethodPut, "/api/admin/notifications", map[string]any{
		"enabled": true, "webhook_url": oldURL,
	}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("initial PUT = %d %s", rec.Code, rec.Body.String())
	}
	oldRef, err := notificationWebhookFromDoc(mustReadConfigDoc(t, f))
	if err != nil || !strings.HasPrefix(oldRef, "secret://serverdesk.managed.") {
		t.Fatalf("initial protected reference = %q, %v", oldRef, err)
	}
	notifier.mu.Lock()
	notifier.failNextUpdate = true
	notifier.mu.Unlock()
	rec, _ = execRequest(f.srv, http.MethodPut, "/api/admin/notifications", map[string]any{
		"webhook_url": "https://allowed.example/hooks/new-token", "retry_max": 9,
	}, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed runtime PUT = %d %s", rec.Code, rec.Body.String())
	}
	if got := notifier.Config(); got.WebhookURL != oldURL || got.RetryMax != 5 {
		t.Fatalf("runtime rollback = %#v", got)
	}
	loaded, err := config.Load(f.cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Notifications.WebhookURL != oldURL || loaded.Notifications.RetryMax != 5 {
		t.Fatalf("file rollback = %#v", loaded.Notifications)
	}
	for _, path := range []string{f.cfgPath, f.cfgPath + ".bak"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "old-token") || strings.Contains(string(data), "new-token") {
			t.Fatalf("plaintext webhook in %s: %s", path, data)
		}
	}
	entries, err := os.ReadDir(f.srv.Store.CredentialDirectory())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != strings.TrimPrefix(oldRef, "secret://") {
		t.Fatalf("failed runtime update left managed credential generations: %v", entries)
	}
}

func TestFirstNotificationRuntimeFailureRemovesNewManagedSecret(t *testing.T) {
	f, notifier := notificationFixture(t)
	notifier.mu.Lock()
	notifier.failNextUpdate = true
	notifier.mu.Unlock()
	rec, _ := execRequest(f.srv, http.MethodPut, "/api/admin/notifications", map[string]any{
		"enabled": true, "webhook_url": "https://allowed.example/hooks/rejected-token",
	}, "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("failed first runtime PUT = %d %s", rec.Code, rec.Body.String())
	}
	doc := mustReadConfigDoc(t, f)
	if _, exists := doc["notifications"]; exists {
		t.Fatalf("failed first update left notification section: %#v", doc["notifications"])
	}
	entries, err := os.ReadDir(f.srv.Store.CredentialDirectory())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed first update left managed credentials: %v", entries)
	}
}

func TestNotificationUpdateFailsClosedOnMalformedPersistedSection(t *testing.T) {
	f, notifier := notificationFixture(t)
	if err := os.WriteFile(f.cfgPath, []byte(`{"secret_policy":"require-references","clusters":[],"edge_devices":[],"notifications":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _ := execRequest(f.srv, http.MethodPut, "/api/admin/notifications", map[string]any{"retry_max": 6}, "")
	if rec.Code != http.StatusInternalServerError || notifier.Config().RetryMax != 5 {
		t.Fatalf("malformed persisted section response=%d runtime=%#v body=%s", rec.Code, notifier.Config(), rec.Body.String())
	}
}

func TestNotificationTestEndpointSupportsStoredAndUnsavedTargets(t *testing.T) {
	f, notifier := notificationFixture(t)
	const stored = "https://allowed.example/hooks/stored-token"
	rec, _ := execRequest(f.srv, http.MethodPut, "/api/admin/notifications", map[string]any{
		"enabled": false, "webhook_url": stored,
	}, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("save disabled target = %d %s", rec.Code, rec.Body.String())
	}
	rec, _ = execRequest(f.srv, http.MethodPost, "/api/admin/notifications/test", map[string]any{}, "")
	if rec.Code != http.StatusOK || notifier.testTarget != stored {
		t.Fatalf("stored target test = %d target=%q body=%s", rec.Code, notifier.testTarget, rec.Body.String())
	}
	const unsaved = "https://allowed.example/hooks/unsaved-token"
	rec, _ = execRequest(f.srv, http.MethodPost, "/api/admin/notifications/test", map[string]any{"webhook_url": unsaved}, "")
	if rec.Code != http.StatusOK || notifier.testTarget != unsaved {
		t.Fatalf("unsaved target test = %d target=%q body=%s", rec.Code, notifier.testTarget, rec.Body.String())
	}
	if notifier.Config().WebhookURL != stored {
		t.Fatalf("test endpoint persisted unsaved target: %#v", notifier.Config())
	}
	rec, _ = execRequest(f.srv, http.MethodPost, "/api/admin/notifications/test", `{bad`, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed test JSON = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDetailedHealthIncludesMaskedNotificationFailure(t *testing.T) {
	f, notifier := notificationFixture(t)
	config.RegisterSecret("health-private-token")
	notifier.status = map[string]any{
		"enabled": true, "configured": true, "healthy": false,
		"last_error": "delivery health-private-token failed", "dead_letter": 1,
	}
	rec, response := execRequest(f.srv, http.MethodGet, "/api/admin/health", nil, "")
	notifications, _ := response["notifications"].(map[string]any)
	if rec.Code != http.StatusOK || notifications["healthy"] != false || notifications["dead_letter"] != float64(1) ||
		strings.Contains(rec.Body.String(), "health-private-token") || !strings.Contains(rec.Body.String(), "delivery *** failed") {
		t.Fatalf("health response = %d %s", rec.Code, rec.Body.String())
	}
}
