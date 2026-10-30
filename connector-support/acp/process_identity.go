package acp

import (
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// ProcessIdentity is a fingerprint of a spawned ACP subprocess used for PID-reuse
// hardening. Before killing a stale process,
// the pool verifies the OS process is still the one we started by comparing pid,
// start time, and executable path. This prevents killing an unrelated process that
// recycled the same PID after the original agent exited.
type ProcessIdentity struct {
	PID        int
	CreateTime time.Time // best-effort process start time; zero if unavailable
	ExeKey     string    // normalized executable path; empty if unavailable
}

// processStartTimeFn is the package-level indirection used by stillSameProcess
// to retrieve a process's OS start time. Tests override it to make identity-check
// tests deterministic on platforms where the atomic fake-PID counter in tests
// can collide with real OS processes (e.g., fake PID 1 == init, PID 2 == kthreadd
// on Linux), causing /proc reads to return real start times.
//
// It is guarded by processStartTimeMu because production reads it from
// stale-kill timer goroutines (scheduleStaleKill -> retireStaleRuntime ->
// retireDetached -> stillSameProcess -> processStartTime) while tests
// concurrently swap in a mock and restore the original via t.Cleanup. A plain
// var would data-race under -race with parallel tests; the RWMutex allows
// concurrent reads (multiple stale-kill goroutines) and exclusive writes (test
// setup/restore). Production callers should not modify this.
var (
	processStartTimeMu sync.RWMutex
	processStartTimeFn = platformProcessStartTime
)

// processStartTime returns the OS process start time for pid, or zero time if
// unavailable on this platform. This is platform-specific and best-effort.
func processStartTime(pid int) time.Time {
	processStartTimeMu.RLock()
	fn := processStartTimeFn
	processStartTimeMu.RUnlock()
	return fn(pid)
}

// normalizeExeKey resolves symlinks and lowercases (on case-insensitive platforms)
// the executable path for cross-platform comparison.
func normalizeExeKey(raw string) string {
	candidate := strings.TrimSpace(strings.Trim(raw, `"`))
	if candidate == "" {
		return ""
	}
	resolved, err := filepath.Abs(candidate)
	if err != nil {
		return normalizeCase(candidate)
	}
	return normalizeCase(resolved)
}

func normalizeCase(s string) string {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(s)
	}
	return s
}

// captureProcessIdentity snapshots a running process for later stale-kill verification.
// cmdFirstArg is the first element of the spawn command (e.g. "cursor-agent"), used as
// a fallback for the executable path when the OS API can't resolve it. Pass "" if
// unavailable.
func captureProcessIdentity(proc Process, cmdFirstArg string) ProcessIdentity {
	pid := proc.PID()
	id := ProcessIdentity{PID: pid}
	if pid <= 0 {
		return id
	}
	ct := processStartTime(pid)
	if !ct.IsZero() {
		id.CreateTime = ct
	}
	exe := processExePath(pid)
	if exe == "" && cmdFirstArg != "" {
		exe = cmdFirstArg
	}
	if exe != "" {
		id.ExeKey = normalizeExeKey(exe)
	}
	return id
}

// processExePath returns the executable path for pid, or empty if unavailable.
func processExePath(pid int) string {
	return platformProcessExePath(pid)
}

// stillSameProcess verifies that the process referenced by proc is still the same
// OS process captured in identity. Returns false if:
//   - identity is zero (no PID)
//   - the process has already exited
//   - the PID no longer matches
//   - the process start time has changed beyond epsilon (PID was recycled)
//   - the executable path has changed (PID was recycled)
func stillSameProcess(proc Process, identity ProcessIdentity) bool {
	if identity.PID <= 0 {
		return false
	}
	if proc == nil {
		return false
	}
	if proc.PID() != identity.PID {
		return false
	}
	// If we have a create time, verify it matches within epsilon.
	if !identity.CreateTime.IsZero() {
		curCT := processStartTime(identity.PID)
		if !curCT.IsZero() {
			epsilon := 750 * time.Millisecond
			if absDuration(curCT.Sub(identity.CreateTime)) > epsilon {
				return false
			}
		}
	}
	// If we have an exe key, verify it matches.
	if identity.ExeKey != "" {
		curExe := normalizeExeKey(processExePath(identity.PID))
		if curExe != "" && curExe != identity.ExeKey {
			return false
		}
	}
	return true
}

// sameProcessInstance reports whether the numeric PID still designates the
// process instance captured in identity, ignoring the executable path. A
// replaced or unlinked executable changes the executable key while the
// instance — same PID and same start time — stays alive. It never reports true
// for a recycled PID, and it never reports true when the start time is
// unavailable, because an unproven instance must not be signalled.
func sameProcessInstance(proc Process, identity ProcessIdentity) bool {
	if identity.PID <= 0 {
		return false
	}
	if proc == nil {
		return false
	}
	if proc.PID() != identity.PID {
		return false
	}
	if identity.CreateTime.IsZero() {
		return false
	}
	curCT := processStartTime(identity.PID)
	if curCT.IsZero() {
		return false
	}
	return absDuration(curCT.Sub(identity.CreateTime)) <= 750*time.Millisecond
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
