//go:build !windows

package sshmetrics

import (
	"context"
	"os/exec"
)

// sshCmd 는 setsid -w 로 TTY 를 분리해 SSH_ASKPASS 가 호출되게 한다(Unix).
func sshCmd(ctx context.Context, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, "setsid", args...)
}
