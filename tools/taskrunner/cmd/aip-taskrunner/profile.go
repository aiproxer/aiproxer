package main

import (
	"context"

	"github.com/aiproxer/aiproxer/tools/taskrunner"
)

// windowsFullReleasePhases is the exact ordered phase list owned by the
// explicit full-profile coordinator. Ordinary Make targets never claim this
// aggregate sequence or its 120-minute deadline.
var windowsFullReleasePhases = []string{
	"quality-checks",
	"test-unit",
	"parity-checks",
	"test-fuzz",
	"qa-tests",
	"lint",
	"vuln",
	"backend-plugin-module-checks",
	"backend-plugin-security-checks",
	"backend-plugin-cross-platform-qa",
	"backend-plugin-release-gates",
}

type profilePhaseRunner func(ctx context.Context, phase string) taskrunner.Result

// runProfilePhases runs the profile phases sequentially under one shared parent
// context and stops at the first non-success result, returning the CLI exit
// code for that result (deadline exit 2, child failure exit 1, infrastructure 3).
// The parent context deadline is passed to every phase; a phase timeout is
// therefore the same 120-minute profile deadline, and the coordinator never
// outlives it.
func runProfilePhases(ctx context.Context, phases []string, run profilePhaseRunner, report func(taskrunner.Result)) int {
	for _, phase := range phases {
		result := run(ctx, phase)
		if report != nil {
			report(result)
		}
		if result.Kind != taskrunner.Success {
			return resultExitCode(result)
		}
	}
	return 0
}
