package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipruntime"
)

func repoConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join("..", "..", "config", "config.yaml")
}

func writeTempConfig(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	return dir, writeTempConfigTo(t, dir, "cfg.yaml", body)
}

func writeTempConfigTo(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func readRepoConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(repoConfigPath(t))
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	return string(raw)
}

func dirOf(t *testing.T, path string) string {
	t.Helper()
	return filepath.Dir(path)
}

func reloadAPI() aipruntime.ReloadTrigger {
	return aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, AcceptedAt: time.Now().UTC(), SafeActor: "external-billing-test"}
}

// TestFailedBuildUnwindsStartedOnly certifies requirement 15.4 from outside:
// a failing candidate unwinds only successfully started owned resources,
// never touches borrowed handles, and leaves the active host usable.
func TestFailedBuildUnwindsStartedOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, path := writeTempConfig(t, readRepoConfig(t))

	activeTracker := &lifecycleTracker{}
	activeFix, err := AssembleBinding(activeTracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	active, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: path}, activeFix.Binding)
	if err != nil {
		t.Fatalf("active build: %v", err)
	}
	t.Cleanup(func() { _ = active.Close(ctx) })
	if !active.Ready() {
		t.Fatal("active host must be ready")
	}

	failingTracker := &lifecycleTracker{}
	failingFix, err := AssembleBinding(failingTracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	failing := failingFix.Binding
	failing.Lifecycle.Owned = append(failing.Lifecycle.Owned, failingTracker.failingOwned("worker-failing"))
	if _, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: path}, failing); err == nil {
		t.Fatal("failing owned start must fail the build")
	}
	snap := failingTracker.snapshot()
	if snap.starts["worker-test"] != 1 || snap.closes["worker-test"] != 1 {
		t.Fatalf("started worker must unwind exactly once: %+v", snap)
	}
	if snap.starts["worker-failing"] != 1 || snap.closes["worker-failing"] != 0 {
		t.Fatalf("never-started worker must never close: %+v", snap)
	}
	if !active.Ready() {
		t.Fatal("failed candidate must not disturb the active host")
	}
	if got := activeTracker.snapshot(); got.closes["worker-test"] != 0 {
		t.Fatalf("active host resources must stay open: %+v", got)
	}
}

// TestReloadRetainsFrozenBindingAcrossGenerations certifies requirement 15.3:
// publication moves the active generation to the new candidate while the
// process-owned binding resources started once stay put; retirement closes
// them exactly once, in reverse order, only at Host Close.
func TestReloadRetainsFrozenBindingAcrossGenerations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	body := readRepoConfig(t)
	const from = "max_attempts: 3"
	if !strings.Contains(body, from) {
		t.Fatalf("repo config lacks %q", from)
	}
	dir, path := writeTempConfig(t, body)

	tracker := &lifecycleTracker{}
	fix, err := AssembleBinding(tracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	rt, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: path}, fix.Binding)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	if rt.ReloadControl() == nil {
		t.Fatal("expected coordinator-bound reload control")
	}
	gen1 := rt.ReloadStatus().ActiveGeneration
	if got := tracker.snapshot(); got.starts["worker-test"] != 1 || got.starts["worker-plain"] != 1 {
		t.Fatalf("owned resources must start exactly once at build: %+v", got)
	}

	// routing.max_attempts is reload-classified (not restart-required), so this
	// publishes a new candidate generation without disturbing the process.
	changed := strings.Replace(body, from, "max_attempts: 4", 1)
	writeTempConfigTo(t, dir, "cfg.yaml", changed)
	res := rt.Reload(ctx, reloadAPI())
	if res.Category != aipruntime.ResultPublished {
		t.Fatalf("reload category=%q reason=%q, want published", res.Category, res.ReasonCategory)
	}
	gen2 := rt.ReloadStatus().ActiveGeneration
	if gen2 <= gen1 {
		t.Fatalf("generation did not advance: %d -> %d", gen1, gen2)
	}
	if !rt.Ready() || rt.ExecutorView() == nil {
		t.Fatal("reloaded host must stay ready with an executor view")
	}
	if got := tracker.snapshot(); got.starts["worker-test"] != 1 || got.starts["worker-plain"] != 1 || got.closes["worker-test"] != 0 || got.closes["worker-plain"] != 0 {
		t.Fatalf("reload must retain frozen binding resources untouched: %+v", got)
	}

	if err := rt.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	got := tracker.snapshot()
	if got.closes["worker-test"] != 1 || got.closes["worker-plain"] != 1 {
		t.Fatalf("retirement must close each owned resource once: %+v", got)
	}
	last := got.order[len(got.order)-2:]
	if last[0] != "close-worker-plain" || last[1] != "close-worker-test" {
		t.Fatalf("retirement order = %v, want reverse [close-worker-plain close-worker-test]", last)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	if again := tracker.snapshot(); again.closes["worker-test"] != 1 || again.closes["worker-plain"] != 1 {
		t.Fatalf("repeated close must not duplicate cleanup: %+v", again)
	}
	if n := tracker.borrowedCount(); n != 1 {
		t.Fatalf("borrowed declarations = %d, want 1 untouched handle", n)
	}
}

// TestBindingWorkerShutsDownWithoutLeak certifies requirement 18.3 for the
// binding-created worker through a real Host: queued work drains, Close joins
// the goroutine, and the exit is observed deterministically without sleeps.
func TestBindingWorkerShutsDownWithoutLeak(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, path := writeTempConfig(t, readRepoConfig(t))

	tracker := &lifecycleTracker{}
	fix, err := AssembleBinding(tracker)
	if err != nil {
		t.Fatalf("AssembleBinding: %v", err)
	}
	rt, err := aipruntime.BuildWithBilling(ctx, aipruntime.Options{ConfigPath: path}, fix.Binding)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	worker := tracker.Worker()
	for _, job := range []string{"job-1", "job-2", "job-3"} {
		worker.Submit(job)
	}
	if got := worker.WaitProcessed(t, 3); got != 3 {
		t.Fatalf("processed jobs = %d, want 3", got)
	}
	if err := rt.Close(ctx); err != nil {
		t.Fatalf("close: %v", err)
	}
	select {
	case <-worker.Exited():
	default:
		t.Fatal("binding worker goroutine must exit on Host Close")
	}
	if got := worker.Processed(); got != 3 {
		t.Fatalf("processed jobs after close = %d, want 3 without loss", got)
	}
}
