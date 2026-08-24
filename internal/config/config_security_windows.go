//go:build windows

package config

import "os"

// Windows deployment ACL and reparse-point validation is enforced by the
// installer/updater. readRegularConfig still rejects a final-path symlink and
// bounds the file before this hook runs.
func validateSecureConfigFile(string, os.FileInfo) error { return nil }
