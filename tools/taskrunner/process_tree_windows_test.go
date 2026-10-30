//go:build windows

package taskrunner

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestProcessTree_WindowsJobObjectDirect(t *testing.T) {
	if os.Getenv("AIP_RUN_WINDOWS_PROCESS_TREE_TESTS") != "1" {
		t.Skip("Windows Job Object process-tree tests run only in remote native QA")
	}
	t.Parallel()
	assertWindowsTreeCleanup(t, []string{"-mode=spawn-grandchild"})
}

func TestProcessTree_WindowsJobObjectShell(t *testing.T) {
	if os.Getenv("AIP_RUN_WINDOWS_PROCESS_TREE_TESTS") != "1" {
		t.Skip("Windows Job Object process-tree tests run only in remote native QA")
	}
	t.Parallel()
	helper := buildHelper(t)
	ready := t.TempDir() + `\ready`
	pid := t.TempDir() + `\pid`
	result, err := runWindowsTreeUntilReady(t, []string{"cmd.exe", "/C", helper, "-mode=spawn-grandchild", "-ready-file", ready, "-pid-file", pid}, ready, pid)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != DeadlineExceeded || !result.Cleanup.Attempted || result.Cleanup.Err != nil {
		t.Fatalf("result = %#v", result)
	}
	assertFileExists(t, ready)
	assertPIDFileGone(t, pid)
}

func assertWindowsTreeCleanup(t *testing.T, args []string) {
	t.Helper()
	dir := t.TempDir()
	ready := dir + `\ready`
	pid := dir + `\pid`
	args = append(args, "-ready-file", ready, "-pid-file", pid)
	result, err := runWindowsTreeUntilReady(t, append([]string{buildHelper(t)}, args...), ready, pid)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != DeadlineExceeded || !result.Cleanup.Attempted || result.Cleanup.Err != nil {
		t.Fatalf("result = %#v", result)
	}
	assertFileExists(t, ready)
	assertPIDFileGone(t, pid)
}

func runWindowsTreeUntilReady(t *testing.T, argv []string, ready, pid string) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- Run(ctx, Request{Argv: argv, Timeout: 10 * time.Second, Output: Capture})
	}()

	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case result := <-resultCh:
			return result, fmt.Errorf("process exited before ready marker %q: %#v", ready, result)
		case <-ticker.C:
			if _, err := os.Stat(ready); err != nil {
				if !os.IsNotExist(err) {
					cancel()
					result := <-resultCh
					return result, fmt.Errorf("stat ready marker %q: %w", ready, err)
				}
				continue
			}
			if _, err := os.Stat(pid); err == nil {
				cancel()
				return awaitWindowsTreeResult(t, resultCh, ready)
			} else if !os.IsNotExist(err) {
				cancel()
				result := awaitWindowsTreeResultValue(t, resultCh)
				return result, fmt.Errorf("stat pid marker %q: %w", pid, err)
			}
		case <-deadline.C:
			cancel()
			result := awaitWindowsTreeResultValue(t, resultCh)
			return result, fmt.Errorf("ready marker %q was not created: %#v", ready, result)
		}
	}
}

func awaitWindowsTreeResult(t *testing.T, resultCh <-chan Result, ready string) (Result, error) {
	t.Helper()
	result := awaitWindowsTreeResultValue(t, resultCh)
	if result.Kind != DeadlineExceeded {
		return result, fmt.Errorf("process exited after ready marker %q without deadline cleanup: %#v", ready, result)
	}
	return result, nil
}

func awaitWindowsTreeResultValue(t *testing.T, resultCh <-chan Result) Result {
	t.Helper()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return result
	case <-timer.C:
		t.Fatalf("taskrunner did not return after cancellation")
		return Result{}
	}
}

func assertFileExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected child marker %q: %v", path, err)
	}
}

func assertPIDFileGone(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.ParseUint(string(data), 10, 32)
		if err != nil {
			t.Fatal(err)
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
		if err != nil {
			if err == windows.ERROR_INVALID_PARAMETER {
				return
			}
			t.Fatal(err)
		}
		state, err := windows.WaitForSingleObject(h, 0)
		_ = windows.CloseHandle(h)
		if err != nil {
			t.Fatal(err)
		}
		if state == windows.WAIT_OBJECT_0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("grandchild process remained alive")
}
