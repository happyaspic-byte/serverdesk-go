//go:build windows

package snmp

import "os"

// Structural checks are performed by ValidateTrapPath. Windows protection
// depends on the deployment's inherited directory ACL, not POSIX mode bits.
// This does not certify ACL protection or Windows commercial readiness.
func validateTrapDirectory(info os.FileInfo, immediate bool) error { return nil }
func validateTrapFile(info os.FileInfo) error                      { return nil }
