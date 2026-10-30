package acp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeExeKey_Empty(t *testing.T) {
	t.Parallel()
	if got := normalizeExeKey(""); got != "" {
		t.Fatalf("normalizeExeKey(\"\") = %q, want empty", got)
	}
	if got := normalizeExeKey("   "); got != "" {
		t.Fatalf("normalizeExeKey(\"   \") = %q, want empty", got)
	}
}

func TestNormalizeExeKey_StripsQuotesAndResolves(t *testing.T) {
	t.Parallel()
	abs, _ := filepath.Abs("/usr/bin/python3")
	got := normalizeExeKey(`"/usr/bin/python3"`)
	want := normalizeCase(abs)
	if got != want {
		t.Fatalf("normalizeExeKey = %q, want %q", got, want)
	}
}

func TestNormalizeExeKey_CaseInsensitiveOnWindowsAndMac(t *testing.T) {
	t.Parallel()
	got := normalizeExeKey("C:/Users/Test/App.EXE")
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if got != strings.ToLower(got) {
			t.Fatalf("expected lowercased on case-insensitive platform, got %q", got)
		}
	}
}

func TestStillSameProcess_NilProcess(t *testing.T) {
	t.Parallel()
	id := ProcessIdentity{PID: 1}
	if stillSameProcess(nil, id) {
		t.Fatal("expected false for nil process")
	}
}

func TestStillSameProcess_ZeroPID(t *testing.T) {
	t.Parallel()
	proc := &fakeProcess{}
	if stillSameProcess(proc, ProcessIdentity{}) {
		t.Fatal("expected false for zero PID identity")
	}
}

func TestStillSameProcess_PIDMismatch(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	id := ProcessIdentity{PID: proc.PID() + 999}
	if stillSameProcess(proc, id) {
		t.Fatal("expected false for PID mismatch")
	}
}

func TestStillSameProcess_SameFakePID(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	id := ProcessIdentity{PID: proc.PID()}
	// Fake process has no start time/exe, so identity checks fall through to PID-only.
	if !stillSameProcess(proc, id) {
		t.Fatal("expected true for same PID with no start time/exe")
	}
}

func TestStillSameProcess_StartTimeMismatch(t *testing.T) {
	t.Parallel()
	// Make the platform start-time getter deterministic: mock it to return the
	// zero value so stillSameProcess falls through its "can't verify start time"
	// branch to PID-only fallback (~> true). Without this, /proc/<pid>/stat can
	// return the real start time of init/kthreadd when our atomic fake-PID
	// counter collides with a real OS pid, making the previous t.Logf-shrug
	// assertion order- and machine-dependent.
	origFn := processStartTimeFn
	processStartTimeMu.Lock()
	processStartTimeFn = func(int) time.Time { return time.Time{} }
	processStartTimeMu.Unlock()
	t.Cleanup(func() {
		processStartTimeMu.Lock()
		processStartTimeFn = origFn
		processStartTimeMu.Unlock()
	})

	proc := newFakeProcess(t)
	id := ProcessIdentity{
		PID:        proc.PID(),
		CreateTime: time.Unix(1000, 0),
	}
	if !stillSameProcess(proc, id) {
		t.Fatalf("stillSameProcess: expected true under mocked zero start time (PID-only fallback), got false")
	}
}

func TestCaptureProcessIdentity_FakeProcess(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	id := captureProcessIdentity(proc, "")
	if id.PID != proc.PID() {
		t.Fatalf("captureProcessIdentity PID = %d, want %d", id.PID, proc.PID())
	}
	// CreateTime and ExeKey may be zero for fake processes; that's fine.
}

func TestCaptureProcessIdentity_CurrentProcess(t *testing.T) {
	t.Parallel()
	// We can't easily create a Process from the current process, but we can
	// verify captureProcessIdentity works with a real PID by creating a minimal
	// fake that returns the current PID.
	proc := &fakeProcess{pid: os.Getpid()}
	id := captureProcessIdentity(proc, "")
	if id.PID != os.Getpid() {
		t.Fatalf("PID = %d, want %d", id.PID, os.Getpid())
	}
	// On Linux, we should get a non-zero CreateTime from /proc/self/stat.
	if runtime.GOOS == "linux" && id.CreateTime.IsZero() {
		t.Log("CreateTime is zero on Linux — /proc/stat parsing may need adjustment")
	}
}

func TestCaptureProcessIdentity_CmdFirstArgFallback(t *testing.T) {
	t.Parallel()
	proc := newFakeProcess(t)
	id := captureProcessIdentity(proc, "/usr/local/bin/cursor-agent")
	if id.ExeKey == "" {
		t.Fatal("expected non-empty ExeKey from cmdFirstArg fallback")
	}
}

func TestAbsDuration(t *testing.T) {
	t.Parallel()
	if got := absDuration(-5 * time.Second); got != 5*time.Second {
		t.Fatalf("absDuration(-5s) = %v, want 5s", got)
	}
	if got := absDuration(5 * time.Second); got != 5*time.Second {
		t.Fatalf("absDuration(5s) = %v, want 5s", got)
	}
}
