//go:build !linux

package acp

import (
	"fmt"
	"os/exec"
)

// The task 2.6 consumer is Linux/WSL-only. Do not fall back to post-reap
// taskkill/group signaling, which cannot safely identify an exited probe tree.
// Start/Kill remain unchanged for existing cross-platform ACP consumers.
func checkRunPlatform() error {
	return fmt.Errorf("acp: safe short-lived process-tree runner unavailable on this platform")
}

func waitRunExit(_ *exec.Cmd) error { return checkRunPlatform() }
