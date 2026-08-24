//go:build windows

package webauth

import "os"

func applyReplacementMetadata(file *os.File, original os.FileInfo) error {
	return file.Chmod(original.Mode().Perm())
}

func syncParentDirectory(string) error { return nil }
