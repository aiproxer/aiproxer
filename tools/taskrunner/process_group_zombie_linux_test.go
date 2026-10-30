//go:build linux

package taskrunner

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestKillGroupIgnoresKilledZombie proves the group confirmation distinguishes a
// live survivor from a killed member that is only waiting to be reaped. A
// process-group existence probe alone sees the zombie and previously reported a
// false cleanup failure after the two-second confirmation window.
//
//nolint:paralleltest // serial: owns a process group and its unreaped child
func TestKillGroupIgnoresKilledZombie(t *testing.T) {
	helper := buildHelper(t)

	leader := exec.Command(helper, "-mode=sleep")
	adapter, err := newProcessAdapter(leader, false)
	if err != nil {
		t.Fatalf("newProcessAdapter: %v", err)
	}
	if err := adapter.start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	t.Cleanup(func() {
		_ = leader.Process.Kill()
		_, _ = leader.Process.Wait()
	})

	member := exec.Command(helper, "-mode=sleep")
	member.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: leader.Process.Pid}
	if err := member.Start(); err != nil {
		t.Fatalf("start group member: %v", err)
	}
	t.Cleanup(func() {
		_ = member.Process.Kill()
		_, _ = member.Process.Wait()
	})

	// Kill the member and leave it unreaped: it stays in the leader's process
	// group as a zombie until this test waits for it.
	if err := member.Process.Kill(); err != nil {
		t.Fatalf("kill member: %v", err)
	}
	waitForZombie(t, member.Process.Pid)

	// Reap the leader, matching the state in which killGroup is called after
	// cmd.Wait. The zombie member keeps the group id addressable.
	if err := leader.Process.Kill(); err != nil {
		t.Fatalf("kill leader: %v", err)
	}
	_, _ = leader.Process.Wait()

	started := time.Now()
	if err := adapter.killGroup(); err != nil {
		t.Fatalf("killGroup = %v after %s; a killed zombie is not a surviving member", err, time.Since(started))
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("killGroup took %s; a group with only a zombie should be recognized without waiting for the confirmation window", elapsed)
	}
}

func waitForZombie(t *testing.T, pid int) {
	t.Helper()
	path := "/proc/" + strconv.Itoa(pid) + "/stat"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if index := strings.LastIndexByte(string(data), ')'); index >= 0 {
				fields := strings.Fields(string(data[index+2:]))
				if len(fields) > 0 && fields[0] == "Z" {
					return
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("member %d never became a zombie", pid)
}
