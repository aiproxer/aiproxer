package archtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

type goworkOffCmd struct {
	Name string
	Args []string
	Dir  string
	Env  []string
}

func buildGOWORKOffCommandPlan(root, tempDir string) []goworkOffCmd {
	env := append(os.Environ(), "GOWORK=off")
	binaryName := "aipstd"
	if runtime.GOOS == "windows" {
		binaryName = "aipstd.exe"
	}
	return []goworkOffCmd{
		{Name: "go", Args: []string{"list", "./..."}, Dir: root, Env: env},
		{Name: "go", Args: []string{"list", "-m", "all"}, Dir: root, Env: env},
		{Name: "go", Args: []string{"build", "-o", filepath.Join(tempDir, binaryName), "./cmd/aipstd"}, Dir: root, Env: env},
		{
			Name: "go",
			Args: []string{
				"test", "-run=^$", "-count=1",
				"./pkg/aipapi/...",
				"./pkg/aipsdk/...",
				"./api/backendplugin/...",
				"./cmd/aipstd",
			},
			Dir: root,
			Env: env,
		},
	}
}

func runGOWORKOffCommandPlanContext(ctx context.Context, plan []goworkOffCmd) error {
	// The plan steps are independent read/build commands over the same tree, and
	// the Go command's own build cache serializes shared work safely. Running
	// them concurrently collapses the plan's wall clock (the single largest
	// archtest cost under a saturated suite) without weakening any assertion:
	// the first failing step still cancels its siblings and surfaces the error.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errCh := make(chan error, len(plan))
	for _, step := range plan {
		wg.Go(func() {
			cmd := exec.CommandContext(ctx, step.Name, step.Args...)
			cmd.Dir = step.Dir
			cmd.Env = step.Env
			out, err := cmd.CombinedOutput()
			if err == nil {
				return
			}
			select {
			case errCh <- fmt.Errorf("%s %v: %w\n%s", step.Name, step.Args, err, out):
				cancel()
			default:
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func runGOWORKOffCommandPlan(t *testing.T, plan []goworkOffCmd) {
	t.Helper()
	if err := runGOWORKOffCommandPlanContext(t.Context(), plan); err != nil {
		t.Fatalf("run GOWORK=off plan: %v", err)
	}
}

func TestGOWORKOff_CommandPlanSafety(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir1, dir2 := filepath.Join(t.TempDir(), "1"), filepath.Join(t.TempDir(), "2")
	p1, p2 := buildGOWORKOffCommandPlan(root, dir1), buildGOWORKOffCommandPlan(root, dir2)

	if len(p1) != 4 {
		t.Fatalf("want 4 commands, got %d", len(p1))
	}
	binaryName := "aipstd"
	if runtime.GOOS == "windows" {
		binaryName = "aipstd.exe"
	}
	wantCommands := [][]string{
		{"list", "./..."},
		{"list", "-m", "all"},
		{"build", "-o", filepath.Join(dir1, binaryName), "./cmd/aipstd"},
		{"test", "-run=^$", "-count=1", "./pkg/aipapi/...", "./pkg/aipsdk/...", "./api/backendplugin/...", "./cmd/aipstd"},
	}
	for i, step := range p1 {
		if step.Name != "go" || step.Dir != root || !slices.Contains(step.Env, "GOWORK=off") {
			t.Errorf("step %d invalid environment/dir: name=%s dir=%s", i, step.Name, step.Dir)
		}
		if !slices.Equal(step.Args, wantCommands[i]) {
			t.Errorf("step %d args mismatch: got %v, want %v", i, step.Args, wantCommands[i])
		}
	}
	if p1[2].Args[2] == p2[2].Args[2] || !strings.HasPrefix(p1[2].Args[2], dir1) {
		t.Errorf("build outputs not distinct per invocation: p1=%s, p2=%s", p1[2].Args[2], p2[2].Args[2])
	}
}

func TestGOWORKOff_CommandPlanCancellation(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	dir := filepath.Join(t.TempDir(), "cancel")
	plan := buildGOWORKOffCommandPlan(root, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runGOWORKOffCommandPlanContext(ctx, plan)
	if err == nil {
		t.Fatal("expected error on canceled context, got nil")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected context cancellation error, got %v", err)
	}
}
