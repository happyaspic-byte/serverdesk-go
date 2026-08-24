//go:build !windows

package webfront

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// replaceOperatorStateFile makes both the file contents and directory entry
// durable before acknowledging an authenticated operator write.
func replaceOperatorStateFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	if syncErr != nil && !errors.Is(syncErr, syscall.EINVAL) && !errors.Is(syncErr, syscall.ENOTSUP) {
		return syncErr
	}
	return closeErr
}
