//go:build !windows

package snmp

import (
	"fmt"
	"os"
	"syscall"
)

func validateTrapDirectory(info os.FileInfo, immediate bool) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("snmp: directory ownership unavailable")
	}
	if immediate {
		if st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("snmp: trap parent must be private and service-owned")
		}
	} else {
		if st.Uid != 0 && st.Uid != uint32(os.Geteuid()) {
			return fmt.Errorf("snmp: unsafe directory owner")
		}
		if info.Mode().Perm()&0022 != 0 && !(st.Uid == 0 && info.Mode()&os.ModeSticky != 0) {
			return fmt.Errorf("snmp: replaceable directory ancestor")
		}
	}
	return nil
}

func validateTrapFile(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return fmt.Errorf("snmp: trap file must be service-owned without hardlinks")
	}
	return nil
}
