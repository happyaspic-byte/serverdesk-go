//go:build !windows

package poller

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func replaceEventFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	// 일부 네트워크/가상 파일시스템은 디렉터리 fsync 자체를 지원하지 않는다.
	if syncErr != nil && !errors.Is(syncErr, syscall.EINVAL) && !errors.Is(syncErr, syscall.ENOTSUP) {
		return syncErr
	}
	return closeErr
}
