package poller

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestEventLogCompactsLegacyFileToKeep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var data bytes.Buffer
	for i := 0; i < 20; i++ {
		line, err := json.Marshal(map[string]any{
			"ts": "2026-08-14 00:00:00", "host": "h", "desc": strconv.Itoa(i),
		})
		if err != nil {
			t.Fatal(err)
		}
		data.Write(line)
		data.WriteByte('\n')
	}
	if err := os.WriteFile(path, data.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	log := NewEventLog(path, 3)
	if log.Len() != 3 {
		t.Fatalf("restored events = %d, want 3", log.Len())
	}
	latest := log.List(3)
	if latest[0].(map[string]any)["desc"] != "19" || latest[2].(map[string]any)["desc"] != "17" {
		t.Fatalf("wrong tail restored: %#v", latest)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(persisted, []byte{'\n'}); got != 3 {
		t.Fatalf("persisted lines = %d, want 3", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("event log mode = %o, want 600", info.Mode().Perm())
	}
	status := log.Status()
	if status["healthy"] != true || status["file_bytes"] != int64(len(persisted)) {
		t.Fatalf("status = %#v", status)
	}
}

func TestEventLogReadsBoundedTailFromLargeLegacyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	oldLine := []byte(`{"ts":"old","desc":"old"}` + "\n")
	data := bytes.Repeat(oldLine, (2*minEventFileBytes)/len(oldLine))
	for i := 0; i < 5; i++ {
		data = append(data, []byte(`{"ts":"new","desc":"`+strconv.Itoa(i)+`"}`+"\n")...)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	log := NewEventLog(path, 5)
	if log.Len() != 5 {
		t.Fatalf("restored events = %d, want 5", log.Len())
	}
	latest := log.List(5)
	if latest[0].(map[string]any)["desc"] != "4" || latest[4].(map[string]any)["desc"] != "0" {
		t.Fatalf("wrong bounded tail restored: %#v", latest)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= minEventFileBytes {
		t.Fatalf("large legacy log was not compacted: %d bytes", info.Size())
	}
}

func TestEventLogCapsAppendFileAndDescription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log := NewEventLog(path, 5)
	log.maxFileBytes = 2048
	for i := 0; i < 100; i++ {
		log.Add("host", "label", "alert", "warning", strings.Repeat("x", 200)+strconv.Itoa(i))
	}
	if log.Len() != 5 {
		t.Fatalf("ring events = %d, want 5", log.Len())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() > log.maxFileBytes {
		t.Fatalf("event log grew to %d bytes, limit %d", info.Size(), log.maxFileBytes)
	}

	log.maxFileBytes = minEventFileBytes
	log.Add("host", "label", "alert", "warning", strings.Repeat("가", maxEventDescriptionBytes))
	desc := log.List(1)[0].(map[string]any)["desc"].(string)
	if len(desc) > maxEventDescriptionBytes || !utf8.ValidString(desc) || !strings.HasSuffix(desc, "…") {
		t.Fatalf("description was not safely truncated: bytes=%d valid=%v", len(desc), utf8.ValidString(desc))
	}

	reloaded := NewEventLog(path, 5)
	if reloaded.Len() != 5 {
		t.Fatalf("reloaded events = %d, want 5", reloaded.Len())
	}
}

func TestEventLogBoundsEscapedJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	log := NewEventLog(path, 2)
	input := strings.Repeat("\x00", maxEventDescriptionBytes)
	log.Add("host", "label", "alert", "warning", input)

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(persisted), []byte{'\n'}) {
		if len(line)+1 > maxEventLineBytes {
			t.Fatalf("escaped JSON line = %d bytes, limit %d", len(line)+1, maxEventLineBytes)
		}
	}
	desc := log.List(1)[0].(map[string]any)["desc"].(string)
	if len(desc) >= len(input) || log.Status()["healthy"] != true {
		t.Fatalf("control-heavy event was not bounded: desc=%d status=%#v", len(desc), log.Status())
	}
}

func TestEventWatcherRecordsStateAlertAndInventoryTransitions(t *testing.T) {
	log := NewEventLog(filepath.Join(t.TempDir(), "watcher-events.jsonl"), 50)
	cache := NewFleetCache()
	devices := []map[string]any{
		{
			"id": "edge-1", "host": "edge-1.local", "status": "op",
			"meta": map[string]any{"label": "Edge One", "alerts": []any{}},
		},
	}
	w := NewEventWatcher(log, cache, nil,
		func() []map[string]any { return devices },
		func(key string) string { return "display-" + key })

	// The first sample establishes a baseline and must not emit boot-time noise.
	w.round()
	if got := log.Len(); got != 0 {
		t.Fatalf("baseline emitted %d events: %#v", got, log.List(50))
	}
	// Skip the intentional boot grace so subsequent rounds exercise real diffs.
	w.t0 = time.Now().Add(-time.Duration(bootGrace+1) * time.Second)

	devices = []map[string]any{
		{
			"id": "edge-1", "host": "edge-1.local", "status": "down",
			"meta": map[string]any{
				"label": "Edge One",
				"alerts": []any{
					map[string]any{"name": "FAN_FAIL", "desc": "", "sev": "critical"},
					// DEVICE_STATE is represented by the dedicated state event, not duplicated.
					map[string]any{"name": "DEVICE_STATE", "desc": "duplicate", "sev": "critical"},
				},
			},
		},
		{
			"id": "edge-2", "host": "edge-2.local", "status": "op",
			"meta": map[string]any{"label": "Edge Two", "alerts": []any{}},
		},
	}
	w.round()

	devices = []map[string]any{
		{
			"id": "edge-1", "host": "edge-1.local", "status": "deg",
			"meta": map[string]any{
				"label": "Edge One",
				"alerts": []any{
					// Unknown severities are deliberately normalized to warning.
					map[string]any{"name": "HOT", "desc": "temperature", "severity": "unexpected"},
				},
			},
		},
	}
	w.round()

	events := log.List(50)
	if len(events) != 7 {
		t.Fatalf("transition events = %d, want 7: %#v", len(events), events)
	}
	seen := map[string]bool{}
	for _, raw := range events {
		ev := raw.(map[string]any)
		key := eventString(ev, "kind") + "|" + eventString(ev, "host") + "|" +
			eventString(ev, "sev") + "|" + eventString(ev, "desc")
		seen[key] = true
		if strings.Contains(eventString(ev, "desc"), "duplicate") {
			t.Fatalf("DEVICE_STATE alert was duplicated: %#v", ev)
		}
	}
	for _, want := range []string{
		"state|edge-1.local|critical|상태 가동 → 오프라인",
		"alert|edge-1.local|critical|FAN_FAIL",
		"new|edge-2.local|info|장비 등록됨",
		"state|edge-1.local|warning|상태 오프라인 → 저하",
		"alert|edge-1.local|warning|temperature",
		"clear|edge-1.local|info|해제: FAN_FAIL",
		"gone|edge-2.local|info|장비 제거됨",
	} {
		if !seen[want] {
			t.Errorf("missing event %q; got %#v", want, events)
		}
	}

	if got := stateLabel("vendor-state"); got != "vendor-state" {
		t.Fatalf("unknown state label = %q", got)
	}
	left := map[[3]string]bool{{"z", "", "warning"}: true, {"a", "", "critical"}: true}
	diff := sortedAlertDiff(left, map[[3]string]bool{{"z", "", "warning"}: true})
	if len(diff) != 1 || diff[0][0] != "a" {
		t.Fatalf("sorted alert difference = %#v", diff)
	}
}

func TestEventLogRepairsMissingFinalNewlineBeforeAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(path, []byte(`{"ts":"old","host":"h","label":"","kind":"state","sev":"info","desc":"first"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	log := NewEventLog(path, 10)
	log.Add("h", "", "state", "info", "second")

	reloaded := NewEventLog(path, 10)
	events := reloaded.List(10)
	if len(events) != 2 || events[0].(map[string]any)["desc"] != "second" ||
		events[1].(map[string]any)["desc"] != "first" {
		t.Fatalf("missing-newline recovery = %#v", events)
	}
}

func TestEventLogPartialLoadFailureDoesNotDuplicateHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	history := []byte(`{"ts":"old","host":"h","label":"","kind":"state","sev":"info","desc":"historical"}` + "\n")
	if err := os.WriteFile(path, history, 0o600); err != nil {
		t.Fatal(err)
	}
	log := &EventLog{
		path: path, keep: 10, maxFileBytes: minEventFileBytes,
		ring: []map[string]any{normalizeEvent(map[string]any{
			"ts": "old", "host": "h", "kind": "state", "sev": "info", "desc": "historical",
		})},
	}
	log.markLoadFailure(errors.New("simulated post-parse chmod failure"))
	if log.Len() != 0 {
		t.Fatalf("partial load was retained as a write buffer: %#v", log.List(10))
	}
	log.Add("h", "", "state", "info", "buffered")

	reloaded := NewEventLog(path, 10)
	events := reloaded.List(10)
	if len(events) != 2 || events[0].(map[string]any)["desc"] != "buffered" ||
		events[1].(map[string]any)["desc"] != "historical" {
		t.Fatalf("partial-load recovery duplicated history: %#v", events)
	}
}
func TestEventLogLoadFailureDoesNotOverwriteHistory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "events.jsonl")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	log := NewEventLog(path, 10)
	if status := log.Status(); status["load_blocked"] != true || status["dirty"] != false {
		t.Fatalf("initial load failure status = %#v", status)
	}
	log.Add("h", "", "state", "warning", "buffered")
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("unread path was overwritten: info=%v err=%v", info, err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	history := []byte(`{"ts":"old","host":"h","label":"","kind":"state","sev":"info","desc":"historical"}` + "\n")
	if err := os.WriteFile(path, history, 0o600); err != nil {
		t.Fatal(err)
	}
	log.Add("h", "", "state", "info", "current")

	reloaded := NewEventLog(path, 10)
	events := reloaded.List(10)
	if len(events) != 3 || events[0].(map[string]any)["desc"] != "current" ||
		events[1].(map[string]any)["desc"] != "buffered" ||
		events[2].(map[string]any)["desc"] != "historical" {
		t.Fatalf("load recovery lost history: %#v", events)
	}
}
func TestEventLogReportsWriteFailureAndRecovery(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "missing")
	path := filepath.Join(dir, "events.jsonl")
	log := NewEventLog(path, 10)

	log.Add("h1", "label", "state", "critical", "offline")
	failed := log.Status()
	if failed["healthy"] != false || failed["last_error"] == nil || failed["buffered"] != 1 {
		t.Fatalf("failed status = %#v", failed)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	log.Add("h1", "label", "state", "info", "online")
	recovered := log.Status()
	if recovered["healthy"] != true || recovered["last_error"] != nil || recovered["last_write_at"] == nil {
		t.Fatalf("recovered status = %#v", recovered)
	}
	if log.Len() != 2 {
		t.Fatalf("memory ring lost events during write failure: %d", log.Len())
	}
	reloaded := NewEventLog(path, 10)
	persisted := reloaded.List(10)
	if len(persisted) != 2 || persisted[0].(map[string]any)["desc"] != "online" ||
		persisted[1].(map[string]any)["desc"] != "offline" {
		t.Fatalf("recovered persistence lost dirty event: %#v", persisted)
	}
}
