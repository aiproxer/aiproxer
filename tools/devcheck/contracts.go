package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/testscope"
)

func runContractCheck(ctx context.Context, root, base string, opts testPlanOptions, output, diagnostics io.Writer) error {
	planning, cancel := context.WithTimeout(ctx, 2*time.Minute)
	plan, err := testscope.BuildContracts(planning, root, base)
	cancel()
	if err != nil {
		return err
	}
	if err := json.NewEncoder(diagnostics).Encode(plan); err != nil {
		return err
	}
	if opts.dry {
		opts.recorder.skip("contract and lint execution", "plan-only request")
		_, err := fmt.Fprintln(diagnostics, "plan-only: no contracts or lint executed")
		return err
	}
	if len(plan.Tests) == 0 && len(plan.Lint) == 0 && len(plan.Scripts) == 0 && len(plan.QA) == 0 && len(plan.Groups) == 0 {
		opts.recorder.skip("contract and lint execution", "no checks selected")
	}
	for _, script := range plan.Scripts {
		command := append([]string{"bash"}, strings.Fields(script)...)
		if err := runContractCommandRecorded(ctx, root, command, false, output, diagnostics, opts.recorder); err != nil {
			return err
		}
	}
	if len(plan.QA) != 0 {
		cmd := exec.CommandContext(ctx, "go", "test", "-list", contractPattern(plan.QA), "./internal/qa")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Stderr = diagnostics
		listed, err := commandOutput(ctx, cmd)
		if err != nil {
			return err
		}
		if err := validateListedContracts(plan.QA, string(listed)); err != nil {
			return err
		}
		command, err := commandFor("test", "./internal/qa", opts.jobs, opts.fresh)
		if err != nil {
			return err
		}
		command = slices.Insert(command, len(command)-1, "-run", contractPattern(plan.QA))
		if err := runContractCommandRecorded(ctx, root, command, true, output, diagnostics, opts.recorder); err != nil {
			return err
		}
	}
	if opts.automationOnly {
		if len(plan.Scripts) == 0 && len(plan.QA) == 0 {
			opts.recorder.skip("automation checks", "no checks selected")
		}
		return nil
	}
	if len(plan.Tests) != 0 {
		// A removed/renamed test must not turn an exact -run filter into a
		// successful no-op. Ask the test binary, not a source-text scanner.
		cmd := exec.CommandContext(ctx, "go", "test", "-mod=readonly", "-trimpath", "-list", contractPattern(plan.Tests), "./internal/archtest")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Stderr = diagnostics
		listed, err := commandOutput(ctx, cmd)
		if err != nil {
			return fmt.Errorf("list selected contracts: %w", err)
		}
		if err := validateListedContracts(plan.Tests, string(listed)); err != nil {
			return err
		}
	}
	for _, group := range plan.Groups {
		cmd := exec.CommandContext(ctx, "go", "test", "-mod=readonly", "-trimpath", "-list", contractPattern(group.Tests), group.Package)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOWORK=off")
		cmd.Stderr = diagnostics
		listed, err := commandOutput(ctx, cmd)
		if err != nil {
			return fmt.Errorf("list selected contracts in %s: %w", group.Package, err)
		}
		if err := validateListedContracts(group.Tests, string(listed)); err != nil {
			return err
		}
	}
	for n := 1; n <= opts.repeat; n++ {
		if len(plan.Tests) != 0 {
			command, err := commandFor("test", "./internal/archtest", opts.jobs, opts.fresh)
			if err != nil {
				return err
			}
			command = slices.Insert(command, len(command)-1, "-trimpath", "-run", contractPattern(plan.Tests))
			if err := runContractCommandRecorded(ctx, root, command, true, output, diagnostics, opts.recorder); err != nil {
				return err
			}
		}
		for _, group := range plan.Groups {
			command, err := commandFor("test", group.Package, opts.jobs, opts.fresh)
			if err != nil {
				return err
			}
			command = slices.Insert(command, len(command)-1, "-trimpath", "-run", contractPattern(group.Tests))
			if err := runContractCommandRecorded(ctx, root, command, true, output, diagnostics, opts.recorder); err != nil {
				return err
			}
		}
		for _, module := range plan.Lint {
			dir, err := moduleDirectory(root, module.Directory)
			if err != nil {
				return err
			}
			command, err := commandFor("lint", strings.Join(module.Packages, " "), opts.jobs, false)
			if err != nil {
				return err
			}
			command = withAnalyzerBudget(root, command)
			if err := runContractCommandRecorded(ctx, dir, command, false, output, diagnostics, opts.recorder); err != nil {
				return err
			}
		}
	}
	_, err = fmt.Fprintln(diagnostics, "Local contract/lint feedback only; comprehensive delivery gates remain required.")
	return err
}

func runContractCommand(ctx context.Context, dir string, command []string, test bool, output, diagnostics io.Writer) error {
	return runContractCommandRecorded(ctx, dir, command, test, output, diagnostics, nil)
}

func runContractCommandRecorded(ctx context.Context, dir string, command []string, test bool, output, diagnostics io.Writer, recorder *evidenceRecorder) error {
	if _, err := fmt.Fprintf(diagnostics, "directory=%q command=%q\n", dir, command); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Stderr = diagnostics
	step := recorder.start("contract-check", command, dir, output)
	cmd.Stderr = step.stderr(diagnostics)
	stats, err := execute(ctx, cmd, test, step.stdout(test), step.rawLog())
	recorder.finishRun(step, stepStats(test, stats), err)
	return err
}

func contractPattern(tests []string) string {
	quoted := make([]string, len(tests))
	for i, name := range tests {
		quoted[i] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(quoted, "|") + ")$"
}

func validateListedContracts(tests []string, listed string) error {
	names := strings.Fields(listed)
	for _, name := range tests {
		if !slices.Contains(names, name) {
			return fmt.Errorf("selected contract %s missing; update the contract selector before continuing", name)
		}
	}
	return nil
}
