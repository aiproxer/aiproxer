//go:build !linux

package certgateway

import "os/exec"

// Real-runtime certification is Linux-only; keep the ordinary parser suite portable.
func certificationOutput(cmd *exec.Cmd, _ string) ([]byte, error) {
	return cmd.CombinedOutput()
}
