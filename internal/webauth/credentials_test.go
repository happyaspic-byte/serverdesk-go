package webauth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInitializeCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	password, err := InitializeCredentials(path)
	if err != nil {
		t.Fatalf("InitializeCredentials: %v", err)
	}
	if len(password) != 32 || strings.ContainsAny(password, "+/=") {
		t.Fatal("generated password is not 24-byte raw URL-safe base64")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), password) {
		t.Fatal("credential store contains the plaintext password")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("credential file permissions = %o, want 600", info.Mode().Perm())
		}
	}
	credentials, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	manager := New(credentials)
	if !manager.validCredentials(Username, password) {
		t.Fatal("generated password was rejected")
	}
	if manager.validCredentials(Username, password+"x") {
		t.Fatal("wrong password was accepted")
	}
}

func TestInitializeCredentialsDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	original := []byte("existing credentials")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := InitializeCredentials(path); err == nil {
		t.Fatal("InitializeCredentials overwrote an existing file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != string(original) {
		t.Fatal("InitializeCredentials changed an existing file")
	}
}

func TestLoadCredentialsRejectsMalformedAndInsecureFiles(t *testing.T) {
	directory := t.TempDir()
	for name, contents := range map[string]string{
		"invalid-json":    "{",
		"unknown-field":   `{"version":1,"username":"admin","algorithm":"PBKDF2-HMAC-SHA256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","digest":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","extra":true}`,
		"wrong-algorithm": `{"version":1,"username":"admin","algorithm":"sha256","iterations":600000,"salt":"AAAAAAAAAAAAAAAAAAAAAA","digest":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`,
		"wrong-length":    `{"version":1,"username":"admin","algorithm":"PBKDF2-HMAC-SHA256","iterations":600000,"salt":"AA","digest":"AA"}`,
		"oversized":       strings.Repeat("x", credentialFileMaxSize+1),
	} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCredentials(path); err == nil {
			t.Errorf("LoadCredentials accepted %s", name)
		}
	}

	path := filepath.Join(directory, "insecure")
	_, err := InitializeCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0640); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadCredentials(path); err == nil {
			t.Fatal("LoadCredentials accepted group-readable credentials")
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(path); err != nil {
		t.Fatalf("LoadCredentials rejected restored permissions: %v", err)
	}

	link := filepath.Join(directory, "link")
	if err := os.Symlink(path, link); err == nil {
		if _, err := LoadCredentials(link); err == nil {
			t.Fatal("LoadCredentials accepted a symlink")
		}
		if err := SetPassword(link, "replacement password"); err == nil {
			t.Fatal("SetPassword accepted a symlink")
		}
	}
}

func TestSetPasswordRotatesCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	oldPassword, err := InitializeCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	newPassword := "new administrator password"
	if err := SetPassword(path, newPassword); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	credentials, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("LoadCredentials after rotation: %v", err)
	}
	manager := New(credentials)
	if manager.validCredentials(Username, oldPassword) {
		t.Fatal("old password was accepted after rotation")
	}
	if !manager.validCredentials(Username, newPassword) {
		t.Fatal("new password was rejected after rotation")
	}
	if err := SetPassword(path, strings.Repeat("x", minimumPasswordBytes)); err != nil {
		t.Fatalf("SetPassword rejected the minimum password length: %v", err)
	}
	for _, password := range []string{"short", strings.Repeat("x", maximumPasswordBytes+1)} {
		if err := SetPassword(path, password); err == nil {
			t.Errorf("SetPassword accepted invalid password length %d", len(password))
		}
	}
}

func TestNewRejectsEmptyCredentials(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New accepted empty credentials")
		}
	}()
	_ = New(Credentials{})
}
