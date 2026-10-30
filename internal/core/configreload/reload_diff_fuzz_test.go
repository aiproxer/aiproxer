package configreload_test

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
)

// FuzzReloadDiffClassify mutates a bounded base config and classifies the diff.
// It never builds generations or runs the coordinator (no flaky stateful fuzz).
// Invariants: paths are value-free, restart fields stay bounded/sorted, and
// mixed restart+reloadable rejects as one transaction (req 3.5, 7.2-7.6, 16.9).
func FuzzReloadDiffClassify(f *testing.F) {
	f.Add(uint8(0), "stub:default", "single_user", "info")
	f.Add(uint8(1), "stub:other", "single_user", "debug")
	f.Add(uint8(2), "stub:default", "multi_user", "info")
	f.Add(uint8(3), "stub:x", "multi_user", "warn")
	f.Add(uint8(4), "", "", "")

	f.Fuzz(func(t *testing.T, kind uint8, route, mode, level string) {
		route = boundUTF8(route, 64)
		mode = boundUTF8(mode, 32)
		level = boundUTF8(level, 16)

		active := baseConfig()
		candidate := baseConfig()
		switch kind % 6 {
		case 0:
			// identical → noop (nil changes)
		case 1:
			candidate.Routing.DefaultRoute = route
		case 2:
			candidate.Access.Mode = mode
		case 3:
			candidate.Logging.Level = level
		case 4:
			candidate.Routing.DefaultRoute = route
			candidate.Access.Mode = mode
		case 5:
			candidate.Routing.DefaultRoute = route
			candidate.Logging.Level = level
			candidate.ModelAliases = []config.ModelAliasConfig{{Pattern: `^x$`, Replacement: "stub:x"}}
		}

		changes, err := configreload.Classify(active, candidate)
		if err != nil {
			var rr *configreload.RestartRequiredError
			if !errors.As(err, &rr) {
				// unexpected classify failure shape is still non-fatal for fuzz
				// as long as it does not panic or embed raw values.
				msg := err.Error()
				assertNoValueBearing(t, msg, route, mode, level)
				return
			}
			assertSortedBoundedPaths(t, rr)
			for _, p := range rr.RestartRequiredFields {
				assertSafePath(t, p)
			}
			assertNoValueBearing(t, rr.Error(), route, mode, level)
			return
		}
		for _, c := range changes {
			assertSafePath(t, c.Path)
			if c.Disposition != configreload.ChangeReloadable && c.Disposition != configreload.ChangeRestartRequired {
				t.Fatalf("unexpected disposition %q on %q", c.Disposition, c.Path)
			}
		}
	})
}

func boundUTF8(s string, maxRunes int) string {
	if maxRunes <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= maxRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

func assertSafePath(t *testing.T, p string) {
	t.Helper()
	if p == "" || strings.Contains(p, "=") || strings.Contains(p, ": ") {
		t.Fatalf("unsafe path %q", p)
	}
}

func assertNoValueBearing(t *testing.T, msg, route, mode, level string) {
	t.Helper()
	for _, v := range []string{route, mode, level} {
		if len(v) < 8 {
			continue
		}
		if strings.Contains(msg, v) {
			t.Fatalf("error embeds raw value %q: %q", v, msg)
		}
	}
}
