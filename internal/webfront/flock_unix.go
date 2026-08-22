//go:build !windows

package webfront

import (
	"os"
	"syscall"
)

// lockFile/unlockFile — 프로세스 간 직렬화 flock(Unix).
func lockFile(fd *os.File) bool { return syscall.Flock(int(fd.Fd()), syscall.LOCK_EX) == nil }
func unlockFile(fd *os.File)    { _ = syscall.Flock(int(fd.Fd()), syscall.LOCK_UN) }
