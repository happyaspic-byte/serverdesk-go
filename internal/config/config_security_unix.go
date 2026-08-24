//go:build !windows

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func validateSecureConfigFile(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("설정 파일 권한은 group/other 접근 없이 0600이어야 합니다: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("설정 파일 소유권을 확인할 수 없습니다: %s", path)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("설정 파일은 현재 서비스 계정 소유여야 합니다: %s", path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("설정 파일 하드링크는 허용되지 않습니다: %s", path)
	}
	parent := filepath.Dir(filepath.Clean(path))
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("설정 디렉터리 검사 실패: %w", err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("설정 디렉터리는 실제 디렉터리여야 합니다: %s", parent)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok || parentStat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("설정 디렉터리는 현재 서비스 계정 소유여야 합니다: %s", parent)
	}
	if parentInfo.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("설정 디렉터리는 group/other 쓰기가 금지되어야 합니다: %s", parent)
	}
	return nil
}
