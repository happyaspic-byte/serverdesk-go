package poller

import "testing"

func TestTopologyFallbackRetainsTimestampAndReportsFailure(t *testing.T) {
	cache := NewFleetCache()
	good, broken := populatedState(t), populatedState(t)
	broken.Key = "broken"
	cache.Update([]*ClusterState{good, broken})
	// Seed the prior complete graph time; successive wall-clock reads may tie.
	previous := cache.snap.Load()
	seeded := *previous
	seeded.topoTS, seeded.fullTS = 1234, 1234
	cache.snap.Store(&seeded)
	_, previousFlat, _ := cache.Snapshot()
	previousFull, previousTS := cache.SnapshotFull()
	broken.Cfg = nil
	cache.Update([]*ClusterState{good, broken})
	_, flat, fleetTS := cache.Snapshot()
	_, flatTS := cache.SnapshotTopology()
	full, ts := cache.SnapshotFull()
	if flatTS != previousTS || fleetTS <= previousTS {
		t.Fatalf("flat/fleet timestamps not independent: %v/%v", flatTS, fleetTS)
	}
	if flat["stale"] != true || flat["last_error"] == nil {
		t.Fatalf("flat fallback presented fresh: %v", flat)
	}
	if full["stale"] != true || full["last_error"] == nil {
		t.Fatalf("partial full topology presented fresh: %v", full)
	}
	if ts != previousTS || full["generated_at"] != previousFull["generated_at"] {
		t.Fatalf("full fallback refreshed timestamp: %v -> %v", previousTS, ts)
	}
	if previousFlat["stale"] == true || previousFull["stale"] == true {
		t.Fatal("fallback mutated published snapshot")
	}
	if flat["generated_at"] != previousFlat["generated_at"] {
		t.Fatal("flat fallback refreshed graph timestamp")
	}
}

func TestTopologyInitialPartialBuildReportsFailure(t *testing.T) {
	cache := NewFleetCache()
	broken := populatedState(t)
	broken.Cfg = nil
	cache.Update([]*ClusterState{broken})
	_, flat, _ := cache.Snapshot()
	full, ts := cache.SnapshotFull()
	if flat["stale"] != true || flat["last_error"] == nil {
		t.Fatalf("initial partial flat graph presented fresh: %v", flat)
	}
	if full["stale"] != true || full["last_error"] == nil || ts != 0 {
		t.Fatalf("initial partial full graph presented fresh: %v/%v", full, ts)
	}
}
