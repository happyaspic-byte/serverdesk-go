//go:build windows

package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const dpapiFlags = 0x1 | 0x4 // CRYPTPROTECT_UI_FORBIDDEN | CRYPTPROTECT_LOCAL_MACHINE

type dataBlob struct {
	length uint32
	data   *byte
}

var (
	crypt32            = syscall.NewLazyDLL("crypt32.dll")
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	cryptProtectData   = crypt32.NewProc("CryptProtectData")
	cryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	localFree          = kernel32.NewProc("LocalFree")
)

func ensureCredentialDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential directory must be a real directory, not a symlink")
	}
	return nil
}

func credentialPath(dir, name string) (string, error) {
	if !validSecretName(name) {
		return "", fmt.Errorf("invalid credential name %q", name)
	}
	return filepath.Join(dir, name+".dpapi"), nil
}

func blobFor(data []byte) dataBlob {
	if len(data) == 0 {
		return dataBlob{}
	}
	return dataBlob{length: uint32(len(data)), data: &data[0]}
}

func callDPAPI(proc *syscall.LazyProc, input []byte) ([]byte, error) {
	in := blobFor(input)
	var out dataBlob
	result, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, dpapiFlags, uintptr(unsafe.Pointer(&out)),
	)
	runtime.KeepAlive(input)
	if result == 0 {
		return nil, callErr
	}
	if out.data == nil || out.length == 0 {
		return nil, errors.New("DPAPI returned empty output")
	}
	defer localFree.Call(uintptr(unsafe.Pointer(out.data)))
	copyOut := append([]byte(nil), unsafe.Slice(out.data, int(out.length))...)
	return copyOut, nil
}

func readCredentialFile(dir, name string) (string, error) {
	path, err := credentialPath(dir, name)
	if err != nil {
		return "", err
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(encoded) > secretFileMaxSize*2 {
		return "", errors.New("credential file is too large")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return "", fmt.Errorf("decode DPAPI credential: %w", err)
	}
	plain, err := callDPAPI(cryptUnprotectData, ciphertext)
	if err != nil {
		return "", fmt.Errorf("DPAPI decrypt credential: %w", err)
	}
	return string(plain), nil
}

func writeCredentialFile(dir, name, value string) error {
	if err := validateCredentialValue(value); err != nil {
		return err
	}
	path, err := credentialPath(dir, name)
	if err != nil {
		return err
	}
	if existing, err := readCredentialFile(dir, name); err == nil {
		if existing == value {
			return nil
		}
		return errors.New("credential already exists with a different value")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ciphertext, err := callDPAPI(cryptProtectData, []byte(value))
	if err != nil {
		return fmt.Errorf("DPAPI encrypt credential: %w", err)
	}
	encoded := []byte(base64.StdEncoding.EncodeToString(ciphertext) + "\r\n")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func listCredentialNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		filename := entry.Name()
		if entry.Type().IsRegular() && strings.HasSuffix(strings.ToLower(filename), ".dpapi") {
			name := filename[:len(filename)-len(".dpapi")]
			if validSecretName(name) {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

func removeCredentialFile(dir, name string) error {
	path, err := credentialPath(dir, name)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential cleanup target must be a regular non-reparse file")
	}
	return os.Remove(path)
}
