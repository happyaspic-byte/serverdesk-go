//go:build windows

package avcli

import (
	"context"
	"os/exec"
)

// avcliCmd 는 구성된 실행 파일을 인수와 함께 직접 실행한다.
func avcliCmd(ctx context.Context, bin string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}
