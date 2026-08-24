//go:build !windows

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestRequireReferencesRejectsPlaintext(t *testing.T) {
	_, err := Parse([]byte(`{
		"secret_policy":"require-references",
		"clusters":[{"key":"ft","mgmt_ip":"10.0.0.1","admin_password":"plain-password"}]
	}`))
	if err == nil || !strings.Contains(err.Error(), "contains plaintext") {
		t.Fatalf("plaintext error = %v", err)
	}
}

func TestSecretReferenceResolution(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ft-admin"), []byte("resolved-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SERVERDESK_CREDENTIALS_DIRECTORY", dir)
	cfg, err := Parse([]byte(`{
		"secret_policy":"require-references",
		"clusters":[{"key":"ft","mgmt_ip":"10.0.0.1","admin_password":"secret://ft-admin"}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Clusters[0].AdminPassword; got != "resolved-password" {
		t.Fatalf("resolved password = %q", got)
	}
}

func TestSecretReferenceRejectsUnsafeNameAndPermissions(t *testing.T) {
	if _, _, err := secretReferenceName("secret://../escape"); err == nil {
		t.Fatal("accepted path traversal reference")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "too-open"), []byte("password"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(dir, "too-open"); err == nil {
		t.Fatal("accepted group/other-readable credential")
	}
}

func TestStoreCredentialIsCreateOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := StoreCredential(dir, "device-password-v1", "first-value"); err != nil {
		t.Fatal(err)
	}
	if err := StoreCredential(dir, "device-password-v1", "first-value"); err != nil {
		t.Fatalf("same-value retry: %v", err)
	}
	if err := StoreCredential(dir, "device-password-v1", "different-value"); err == nil {
		t.Fatal("overwrote an existing credential")
	}
	if err := StoreCredential(dir, "../escape", "value"); err == nil {
		t.Fatal("accepted unsafe credential name")
	}
}

func TestMigratePlaintextSecrets(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	credentialDir := filepath.Join(dir, "credentials")
	configPath := filepath.Join(dir, "config.local.json")
	input := `{
		"secret_policy":"allow-plaintext",
		"clusters":[{"key":"ft-a","mgmt_ip":"10.0.0.1","admin_password":"cluster-password"}],
		"edge_devices":[{"key":"pve-a","kind":"proxmox","password":"pve-password"}]
	}`
	if err := os.WriteFile(configPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := MigratePlaintextSecrets(configPath, credentialDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 || len(result.Names) != 2 {
		t.Fatalf("migration result = %+v", result)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cluster-password") || strings.Contains(string(data), "pve-password") {
		t.Fatalf("migrated config retained plaintext: %s", data)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["secret_policy"] != SecretPolicyRequireReferences {
		t.Fatalf("secret policy = %v", doc["secret_policy"])
	}
	t.Setenv("SERVERDESK_CREDENTIALS_DIRECTORY", credentialDir)
	loaded, err := Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Clusters[0].AdminPassword != "cluster-password" || loaded.EdgeDevices[0].Password != "pve-password" {
		t.Fatal("migrated credentials did not round-trip")
	}
	if _, err := MigratePlaintextSecrets(configPath, credentialDir); err != nil {
		t.Fatalf("idempotent rerun: %v", err)
	}
}

func TestMigrationPreservesMetadataAndUsesSafeDestinations(t *testing.T) {
	dir := t.TempDir()
	credentialDir := filepath.Join(dir, "credentials")
	configPath := filepath.Join(dir, "config.local.json")
	input := []byte(`{"secret_policy":"allow-plaintext","edge_devices":[{"key":"pve","password":"value"}]}`)
	if err := os.WriteFile(configPath, input, 0o640); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		if err := os.Chown(configPath, 65534, 65534); err != nil {
			t.Logf("owner-change subcase unavailable: %v", err)
		}
	}
	before, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	oldPredictable := configPath + ".secrets.tmp"
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, oldPredictable); err != nil {
		t.Fatal(err)
	}
	if _, err := MigratePlaintextSecrets(configPath, credentialDir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	bst, bok := before.Sys().(*syscall.Stat_t)
	ast, aok := after.Sys().(*syscall.Stat_t)
	if !bok || !aok || bst.Uid != ast.Uid || bst.Gid != ast.Gid || before.Mode().Perm() != after.Mode().Perm() {
		t.Fatalf("metadata changed: before=%v/%#v after=%v/%#v", before.Mode(), bst, after.Mode(), ast)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "unchanged" {
		t.Fatalf("predictable temp symlink target changed: %q, %v", got, err)
	}

	unsafeConfig := filepath.Join(dir, "unsafe.json")
	if err := os.WriteFile(unsafeConfig, []byte(`{"secret_policy":"allow-plaintext"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	backupVictim := filepath.Join(dir, "backup-victim")
	if err := os.WriteFile(backupVictim, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backupVictim, unsafeConfig+".pre-secrets.bak"); err != nil {
		t.Fatal(err)
	}
	if _, err := MigratePlaintextSecrets(unsafeConfig, filepath.Join(dir, "credentials-2")); err == nil ||
		!strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("backup symlink error=%v", err)
	}
	if got, err := os.ReadFile(backupVictim); err != nil || string(got) != "safe" {
		t.Fatalf("backup symlink target changed: %q, %v", got, err)
	}
}

func TestMigrationRejectsTrailingInvalidAndUnresolvableSecrets(t *testing.T) {
	dir := t.TempDir()
	for name, contents := range map[string][]byte{
		"trailing":  []byte(`{"secret_policy":"allow-plaintext"} attacker-data`),
		"nul":       []byte("{\"secret_policy\":\"allow-plaintext\",\"password\":\"bad\\u0000value\"}"),
		"oversized": []byte(`{"secret_policy":"allow-plaintext","password":"` + strings.Repeat("x", secretFileMaxSize) + `"}`),
	} {
		path := filepath.Join(dir, name+".json")
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := MigratePlaintextSecrets(path, filepath.Join(dir, name+"-credentials")); err == nil {
			t.Fatalf("migration accepted %s input", name)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(contents) {
			t.Fatalf("failed %s migration changed config", name)
		}
	}

	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "config-link.json")
	if err := os.WriteFile(target, []byte(`{"secret_policy":"allow-plaintext"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := MigratePlaintextSecrets(link, filepath.Join(dir, "link-credentials")); err == nil ||
		!strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("config symlink error=%v", err)
	}
}
