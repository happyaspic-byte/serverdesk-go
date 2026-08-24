//go:build !windows

package config

import (
	"fmt"
	"os"
	"syscall"
)

func applyReplacementMetadata(file *os.File, original os.FileInfo) error {
	stat, ok := original.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("unsupported original file metadata")
	}
	if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
		return err
	}
	return file.Chmod(original.Mode().Perm())
}

func syncParentDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
