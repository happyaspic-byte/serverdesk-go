// Package alerting implements server-resident, restart-safe critical webhook
// delivery. It deliberately has no browser dependency.
package alerting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"serverdesk/internal/config"
)

const (
	stateVersion           = 1
	stateMaxSize           = 4 << 20
	retention              = 30 * 24 * time.Hour
	maxDeliveryBatch       = 32
	maxDeliveryConcurrency = 8
)

// Signal is one active critical condition observed from the current fleet.
// Key and AckKey must be stable across polling rounds and restarts.
type Signal struct {
	Key         string
	Host        string
	Description string
	AckKey      string
	StartedAt   time.Time
	// SourceUnready marks a signal produced from a collector snapshot that is
	// not currently authoritative. The zero value is deliberately trusted so
	// package callers and tests remain fail-safe by default. During a partial
	// fleet outage the engine still ingests signals from ready sources, while
	// unready sources can neither create false startup alarms nor recover an
	// existing condition.
	SourceUnready bool
	// SuppressEscalation is used for collector-health conditions that are
	// actionable but do not have a matching browser ACK row.
	SuppressEscalation bool
}

type Sender interface {
	ValidateNotifyTarget(string) error
	SendWebhook(context.Context, string, string, string) (int, error)
}

// Source returns the current critical signals and whether every configured
// collector has produced its first snapshot. An empty but ready snapshot is a
// valid zero-device deployment. An unready snapshot must never be reconciled
// as recovery, especially immediately after a process restart.
type Source func() (signals []Signal, ready bool)

// SnapshotSource is the commercial multi-collector contract. recoveryReady is
// keyed by Signal.Host and grants permission to treat an absent condition as a
// recovery for that host. This prevents one failed collector from blocking
// recoveries for every other device while still forbidding false recoveries
// from stale or incomplete snapshots.
type SnapshotSource func() (signals []Signal, recoveryReady map[string]bool, allReady bool)

// SilenceSource returns browser-independent shared acknowledgement and active
// maintenance maps. It is optional; nil means no suppression.
type SilenceSource func() (acked map[string]bool, maintenance map[string]bool)

type SilenceSourceWithError func() (acked map[string]bool, maintenance map[string]bool, err error)

type LogFunc func(level, component, message string)

type condition struct {
	Key                string    `json:"key"`
	Host               string    `json:"host"`
	Description        string    `json:"description"`
	AckKey             string    `json:"ack_key,omitempty"`
	FirstSeen          time.Time `json:"first_seen"`
	LastSeen           time.Time `json:"last_seen"`
	Active             bool      `json:"active"`
	SuppressEscalation bool      `json:"suppress_escalation,omitempty"`
}

type delivery struct {
	ID           string    `json:"id"`
	ConditionKey string    `json:"condition_key"`
	Kind         string    `json:"kind"` // critical | escalation | recovery
	Message      string    `json:"message"`
	CreatedAt    time.Time `json:"created_at"`
	NextAttempt  time.Time `json:"next_attempt"`
	Attempts     int       `json:"attempts"`
	DeliveredAt  time.Time `json:"delivered_at,omitempty"`
	DeadLetter   bool      `json:"dead_letter,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	LastErrorAt  time.Time `json:"last_error_at,omitempty"`
}

type diskState struct {
	Version     int                   `json:"version"`
	Conditions  map[string]*condition `json:"conditions"`
	Deliveries  map[string]*delivery  `json:"deliveries"`
	LastSuccess time.Time             `json:"last_success,omitempty"`
	LastError   string                `json:"last_error,omitempty"`
	LastErrorAt time.Time             `json:"last_error_at,omitempty"`
	LastScan    time.Time             `json:"last_scan,omitempty"`
	LastPersist time.Time             `json:"last_persist,omitempty"`
}

type Options struct {
	StateDir         string
	Config           config.NotificationConfig
	Source           Source
	Snapshot         SnapshotSource
	Silence          SilenceSource
	SilenceWithError SilenceSourceWithError
	Sender           Sender
	Logf             LogFunc
	Interval         time.Duration
	Now              func() time.Time
}

type Engine struct {
	mu     sync.Mutex
	scanMu sync.Mutex

	path             string
	cfg              config.NotificationConfig
	source           Source
	snapshot         SnapshotSource
	silence          SilenceSource
	silenceWithError SilenceSourceWithError
	sender           Sender
	logf             LogFunc
	interval         time.Duration
	now              func() time.Time
	state            diskState
	// sourceReady is runtime health and is deliberately not restored from disk.
	// A restarted process must prove that its collectors are ready again.
	sourceReady    bool
	sourceObserved bool
	persistFault   bool
	silenceFault   string
	// persistHook is an unexported fault-injection seam used by package tests to
	// prove that an outbound side effect never precedes its durable claim.
	persistHook func() error
}

func New(opts Options) (*Engine, error) {
	if (opts.Source == nil && opts.Snapshot == nil) || opts.Sender == nil {
		return nil, errors.New("alerting source and sender are required")
	}
	if strings.TrimSpace(opts.StateDir) == "" {
		return nil, errors.New("alerting state directory is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 10 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logf == nil {
		opts.Logf = func(string, string, string) {}
	}
	e := &Engine{
		path: filepath.Join(opts.StateDir, "notification-state.json"), cfg: opts.Config,
		source: opts.Source, snapshot: opts.Snapshot, silence: opts.Silence,
		silenceWithError: opts.SilenceWithError, sender: opts.Sender,
		logf: opts.Logf, interval: opts.Interval, now: opts.Now,
		state: diskState{Version: stateVersion, Conditions: map[string]*condition{}, Deliveries: map[string]*delivery{}},
	}
	if err := e.ValidateConfig(opts.Config); err != nil {
		return nil, err
	}
	if err := e.load(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) ValidateConfig(cfg config.NotificationConfig) error {
	if strings.TrimSpace(cfg.WebhookURL) == "" {
		if cfg.Enabled {
			return errors.New("notification webhook is not configured")
		}
		return nil
	}
	return e.sender.ValidateNotifyTarget(cfg.WebhookURL)
}

func (e *Engine) UpdateConfig(cfg config.NotificationConfig) error {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()
	if err := e.ValidateConfig(cfg); err != nil {
		return err
	}
	e.mu.Lock()
	oldCfg := e.cfg
	oldDeliveries := make(map[string]delivery, len(e.state.Deliveries))
	for id, item := range e.state.Deliveries {
		oldDeliveries[id] = *item
	}
	urlChanged := cfg.WebhookURL != e.cfg.WebhookURL
	reactivated := !e.cfg.Enabled && cfg.Enabled
	e.cfg = cfg
	if urlChanged || reactivated {
		for _, item := range e.state.Deliveries {
			if item.DeadLetter && item.DeliveredAt.IsZero() {
				item.DeadLetter = false
				item.Attempts = 0
				item.LastError = ""
				item.LastErrorAt = time.Time{}
				item.NextAttempt = e.now().UTC()
			}
		}
	}
	e.refreshDeliveryErrorLocked()
	err := e.persistLocked()
	if err != nil {
		e.cfg = oldCfg
		e.state.Deliveries = make(map[string]*delivery, len(oldDeliveries))
		for id, item := range oldDeliveries {
			copy := item
			e.state.Deliveries[id] = &copy
		}
		e.markPersistFailureLocked("persist notification configuration", err, e.now().UTC())
	}
	e.mu.Unlock()
	return err
}

func (e *Engine) Config() config.NotificationConfig {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

func (e *Engine) Start(ctx context.Context) {
	e.ScanOnce(ctx)
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.ScanOnce(ctx)
		}
	}
}

func deliveryID(conditionKey, kind string, activation time.Time) string {
	sum := sha256.Sum256([]byte(conditionKey + "\x00" + kind + "\x00" + activation.UTC().Format(time.RFC3339Nano)))
	return "serverdesk-" + hex.EncodeToString(sum[:16])
}

func (e *Engine) enqueueLocked(c *condition, kind, message string, now time.Time) {
	id := deliveryID(c.Key, kind, c.FirstSeen)
	if _, exists := e.state.Deliveries[id]; exists {
		return
	}
	e.state.Deliveries[id] = &delivery{
		ID: id, ConditionKey: c.Key, Kind: kind, Message: message,
		CreatedAt: now, NextAttempt: now,
	}
	e.logf("info", "notify", fmt.Sprintf("queued %s delivery=%s condition=%s", kind, id, c.Key))
}

func conditionMessage(kind string, c *condition, hours int) string {
	host := c.Host
	if host == "" {
		host = c.Key
	}
	switch kind {
	case "recovery":
		return fmt.Sprintf("✅ [serverdesk] RECOVERED — %s: %s", host, c.Description)
	case "escalation":
		return fmt.Sprintf("🚨 [serverdesk] CRITICAL unacknowledged %dh+ — %s: %s (since %s)",
			hours, host, c.Description, c.FirstSeen.UTC().Format(time.RFC3339))
	default:
		return fmt.Sprintf("🚨 [serverdesk] CRITICAL — %s: %s", host, c.Description)
	}
}

func (e *Engine) ScanOnce(ctx context.Context) {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()

	e.mu.Lock()
	cfg := e.cfg
	e.mu.Unlock()
	if !cfg.Enabled || ctx.Err() != nil {
		return
	}

	now := e.now().UTC()
	var signals []Signal
	var recoveryReady map[string]bool
	var sourceReady bool
	if e.snapshot != nil {
		signals, recoveryReady, sourceReady = e.snapshot()
	} else {
		signals, sourceReady = e.source()
	}
	acked, maintenance := map[string]bool{}, map[string]bool{}
	if e.silenceWithError != nil {
		a, m, err := e.silenceWithError()
		if err != nil {
			e.mu.Lock()
			e.sourceReady = sourceReady
			e.sourceObserved = true
			e.silenceFault = "operator acknowledgement/maintenance state is unavailable"
			e.mu.Unlock()
			e.logf("error", "notify", "operator acknowledgement/maintenance state read failed; notification delivery paused")
			return
		}
		if a != nil || m != nil {
			acked, maintenance = a, m
		}
	} else if e.silence != nil {
		if a, m := e.silence(); a != nil || m != nil {
			acked, maintenance = a, m
		}
	}
	current := make(map[string]Signal, len(signals))
	for _, signal := range signals {
		signal.Key = strings.TrimSpace(signal.Key)
		if signal.Key != "" && (sourceReady || !signal.SourceUnready) {
			current[signal.Key] = signal
		}
	}

	e.mu.Lock()
	e.silenceFault = ""
	readyChanged := !e.sourceObserved || e.sourceReady != sourceReady
	e.sourceReady = sourceReady
	e.sourceObserved = true
	// A fleet-wide readiness failure must only block recovery. Otherwise one
	// failed collector suppresses unrelated critical alarms from every healthy
	// cluster. current contains only signals whose own source is authoritative.
	if sourceReady || len(current) > 0 {
		for key, signal := range current {
			c := e.state.Conditions[key]
			if c == nil || !c.Active {
				firstSeen := signal.StartedAt.UTC()
				if firstSeen.IsZero() || firstSeen.After(now) {
					firstSeen = now
				}
				c = &condition{Key: key, FirstSeen: firstSeen, Active: true}
				e.state.Conditions[key] = c
			}
			c.Host, c.Description, c.AckKey, c.LastSeen = signal.Host, signal.Description, signal.AckKey, now
			c.SuppressEscalation = signal.SuppressEscalation
			c.Active = true
			inMaintenance := maintenance[c.Host]
			if !inMaintenance {
				e.enqueueLocked(c, "critical", conditionMessage("critical", c, 0), now)
				initial := e.state.Deliveries[deliveryID(c.Key, "critical", c.FirstSeen)]
				if !c.SuppressEscalation && (cfg.EscalationHours == 4 || cfg.EscalationHours == 24) &&
					initial != nil && !initial.DeliveredAt.IsZero() &&
					(c.AckKey == "" || !acked[c.AckKey]) &&
					now.Sub(c.FirstSeen) >= time.Duration(cfg.EscalationHours)*time.Hour {
					e.enqueueLocked(c, "escalation", conditionMessage("escalation", c, cfg.EscalationHours), now)
				}
			}
		}
	}
	for key, c := range e.state.Conditions {
		if !c.Active {
			continue
		}
		if _, exists := current[key]; exists {
			continue
		}
		if !sourceReady && !recoveryReady[c.Host] {
			continue
		}
		c.Active = false
		c.LastSeen = now
		criticalID := deliveryID(c.Key, "critical", c.FirstSeen)
		if initial := e.state.Deliveries[criticalID]; initial != nil && !initial.DeliveredAt.IsZero() {
			e.enqueueLocked(c, "recovery", conditionMessage("recovery", c, 0), now)
		} else if initial != nil && initial.DeliveredAt.IsZero() {
			delete(e.state.Deliveries, criticalID) // condition recovered before anyone was notified
		}
	}
	if !sourceReady && readyChanged {
		e.logf("warn", "notify", "one or more notification sources are not ready; recovery reconciliation is paused")
	}
	for key, c := range e.state.Conditions {
		if !c.Active && now.Sub(c.LastSeen) > retention {
			delete(e.state.Conditions, key)
		}
	}
	for id, item := range e.state.Deliveries {
		stamp := item.CreatedAt
		if !item.DeliveredAt.IsZero() {
			stamp = item.DeliveredAt
		}
		if now.Sub(stamp) > retention {
			delete(e.state.Deliveries, id)
		}
	}
	e.state.LastScan = now
	if err := e.persistLocked(); err != nil {
		e.markPersistFailureLocked("persist notification queue", err, now)
		e.mu.Unlock()
		e.logf("error", "notify", "notification queue persistence failed")
		return
	}

	due := make([]delivery, 0)
	for _, item := range e.state.Deliveries {
		if item.DeliveredAt.IsZero() && !item.DeadLetter && !item.NextAttempt.After(now) {
			due = append(due, *item)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].CreatedAt.Before(due[j].CreatedAt) })
	if len(due) > maxDeliveryBatch {
		due = due[:maxDeliveryBatch]
	}
	if len(due) == 0 {
		e.mu.Unlock()
		return
	}
	for i := range due {
		if current := e.state.Deliveries[due[i].ID]; current != nil {
			current.Attempts++
			due[i].Attempts = current.Attempts
		}
	}
	if err := e.persistLocked(); err != nil { // persist claim before outbound side effect
		for i := range due {
			if current := e.state.Deliveries[due[i].ID]; current != nil && current.Attempts > 0 {
				current.Attempts--
			}
		}
		e.markPersistFailureLocked("persist notification delivery claim", err, now)
		e.mu.Unlock()
		e.logf("error", "notify", "notification attempt claim persistence failed; outbound delivery skipped")
		return
	}
	e.mu.Unlock()
	sem := make(chan struct{}, maxDeliveryConcurrency)
	var deliveries sync.WaitGroup
	for _, queued := range due {
		item := queued
		deliveries.Add(1)
		go func() {
			defer deliveries.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			status, sendErr := e.sender.SendWebhook(ctx, cfg.WebhookURL, item.Message, item.ID)
			finished := e.now().UTC()
			e.mu.Lock()
			currentItem := e.state.Deliveries[item.ID]
			if currentItem == nil || !currentItem.DeliveredAt.IsZero() {
				e.mu.Unlock()
				return
			}
			if sendErr == nil {
				currentItem.DeliveredAt = finished
				currentItem.LastError = ""
				currentItem.LastErrorAt = time.Time{}
				e.state.LastSuccess = finished
				e.refreshDeliveryErrorLocked()
				e.logf("info", "notify", fmt.Sprintf("delivered kind=%s delivery=%s status=%d", item.Kind, item.ID, status))
			} else {
				currentItem.LastError = sendErr.Error()
				currentItem.LastErrorAt = finished
				e.refreshDeliveryErrorLocked()
				permanentClientError := status >= 400 && status < 500 && status != 408 && status != 429
				if permanentClientError || currentItem.Attempts >= cfg.RetryMax {
					currentItem.DeadLetter = true
					e.logf("error", "notify", fmt.Sprintf("dead-letter kind=%s delivery=%s attempts=%d status=%d", item.Kind, item.ID, currentItem.Attempts, status))
				} else {
					delay := time.Duration(cfg.RetryBaseSeconds) * time.Second
					for n := 1; n < currentItem.Attempts && delay < time.Hour; n++ {
						delay *= 2
					}
					if delay > time.Hour {
						delay = time.Hour
					}
					currentItem.NextAttempt = finished.Add(delay)
					e.logf("warn", "notify", fmt.Sprintf("delivery failed kind=%s delivery=%s attempt=%d retry_in=%s", item.Kind, item.ID, currentItem.Attempts, delay))
				}
			}
			if err := e.persistLocked(); err != nil {
				e.markPersistFailureLocked("persist notification delivery result", err, finished)
				e.logf("error", "notify", "notification delivery result persistence failed")
			}
			e.mu.Unlock()
		}()
	}
	deliveries.Wait()
}

func (e *Engine) Test(ctx context.Context, target string) (int, error) {
	e.scanMu.Lock()
	defer e.scanMu.Unlock()

	e.mu.Lock()
	cfg := e.cfg
	e.mu.Unlock()
	cfg.Enabled = true // a test requires a destination even when delivery is disabled
	if strings.TrimSpace(target) != "" {
		cfg.WebhookURL = strings.TrimSpace(target)
	}
	if err := e.ValidateConfig(cfg); err != nil {
		return 0, err
	}
	id := deliveryID("configuration-test", "test", e.now().UTC())
	return e.sender.SendWebhook(ctx, cfg.WebhookURL, "[serverdesk] webhook test — server-resident delivery is working", id)
}

func (e *Engine) Status() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	pending, dead, delivered, active := 0, 0, 0, 0
	for _, c := range e.state.Conditions {
		if c.Active {
			active++
		}
	}
	for _, item := range e.state.Deliveries {
		switch {
		case item.DeadLetter:
			dead++
		case !item.DeliveredAt.IsZero():
			delivered++
		default:
			pending++
		}
	}
	latestDeliveryError, latestDeliveryErrorAt, hasDeliveryError := e.latestDeliveryErrorLocked()
	lastError, lastErrorAt := e.state.LastError, e.state.LastErrorAt
	if !e.persistFault && hasDeliveryError {
		lastError, lastErrorAt = latestDeliveryError, latestDeliveryErrorAt
	}
	if e.silenceFault != "" {
		lastError, lastErrorAt = e.silenceFault, e.now().UTC()
	}
	healthy := dead == 0 && !hasDeliveryError && lastError == "" && (!e.cfg.Enabled || e.sourceReady)
	return map[string]any{
		"enabled": e.cfg.Enabled, "configured": e.cfg.WebhookURL != "", "healthy": healthy,
		"source_ready":      e.sourceReady,
		"active_conditions": active, "pending": pending, "delivered_retained": delivered, "dead_letter": dead,
		"last_scan": e.state.LastScan, "last_success": e.state.LastSuccess,
		"last_error": lastError, "last_error_at": lastErrorAt,
	}
}

func (e *Engine) load() error {
	data, err := os.ReadFile(e.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read notification state: %w", err)
	}
	if len(data) > stateMaxSize {
		return errors.New("notification state exceeds 4 MiB")
	}
	var state diskState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode notification state: %w", err)
	}
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported notification state version %d", state.Version)
	}
	if state.Conditions == nil {
		state.Conditions = map[string]*condition{}
	}
	if state.Deliveries == nil {
		state.Deliveries = map[string]*delivery{}
	}
	e.state = state
	e.refreshDeliveryErrorLocked()
	return nil
}

func (e *Engine) latestDeliveryErrorLocked() (string, time.Time, bool) {
	var message string
	var at time.Time
	for _, item := range e.state.Deliveries {
		if !item.DeliveredAt.IsZero() || item.LastError == "" {
			continue
		}
		if message == "" || item.LastErrorAt.After(at) {
			message, at = item.LastError, item.LastErrorAt
		}
	}
	return message, at, message != ""
}

func (e *Engine) refreshDeliveryErrorLocked() {
	if e.persistFault {
		return
	}
	message, at, _ := e.latestDeliveryErrorLocked()
	e.state.LastError, e.state.LastErrorAt = message, at
}

func (e *Engine) markPersistFailureLocked(operation string, err error, now time.Time) {
	e.persistFault = true
	e.state.LastError = operation + ": " + err.Error()
	e.state.LastErrorAt = now
}

// persistLocked clears a previous persistence-only health error only as part of
// the next successful atomic write. If that write fails, the in-memory error is
// restored so health cannot report a false success.
func (e *Engine) persistLocked() error {
	previousError, previousErrorAt := e.state.LastError, e.state.LastErrorAt
	if e.persistFault {
		message, at, _ := e.latestDeliveryErrorLocked()
		e.state.LastError, e.state.LastErrorAt = message, at
	}
	err := e.writeStateLocked()
	if err != nil {
		if e.persistFault {
			e.state.LastError, e.state.LastErrorAt = previousError, previousErrorAt
		}
		return err
	}
	e.persistFault = false
	return nil
}

func (e *Engine) writeStateLocked() error {
	if e.persistHook != nil {
		if err := e.persistHook(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(e.path), 0o700); err != nil {
		return err
	}
	e.state.Version = stateVersion
	e.state.LastPersist = e.now().UTC()
	data, err := json.MarshalIndent(e.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > stateMaxSize {
		return errors.New("notification state exceeds 4 MiB")
	}
	tmp, err := os.CreateTemp(filepath.Dir(e.path), ".notification-state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceStateFile(name, e.path); err != nil {
		return err
	}
	ok = true
	return nil
}
