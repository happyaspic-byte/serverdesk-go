//go:build !windows

package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secureCredentialDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := ensureCredentialDirectory(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadSecureRejectsReplaceableStartupConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	doc := []byte(`{"secret_policy":"allow-plaintext","clusters":[]}`)
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecure(path); err != nil {
		t.Fatalf("secure 0600 config rejected: %v", err)
	}
	if err := os.Chmod(path, 0o620); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecure(path); err == nil {
		t.Fatal("group-writable startup config accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecure(link); err == nil {
		t.Fatal("symlink startup config accepted")
	}
	hard := filepath.Join(dir, "config-hard.json")
	if err := os.Link(path, hard); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSecure(path); err == nil {
		t.Fatal("hard-linked startup config accepted")
	}
	if err := os.Remove(hard); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o770); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if _, err := LoadSecure(path); err == nil {
		t.Fatal("group-writable startup directory accepted")
	}
}

func TestCredentialProviderSecurityBranches(t *testing.T) {
	primary := secureCredentialDir(t)
	fallback := secureCredentialDir(t)
	t.Setenv("CREDENTIALS_DIRECTORY", primary)
	t.Setenv("SERVERDESK_CREDENTIALS_DIRECTORY", fallback)
	t.Setenv("SERVERDESK_CREDENTIALS_STORE", "")
	if got := credentialDirectory(); got != "" {
		t.Fatalf("read-only credential source selected for writes: %q", got)
	}
	writable := secureCredentialDir(t)
	t.Setenv("SERVERDESK_CREDENTIALS_STORE", writable)
	if got := credentialDirectory(); got != writable {
		t.Fatalf("managed credential write directory = %q, want %q", got, writable)
	}

	for _, name := range []string{"", ".hidden", strings.Repeat("x", 129), "../escape", "slash/name", "white space"} {
		if validSecretName(name) {
			t.Errorf("unsafe secret name accepted: %q", name)
		}
	}
	if !validSecretName("장비.admin_01-token") {
		t.Fatal("safe unicode credential name rejected")
	}
	if _, err := credentialPath(fallback, "../escape"); err == nil {
		t.Fatal("credentialPath accepted traversal")
	}

	if got, err := resolveSecretValue("", SecretPolicyRequireReferences, "field"); err != nil || got != "" {
		t.Fatalf("empty optional secret = %q, %v", got, err)
	}
	if got, err := resolveSecretValue("legacy", SecretPolicyAllowPlaintext, "field"); err != nil || got != "legacy" {
		t.Fatalf("migration plaintext = %q, %v", got, err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("SERVERDESK_CREDENTIALS_STORE", "")
	t.Setenv("SERVERDESK_CREDENTIALS_DIRECTORY", "")
	if _, err := resolveSecretValue("secret://missing", SecretPolicyRequireReferences, "field"); err == nil || !strings.Contains(err.Error(), "no CREDENTIALS_DIRECTORY") {
		t.Fatalf("missing provider error = %v", err)
	}

	t.Setenv("SERVERDESK_CREDENTIALS_DIRECTORY", fallback)
	if err := os.WriteFile(filepath.Join(fallback, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveSecretValue("secret://empty", SecretPolicyRequireReferences, "field"); err == nil || !strings.Contains(err.Error(), "credential is empty") {
		t.Fatalf("empty credential error = %v", err)
	}
}

func TestCredentialFileRejectsUnsafeObjectsAndContent(t *testing.T) {
	dir := secureCredentialDir(t)

	if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(dir, "link"); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("symlink credential error = %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(dir, "directory"); err == nil {
		t.Fatal("directory accepted as credential")
	}
	if err := os.WriteFile(filepath.Join(dir, "nul"), []byte("abc\x00def"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(dir, "nul"); err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("NUL credential error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large"), make([]byte, secretFileMaxSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredentialFile(dir, "large"); err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("oversized credential error = %v", err)
	}

	if err := writeCredentialFile(dir, "stable", "value"); err != nil {
		t.Fatal(err)
	}
	if err := writeCredentialFile(dir, "stable", "value"); err != nil {
		t.Fatalf("idempotent credential write: %v", err)
	}
	if err := writeCredentialFile(dir, "stable", "different"); err == nil || !strings.Contains(err.Error(), "different value") {
		t.Fatalf("credential overwrite error = %v", err)
	}
	if err := writeCredentialFile(dir, "../bad", "value"); err == nil {
		t.Fatal("unsafe credential write accepted")
	}
}

func TestCredentialDirectoryRejectsSymlinkFileAndOpenPermissions(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link")
	if err := os.Symlink(realDir, symlink); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialDirectory(symlink); err == nil || !strings.Contains(err.Error(), "real directory") {
		t.Fatalf("symlink directory error = %v", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialDirectory(file); err == nil {
		t.Fatal("regular file accepted as credential directory")
	}
	openDir := filepath.Join(root, "open")
	if err := os.Mkdir(openDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(openDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureCredentialDirectory(openDir); err == nil || !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("open directory error = %v", err)
	}
}

func TestProtectDocumentSecretsNestedAndFailureBranches(t *testing.T) {
	dir := secureCredentialDir(t)
	doc := map[string]any{
		"_comment": "password must remain documentation",
		"clusters": []any{
			map[string]any{"key": "FT A", "admin_password": "alpha", "token": ""},
			map[string]any{"name": "fallback", "password": "secret://already"},
			map[string]any{"id": "numeric-secret", "api_key": 42},
			map[string]any{"private_key": "omega"},
		},
	}
	var result MigrationResult
	if err := protectDocumentSecrets(doc, nil, dir, &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 || len(result.Names) != 2 {
		t.Fatalf("nested protection result = %+v", result)
	}
	if doc["_comment"] != "password must remain documentation" {
		t.Fatal("documentation metadata was rewritten")
	}
	if got := doc["clusters"].([]any)[0].(map[string]any)["admin_password"].(string); !strings.HasPrefix(got, secretReferencePrefix) {
		t.Fatalf("protected value = %q", got)
	}

	badRef := map[string]any{"password": "secret://../escape"}
	if err := protectDocumentSecrets(badRef, nil, dir, &MigrationResult{}); err == nil {
		t.Fatal("invalid existing reference accepted")
	}
	plain := map[string]any{"password": "plain"}
	if err := protectDocumentSecrets(plain, nil, "", &MigrationResult{}); err == nil || !strings.Contains(err.Error(), "no writable SERVERDESK_CREDENTIALS_STORE") {
		t.Fatalf("missing migration destination error = %v", err)
	}
}

func TestProtectRequiredRawDocumentPoliciesAndErrors(t *testing.T) {
	invalid := map[string]json.RawMessage{"secret_policy": json.RawMessage(`"invalid"`)}
	if err := protectRequiredRawDocument(invalid); err == nil {
		t.Fatal("invalid policy accepted")
	}
	allow := map[string]json.RawMessage{
		"secret_policy": json.RawMessage(`"allow-plaintext"`),
		"password":      json.RawMessage(`"plain"`),
	}
	if err := protectRequiredRawDocument(allow); err != nil || string(allow["password"]) != `"plain"` {
		t.Fatalf("allow-plaintext document changed: %v %s", err, allow["password"])
	}
	broken := map[string]json.RawMessage{"broken": json.RawMessage(`{`)}
	if err := protectRequiredRawDocument(broken); err == nil {
		t.Fatal("invalid raw JSON accepted")
	}

	dir := secureCredentialDir(t)
	t.Setenv("SERVERDESK_CREDENTIALS_STORE", dir)
	required := map[string]json.RawMessage{
		"secret_policy": json.RawMessage(`"require-references"`),
		"edge_devices":  json.RawMessage(`[{"key":"pve","password":"plain"}]`),
	}
	if err := protectRequiredRawDocument(required); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(required["edge_devices"]), `"plain"`) || !strings.Contains(string(required["edge_devices"]), "secret://") {
		t.Fatalf("required document not protected: %s", required["edge_devices"])
	}
}

func TestMigrationAndStoreErrorBranches(t *testing.T) {
	if _, err := MigratePlaintextSecrets("missing.json", ""); err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("empty migration directory error = %v", err)
	}
	if _, err := MigratePlaintextSecrets("missing.json", filepath.Join(t.TempDir(), "creds")); err == nil || !strings.Contains(err.Error(), "inspect config") {
		t.Fatalf("missing config error = %v", err)
	}
	badConfig := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badConfig, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MigratePlaintextSecrets(badConfig, filepath.Join(t.TempDir(), "creds")); err == nil || !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("invalid migration JSON error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"secret_policy":"allow-plaintext","clusters":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore(path)
	if store.Path() != path {
		t.Fatalf("store path = %q", store.Path())
	}
	if err := store.SetSectionValue("thresholds", map[string]any{"warn": 70, "crit": 90}); err != nil {
		t.Fatal(err)
	}
	doc, err := store.ReadDoc()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceDoc(doc); err != nil {
		t.Fatal(err)
	}
	if err := store.SetSectionValue("bad", make(chan int)); err == nil {
		t.Fatal("unmarshalable section accepted")
	}

	missingStore := NewStore(filepath.Join(t.TempDir(), "missing.json"))
	if _, err := missingStore.ReadDoc(); err == nil {
		t.Fatal("missing store read succeeded")
	}
	invalidPath := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(invalidPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(invalidPath).ReadDoc(); err == nil {
		t.Fatal("invalid store JSON read succeeded")
	}
	if _, err := sectionArray(map[string]json.RawMessage{"clusters": json.RawMessage(`{}`)}, "clusters"); err == nil {
		t.Fatal("non-array section accepted")
	}
	if got, err := sectionArray(map[string]json.RawMessage{}, "clusters"); err != nil || len(got) != 0 {
		t.Fatalf("missing section = %#v, %v", got, err)
	}
	if err := writeFile0600(filepath.Join(t.TempDir(), "missing", "file"), []byte("x")); err == nil {
		t.Fatal("write into missing parent unexpectedly succeeded")
	}
	if !errors.Is(ErrConfigChanged, ErrConfigChanged) {
		t.Fatal("sentinel error identity changed")
	}
}

func TestDeploymentChecksAndCompleteTrapOverlay(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("SERVERDESK_CREDENTIALS_DIRECTORY", "")

	// Exercise the real deployment probes without asserting host-specific policy.
	_ = procHidepid()
	_ = otherLocalUsers()
	if _, err := CheckArgvExposure(true); err != nil {
		t.Fatalf("allow override must not fail deployment probe: %v", err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("Load accepted missing configuration")
	}

	cfg, err := Parse([]byte(`{
		"clusters":[],
		"trap":{"enabled":false,"bind":"127.0.0.1","port":1162,
			"community":"secret://trap","persist":"custom.jsonl","ring":42,
			"view_max":7,"mib_dir":"custom-mibs"}
	}`))
	if err == nil || !strings.Contains(err.Error(), "no CREDENTIALS_DIRECTORY") {
		t.Fatalf("trap secret without provider error = %v", err)
	}
	dir := secureCredentialDir(t)
	if err := writeCredentialFile(dir, "trap", "private-community"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	cfg, err = Parse([]byte(`{
		"clusters":[],
		"trap":{"enabled":false,"bind":"127.0.0.1","port":1162,
			"community":"secret://trap","persist":"custom.jsonl","ring":42,
			"view_max":7,"mib_dir":"custom-mibs"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Trap.Enabled || cfg.Trap.Bind != "127.0.0.1" || cfg.Trap.Port != 1162 ||
		cfg.Trap.Community == nil || *cfg.Trap.Community != "private-community" ||
		cfg.Trap.Persist != "custom.jsonl" || cfg.Trap.Ring != 42 || cfg.Trap.ViewMax != 7 || cfg.Trap.MibDir != "custom-mibs" {
		t.Fatalf("complete trap overlay = %+v", cfg.Trap)
	}
}
