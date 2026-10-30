package diag

import (
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const envDebugTurns = "AIP_CODEX_DEBUG_TURNS"

var debugTurnsEnabled = sync.OnceValue(func() bool {
	return strings.TrimSpace(os.Getenv(envDebugTurns)) != ""
})

// DebugTurnsEnabled reports whether verbose per-turn diagnostics are enabled for
// this process. The environment is read once so debug wrappers agree on a single
// process-lifetime gate.
func DebugTurnsEnabled() bool {
	return debugTurnsEnabled()
}

// LoggerOrDefault returns log when present, otherwise slog.Default().
func LoggerOrDefault(log *slog.Logger) *slog.Logger {
	if log != nil {
		return log
	}
	return slog.Default()
}

// StableCounts formats count maps as sorted "key=value" strings for stable logs.
func StableCounts(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+strconv.Itoa(counts[k]))
	}
	return out
}

// AppendLimited appends a trimmed non-empty value until maxLen entries are present.
func AppendLimited(values []string, value string, maxLen int) []string {
	value = strings.TrimSpace(value)
	if value == "" || len(values) >= maxLen {
		return values
	}
	return append(values, value)
}
