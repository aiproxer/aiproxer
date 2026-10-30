//go:build windows

package taskrunner

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunner_WindowsRestrictedAdminToken(t *testing.T) {
	t.Parallel()

	result := Run(context.Background(), Request{
		Argv: []string{
			"powershell", "-NoProfile", "-Command",
			`([Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)`,
		},
		Timeout:       30 * time.Second,
		Output:        Capture,
		RestrictAdmin: true,
	})
	if result.Kind != Success {
		t.Fatalf("result = %#v, error = %v", result, result.Err)
	}
	if got := strings.TrimSpace(string(result.Stdout)); !strings.EqualFold(got, "false") {
		t.Fatalf("restricted child administrative membership = %q, want false", got)
	}
}

func TestRunner_WindowsJobAccountingIncludesDescendants(t *testing.T) {
	t.Parallel()

	ioFile := filepath.Join(t.TempDir(), "child-io.bin")
	result := Run(context.Background(), Request{
		Argv:    []string{buildHelper(t), "-mode=accounting-tree", "-io-file", ioFile},
		Timeout: 30 * time.Second,
		Output:  Capture,
	})
	if result.Kind != Success {
		t.Fatalf("result = %#v", result)
	}
	if !result.Accounting.Supported {
		t.Fatal("Windows Job Object accounting is unsupported")
	}
	if result.Accounting.TotalProcesses < 2 {
		t.Fatalf("total processes = %d, want root plus child", result.Accounting.TotalProcesses)
	}
	if result.Accounting.TotalCPUNanos <= 0 {
		t.Fatalf("total CPU nanos = %d, want positive descendant-inclusive CPU", result.Accounting.TotalCPUNanos)
	}
	operations := result.Accounting.ReadOperations + result.Accounting.WriteOperations + result.Accounting.OtherOperations
	if operations == 0 {
		t.Fatal("I/O operations = 0, want descendant-inclusive I/O")
	}
	if result.Elapsed <= 0 {
		t.Fatalf("elapsed = %s, want positive duration", result.Elapsed)
	}
	if result.AccountingErr != nil {
		t.Fatalf("accounting error: %v", result.AccountingErr)
	}
}

func TestWindowsProcess_UnassignedAccountingReturnsError(t *testing.T) {
	t.Parallel()

	p := &windowsProcess{assigned: false}
	acc, err := p.accounting()
	if err == nil {
		t.Fatal("want error when unassigned, got nil")
	}
	if !acc.Supported {
		t.Fatal("Supported should remain true on Windows")
	}
}
