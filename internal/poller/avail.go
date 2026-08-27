package poller

// AvailTracker — 실측 가용성 트래커(avail_tracker.py 포트).
//
// availN 이 현재 상태의 명목 환산(op=99.99/deg=99.9/down=99.0)이던 것을,
// 60초 샘플로 장비별 관측시간·다운시간을 일 단위(KST)로 누적해 디스크에 영속하고
// (availability.json, 재시작 생존), 최근 30일 실측 가용성으로 교체한다.
//
// 정의: 가용 = op 또는 deg(서비스는 계속됨), 불가용 = down. 폴러 미가동 구간은
// 관측에서 제외(모르는 시간을 up 으로도 down 으로도 치지 않는다 — 정직 우선).

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

const (
	availSampleSec       = 60  // 샘플 주기
	availFlushEvery      = 5   // N 샘플마다 디스크 flush
	availGapCap          = 180 // 샘플 간격이 이보다 크면 폴러 미가동 — 그 구간은 관측 제외
	defaultRetentionDays = 90
	minRetentionDays     = 30
	maxRetentionDays     = 365
	availMinObsSec       = 600 // 관측 10분 미만이면 실측 대신 명목값 유지
	kstOffset            = 9 * 3600
)

// AvailTracker 는 상태 샘플러 + 실측 가용성 계산이다.
// GetStatuses: () -> [(id, status)].
type AvailTracker struct {
	path          string
	retentionDays int
	// GetStatuses 는 (장비 id, "op"|"deg"|"down") 목록을 돌려준다.
	GetStatuses func() [][2]string

	mu    sync.Mutex
	state map[string]*availRec
	last  float64
	n     int
}

type availRec struct {
	Days map[string][]float64 `json:"days"`
}

// kstDay 는 epoch 의 KST 일자 문자열이다.
func kstDay(epoch float64) string {
	return time.Unix(int64(epoch), 0).UTC().Add(kstOffset * time.Second).Format("2006-01-02")
}

// NewAvailTracker 는 기본 90일 보존 설정으로 트래커를 만든다.
func NewAvailTracker(runtimeDir string, getStatuses func() [][2]string) *AvailTracker {
	return NewAvailTrackerWithRetention(runtimeDir, defaultRetentionDays, getStatuses)
}

// NewAvailTrackerWithRetention 은 지정된 보존 기간(30~365일)으로 트래커를 만든다.
func NewAvailTrackerWithRetention(runtimeDir string, retentionDays int, getStatuses func() [][2]string) *AvailTracker {
	if retentionDays < minRetentionDays {
		retentionDays = defaultRetentionDays
	}
	if retentionDays > maxRetentionDays {
		retentionDays = maxRetentionDays
	}
	t := &AvailTracker{
		path:          runtimeDir + string(os.PathSeparator) + "availability.json",
		retentionDays: retentionDays,
		GetStatuses:   getStatuses,
		state:         map[string]*availRec{},
	}
	if data, err := os.ReadFile(t.path); err == nil {
		var raw map[string]*availRec
		if json.Unmarshal(data, &raw) == nil && raw != nil {
			for id, rec := range raw {
				if rec != nil && rec.Days != nil {
					t.state[id] = rec
				}
			}
		}
	}
	return t
}

// Path 는 영속 파일 경로다(기동 로그용).
func (t *AvailTracker) Path() string { return t.path }

// flush 는 tmp + rename 으로 원자 저장한다. 실패는 경고만 — 저장 실패가
// 폴러를 죽이면 안 된다.
func (t *AvailTracker) flush() {
	b, err := json.Marshal(t.state)
	if err != nil {
		return
	}
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		logf("warn", "avail", fmt.Sprintf("가용성 이력 저장 실패: %v", err))
		return
	}
	if err := os.Rename(tmp, t.path); err != nil {
		logf("warn", "avail", fmt.Sprintf("가용성 이력 교체 실패: %v", err))
	}
}

// Start 는 ctx 종료까지 60초 샘플 루프를 돌고, 끝나면 마지막 flush 를 한다.
func (t *AvailTracker) Start(ctx context.Context) {
	for ctx.Err() == nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					logf("error", "avail", fmt.Sprintf("가용성 샘플 실패: %v\n%s", r, debug.Stack()))
				}
			}()
			t.Sample()
		}()
		select {
		case <-ctx.Done():
		case <-time.After(availSampleSec * time.Second):
		}
	}
	t.mu.Lock()
	t.flush()
	t.mu.Unlock()
}

// Flush 는 즉시 디스크에 저장한다(종료 경로에서 명시 호출용).
func (t *AvailTracker) Flush() {
	t.mu.Lock()
	t.flush()
	t.mu.Unlock()
}

// Sample 은 현재 장비 상태를 1회 누적한다.
func (t *AvailTracker) Sample() {
	now := nowFloat()
	dt := float64(availSampleSec)
	if t.last != 0 {
		dt = now - t.last
	}
	t.last = now
	if dt > availGapCap { // 폴러 미가동 구간 — 관측 제외
		dt = availSampleSec
	}
	day := kstDay(now)
	var statuses [][2]string
	if t.GetStatuses != nil {
		statuses = t.GetStatuses()
	}
	t.mu.Lock()
	for _, s := range statuses {
		devID, status := s[0], s[1]
		rec := t.state[devID]
		if rec == nil {
			rec = &availRec{Days: map[string][]float64{}}
			t.state[devID] = rec
		}
		cell := rec.Days[day]
		if cell == nil {
			cell = []float64{0, 0}
		}
		cell[0] += dt
		if status == "down" {
			cell[1] += dt
		}
		rec.Days[day] = cell
	}
	t.n++
	if t.n%availFlushEvery == 0 {
		t.pruneLocked()
		t.flush()
	}
	t.mu.Unlock()
}

// pruneLocked 은 보존 기간을 넘은 일자 버킷과 빈 장비 레코드를 버린다.
func (t *AvailTracker) pruneLocked() {
	cut := kstDay(nowFloat() - float64(t.retentionDays)*86400)
	for id, rec := range t.state {
		for k := range rec.Days {
			if k < cut {
				delete(rec.Days, k)
			}
		}
		if len(rec.Days) == 0 {
			delete(t.state, id)
		}
	}
}

func (t *AvailTracker) windowDays(days int) int {
	if days <= 0 {
		return t.retentionDays
	}
	if days > t.retentionDays {
		return t.retentionDays
	}
	return days
}

// Apply 는 /api/devices 응답의 device[] 에 실측 availN·availDays 를 주입한다.
// 관측이 10분 미만인 장비는 명목값을 유지한다.
func (t *AvailTracker) Apply(devices []map[string]any) {
	cut := kstDay(nowFloat() - float64(t.retentionDays)*86400)
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, d := range devices {
		rec := t.state[strVal(d["id"])]
		if rec == nil {
			continue
		}
		tot, down := 0.0, 0.0
		for k, cell := range rec.Days {
			if k >= cut && len(cell) >= 2 {
				tot += cell[0]
				down += cell[1]
			}
		}
		if tot < availMinObsSec {
			continue
		}
		d["availN"] = round3(100.0 * (1.0 - down/tot))
		days := math.RoundToEven(tot/86400.0*100) / 100
		if days == 0 {
			days = 0.01
		}
		d["availDays"] = days
	}
}

// AvailCSVRow 는 가용성 CSV 의 한 행이다.
type AvailCSVRow struct {
	Day, Device string
	Avail       float64
	ObservedSec float64
}

// DeviceSLARow 는 지정 기간의 장비별 SLA 집계다.
type DeviceSLARow struct {
	Device       string
	Avail        float64
	ObservedSec  float64
	DownSec      float64
	ObservedDays int
}

// CSVSnapshot 은 전체 보존 창의 일자별 실측 가용성을 돌려준다.
func (t *AvailTracker) CSVSnapshot() []AvailCSVRow {
	return t.CSVSnapshotDays(0)
}

// CSVSnapshotDays 는 지정 기간의 일자별 실측 가용성을 돌려준다.
func (t *AvailTracker) CSVSnapshotDays(days int) []AvailCSVRow {
	cut := kstDay(nowFloat() - float64(t.windowDays(days))*86400)
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []AvailCSVRow
	for id, rec := range t.state {
		for day, cell := range rec.Days {
			if day < cut || len(cell) < 2 || cell[0] < availMinObsSec {
				continue
			}
			out = append(out, AvailCSVRow{Day: day, Device: id,
				Avail: round3(100.0 * (1.0 - cell[1]/cell[0])), ObservedSec: cell[0]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Day != out[j].Day {
			return out[i].Day < out[j].Day
		}
		return out[i].Device < out[j].Device
	})
	return out
}

// SLASnapshot 은 지정 기간의 장비별 가용성 집계를 돌려준다.
func (t *AvailTracker) SLASnapshot(days int) []DeviceSLARow {
	cut := kstDay(nowFloat() - float64(t.windowDays(days))*86400)
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []DeviceSLARow
	for id, rec := range t.state {
		tot, down, observedDays := 0.0, 0.0, 0
		for day, cell := range rec.Days {
			if day < cut || len(cell) < 2 || cell[0] < availMinObsSec {
				continue
			}
			tot += cell[0]
			down += cell[1]
			observedDays++
		}
		if tot < availMinObsSec {
			continue
		}
		out = append(out, DeviceSLARow{Device: id,
			Avail: round3(100.0 * (1.0 - down/tot)), ObservedSec: tot,
			DownSec: down, ObservedDays: observedDays})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Device < out[j].Device })
	return out
}

// round3 은 Python round(x, 3) 에 해당한다(round-half-even).
func round3(x float64) float64 {
	return math.RoundToEven(x*1000) / 1000
}
