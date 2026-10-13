package poller

import (
	"context"
	"os"
	"path/filepath"
	"serverdesk/internal/config"
	"serverdesk/internal/snmp"
	"testing"
)

func TestTrapRawAndRestoredCommunityIsTransient(t *testing.T) {
	raw := TrapToRaw(snmp.Trap{Community: "secret", Src: "127.0.0.1", OID: "1.3.6.1.6.3.1.1.5.1"})
	if _, ok := raw["community"]; ok {
		t.Error("raw event retains community")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "traps.jsonl")
	os.WriteFile(path, []byte(`{"ts":1,"src":"127.0.0.1","_route":"a","trap_oid":"1.3.6.1.6.3.1.1.5.1","community":"secret"}`+"\n"), 0600)
	st := NewClusterState(&config.ClusterConfig{Key: "a"}, 10)
	if n := restoreTraps(path, map[string]*ClusterState{"a": st}, func(string) *ClusterState { return st }); n != 1 {
		t.Fatalf("restored=%d", n)
	}
	if _, ok := st.traps[0]["community"]; ok {
		t.Error("restored raw event retains community")
	}
	if st.traps[0]["trap_oid"] != "1.3.6.1.6.3.1.1.5.1" {
		t.Error("legacy oid lost")
	}
}

func TestTrapPersistenceRejectsRelativeEscape(t *testing.T) {
	dir := t.TempDir()
	runtime := filepath.Join(dir, "runtime")
	os.Mkdir(runtime, 0700)
	victim := filepath.Join(dir, "victim.jsonl")
	original := []byte(`{"ts":1,"src":"127.0.0.1"}` + "\n")
	os.WriteFile(victim, original, 0600)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt := StartTrapReceiver(ctx, true, "127.0.0.1", 0, "s", "../victim.jsonl", 10, "", runtime, nil)
	if rt != nil {
		defer rt.Receiver.Close()
		rt.Store.Add(snmp.Trap{Ts: 2})
	}
	data, _ := os.ReadFile(victim)
	if string(data) != string(original) {
		t.Fatal("relative escape modified file")
	}
	if rt != nil {
		t.Error("relative escape not refused")
	}
}
