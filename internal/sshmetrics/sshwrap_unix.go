//go:build !windows

package sshmetrics

import (
	"context"
	"os/exec"
	"path/filepath"
)

// sshCmd 는 setsid -w 로 TTY 를 분리해 SSH_ASKPASS 가 호출되게 한다(Unix).
func sshCmd(ctx context.Context, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, "setsid", args...)
}

// askpassFile/askpassScript — SSH_ASKPASS 가 호출하는 비밀번호 에코 스크립트(Unix sh).
const askpassFile = "askpass.sh"
const askpassScript = "#!/bin/sh\necho \"$SSH_PW\"\n"

// extraSSHArgs — ControlMaster 로 폴 주기마다 재인증 비용을 피한다(Unix 소켓).
func extraSSHArgs(cmDir string) []string {
	return []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + filepath.Join(cmDir, "%r@%h:%p"),
		"-o", "ControlPersist=300",
	}
}
