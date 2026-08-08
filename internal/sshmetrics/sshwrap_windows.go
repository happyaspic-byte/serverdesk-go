//go:build windows

package sshmetrics

import (
	"context"
	"os/exec"
)

// Windows 에는 setsid 가 없다 — ssh.exe 를 직접 호출한다. Stdin=nil 이라 TTY 프롬프트는
// 못 뜨고 SSH_ASKPASS_REQUIRE=force 환경변수로 askpass 를 탄다(Windows OpenSSH 기준 —
// Unix 만큼 검증되지 않은 경로라 비밀번호 수집이 제한될 수 있다).
func sshCmd(ctx context.Context, args []string) *exec.Cmd {
	// args 는 setsid 형식("-w", "ssh", ...) — "ssh" 본래 인수만 넘긴다.
	return exec.CommandContext(ctx, args[1], args[2:]...)
}
