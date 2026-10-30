//go:build unix

package main

import "syscall"

// detachedProcessGroupAttr starts the grandchild in its own process group so
// the runner's process-group kill cannot reach it.
func detachedProcessGroupAttr() (*syscall.SysProcAttr, bool) {
	return &syscall.SysProcAttr{Setpgid: true}, true
}
