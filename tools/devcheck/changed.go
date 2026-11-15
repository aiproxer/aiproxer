package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/testscope"
)

type testPlanOptions struct {
	jobs, parallel, moduleJobs, repeat int
	fresh, dry                         bool
	quarantine                         []quarantineEntry
	recorder                           *evidenceRecorder
	automationOnly                     bool
}

func validateChangedScope(task, module, packages string) error {
	if task != "test" || module != "." || packages != "" {
		return errors.New("automatic selection requires task=test and no explicit PKGS or MODULE; use dev-test for manual scope")
	}
	return nil
}

func runTestPlan(ctx context.Context, root string, plan testscope.Plan, opts testPlanOptions, output, diagnostics io.Writer) error {
	if err := reportTestPlan(plan, opts.dry, diagnostics); err != nil {
		return err
	}
	if opts.dry || len(plan.Modules) == 0 {
		if len(plan.Modules) == 0 {
			opts.recorder.skip("default tests", "no packages selected")
		}
		return nil
	}
	if opts.moduleJobs > 1 && len(plan.Modules) > 1 {
		output = &lockedWriter{w: output}
		diagnostics = &lockedWriter{w: diagnostics}
	}
	return repeatTestModules(ctx, plan.Modules, opts.repeat, opts.moduleJobs, diagnostics, func(runCtx context.Context, module testscope.Module, iteration int) error {
		return runTestModule(runCtx, root, module, opts, iteration, output, diagnostics)
	})
}

func repeatTestModules(ctx context.Context, modules []testscope.Module, repeat, moduleJobs int, diagnostics io.Writer, runModule func(context.Context, testscope.Module, int) error) error {
	for n := 1; n <= repeat; n++ {
		start := time.Now()
		if err := runModulesBounded(ctx, modules, moduleJobs, func(runCtx context.Context, module testscope.Module) error {
			return runModule(runCtx, module, n)
		}); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(diagnostics, "[%d/%d] execution_elapsed=%.3fs\n", n, repeat, time.Since(start).Seconds()); err != nil {
			return err
		}
	}
	return nil
}

// runModulesBounded runs up to limit modules at once. The first failure
// cancels the remaining work and is returned after every started module has
// stopped, so a later repeat never starts against a failed plan.
func runModulesBounded(ctx context.Context, modules []testscope.Module, limit int, run func(context.Context, testscope.Module) error) error {
	if limit > len(modules) {
		limit = len(modules)
	}
	if limit <= 1 {
		for _, module := range modules {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := run(ctx, module); err != nil {
				return err
			}
		}
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var firstErr error
	var mu sync.Mutex
launch:
	for _, module := range modules {
		select {
		case sem <- struct{}{}:
		case <-runCtx.Done():
			break launch
		}
		wg.Add(1)
		go func(module testscope.Module) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := run(runCtx, module); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				cancel()
			}
		}(module)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

// lockedWriter serializes writes from concurrently running modules so one
// module's JSON events or diagnostics cannot split another's line.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func reportTestPlan(plan testscope.Plan, dry bool, diagnostics io.Writer) error {
	lines := []string{
		"Local development feedback only; comprehensive delivery gates remain required.",
		fmt.Sprintf("comparison=%q changed_files=%d selected_modules=%d", plan.Base, len(plan.Changed), len(plan.Modules)),
	}
	if plan.Fallback != "" {
		lines = append(lines, fmt.Sprintf("full_default_tests_reason=%q", plan.Fallback))
	}
	for _, module := range plan.Modules {
		lines = append(lines, fmt.Sprintf("module=%q packages=%q", module.Directory, module.Packages))
		for _, reason := range module.Reasons {
			lines = append(lines, fmt.Sprintf("  reason=%q", reason))
		}
	}
	if dry {
		lines = append(lines, "plan-only: no tests executed")
	} else if len(plan.Modules) == 0 {
		lines = append(lines, "nothing selected: no changes requiring default tests")
	}
	_, err := io.WriteString(diagnostics, strings.Join(lines, "\n")+"\n")
	return err
}

func runTestModule(ctx context.Context, root string, module testscope.Module, opts testPlanOptions, iteration int, output, diagnostics io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := moduleDirectory(root, module.Directory)
	if err != nil {
		return err
	}
	command, err := commandFor("test", strings.Join(module.Packages, " "), opts.jobs, opts.parallel, opts.fresh)
	if err != nil {
		return err
	}
	command = withQuarantine(command, opts.quarantine)
	if _, err := fmt.Fprintf(diagnostics, "[%d/%d] module=%q command=%q\n", iteration, opts.repeat, module.Directory, command); err != nil {
		return err
	}
	start := time.Now()
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Stderr = diagnostics
	step := opts.recorder.start(module.Directory, command, dir, output)
	cmd.Stderr = step.stderr(diagnostics)
	stats, err := execute(ctx, cmd, true, step.stdout(true), step.rawLog())
	opts.recorder.finishRun(step, stepStats(true, stats), err)
	_, reportErr := fmt.Fprintf(diagnostics, "elapsed=%.3fs passed=%d cached=%d failed=%d skipped=%d\n", time.Since(start).Seconds(), stats.Passed, stats.Cached, stats.Failed, stats.Skipped)
	if err = errors.Join(err, reportErr); err != nil {
		return fmt.Errorf("default tests failed in %s: %w", module.Directory, err)
	}
	return nil
}
