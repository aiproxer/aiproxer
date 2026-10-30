package qa

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// The repository lint gate is a mandatory multi-linter gate, not an advisory
// report: `make lint`, the pre-commit staged lint and CI all invoke
// scripts/lint-all-modules.sh. When golangci-lint is unavailable that script
// used to print a warning and exit 0, so a missing analyzer silently turned the
// gate into a no-op, and a staticcheck-only host silently narrowed the analyzer
// set that CI enforces. tools/devcheck already refuses that substitution for
// `make dev-lint`; these tests execute the real script against fake linter
// binaries on a temporary PATH so both hosts are pinned without ever running a
// real analysis.

const lintAllModulesScript = "scripts/lint-all-modules.sh"

// The PowerShell entry point must retain successful advisory output beyond the
// taskrunner's diagnostic capture limit, and preserve ratchet failures.
func TestPowerShellLintAdvisoryReportsCompleteOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX fake analyzer")
	}
	pwsh, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell not installed")
	}
	bin := t.TempDir()
	writeLintFixture(t, filepath.Join(bin, "golangci-lint"), `#!/bin/sh
i=0
while [ "$i" -lt 2000 ]; do
  printf 'finding-%04d %0100d\n' "$i" "$i"
  i=$((i+1))
done
case " $* " in *--new-from-rev=*|*--disable=*) exit 1;; esac
`, 0o755)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"advisory", "ratchet", "mandatory"} {
		args := []string{"-NoProfile", "-File", filepath.Join(root, "scripts", "lint-all-modules.ps1"), "-Modules", "."}
		if mode != "mandatory" {
			args = append(args, "-Advisory")
		}
		if mode == "ratchet" {
			args = append(args, "-Ratchet", "-RatchetBase", "HEAD")
		}
		cmd := exec.CommandContext(t.Context(), pwsh, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		out, err := cmd.CombinedOutput()
		if (err != nil) != (mode != "advisory") {
			t.Fatalf("mode=%s: %v\n%s", mode, err, out)
		}
		for _, marker := range []string{"finding-0000", "finding-1000", "finding-1999"} {
			if !strings.Contains(string(out), marker) {
				t.Fatalf("mode=%s missing %s", mode, marker)
			}
		}
	}
}

type lintToolScenario struct {
	name string
	// advisory adds --advisory, whose report is defined as the full
	// golangci-lint analyzer set including the advisory style and complexity
	// linters.
	advisory bool
	// installFakeLinters are the linter binaries placed on the temporary PATH.
	installFakeLinters []string
	// wantLintRuns is how many linter invocations the script may record.
	wantLintRuns int
	// wantDisableFlag requires the mandatory analyzer set to be enforced.
	wantDisableFlag bool
}

func TestLintAllModulesFailsClosedWithoutRequiredAnalyzer(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the authoritative gate is
		// scripts/lint-all-modules.sh on Linux/macOS CI, and the Windows shim
		// is scripts/lint-all-modules.ps1.
		t.Skip("lint-all-modules.sh contract requires the repository's POSIX bash runtime")
	}

	scenarios := []lintToolScenario{
		{
			name:               "no analyzer available fails the mandatory gate",
			wantLintRuns:       0,
			wantDisableFlag:    true,
			installFakeLinters: nil,
		},
		{
			name:               "staticcheck is never substituted for the mandatory gate",
			installFakeLinters: []string{"staticcheck"},
			wantLintRuns:       0,
			wantDisableFlag:    true,
		},
		{
			name:               "advisory report also fails closed without golangci-lint",
			advisory:           true,
			installFakeLinters: nil,
			wantLintRuns:       0,
			wantDisableFlag:    false,
		},
		{
			name:               "golangci-lint runs the mandatory analyzer set",
			installFakeLinters: []string{"golangci-lint"},
			wantLintRuns:       1,
			wantDisableFlag:    true,
		},
		{
			name:               "advisory mode keeps the full analyzer set",
			advisory:           true,
			installFakeLinters: []string{"golangci-lint"},
			wantLintRuns:       1,
			wantDisableFlag:    false,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			assertLintToolScenario(t, scenario)
		})
	}
}

func assertLintToolScenario(t *testing.T, scenario lintToolScenario) {
	t.Helper()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLintFixture(t, filepath.Join(root, lintAllModulesScript), readRepositoryFile(t, "scripts", "lint-all-modules.sh"), 0o755)
	writeLintFixture(t, filepath.Join(root, "scripts", "go-dev-guard.sh"), readRepositoryFile(t, "scripts", "go-dev-guard.sh"), 0o755)
	writeLintFixture(t, filepath.Join(root, "scripts", "dev-host-policy.sh"), readRepositoryFile(t, "scripts", "dev-host-policy.sh"), 0o644)
	// The root module must exist so a resolved analyzer actually runs; the fake
	// linter replaces all analysis.
	writeLintFixture(t, filepath.Join(root, "go.mod"), "module example.invalid/lintfixture\n\ngo 1.26\n", 0o644)

	binDir := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(root, "linter-invocations.log")
	if err := os.WriteFile(recordPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, linter := range scenario.installFakeLinters {
		writeLintFixture(t, filepath.Join(binDir, linter), fakeLintRecordingLinter, 0o755)
	}

	args := []string{lintAllModulesScript}
	if scenario.advisory {
		args = append(args, "--advisory")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Dir = root
	// The fake bin dir is prepended and every PATH entry that really ships a
	// linter is dropped, so the developer's own installation cannot decide the
	// outcome of the "unavailable analyzer" cases.
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+lintPathWithoutLinters(t),
		"AIP_FAKE_LINTER_RECORD="+recordPath,
		"AIP_FAKE_LINTER_NAME=",
		"AIP_LINT_JOBS=",
		"AIP_LINT_CONCURRENCY=",
		"AIP_GO_SLOT_DIR="+filepath.Join(root, "slots"),
		"AIP_DEV_TMPDIR="+filepath.Join(root, "tmp"),
	)
	output, runErr := cmd.CombinedOutput()

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("lint-all-modules.sh execution error: %v\n%s", runErr, output)
		}
		exitCode = exitErr.ExitCode()
	}
	invocations := string(readLintFixture(t, recordPath))

	haveAnalyzer := slices.Contains(scenario.installFakeLinters, "golangci-lint")
	if haveAnalyzer && exitCode != 0 {
		t.Fatalf("installed analyzer must let the gate run, exit = %d\n%s", exitCode, output)
	}
	if !haveAnalyzer {
		if exitCode == 0 {
			t.Fatalf("missing golangci-lint must fail the gate, exit = 0\nscript output:\n%s", output)
		}
		if !strings.Contains(string(output), "golangci-lint") {
			t.Errorf("failure must name the missing analyzer\nscript output:\n%s", output)
		}
	}

	runs := countLintFixtureLines(t, invocations)
	if runs != scenario.wantLintRuns {
		t.Errorf("recorded linter invocations = %d, want %d\nrecorded:\n%s\nscript output:\n%s",
			runs, scenario.wantLintRuns, invocations, output)
	}
	if scenario.wantDisableFlag && runs > 0 && !strings.Contains(invocations, "--disable=modernize,paralleltest,thelper,gocognit") {
		t.Errorf("mandatory gate must enforce the reduced analyzer set\nrecorded:\n%s\nscript output:\n%s", invocations, output)
	}
	if !scenario.wantDisableFlag && runs > 0 && strings.Contains(invocations, "--disable=") {
		t.Errorf("advisory report must keep the full analyzer set\nrecorded:\n%s", invocations)
	}
	if scenario.advisory && runs > 0 {
		for _, want := range []string{
			"--build-tags=integration,precommit",
			"--max-issues-per-linter=0",
			"--max-same-issues=0",
			"--issues-exit-code=0",
		} {
			if !strings.Contains(invocations, want) {
				t.Errorf("advisory report must pass %s; recorded:\n%s", want, invocations)
			}
		}
	}
}

// fakeLintRecordingLinter records one line per invocation and succeeds, so the
// gate exercises only analyzer selection and argument construction.
const fakeLintRecordingLinter = `#!/usr/bin/env bash
{
	printf 'PWD=%s LINTER=%s ARGS' "$PWD" "${AIP_FAKE_LINTER_NAME:-${0##*/}}"
	for arg in "$@"; do printf ' %s' "$arg"; done
	printf '\n'
} >>"$AIP_FAKE_LINTER_RECORD"
exit "${AIP_FAKE_LINTER_EXIT:-0}"
`

// lintPathWithoutLinters drops every PATH entry that really provides one of the
// analyzers the script may select.
func lintPathWithoutLinters(t *testing.T) string {
	t.Helper()
	kept := make([]string, 0, 8)
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		shipsLinter := false
		for _, linter := range []string{"golangci-lint", "staticcheck"} {
			info, err := os.Stat(filepath.Join(dir, linter))
			if err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
				shipsLinter = true
				break
			}
		}
		if !shipsLinter {
			kept = append(kept, dir)
		}
	}
	return strings.Join(kept, string(os.PathListSeparator))
}

func writeLintFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func readLintFixture(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func countLintFixtureLines(t *testing.T, content string) int {
	t.Helper()
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}
