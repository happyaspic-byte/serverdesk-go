//go:build windows

package snmp

import "syscall"

// Windows 는 SetsockoptInt 가 Handle 을 받아 Unix 경로를 공유할 수 없다.
// SO_REUSEADDR 는 어차피 best-effort(재바인드 편의)라 미설정으로 둔다.
func reuseaddrControl(_, _ string, c syscall.RawConn) error { return nil }
