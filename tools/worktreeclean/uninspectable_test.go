package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeFakeProcStatus(t *testing.T, proc, pid string, uid int, zombie bool) {
	t.Helper()
	state := "S (sleeping)"
	if zombie {
		state = "Z (zombie)"
	}
	uids := strconv.Itoa(uid)
	status := "Name:\ttest\nState:\t" + state + "\nUid:\t" + uids + "\t" + uids + "\t" + uids + "\t" + uids + "\n"
	if err := os.MkdirAll(filepath.Join(proc, pid), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proc, pid, "status"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestScanProcUsers_SeparatesUninspectableCwd proves the process scan
// distinguishes three same-user outcomes: a process inside the worktree is an
// active user, a process whose cwd cannot be read is uncertain rather than
// fatal, and other-uid or zombie processes are ignored.
func TestScanProcUsers_SeparatesUninspectableCwd(t *testing.T) {
	t.Parallel()
	const euid = 424242
	proc := filepath.Join(t.TempDir(), "proc")
	target := t.TempDir()

	writeFakeProcStatus(t, proc, "101", euid, false)
	if err := os.Symlink(target, filepath.Join(proc, "101", "cwd")); err != nil {
		t.Fatal(err)
	}

	writeFakeProcStatus(t, proc, "102", euid, false)
	if err := os.WriteFile(filepath.Join(proc, "102", "comm"), []byte("agent-runner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A directory where the cwd symlink would be makes Readlink fail without
	// removing the process, the way a permission-denied /proc/<pid>/cwd does.
	if err := os.Mkdir(filepath.Join(proc, "102", "cwd"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeFakeProcStatus(t, proc, "103", euid+1, false)
	if err := os.Symlink(target, filepath.Join(proc, "103", "cwd")); err != nil {
		t.Fatal(err)
	}

	writeFakeProcStatus(t, proc, "104", euid, true)
	if err := os.Symlink(target, filepath.Join(proc, "104", "cwd")); err != nil {
		t.Fatal(err)
	}

	users, uncertain, err := scanProcUsers(t.Context(), proc, euid, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0] != "pid 101" {
		t.Fatalf("active users = %v, want [pid 101]", users)
	}
	if len(uncertain) != 1 || uncertain[0] != "pid 102 (agent-runner)" {
		t.Fatalf("uncertain users = %v, want [pid 102 (agent-runner)]", uncertain)
	}
}

// TestSafeSnapshot_RequiresOptInForUncertainUsers proves a clean worktree with
// uninspectable processes is refused by default with a message naming the
// opt-in, accepted with it, and that confirmed active users still refuse.
func TestSafeSnapshot_RequiresOptInForUncertainUsers(t *testing.T) {
	t.Parallel()
	clean := snapshot{Head: "tip", PublishedHead: "tip", State: "MERGED", Uncertain: []string{"pid 1342 (systemd)"}}
	if err := safeSnapshot(clean, false, false); err == nil {
		t.Fatal("accepted uninspectable processes without the opt-in")
	} else if !strings.Contains(err.Error(), "allow-uncertain-users") {
		t.Fatalf("refusal does not name the opt-in: %v", err)
	}
	if err := safeSnapshot(clean, false, true); err != nil {
		t.Fatalf("opt-in refused a clean worktree: %v", err)
	}
	occupied := clean
	occupied.Users = []string{"pid 42"}
	if err := safeSnapshot(occupied, false, true); err == nil {
		t.Fatal("opt-in accepted a confirmed active user")
	}
}
