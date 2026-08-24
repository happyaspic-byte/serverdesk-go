//go:build windows

package config

import (
	"syscall"
	"unsafe"
)

const (
	configMoveFileReplaceExisting = 0x1
	configMoveFileWriteThrough    = 0x8
)

var configMoveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceConfigFile(from, to string) error {
	fromPtr, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	result, _, callErr := configMoveFileExW.Call(
		uintptr(unsafe.Pointer(fromPtr)), uintptr(unsafe.Pointer(toPtr)),
		uintptr(configMoveFileReplaceExisting|configMoveFileWriteThrough),
	)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return syscall.EINVAL
}
