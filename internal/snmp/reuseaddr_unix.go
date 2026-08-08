//go:build !windows

package snmp

import "syscall"

// reuseaddrControl 은 SO_REUSEADDR best-effort 설정이다(실패해도 바인드를 막지 않음).
func reuseaddrControl(_, _ string, c syscall.RawConn) error {
	_ = c.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	})
	return nil
}
