//go:build windows

package webfront

import (
	"syscall"
	"unsafe"
)

const (
	operatorMoveFileReplaceExisting = 0x1
	operatorMoveFileWriteThrough    = 0x8
)

var operatorMoveFileExW = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceOperatorStateFile(from, to string) error {
	fromPtr, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	toPtr, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	result, _, callErr := operatorMoveFileExW.Call(
		uintptr(unsafe.Pointer(fromPtr)), uintptr(unsafe.Pointer(toPtr)),
		uintptr(operatorMoveFileReplaceExisting|operatorMoveFileWriteThrough),
	)
	if result != 0 {
		return nil
	}
	if callErr != syscall.Errno(0) {
		return callErr
	}
	return syscall.EINVAL
}
