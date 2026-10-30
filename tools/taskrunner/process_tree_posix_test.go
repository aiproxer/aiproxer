//go:build !windows

package taskrunner

import (
	"context"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestProcessTree_POSIXProcessGroup(t *testing.T) {
	dir := t.TempDir()
	ready := dir + "/ready"
	pidFile := dir + "/pid"
	result := Run(context.Background(), Request{
		Argv:    []string{buildHelper(t), "-mode=spawn-grandchild", "-ready-file", ready, "-pid-file", pidFile},
		Timeout: 500 * time.Millisecond,
		Output:  Capture,
	})
	if result.Kind != DeadlineExceeded || !result.Cleanup.Attempted || result.Cleanup.Err != nil {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		err = syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("grandchild process remained alive")
}
