package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"serverdesk/internal/config"
	"serverdesk/internal/poller"
)

func TestAvailabilityStatusesExcludeUnobservedAndStaleSources(t *testing.T) {
	cfg := &config.ClusterConfig{Key: "ft"}
	cfg.Intervals.Fast = 10
	state := poller.NewClusterState(cfg, 50)
	state.Mark("fast", "")
	age := 0.0
	cluster := map[string]any{"key": "ft", "stale": false, "nodes": []any{map[string]any{"state": "running"}}, "collection": map[string]any{"fast_age_secs": &age, "last_success": map[string]any{"fast": time.Now().Unix()}, "errors": map[string]any{}}}
	fleet := map[string]any{"clusters": []any{cluster}}
	edges := []map[string]any{{"id": "edge", "status": "down", "meta": map[string]any{"alerts": []any{
		map[string]any{"name": "DISK_FAULT", "severity": "critical", "observed": false},
	}}}}
	edgeStatus := poller.EdgeCollectorStatus{Configured: 1, Observed: 1, LastRoundAt: time.Now()}
	if got := availabilityStatuses(fleet, []*poller.ClusterState{state}, edges, edgeStatus); len(got) != 2 {
		t.Fatalf("fresh sources omitted: %v", got)
	}
	cluster["stale"] = true
	edgeStatus.LastRoundAt = time.Now().Add(-131 * time.Second)
	if got := availabilityStatuses(fleet, []*poller.ClusterState{state}, edges, edgeStatus); len(got) != 0 {
		t.Fatalf("stale sources counted: %v", got)
	}
	cluster["stale"] = false
	cluster["collection"].(map[string]any)["errors"] = map[string]any{"fast": "failed"}
	edgeStatus.LastRoundAt = time.Time{}
	if got := availabilityStatuses(fleet, []*poller.ClusterState{state}, edges, edgeStatus); len(got) != 0 {
		t.Fatalf("failed/unobserved sources counted: %v", got)
	}
	cluster["collection"].(map[string]any)["errors"] = map[string]any{"slow": "failed"}
	cfg.Intervals.Fast = -1 // Live state has exceeded its configured freshness window even if cache is frozen.
	if got := availabilityStatuses(fleet, []*poller.ClusterState{state}, nil, poller.EdgeCollectorStatus{}); len(got) != 0 {
		t.Fatalf("frozen cache counted stale live state: %v", got)
	}
	cfg.Intervals.Fast = 10
	cluster["collection"].(map[string]any)["last_success"] = map[string]any{"fast": time.Now().Add(-time.Minute).Unix()}
	if got := availabilityStatuses(fleet, []*poller.ClusterState{state}, nil, poller.EdgeCollectorStatus{}); len(got) != 0 {
		t.Fatalf("old cache with fresh live state counted: %v", got)
	}
	cluster["collection"].(map[string]any)["last_success"] = map[string]any{"fast": time.Now().Unix()}
	edgeStatus.LastRoundAt = time.Now()
	if got := availabilityStatuses(fleet, []*poller.ClusterState{state}, edges, edgeStatus); len(got) != 2 {
		t.Fatalf("unrelated tier failure blocked fast status: %v", got)
	}
}

func TestShutdownWaitsForActiveRequestAndWorkerPersistence(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "complete")
	})}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(listener)
	defer srv.Close()
	response := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			data, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			err = readErr
			if string(data) != "complete" {
				err = io.ErrUnexpectedEOF
			}
		}
		response <- err
	}()
	<-entered
	workerCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var order []string
	wg.Add(1)
	go func() { defer wg.Done(); <-workerCtx.Done(); order = append(order, "worker") }()
	done := make(chan error, 1)
	go func() {
		done <- shutdownServer(srv, cancel, &wg, func() error { order = append(order, "flush"); return nil }, time.Second)
	}()
	select {
	case err := <-done:
		t.Fatalf("returned before active request drained: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"worker", "flush"}) {
		t.Fatalf("persistence order=%v", order)
	}
}

func TestShutdownBoundsFinalPersistenceAfterDrain(t *testing.T) {
	release, entered := make(chan struct{}), make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- shutdownServerContext(ctx, &http.Server{}, func() {}, &sync.WaitGroup{}, func() error { close(entered); <-release; return nil })
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("persistence did not start")
	}
	cancel() // Only stop waiting after persistence has demonstrably begun.
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("persistence wait status: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("persistence wait not bounded after cancellation")
	}
}

func TestShutdownReportsActualFinalSaveFailure(t *testing.T) {
	avail := poller.NewAvailTracker(filepath.Join(t.TempDir(), "missing"), nil)
	err := shutdownServer(&http.Server{}, func() {}, &sync.WaitGroup{}, avail.FlushWithError, time.Second)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("final save failure hidden or mislabeled: %v", err)
	}
}

func TestShutdownReportsEdgeJoinFailure(t *testing.T) {
	err := shutdownServer(&http.Server{}, func() {}, &sync.WaitGroup{}, func() error { return nil }, time.Second, func(context.Context) error { return io.ErrUnexpectedEOF })
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("edge worker join failure hidden: %v", err)
	}
}

func TestShutdownDeadlineClosesHTTPAndBoundsPersistence(t *testing.T) {
	for _, blockWorker := range []bool{false, true} {
		t.Run(map[bool]string{false: "persistence", true: "worker"}[blockWorker], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			srv := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { close(entered); <-release })}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			go srv.Serve(listener)
			defer srv.Close()
			response := make(chan error, 1)
			go func() {
				resp, err := http.Get("http://" + listener.Addr().String())
				if resp != nil {
					resp.Body.Close()
				}
				response <- err
			}()
			<-entered
			var wg sync.WaitGroup
			if blockWorker {
				wg.Add(1)
				go func() { defer wg.Done(); <-release }()
			}
			start := time.Now()
			err = shutdownServer(srv, func() {}, &wg, func() error { <-release; return nil }, 40*time.Millisecond)
			if err == nil {
				t.Fatal("deadline failure not reported")
			}
			if time.Since(start) > time.Second {
				t.Fatal("shutdown exceeded bounded deadline")
			}
			select {
			case err := <-response:
				if err == nil {
					t.Fatal("HTTP not force-closed")
				}
			case <-time.After(time.Second):
				t.Fatal("active connection left open")
			}
			close(release)
		})
	}
}
