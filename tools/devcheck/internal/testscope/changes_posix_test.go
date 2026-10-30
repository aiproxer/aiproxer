//go:build linux || darwin

package testscope

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestScopeProbe_DeadlineStopsDescendants(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "child.pid")
	kill := func() {
		data, _ := os.ReadFile(path)
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if pid > 0 {
			process, _ := os.FindProcess(pid)
			_ = process.Kill()
		}
	}
	t.Cleanup(kill)
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := commandOutput(ctx, filepath.Dir(path), "sh", "-c", `sleep 600 & echo $! > "$1"; wait`, "fixture", path)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline error = %v", err)
		}
	case <-time.After(3 * time.Second):
		kill()
		cancel()
		<-done
		t.Fatal("scope discovery left a descendant holding its output pipe")
	}
}

func TestPlan_CancellationCannotBecomeFullFallback(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, full := range []bool{false, true} {
		plan, err := Build(ctx, t.TempDir(), Options{Full: full})
		if !errors.Is(err, context.Canceled) || len(plan.Modules) != 0 {
			t.Fatalf("cancelled planning reported a usable plan: %+v, %v", plan, err)
		}
	}
}
