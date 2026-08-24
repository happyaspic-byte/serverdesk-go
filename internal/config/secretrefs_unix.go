//go:build !windows

package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func ensureCredentialDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect credential directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("credential directory must be a real directory, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("credential directory permissions must not grant group/other access (got %o)", info.Mode().Perm())
	}
	return nil
}

func credentialPath(dir, name string) (string, error) {
	if !validSecretName(name) {
		return "", fmt.Errorf("invalid credential name %q", name)
	}
	return filepath.Join(dir, name), nil
}

func readCredentialFile(dir, name string) (string, error) {
	path, err := credentialPath(dir, name)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("credential must be a regular non-symlink file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("credential permissions must not grant group/other access (got %o)", info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, secretFileMaxSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > secretFileMaxSize {
		return "", errors.New("credential exceeds 64 KiB")
	}
	if strings.IndexByte(string(data), 0) >= 0 {
		return "", errors.New("credential contains NUL")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), nil
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
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString(value + "\n"); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func listCredentialNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && validSecretName(entry.Name()) {
			names = append(names, entry.Name())
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
		return errors.New("credential cleanup target must be a regular non-symlink file")
	}
	return os.Remove(path)
}
