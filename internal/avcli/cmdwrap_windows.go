//go:build windows

package avcli

import (
	"context"
	"os/exec"
)

// avcliCmd — Windows 의 avcli 는 java -jar 를 호출하는 배치 파일이라 cmd /c 가 필요하다
// (Go exec 는 .bat 을 직접 실행하지 못한다).
func avcliCmd(ctx context.Context, bin string, args []string) *exec.Cmd {
	all := append([]string{"/c", bin}, args...)
	return exec.CommandContext(ctx, "cmd", all...)
}
