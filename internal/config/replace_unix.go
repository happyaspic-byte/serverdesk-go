//go:build !windows

package config

import "os"

func replaceConfigFile(from, to string) error {
	return os.Rename(from, to)
}
