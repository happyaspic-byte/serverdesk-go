//go:build !windows

package poller

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"serverdesk/internal/avcli"
	"serverdesk/internal/config"
)

// TestAvcliWorkerCapturedTierLifecycle runs every production collection tier through the actual
// subprocess/XML boundary using captured, sanitized device responses.
func TestAvcliWorkerCapturedTierLifecycle(t *testing.T) {
	fixtureDir, err := filepath.Abs(filepath.Join("..", "avcli", "testdata"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AVCLI_FIXTURE_DIR", fixtureDir)
	stub := filepath.Join(t.TempDir(), "avcli")
	script := `#!/bin/sh
cmd=
for arg do
  case "$arg" in
    node-info|alert-info|unit-info|vm-info|network-info|storage-info-v2|storage-info|volume-info|image-container-info|LED-info|license-info)
      cmd=$arg
      ;;
  esac
done
case "$cmd" in
  node-info) cat "$AVCLI_FIXTURE_DIR/node_info.xml" ;;
  alert-info) cat "$AVCLI_FIXTURE_DIR/alert_info.xml" ;;
  unit-info) cat "$AVCLI_FIXTURE_DIR/unit_info.xml" ;;
  vm-info) cat "$AVCLI_FIXTURE_DIR/vm_info.xml" ;;
  network-info) cat "$AVCLI_FIXTURE_DIR/network_info.xml" ;;
  storage-info-v2|storage-info) cat "$AVCLI_FIXTURE_DIR/storage_info_v2.xml" ;;
  volume-info) cat "$AVCLI_FIXTURE_DIR/volume_info.xml" ;;
  image-container-info) cat "$AVCLI_FIXTURE_DIR/image_container_info.xml" ;;
  LED-info) cat "$AVCLI_FIXTURE_DIR/led_info.xml" ;;
  license-info) cat "$AVCLI_FIXTURE_DIR/license_info.xml" ;;
  *) echo "unsupported command" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ClusterConfig{
		Key: "captured", MgmtIP: "192.0.2.50", AdminUser: "admin", AdminPassword: "test-only",
		HistoryPoints: 10,
		Intervals:     config.Intervals{Fast: 60, Slow: 300, Static: 86400, OS: 10, SNMP: 60},
	}
	state := NewClusterState(cfg, 10)
	client := avcli.NewClient(cfg.Key, cfg.MgmtIP, cfg.AdminUser, cfg.AdminPassword)
	client.Bin = stub
	client.Timeout = 5 * time.Second
	client.RetryDelay = time.Millisecond

	NewAvcliWorker(state, client).RunTiersOnce()

	if nodes, vms := state.NodeCounts(); nodes != 2 || vms != 2 {
		t.Fatalf("captured worker counts nodes=%d vms=%d", nodes, vms)
	}
	for _, tier := range []string{"fast", "slow", "static"} {
		if got := state.TierErr(tier); got != "" {
			t.Fatalf("%s tier error: %s", tier, got)
		}
	}
	snapshot := state.snapshot()
	if len(snapshot.networks) == 0 || len(snapshot.sgroups) == 0 ||
		len(snapshot.volumes) == 0 || len(snapshot.containers) == 0 ||
		len(snapshot.alerts) == 0 || snapshot.license == nil || snapshot.unit == nil {
		t.Fatalf("captured worker produced an incomplete snapshot: %+v", snapshot)
	}
	if stats := client.Stats(); stats.Errors != 0 || stats.Retries != 0 || stats.Calls < 9 {
		t.Fatalf("unexpected avcli stats: %+v", stats)
	}
}
