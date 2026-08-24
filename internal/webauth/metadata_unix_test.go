//go:build !windows

package webauth

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSetPasswordPreservesOwnerAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if _, err := InitializeCredentials(path); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(path, 65534, 65534); err != nil {
			t.Logf("owner-change subcase unavailable: %v", err)
		}
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetPassword(path, "replacement password"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	bst, bok := before.Sys().(*syscall.Stat_t)
	ast, aok := after.Sys().(*syscall.Stat_t)
	if !bok || !aok || bst.Uid != ast.Uid || bst.Gid != ast.Gid || before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("metadata changed: before=%v/%#v after=%v/%#v", before.Mode(), bst, after.Mode(), ast)
	}
}
