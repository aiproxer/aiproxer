//go:build aix || darwin || dragonfly || freebsd || netbsd || openbsd || solaris

package taskrunner

import (
	"errors"
	"syscall"
)

// processGroupHasLiveMembers falls back to group existence on platforms without
// Linux's per-process state interface. A zombie remains addressable here, so the
// confirmation window can still report a killed-but-unreaped member; the product
// smoke runs on Linux and Windows, where the distinction is available.
func processGroupHasLiveMembers(pgid int) (bool, error) {
	err := syscall.Kill(-pgid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	case errors.Is(err, syscall.EPERM):
		return true, nil
	default:
		return false, err
	}
}
