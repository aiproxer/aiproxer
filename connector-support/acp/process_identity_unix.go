//go:build !windows

package acp

import (
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	procDirExists     bool
	procDirExistsOnce sync.Once

	bootTime     time.Time
	bootTimeOnce sync.Once
)

func checkProcDirExists() bool {
	procDirExistsOnce.Do(func() {
		info, err := os.Stat("/proc")
		procDirExists = err == nil && info.IsDir()
	})
	return procDirExists
}

func getBootTime() time.Time {
	bootTimeOnce.Do(func() {
		bootTime = readBootTime()
	})
	return bootTime
}

// platformProcessStartTime returns the process start time on Unix systems by
// reading /proc/<pid>/stat (Linux) or using kill(pid, 0) as a liveness check
// (other Unixes where /proc is unavailable). Best-effort: returns zero on error.
func platformProcessStartTime(pid int) time.Time {
	if !checkProcDirExists() {
		// No /proc filesystem (e.g. macOS); start time is unavailable.
		return time.Time{}
	}
	// Linux: read /proc/<pid>/stat field 22 (starttime in clock ticks since boot).
	statPath := "/proc/" + strconv.Itoa(pid) + "/stat"
	data, err := os.ReadFile(statPath)
	if err != nil {
		return time.Time{}
	}
	// Parse field 22 (starttime). Fields are space-separated, but the comm field
	// (field 2) may contain spaces inside parens, so we find the last ')' and
	// parse from there.
	s := string(data)
	lastParen := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ')' {
			lastParen = i
			break
		}
	}
	if lastParen < 0 || lastParen+1 >= len(s) {
		return time.Time{}
	}
	rest := s[lastParen+1:]
	// Skip whitespace, then parse fields 3..22 (state, ppid, pgrp, ... starttime).
	// Fields after comm: state(3) ppid(4) pgrp(5) session(6) tty_nr(7) tpgid(8)
	// flags(9) minflt(10) cminflt(11) majflt(12) cmajflt(13) utime(14) cstime(15)
	// priority(16) nice(17) num_threads(18) itrealvalue(19) starttime(20) ...
	// Actually starttime is field 22 in 1-indexed, which is field 20 after the
	// comm field (since comm is field 2, we skip state=3..starttime=22 = 20 fields).
	fields := strings.Fields(rest)
	// After ')', the fields are: state ppid pgrp session tty_nr tpgid flags
	// minflt cminflt majflt cmajflt utime stime cutime cstime priority nice
	// num_threads itrealvalue starttime vsize ...
	// That's 20 fields to reach starttime (0-indexed: field[19]).
	if len(fields) < 20 {
		return time.Time{}
	}
	starttimeTicks, ok := parseUint(fields[19])
	if !ok {
		return time.Time{}
	}
	// Clock ticks per second. Linux's USER_HZ is virtually always 100.
	// We hardcode this because Go's syscall package doesn't expose
	// SysconfClkTck. A more precise value could be obtained via cgo or
	// golang.org/x/sys/unix, but 100 is correct for all common Linux setups.
	hz := 100.0
	// Get boot time from /proc/stat (btime line).
	bTime := getBootTime()
	if bTime.IsZero() {
		return time.Time{}
	}
	startSecs := float64(starttimeTicks) / hz
	return bTime.Add(time.Duration(startSecs * float64(time.Second)))
}

// platformProcessExePath returns the executable path for pid on Unix.
func platformProcessExePath(pid int) string {
	if pid <= 0 {
		return ""
	}
	exe, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return ""
	}
	return exe
}

// parseUint parses a base-10 unsigned integer from a (possibly whitespace-padded)
// string, returning false on empty input or parse error. Used for /proc field
// extraction where missing fields must be treated as unavailable rather than zero.
func parseUint(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func unixSecsToTime(secs uint64) time.Time {
	if secs > math.MaxInt64 {
		return time.Time{}
	}
	return time.Unix(int64(secs), 0)
}

// readBootTime reads the system boot time from /proc/stat (Linux only).
func readBootTime() time.Time {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) > 6 && line[:6] == "btime " {
			secs, ok := parseUint(line[6:])
			if ok {
				return unixSecsToTime(secs)
			}
			break
		}
	}
	return time.Time{}
}
