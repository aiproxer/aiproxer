package taskrunner

import "os/exec"

type processAdapter interface {
	start() error
	startupCleanupError() error
	kill() error
	killGroup() error
	accounting() (ProcessAccounting, error)
	close() error
}

type ProcessAccounting struct {
	Supported bool

	UserCPUNanos   int64
	KernelCPUNanos int64
	TotalCPUNanos  int64

	TotalProcesses      uint32
	ActiveProcesses     uint32
	TerminatedProcesses uint32
	PageFaults          uint32

	ReadOperations  uint64
	WriteOperations uint64
	OtherOperations uint64

	ReadBytes  uint64
	WriteBytes uint64
	OtherBytes uint64
}

func newProcessAdapter(cmd *exec.Cmd, restrictAdmin bool) (processAdapter, error) {
	return newPlatformProcessAdapter(cmd, restrictAdmin)
}
