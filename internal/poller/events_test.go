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
