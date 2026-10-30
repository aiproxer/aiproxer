//go:build windows

package taskrunner

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcess struct {
	cmd      *exec.Cmd
	job      windows.Handle
	process  windows.Handle
	assigned bool
	startErr error
	closed   bool
	killOnce sync.Once
	killErr  error
	token    windows.Token
}

type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

type jobObjectBasicAndIOAccountingInformation struct {
	BasicInfo jobObjectBasicAccountingInformation
	IOInfo    windows.IO_COUNTERS
}

func newPlatformProcessAdapter(cmd *exec.Cmd, restrictAdmin bool) (processAdapter, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create job object: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure job object: %w", err)
	}
	p := &windowsProcess{cmd: cmd, job: job}
	if restrictAdmin {
		token, err := newRestrictedProcessToken()
		if err != nil {
			_ = windows.CloseHandle(job)
			return nil, fmt.Errorf("create restricted process token: %w", err)
		}
		p.token = token
		cmd.SysProcAttr = &syscall.SysProcAttr{Token: syscall.Token(token)}
	}
	return p, nil
}

func (p *windowsProcess) start() error {
	if p.token != 0 {
		defer func() {
			_ = p.token.Close()
			p.token = 0
		}()
	}
	if err := p.cmd.Start(); err != nil {
		return err
	}
	handle, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(p.cmd.Process.Pid))
	if err != nil {
		p.startErr = fmt.Errorf("open child process: %w", err)
		return nil
	}
	p.process = handle
	if err := windows.AssignProcessToJobObject(p.job, handle); err != nil {
		var code uint32
		if exitErr := windows.GetExitCodeProcess(handle, &code); exitErr == nil && code == stillActive {
			p.startErr = fmt.Errorf("assign process to job object: %w", err)
			p.startErr = errors.Join(p.startErr, p.killDirect())
			return nil
		}
		// A process that exited before assignment needs only the normal Wait.
		return nil
	}
	p.assigned = true
	return nil
}

func (p *windowsProcess) startupCleanupError() error { return p.startErr }
func (p *windowsProcess) killDirect() error {
	if p.process != 0 {
		state, err := windows.WaitForSingleObject(p.process, 0)
		if err != nil {
			return err
		}
		if state == windows.WAIT_OBJECT_0 {
			return nil
		}
		return windows.TerminateProcess(p.process, 1)
	}
	if p.cmd.Process == nil || p.cmd.ProcessState != nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

func (p *windowsProcess) kill() error {
	p.killOnce.Do(func() {
		if p.assigned {
			p.killErr = windows.TerminateJobObject(p.job, 1)
			return
		}
		p.killErr = p.killDirect()
	})
	return p.killErr
}

// killGroup needs no extra action on Windows: close() closes the job handle,
// and JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE terminates every surviving member when
// the last handle closes. waitForProcess closes the job on every outcome, so a
// leader that exited normally cannot leave job members behind either.
func (p *windowsProcess) killGroup() error { return nil }

func (p *windowsProcess) accounting() (ProcessAccounting, error) {
	if !p.assigned {
		return ProcessAccounting{Supported: true}, errors.New("process not assigned to job object")
	}
	var info jobObjectBasicAndIOAccountingInformation
	if err := windows.QueryInformationJobObject(
		p.job,
		windows.JobObjectBasicAndIoAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	); err != nil {
		return ProcessAccounting{Supported: true}, fmt.Errorf("query job object accounting: %w", err)
	}
	userNanos := info.BasicInfo.TotalUserTime * 100
	kernelNanos := info.BasicInfo.TotalKernelTime * 100
	return ProcessAccounting{
		Supported:           true,
		UserCPUNanos:        userNanos,
		KernelCPUNanos:      kernelNanos,
		TotalCPUNanos:       userNanos + kernelNanos,
		TotalProcesses:      info.BasicInfo.TotalProcesses,
		ActiveProcesses:     info.BasicInfo.ActiveProcesses,
		TerminatedProcesses: info.BasicInfo.TotalTerminatedProcesses,
		PageFaults:          info.BasicInfo.TotalPageFaultCount,
		ReadOperations:      info.IOInfo.ReadOperationCount,
		WriteOperations:     info.IOInfo.WriteOperationCount,
		OtherOperations:     info.IOInfo.OtherOperationCount,
		ReadBytes:           info.IOInfo.ReadTransferCount,
		WriteBytes:          info.IOInfo.WriteTransferCount,
		OtherBytes:          info.IOInfo.OtherTransferCount,
	}, nil
}

func (p *windowsProcess) close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	var err error
	if p.token != 0 {
		err = p.token.Close()
		p.token = 0
	}
	if p.process != 0 {
		err = windows.CloseHandle(p.process)
	}
	if closeErr := windows.CloseHandle(p.job); err == nil {
		err = closeErr
	}
	return err
}

const stillActive = 259

var createRestrictedToken = windows.NewLazySystemDLL("advapi32.dll").NewProc("CreateRestrictedToken")

const luaToken = 0x4

func newRestrictedProcessToken() (windows.Token, error) {
	var current windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_ASSIGN_PRIMARY|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_DEFAULT,
		&current,
	); err != nil {
		return 0, err
	}
	defer func() { _ = current.Close() }()
	var token windows.Token
	ok, _, callErr := createRestrictedToken.Call(
		uintptr(current),
		luaToken,
		0,
		0,
		0, 0,
		0, 0,
		uintptr(unsafe.Pointer(&token)),
	)
	if ok == 0 {
		return 0, callErr
	}
	if err := setRestrictedTokenDefaultDACL(token, current); err != nil {
		_ = token.Close()
		return 0, fmt.Errorf("set restricted token default DACL: %w", err)
	}
	return token, nil
}

type tokenDefaultDACL struct {
	DefaultDACL *windows.ACL
}

func setRestrictedTokenDefaultDACL(token, current windows.Token) error {
	user, err := current.GetTokenUser()
	if err != nil {
		return err
	}
	restrictedSID, err := windows.CreateWellKnownSid(windows.WinRestrictedCodeSid)
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}

	var pins runtime.Pinner
	pins.Pin(user.User.Sid)
	pins.Pin(restrictedSID)
	pins.Pin(systemSID)
	defer pins.Unpin()
	entries := []windows.EXPLICIT_ACCESS{
		fullAccessForSID(user.User.Sid, windows.TRUSTEE_IS_USER),
		fullAccessForSID(restrictedSID, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
		fullAccessForSID(systemSID, windows.TRUSTEE_IS_WELL_KNOWN_GROUP),
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}
	info := tokenDefaultDACL{DefaultDACL: acl}
	err = windows.SetTokenInformation(
		token,
		windows.TokenDefaultDacl,
		(*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	runtime.KeepAlive(acl)
	return err
}

func fullAccessForSID(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
