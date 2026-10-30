//go:build !linux

package product

import (
	"errors"

	"github.com/aiproxer/aiproxer/connector-support/acp"
)

func RunRuntimeSupervisor([]string) error {
	return errors.New("claudeagentacp: runtime supervisor requires Linux")
}
func wrapSupervisorProcess(proc acp.Process, _ string) acp.Process { return proc }
