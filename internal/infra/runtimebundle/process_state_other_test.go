//go:build !windows

package runtimebundle_test

import (
	"errors"
	"math"
	"os"
	"syscall"
)

func processAlive(pid int) bool {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = p.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}
