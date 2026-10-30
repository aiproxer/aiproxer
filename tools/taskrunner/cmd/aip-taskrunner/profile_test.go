package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

func TestProfile_WindowsFullReleaseDeadlinePropagation(t *testing.T) {
	t.Parallel()
	wantPhases := []string{
		"quality-checks", "test-unit", "parity-checks", "test-fuzz", "qa-tests",
		"lint", "vuln", "backend-plugin-module-checks", "backend-plugin-security-checks",
		"backend-plugin-cross-platform-qa", "backend-plugin-release-gates",
	}
	if fmt.Sprint(windowsFullReleasePhases) != fmt.Sprint(wantPhases) {
		t.Fatalf("profile phase list drift: %v", windowsFullReleasePhases)
	}

	//nolint:paralleltest // verifies deliberate sequential process execution.
	t.Run("sequential stop on child failure", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		parentDeadline, _ := parent.Deadline()
		var ran []string
		run := func(ctx context.Context, phase string) taskrunner.Result {
			ran = append(ran, phase)
			childDeadline, ok := ctx.Deadline()
			if !ok || !childDeadline.Equal(parentDeadline) {
				t.Errorf("phase %s did not inherit the profile deadline (deadline=%v ok=%v)", phase, childDeadline, ok)
			}
			if phase == "test-unit" {
				return taskrunner.Result{Kind: taskrunner.ChildFailure, ExitCode: 7, Err: errors.New("child failed"), Label: "windows-full-release:test-unit"}
			}
			return taskrunner.Result{Kind: taskrunner.Success, Label: "windows-full-release:" + phase}
		}
		code := runProfilePhases(parent, windowsFullReleasePhases, run, nil)
		if code != 1 {
			t.Fatalf("child failure exit = %d, want 1", code)
		}
		if fmt.Sprint(ran) != fmt.Sprint([]string{"quality-checks", "test-unit"}) {
			t.Fatalf("sequential stop violated: ran %v", ran)
		}
	})

	//nolint:paralleltest // verifies deadline propagation in process execution.
	t.Run("parent deadline propagation and distinct timeout exit", func(t *testing.T) {
		parent, cancel := context.WithCancel(context.Background())
		cancel()
		var ran []string
		run := func(ctx context.Context, phase string) taskrunner.Result {
			ran = append(ran, phase)
			if ctx.Err() != nil {
				return taskrunner.Result{Kind: taskrunner.DeadlineExceeded, Err: ctx.Err(), Label: "windows-full-release:" + phase}
			}
			return taskrunner.Result{Kind: taskrunner.Success, Label: "windows-full-release:" + phase}
		}
		code := runProfilePhases(parent, windowsFullReleasePhases, run, nil)
		if code != 2 {
			t.Fatalf("profile deadline exit = %d, want 2 (distinct from child failure exit 1)", code)
		}
		if fmt.Sprint(ran) != fmt.Sprint([]string{"quality-checks"}) {
			t.Fatalf("deadline must stop after the first phase, ran %v", ran)
		}
	})
}
