package poller

// EventLog / EventWatcher — 장비 이벤트 이력(poller.py 의 동명 클래스 포트).
//
// 로그 화면의 '라이브 tail' 은 활성 경보 스냅샷이 아니라 **일어난 일의 이력**이어야
// 한다. 스냅샷 방식은 경보가 해소되는 순간 로그에서도 증발했다. 여기서 상태
// 전이·경보 발생/해제를 이벤트로 남기고 /api/devices 응답에 최근분(events[])을
// 싣는다. 재시작해도 jsonl 테일을 복원해 이력이 이어진다.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	defaultEventKeep         = 500
	maxEventHostBytes        = 512
	maxEventLabelBytes       = 2048
	maxEventDescriptionBytes = 8192
	maxAuditReasonRunes      = 500
	maxEventLineBytes        = 16 << 10
	minEventFileBytes        = 1 << 20
)

// AuditRecord is a structured, secret-free operator mutation record. Phase is
// "prepared" before the state change and "committed" only after every part of
// the mutation succeeded. The shared ID makes rollback/failure records
// unambiguous during incident review.
type AuditRecord struct {
	ID        string
	Action    string
	Target    string
	Reason    string
	Operator  string
	Phase     string
	Timestamp time.Time
}

// EventLog 는 장비 이벤트의 링 + 크기가 제한된 jsonl 영속 저장소다.
type EventLog struct {
	path         string
	keep         int
	maxFileBytes int64
	mu           sync.RWMutex
	ring         []map[string]any
	fileBytes    int64
	lastWriteAt  time.Time
	lastError    string
	lastErrorAt  time.Time
	dirty        bool
	loadBlocked  bool

	sinks        []AuditSink
	forwardCh    chan AuditEvent
	sinkWG       sync.WaitGroup
	forwardCtx   context.Context
	forwardDone  context.CancelFunc
	forwardDrops atomic.Int64
	forwardSent  atomic.Int64
	forwardErrs  atomic.Int64
	lastSinkErr  string
}

// RegisterAuditSink 는 외부 Syslog/SIEM 수신처를 등록한다.
func (el *EventLog) RegisterAuditSink(sink AuditSink) {
	if sink == nil || el == nil {
		return
	}
	el.mu.Lock()
	defer el.mu.Unlock()
	el.sinks = append(el.sinks, sink)
}

// StartAuditForwarder 는 비동기 전송 워커 루프를 시작한다.
func (el *EventLog) StartAuditForwarder(ctx context.Context, buffer int) {
	if el == nil {
		return
	}
	if buffer <= 0 {
		buffer = 1000
	}
	el.mu.Lock()
	el.forwardCh = make(chan AuditEvent, buffer)
	el.forwardCtx, el.forwardDone = context.WithCancel(ctx)
	el.mu.Unlock()

	el.sinkWG.Add(1)
	go func() {
		defer el.sinkWG.Done()
		for {
			select {
			case <-el.forwardCtx.Done():
				el.flushQueuedAudits()
				el.closeAuditSinks()
				return
			case ev, ok := <-el.forwardCh:
				if !ok {
					el.flushQueuedAudits()
					el.closeAuditSinks()
					return
				}
				el.dispatchAudit(ev)
			}
		}
	}()
}

func (el *EventLog) dispatchAudit(ev AuditEvent) {
	el.mu.RLock()
	sinks := append([]AuditSink(nil), el.sinks...)
	el.mu.RUnlock()

	for _, s := range sinks {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		err := s.Send(ctx, ev)
		cancel()
		if err != nil {
			el.forwardErrs.Add(1)
			el.mu.Lock()
			el.lastSinkErr = err.Error()
			el.mu.Unlock()
			logf("warn", "events", fmt.Sprintf("외부 감사 전송 실패: %v", err))
		} else {
			el.forwardSent.Add(1)
		}
	}
}

func (el *EventLog) flushQueuedAudits() {
	for {
		select {
		case ev, ok := <-el.forwardCh:
			if !ok {
				return
			}
			el.dispatchAudit(ev)
		default:
			return
		}
	}
}

func (el *EventLog) closeAuditSinks() {
	el.mu.RLock()
	sinks := append([]AuditSink(nil), el.sinks...)
	el.mu.RUnlock()
	for _, s := range sinks {
		_ = s.Close()
	}
}

// StopAuditForwarder 는 워커를 안전하게 중지한다.
func (el *EventLog) StopAuditForwarder() {
	if el == nil {
		return
	}
	el.mu.Lock()
	done := el.forwardDone
	el.mu.Unlock()
	if done != nil {
		done()
		el.sinkWG.Wait()
	}
}

// NewEventLog 는 path 의 제한된 테일(최대 keep 건)만 복원한다. 이전 버전의
// 무제한 파일도 기동 시 현재 링으로 원자적으로 압축해 이후 디스크 사용량을 제한한다.
func NewEventLog(path string, keep int) *EventLog {
	if keep <= 0 {
		keep = defaultEventKeep
	}
	maxBytes := int64(keep) * int64(maxEventLineBytes)
	if maxBytes < minEventFileBytes {
		maxBytes = minEventFileBytes
	}
	el := &EventLog{
		path: path, keep: keep, maxFileBytes: maxBytes,
		ring: []map[string]any{},
	}
	if err := el.load(); err != nil {
		el.markLoadFailure(err)
		logf("warn", "events", fmt.Sprintf("이벤트 이력 복원 실패: %v", err))
	}
	return el
}

func (el *EventLog) markLoadFailure(err error) {
	// load가 JSON 파싱 뒤 chmod/교체에서 실패했을 수도 있다. 그 부분 이력을
	// post-start 버퍼로 오인하면 재시도 때 같은 기록을 중복 병합하므로 비운다.
	el.ring = []map[string]any{}
	el.dirty = false
	el.loadBlocked = true
	el.lastError = err.Error()
	el.lastErrorAt = time.Now()
}

func (el *EventLog) load() error {
	f, err := os.Open(el.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	el.fileBytes = info.Size()
	readLimit := el.maxFileBytes + maxEventLineBytes
	start := info.Size() - readLimit
	if start < 0 {
		start = 0
	}
	if start > 0 {
		if _, err := f.Seek(start, io.SeekStart); err != nil {
			_ = f.Close()
			return err
		}
	}
	data, readErr := io.ReadAll(io.LimitReader(f, readLimit))
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}

	needsCompact := start > 0 || info.Size() > el.maxFileBytes ||
		(len(data) > 0 && data[len(data)-1] != '\n')
	newestFirst := make([]map[string]any, 0, el.keep)
	end := len(data)
	for end > 0 && len(newestFirst) < el.keep {
		lineStart := bytes.LastIndexByte(data[:end], '\n')
		var raw []byte
		if lineStart < 0 {
			if start > 0 {
				// seek 지점에서 시작한 첫 조각은 온전한 JSON 줄이라고 보장할 수 없다.
				needsCompact = true
				break
			}
			raw = data[:end]
			end = 0
		} else {
			raw = data[lineStart+1 : end]
			end = lineStart
		}
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		if len(raw) > maxEventLineBytes {
			needsCompact = true
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal(raw, &ev); err != nil {
			needsCompact = true
			continue
		}
		ev = normalizeEvent(ev)
		if _, err := marshalEventLine(ev); err != nil {
			needsCompact = true
			continue
		}
		newestFirst = append(newestFirst, ev)
	}
	if end > 0 {
		needsCompact = true
	}
	events := make([]map[string]any, len(newestFirst))
	for i := range newestFirst {
		events[len(newestFirst)-1-i] = newestFirst[i]
	}
	el.ring = events
	if needsCompact {
		return el.compactLocked()
	}
	return os.Chmod(el.path, 0o600)
}

// Len 은 현재 링의 건수다(기동 로그용).
func (el *EventLog) Len() int {
	el.mu.RLock()
	defer el.mu.RUnlock()
	return len(el.ring)
}

func eventStamp() string {
	return time.Now().UTC().Add(9 * time.Hour).Format("2006-01-02 15:04:05")
}

func truncateEventText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	if limit <= len("…") {
		return ""
	}
	cut := limit - len("…")
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + "…"
}

func eventString(ev map[string]any, key string) string {
	value, _ := ev[key].(string)
	return value
}

func normalizeEvent(ev map[string]any) map[string]any {
	out := map[string]any{
		"ts":    truncateEventText(eventString(ev, "ts"), 64),
		"host":  truncateEventText(eventString(ev, "host"), maxEventHostBytes),
		"label": truncateEventText(eventString(ev, "label"), maxEventLabelBytes),
		"kind":  truncateEventText(eventString(ev, "kind"), 64),
		"sev":   truncateEventText(eventString(ev, "sev"), 32),
		"desc":  truncateEventText(eventString(ev, "desc"), maxEventDescriptionBytes),
	}
	if eventString(ev, "kind") == "audit" {
		out["audit_id"] = truncateEventText(eventString(ev, "audit_id"), 64)
		out["action"] = truncateEventText(eventString(ev, "action"), 128)
		out["target"] = truncateEventText(eventString(ev, "target"), maxEventHostBytes)
		out["reason"] = truncateEventText(eventString(ev, "reason"), 2048)
		out["operator"] = truncateEventText(eventString(ev, "operator"), 64)
		out["phase"] = truncateEventText(eventString(ev, "phase"), 32)
	}
	return out
}

// marshalEventLine 은 JSON escaping까지 적용된 최종 한 줄을 절대 상한 안에 둔다.
// 제어문자는 한 바이트가 \\u00xx 여섯 바이트로 팽창하므로 입력 길이 제한만으로는
// 충분하지 않다.
func marshalEventLine(ev map[string]any) ([]byte, error) {
	shrinkOrder := []string{"desc", "label", "host", "kind", "sev", "ts"}
	for attempts := 0; attempts < 128; attempts++ {
		line, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		if len(line)+1 <= maxEventLineBytes {
			return append(line, '\n'), nil
		}
		shrunk := false
		for _, key := range shrinkOrder {
			value := eventString(ev, key)
			if value == "" {
				continue
			}
			nextLimit := len(value) * 3 / 4
			if nextLimit >= len(value) {
				nextLimit = len(value) - 1
			}
			ev[key] = truncateEventText(value, nextLimit)
			shrunk = true
			break
		}
		if !shrunk {
			break
		}
	}
	return nil, fmt.Errorf("event JSON exceeds %d-byte limit", maxEventLineBytes)
}

func (el *EventLog) compactLocked() error {
	dir := filepath.Dir(el.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(el.path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	var written int64
	for _, ev := range el.ring {
		line, err := marshalEventLine(ev)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if written+int64(len(line)) > el.maxFileBytes {
			_ = tmp.Close()
			return fmt.Errorf("event ring exceeds %d-byte file limit", el.maxFileBytes)
		}
		n, err := tmp.Write(line)
		written += int64(n)
		if err != nil {
			_ = tmp.Close()
			return err
		}
		if n != len(line) {
			_ = tmp.Close()
			return io.ErrShortWrite
		}
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceEventFile(tmpPath, el.path); err != nil {
		return err
	}
	removeTmp = false
	el.fileBytes = written
	return nil
}

func (el *EventLog) appendLineLocked(line []byte) error {
	if len(line) > maxEventLineBytes {
		return fmt.Errorf("event line exceeds %d-byte limit", maxEventLineBytes)
	}
	f, err := os.OpenFile(el.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	n, writeErr := f.Write(line)
	closeErr := f.Close()
	el.fileBytes += int64(n)
	if writeErr != nil {
		if info, err := os.Stat(el.path); err == nil {
			el.fileBytes = info.Size()
		}
		return writeErr
	}
	if n != len(line) {
		return io.ErrShortWrite
	}
	return closeErr
}

func (el *EventLog) recoverLoadLocked() error {
	buffered := append([]map[string]any(nil), el.ring...)
	el.ring = nil
	if err := el.load(); err != nil {
		el.ring = buffered
		return err
	}
	el.loadBlocked = false
	el.ring = append(el.ring, buffered...)
	if len(el.ring) > el.keep {
		el.ring = el.ring[len(el.ring)-el.keep:]
	}
	return el.compactLocked()
}

// Add 는 이벤트를 메모리 링에 항상 남기고 jsonl 쓰기 상태를 별도로 추적한다.
// 영속 실패가 폴러를 중단시키지는 않지만 health와 운영 로그에서 숨기지 않는다.
func (el *EventLog) Add(host, label, kind, sev, desc string) {
	ev := normalizeEvent(map[string]any{
		"ts": eventStamp(), "host": host, "label": label,
		"kind": kind, "sev": sev, "desc": desc,
	})
	line, marshalErr := marshalEventLine(ev)

	el.mu.Lock()
	el.ring = append(el.ring, ev)
	if len(el.ring) > el.keep {
		el.ring = el.ring[len(el.ring)-el.keep:]
	}
	err := marshalErr
	if err == nil {
		if el.loadBlocked {
			// 시작 시 읽지 못한 기존 파일은 현재 링으로 덮지 않는다. 다시 읽기에
			// 성공한 뒤 기존 이력과 버퍼를 합쳐 원자적으로 교체한다.
			err = el.recoverLoadLocked()
		} else if el.dirty || el.fileBytes+int64(len(line)) > el.maxFileBytes {
			// 이전 쓰기 실패 뒤에는 새 이벤트만 append하지 않고 메모리 링 전체를
			// 교체해 누락된 이벤트까지 함께 복구한다.
			err = el.compactLocked()
		} else {
			err = el.appendLineLocked(line)
		}
	}
	previousError := el.lastError
	previousDirty := el.dirty
	previousLoadBlocked := el.loadBlocked
	recovered := err == nil && (previousError != "" || previousDirty || previousLoadBlocked)
	if err != nil {
		if !el.loadBlocked {
			el.dirty = true
		}
		el.lastError = err.Error()
		el.lastErrorAt = time.Now()
	} else {
		el.dirty = false
		el.lastError = ""
		el.lastErrorAt = time.Time{}
		el.lastWriteAt = time.Now()
	}
	newError := err != nil && err.Error() != previousError
	forwardCh := el.forwardCh
	el.mu.Unlock()

	if forwardCh != nil {
		audit := AuditEvent{
			Timestamp:   time.Now().UTC(),
			Host:        eventString(ev, "host"),
			Label:       eventString(ev, "label"),
			Kind:        eventString(ev, "kind"),
			Severity:    eventString(ev, "sev"),
			Description: eventString(ev, "desc"),
		}
		select {
		case forwardCh <- audit:
		default:
			el.forwardDrops.Add(1)
		}
	}

	if newError {
		logf("warn", "events", fmt.Sprintf("이벤트 이력 저장 실패: %v", err))
	} else if recovered {
		logf("info", "events", "이벤트 이력 저장 복구")
	}
}

// RecordAudit atomically rewrites and fsyncs the bounded event log before it
// reports success. Records share EventLog's keep limit; this is a durable recent
// operations trail, not an immutable or indefinite compliance archive. Unlike
// Add, a failed audit write is never retained in the in-memory retry ring:
// callers may roll the corresponding mutation back, so a later retry must not
// resurrect a false "committed" record.
func (el *EventLog) RecordAudit(record AuditRecord) error {
	if el == nil {
		return errors.New("audit event log is unavailable")
	}
	record.ID = strings.TrimSpace(record.ID)
	record.Action = strings.TrimSpace(record.Action)
	record.Target = strings.TrimSpace(record.Target)
	record.Reason = strings.TrimSpace(record.Reason)
	record.Operator = strings.TrimSpace(record.Operator)
	record.Phase = strings.TrimSpace(record.Phase)
	if record.ID == "" || record.Action == "" || record.Target == "" || record.Operator == "" || record.Phase == "" {
		return errors.New("audit record is missing a required field")
	}
	if record.Reason == "" || !utf8.ValidString(record.Reason) || utf8.RuneCountInString(record.Reason) > maxAuditReasonRunes {
		return fmt.Errorf("audit reason must contain 1-%d valid Unicode characters", maxAuditReasonRunes)
	}
	// These byte limits exactly match normalizeEvent so structured fields are
	// rejected rather than silently truncated in the durable record.
	if len(record.ID) > 64 || len(record.Action) > 128 || len(record.Target) > maxEventHostBytes ||
		len(record.Operator) > 64 || len(record.Phase) > 32 || len(record.Reason) > 2048 {
		return errors.New("audit record field exceeds its limit")
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now()
	}
	ev := normalizeEvent(map[string]any{
		"ts": record.Timestamp.UTC().Format(time.RFC3339Nano), "host": record.Target,
		"label": record.Operator, "kind": "audit", "sev": "info", "desc": record.Reason,
		"audit_id": record.ID, "action": record.Action, "target": record.Target,
		"reason": record.Reason, "operator": record.Operator, "phase": record.Phase,
	})
	if _, err := marshalEventLine(ev); err != nil {
		return err
	}

	el.mu.Lock()
	if el.loadBlocked {
		err := errors.New("audit event log is blocked by an unreadable existing history")
		el.lastError = err.Error()
		el.lastErrorAt = time.Now()
		el.mu.Unlock()
		return err
	}
	previousRing := el.ring
	candidate := append(append([]map[string]any(nil), previousRing...), ev)
	if len(candidate) > el.keep {
		candidate = candidate[len(candidate)-el.keep:]
	}
	el.ring = candidate
	err := el.compactLocked()
	if err != nil {
		el.ring = previousRing
		el.dirty = true
		el.lastError = err.Error()
		el.lastErrorAt = time.Now()
		el.mu.Unlock()
		logf("warn", "events", fmt.Sprintf("감사 이벤트 저장 실패: %v", err))
		return err
	}
	el.dirty = false
	el.lastError = ""
	el.lastErrorAt = time.Time{}
	el.lastWriteAt = time.Now()
	el.mu.Unlock()
	return nil
}

// Status 는 인증된 상세 health에 노출할 영속 저장 상태다. 파일 경로와 이벤트
// 내용은 포함하지 않는다.
func (el *EventLog) Status() map[string]any {
	el.mu.RLock()
	defer el.mu.RUnlock()
	var lastWrite, lastErrAt any
	if !el.lastWriteAt.IsZero() {
		lastWrite = el.lastWriteAt.UTC().Format(time.RFC3339)
	}
	if !el.lastErrorAt.IsZero() {
		lastErrAt = el.lastErrorAt.UTC().Format(time.RFC3339)
	}
	var lastErr any
	if el.lastError != "" {
		lastErr = el.lastError
	}
	var sinkErr any
	if el.lastSinkErr != "" {
		sinkErr = el.lastSinkErr
	}
	queueDepth := 0
	if el.forwardCh != nil {
		queueDepth = len(el.forwardCh)
	}
	return map[string]any{
		"healthy":        !el.dirty && !el.loadBlocked && el.lastError == "",
		"buffered":       len(el.ring),
		"dirty":          el.dirty,
		"load_blocked":   el.loadBlocked,
		"file_bytes":     el.fileBytes,
		"max_file_bytes": el.maxFileBytes,
		"last_write_at":  lastWrite,
		"last_error":     lastErr,
		"last_error_at":  lastErrAt,
		"forwarder": map[string]any{
			"enabled":     el.forwardCh != nil,
			"healthy":     el.forwardErrs.Load() == 0 && el.forwardDrops.Load() == 0,
			"queue_depth": queueDepth,
			"sent":        el.forwardSent.Load(),
			"errors":      el.forwardErrs.Load(),
			"dropped":     el.forwardDrops.Load(),
			"last_error":  sinkErr,
		},
	}
}

// List 는 최신순 최대 limit 건을 돌려준다.
func (el *EventLog) List(limit int) []any {
	el.mu.RLock()
	defer el.mu.RUnlock()
	n := len(el.ring)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]any, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, el.ring[n-1-i])
	}
	return out
}

// EventWatcher 는 10초 주기로 장비 뷰(FT+엣지)를 diff 해 이벤트를 EventLog 에
// 남긴다. 첫 스냅은 기준선으로만 쓴다(부팅 때 전 장비 '등록' 스팸 방지).
// 경보 diff 키는 (name, desc, sev) — DEVICE_STATE 는 상태 전이 이벤트가 대신
// 말하므로 제외한다.
type EventWatcher struct {
	ev     *EventLog
	cache  *FleetCache
	states []*ClusterState
	// edgeDevices 는 엣지 장비 스냅샷 공급 함수다(edge.Worker.LatestDevices 에 해당).
	edgeDevices func() []map[string]any
	// display 는 클러스터 key → 표시 라벨 공급 함수다.
	display func(key string) string

	snap   map[string]watchEntry
	primed bool
	t0     time.Time
}

type watchEntry struct {
	status string
	alerts map[[3]string]bool
	label  string
}

// bootGrace 는 기동 유예(초)다 — FT 티어가 순차 채워지며 기존 경보가 전부
// '신규 발생'으로 찍히는 재시작 노이즈를 흡수한다.
const bootGrace = 120.0

var stateLabelKO = map[string]string{"op": "가동", "deg": "저하", "down": "오프라인"}

// NewEventWatcher 는 이벤트 워처를 만든다.
func NewEventWatcher(ev *EventLog, cache *FleetCache, states []*ClusterState,
	edgeDevices func() []map[string]any, display func(key string) string) *EventWatcher {
	return &EventWatcher{
		ev: ev, cache: cache, states: states,
		edgeDevices: edgeDevices, display: display,
		snap: map[string]watchEntry{}, t0: time.Now(),
	}
}

// Start 는 ctx 종료까지 10초 주기로 diff 를 돌린다.
func (w *EventWatcher) Start(ctx context.Context) {
	for ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("error", "events", fmt.Sprintf("이벤트 워처 실패: %v\n%s", r, debug.Stack()))
				}
			}()
			w.round()
		}()
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}

// devices 는 현재 장비 뷰(FT 변환 + 엣지)를 모은다.
func (w *EventWatcher) devices() []map[string]any {
	var devs []map[string]any
	fleet, _, _ := w.cache.Snapshot()
	if fleet != nil {
		func() {
			defer func() { _ = recover() }()
			disp := map[string]DisplayMeta{}
			for _, st := range w.states {
				disp[st.Key] = DisplayMeta{Label: w.display(st.Key)}
			}
			out := BuildDevices(fleet, disp, 30)
			for _, d := range listVal(out["devices"]) {
				if dm := dictVal(d); dm != nil {
					devs = append(devs, dm)
				}
			}
		}()
	}
	if w.edgeDevices != nil {
		devs = append(devs, w.edgeDevices()...)
	}
	return devs
}

type alertKey [3]string

func (w *EventWatcher) round() {
	cur := map[string]watchEntry{}
	for _, d := range w.devices() {
		host := strVal(d["host"])
		if host == "" {
			host = strVal(d["id"])
		}
		if host == "" {
			continue
		}
		m := dictVal(d["meta"])
		al := map[[3]string]bool{}
		for _, av := range listVal(m["alerts"]) {
			a := dictVal(av)
			if a == nil {
				continue
			}
			if strVal(a["name"]) == "DEVICE_STATE" {
				continue
			}
			sev := strVal(a["sev"])
			if sev == "" {
				sev = strVal(a["severity"])
			}
			if sev == "" {
				sev = "warning"
			}
			if sev != "critical" && sev != "warning" && sev != "info" {
				sev = "warning"
			}
			al[alertKey{strVal(a["name"]), strVal(a["desc"]), sev}] = true
		}
		label := host
		if m != nil && strVal(m["label"]) != "" {
			label = strVal(m["label"])
		}
		cur[host] = watchEntry{status: strVal(d["status"]), alerts: al, label: label}
	}
	if w.primed && time.Since(w.t0).Seconds() > bootGrace {
		for host, c := range cur {
			p, ok := w.snap[host]
			if !ok {
				w.ev.Add(host, c.label, "new", "info", "장비 등록됨")
				continue
			}
			if p.status != c.status {
				sev := "info"
				if c.status == "down" {
					sev = "critical"
				} else if c.status == "deg" {
					sev = "warning"
				}
				w.ev.Add(host, c.label, "state", sev,
					fmt.Sprintf("상태 %s → %s", stateLabel(p.status), stateLabel(c.status)))
			}
			for _, k := range sortedAlertDiff(c.alerts, p.alerts) {
				desc := k[1]
				if desc == "" {
					desc = k[0]
				}
				w.ev.Add(host, c.label, "alert", k[2], desc)
			}
			for _, k := range sortedAlertDiff(p.alerts, c.alerts) {
				desc := k[1]
				if desc == "" {
					desc = k[0]
				}
				w.ev.Add(host, c.label, "clear", "info", "해제: "+desc)
			}
		}
		for host, p := range w.snap {
			if _, ok := cur[host]; !ok {
				w.ev.Add(host, p.label, "gone", "info", "장비 제거됨")
			}
		}
	}
	w.snap = cur
	w.primed = true
}

func stateLabel(s string) string {
	if v, ok := stateLabelKO[s]; ok {
		return v
	}
	return s
}

// sortedAlertDiff 는 a 에만 있는 경보 키를 정렬해 돌려준다(Python sorted(set 차)).
func sortedAlertDiff(a, b map[[3]string]bool) []alertKey {
	var out []alertKey
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		for x := 0; x < 3; x++ {
			if out[i][x] != out[j][x] {
				return out[i][x] < out[j][x]
			}
		}
		return false
	})
	return out
}
