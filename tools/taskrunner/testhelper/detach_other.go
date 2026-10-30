//go:build !unix

package main

import "syscall"

// detachedProcessGroupAttr reports that this platform cannot place the
// grandchild in a separate process group from the test helper.
func detachedProcessGroupAttr() (*syscall.SysProcAttr, bool) {
	return nil, false
}
