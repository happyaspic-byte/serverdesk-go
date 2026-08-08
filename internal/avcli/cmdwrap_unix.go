//go:build !windows

package avcli

import (
	"context"
	"os/exec"
)

// avcliCmd 는 avcli 셸 스크립트를 직접 실행한다(Unix).
func avcliCmd(ctx context.Context, bin string, args []string) *exec.Cmd {
	return exec.CommandContext(ctx, bin, args...)
}
