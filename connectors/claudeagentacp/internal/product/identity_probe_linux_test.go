//go:build linux

package product

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Requirement 2.5 and whole-feature F1: the resolved native CLI receives only
// auth status --json, the prepared environment and cwd; stderr is discarded.
// Mutants: add the adapter-only --cli flag, invoke PATH, inherit env, or omit reap.
func TestIdentityProbe_ExactLaunchAndTimeout(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "claude")
	env := []string{"PATH=/bin", "HOME=" + root, "CLAUDE_CONFIG_DIR=" + root, "ANTHROPIC_API_KEY=synthetic-secret"}
	for _, tc := range []struct {
		name, script string
		timeout      bool
		failure      bool
	}{
		{"exact", `test "$#" = 3 && test "$1" = auth && test "$2" = status && test "$3" = --json || exit 2
test "$PWD" = "$HOME" || exit 3
test "$CLAUDE_CONFIG_DIR" = "$HOME" || exit 4
test "$ANTHROPIC_API_KEY" = synthetic-secret || exit 5
test "$(env | wc -l)" = 5 || exit 6
printf 'private-email synthetic-secret' >&2
printf '{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}'`, false, false},
		{"timeout", "exec sleep 30", true, false},
		{"descendant timeout", "sleep 30 &\nwait", true, false},
		{"overflow", "head -c 65537 /dev/zero\nexec sleep 30", false, true},
		{"stderr drain", "head -c 262144 /dev/zero >&2\nprintf 'ok'", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+tc.script+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			if tc.timeout {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
			}
			defer cancel()
			data, err := probeIdentity(ctx, executable, root, env)
			if tc.timeout {
				if !errors.Is(err, context.DeadlineExceeded) || len(data) != 0 {
					t.Fatalf("timeout: %q %v", data, err)
				}
			} else if tc.failure {
				if err == nil || len(data) != 0 {
					t.Fatalf("oversized output accepted: %d bytes, %v", len(data), err)
				}
			} else if tc.name == "stderr drain" {
				if err != nil || string(data) != "ok" {
					t.Fatalf("stderr drain: %q %v", data, err)
				}
			} else if err != nil || string(data) != `{"loggedIn":true,"authMethod":"api_key","apiProvider":"firstParty","apiKeySource":"ANTHROPIC_API_KEY"}` {
				t.Fatalf("exact probe: %q %v", data, err)
			}
		})
	}
}
