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

// scripts/race-check.sh already partitions the full (non-staged) scan: it removes
// internal/archtest from the ordinary scan and runs it afterwards with a dedicated
// 25m budget, because the archtest AST scans are slow enough under -race to push
// concurrent package runs past their timeouts when they share one `go test`
// invocation (issue #262).
//
// The staged path selects the same package scopes from the staged file set, so it
// must apply the same partition: running archtest together with the rest of the
// staged scopes restores exactly the CPU contention that separation exists to
// avoid. These tests execute the real script against fake `go`/`git`/`cc`
// binaries on a temporary PATH, so they assert scheduling without ever building,
// scanning, or reading the developer's real Git index.

const raceCheckScript = "scripts/race-check.sh"

// raceCheckInvocation is one recorded `go test` call made by race-check.sh.
type raceCheckInvocation struct {
	line     string
	flags    []string
	packages []string
}

type raceCheckExpectation struct {
	packages []string
	// requiredFlags must appear in the recorded invocation.
	requiredFlags []string
	// forbiddenFlags must not appear in the recorded invocation.
	forbiddenFlags []string
}

type raceCheckScenario struct {
	name string
	// staged selects --staged; otherwise the full scan runs.
	staged bool
	// stagedFiles is the `git diff --cached --name-only` answer.
	stagedFiles []string
	// fixtureFiles are additional empty files created in the fixture root
	// before running the script (for example, a nested module marker such as
	// connectors/codex/go.mod that module discovery reads from disk).
	fixtureFiles []string
	// fullList is the `go list ./...` answer used by the full scan.
	fullList []string
	// failMatch makes the fake `go test` fail when it sees a package containing it.
	failMatch string
	wantExit  int
	want      []raceCheckExpectation
}

func TestRaceCheckStagedScanPartitionsArchtestFromOrdinaryScopes(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the authoritative race gate is
		// scripts/race-check.sh on Linux/macOS CI, and the Windows shim is
		// scripts/race-check.ps1.
		t.Skip("race-check.sh contract requires the repository's POSIX bash runtime")
	}

	// The staged budget, race flag, tags, and count flag are part of the
	// preserved contract for every staged invocation.
	stagedFlags := []string{"-race", "-tags=precommit,integration", "-count=1"}
	stagedForbidden := []string{"-timeout="}

	scenarios := []raceCheckScenario{
		{
			// RED with the pre-partition script: it issued a single invocation
			// combining archtest with extensions/runtime.
			name:   "mixed selection runs ordinary and archtest in separate invocations",
			staged: true,
			stagedFiles: []string{
				"internal/archtest/plane_rules_tables.go",
				"internal/archtest/tools/changesurface/scan.go",
				"internal/core/extensions/control_tool.go",
				"internal/core/runtime/attempt_session.go",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/core/extensions/...", "./internal/core/runtime/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
				{
					packages: []string{
						"./internal/archtest/...",
						"./internal/archtest/tools/changesurface/...",
					},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			name:   "archtest-only selection runs only the selected archtest scope",
			staged: true,
			stagedFiles: []string{
				"internal/archtest/plane_rules_tables.go",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/archtest/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			// A staged tree containing only nested-module Go files must scan
			// the nested module in its own module context instead of tripping
			// the empty-set guard: PACKAGES and ARCH_PACKAGES are both empty
			// here, so omitting NESTED_SCOPES from the guard exits before the
			// nested scan is reached.
			name:   "nested-module-only selection scans the nested module in its own context",
			staged: true,
			stagedFiles: []string{
				"connectors/codex/internal/responsestream/mapper.go",
			},
			fixtureFiles: []string{
				"connectors/codex/go.mod",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/responsestream/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			// Mixed root and nested selections scan both groups: the ordinary
			// root-module scope first, then the nested module in its own
			// context. The nested invocation records module-relative package
			// paths.
			name:   "mixed root and nested selection scans both groups",
			staged: true,
			stagedFiles: []string{
				"internal/core/runtime/attempt_session.go",
				"connectors/codex/internal/responsestream/mapper.go",
			},
			fixtureFiles: []string{
				"connectors/codex/go.mod",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/core/runtime/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
				{
					packages:       []string{"./internal/responsestream/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			// The empty ordinary group must not be invoked at all: `go test`
			// with no package args silently scans the current directory.
			name:   "ordinary-only selection never scans archtest",
			staged: true,
			stagedFiles: []string{
				"internal/core/extensions/control_tool.go",
				"internal/core/runtime/attempt_session.go",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/core/extensions/...", "./internal/core/runtime/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			name:   "archtest child package scopes stay in the archtest group",
			staged: true,
			stagedFiles: []string{
				"internal/archtest/tools/changesurface/scan.go",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/archtest/tools/changesurface/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			// The partition must key on the archtest subtree itself, not on any
			// path that merely contains "archtest": ./internal/archtest_extra and
			// ./pkg/aipsdk/auxiliary/archtest are ordinary scopes and must stay in
			// the ordinary group.
			name:   "archtest lookalike paths stay in the ordinary group",
			staged: true,
			stagedFiles: []string{
				"internal/archtest/plane_rules_tables.go",
				"internal/archtest_extra/whitelist.go",
				"pkg/aipsdk/auxiliary/archtest/helper.go",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages: []string{
						"./internal/archtest_extra/...",
						"./pkg/aipsdk/auxiliary/archtest/...",
					},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
				{
					packages:       []string{"./internal/archtest/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			name:   "ordinary group failure still fails the scan after archtest runs",
			staged: true,
			stagedFiles: []string{
				"internal/archtest/plane_rules_tables.go",
				"internal/core/runtime/attempt_session.go",
			},
			failMatch: "./internal/core/runtime/...",
			wantExit:  1,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/core/runtime/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
				{
					packages:       []string{"./internal/archtest/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			name:   "archtest group failure still fails the scan after the ordinary run",
			staged: true,
			stagedFiles: []string{
				"internal/archtest/plane_rules_tables.go",
				"internal/core/runtime/attempt_session.go",
			},
			failMatch: "./internal/archtest/...",
			wantExit:  1,
			want: []raceCheckExpectation{
				{
					packages:       []string{"./internal/core/runtime/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
				{
					packages:       []string{"./internal/archtest/..."},
					requiredFlags:  stagedFlags,
					forbiddenFlags: stagedForbidden,
				},
			},
		},
		{
			// The full scan already separates archtest with a dedicated 25m
			// budget; the partition must not disturb it. Main's lane structure
			// additionally separates billing, billingstore and durable runtime;
			// the support-agreement run carries -run so the fixture harness
			// records only the five below.
			name:   "full scan keeps its separate archtest invocation and 25m budget",
			staged: false,
			fullList: []string{
				"github.com/aiproxer/aiproxer/internal/archtest",
				"github.com/aiproxer/aiproxer/internal/archtest/tools/changesurface",
				"github.com/aiproxer/aiproxer/internal/core/runtime",
				"github.com/aiproxer/aiproxer/enterprise/internal/infra/billingstore",
				"github.com/aiproxer/aiproxer/pkg/aipsdk",
			},
			wantExit: 0,
			want: []raceCheckExpectation{
				{
					packages: []string{
						"github.com/aiproxer/aiproxer/pkg/aipsdk",
					},
					requiredFlags:  []string{"-race", "-tags=precommit,integration", "-count=1"},
					forbiddenFlags: []string{"-timeout="},
				},
				{
					packages:       []string{"./enterprise/internal/infra/billingstore"},
					requiredFlags:  []string{"-race", "-tags=precommit,integration", "-count=1", "-timeout=60m"},
					forbiddenFlags: []string{"-skip", "-run"},
				},
				{
					packages: []string{
						"./enterprise/internal/core/billing",
					},
					requiredFlags:  []string{"-race", "-tags=precommit,integration", "-count=1", "-timeout=60m", "-skip"},
					forbiddenFlags: nil,
				},
				{
					packages: []string{
						"./internal/core/runtime",
					},
					requiredFlags:  []string{"-race", "-tags=precommit,integration", "-count=1"},
					forbiddenFlags: []string{"-timeout="},
				},
				{
					packages:       []string{"./internal/archtest/..."},
					requiredFlags:  []string{"-race", "-tags=precommit,integration", "-count=1", "-timeout=25m"},
					forbiddenFlags: nil,
				},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			assertRaceCheckScenario(t, scenario)
		})
	}
}

func assertRaceCheckScenario(t *testing.T, scenario raceCheckScenario) {
	t.Helper()

	root := t.TempDir()
	writeRaceFixture(t, filepath.Join(root, raceCheckScript), readRepositoryFile(t, "scripts", "race-check.sh"), 0o755)
	writeRaceFixture(t, filepath.Join(root, "scripts", "dev-host-policy.sh"), readRepositoryFile(t, "scripts", "dev-host-policy.sh"), 0o644)

	binDir := filepath.Join(root, "fakebin")
	recordPath := filepath.Join(root, "go-invocations.log")
	listPath := filepath.Join(root, "go-list.txt")
	writeRaceFixture(t, filepath.Join(binDir, "go"), fakeRaceCheckGo, 0o755)
	writeRaceFixture(t, filepath.Join(binDir, "git"), fakeRaceCheckGit, 0o755)
	writeRaceFixture(t, filepath.Join(binDir, "cc"), "#!/usr/bin/env bash\nexit 0\n", 0o755)
	writeRaceFixture(t, listPath, strings.Join(scenario.fullList, "\n")+"\n", 0o644)
	if err := os.WriteFile(recordPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range scenario.fixtureFiles {
		writeRaceFixture(t, filepath.Join(root, name), "", 0o644)
	}

	args := []string{raceCheckScript, "--strict"}
	if scenario.staged {
		args = append(args, "--staged")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Dir = root
	// The fake bin dir is prepended so the script can never reach the real Go
	// toolchain, the real Git index, or a real C compiler.
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		// Bypass the development-host guard in scripts/race-check.sh: these
		// scenarios exercise the scan scheduling with a stubbed toolchain and
		// must run identically on every host, including blocked dev machines.
		// The guard itself is pinned separately by TestRaceCheckDevHostGuard.
		"AIP_ALLOW_RACE_ON_DEV=1",
		"AIP_FAKE_GO_RECORD="+recordPath,
		"AIP_FAKE_GO_LIST="+listPath,
		"AIP_FAKE_GO_FAIL="+scenario.failMatch,
		"AIP_FAKE_GIT_FILES="+strings.Join(scenario.stagedFiles, "\n"),
	)
	output, runErr := cmd.CombinedOutput()

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			t.Fatalf("race-check.sh execution error: %v\n%s", runErr, output)
		}
		exitCode = exitErr.ExitCode()
	}
	if exitCode != scenario.wantExit {
		t.Fatalf("race-check.sh exit = %d, want %d\n%s", exitCode, scenario.wantExit, output)
	}

	invocations := readRaceInvocations(t, recordPath)
	if len(invocations) != len(scenario.want) {
		t.Fatalf("go test invocation count = %d, want %d\nrecorded:\n%s\nscript output:\n%s",
			len(invocations), len(scenario.want), strings.Join(raceInvocationLines(invocations), "\n"), output)
	}
	for i, expected := range scenario.want {
		got := invocations[i]
		if !equalRaceStrings(got.packages, expected.packages) {
			t.Errorf("invocation %d packages = %v, want %v\nrecorded: %s", i, got.packages, expected.packages, got.line)
		}
		// Required flags match exactly: a prefix match would let -count=10
		// satisfy -count=1, or -racefoo satisfy -race.
		for _, flag := range expected.requiredFlags {
			if !hasRaceFlag(got.flags, flag) {
				t.Errorf("invocation %d missing exact flag %q\nrecorded: %s", i, flag, got.line)
			}
		}
		// Forbidden flags match by prefix: a staged invocation must not add any
		// -timeout=<budget> variant, so -timeout=25m also has to be caught.
		for _, flag := range expected.forbiddenFlags {
			if hasRaceFlagPrefix(got.flags, flag) {
				t.Errorf("invocation %d must not contain %q\nrecorded: %s", i, flag, got.line)
			}
		}
		if len(got.packages) == 0 {
			t.Errorf("invocation %d passed no package args; go test would scan the current directory\nrecorded: %s", i, got.line)
		}
	}
}

func readRaceInvocations(t *testing.T, recordPath string) []raceCheckInvocation {
	t.Helper()
	raw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var invocations []raceCheckInvocation
	for line := range strings.SplitSeq(strings.TrimRight(string(raw), "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		invocation := raceCheckInvocation{line: line}
		for _, field := range fields {
			if strings.HasPrefix(field, "./") || strings.HasPrefix(field, "github.com/") {
				invocation.packages = append(invocation.packages, field)
				continue
			}
			invocation.flags = append(invocation.flags, field)
		}
		invocations = append(invocations, invocation)
	}
	return invocations
}

func raceInvocationLines(invocations []raceCheckInvocation) []string {
	lines := make([]string, 0, len(invocations))
	for _, invocation := range invocations {
		lines = append(lines, invocation.line)
	}
	return lines
}

func equalRaceStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// hasRaceFlag reports whether want is present as an exact argument token.
func hasRaceFlag(tokens []string, want string) bool {
	return slices.Contains(tokens, want)
}

// hasRaceFlagPrefix reports whether any token starts with prefix.
func hasRaceFlagPrefix(tokens []string, prefix string) bool {
	for _, token := range tokens {
		if strings.HasPrefix(token, prefix) {
			return true
		}
	}
	return false
}

func writeRaceFixture(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

// fakeRaceCheckGo answers only the `go` subcommands race-check.sh uses: the race
// prerequisites (`go env`), the full-scan package list (`go list ./...`), and the
// test runs. The compile-only precheck is not recorded, every real test run is
// appended to $AIP_FAKE_GO_RECORD, and a package containing $AIP_FAKE_GO_FAIL
// fails so exit propagation is observable.
const fakeRaceCheckGo = `#!/usr/bin/env bash
set -uo pipefail

sub="${1:-}"
shift || true

case "$sub" in
env)
	case "${1:-}" in
	CGO_ENABLED) printf '1\n'; exit 0 ;;
	CC) printf 'cc\n'; exit 0 ;;
	esac
	printf '\n'
	exit 0
	;;
list)
	if [[ -n "${AIP_FAKE_GO_LIST:-}" && -f "${AIP_FAKE_GO_LIST}" ]]; then
		cat "$AIP_FAKE_GO_LIST"
	fi
	exit 0
	;;
test)
	;;
*)
	exit 0
	;;
esac

# The compile-only precheck is not a package scan; never record it.
for arg in "$@"; do
	case "$arg" in
	-c | -run) exit 0 ;;
	esac
done

if [[ -n "${AIP_FAKE_GO_RECORD:-}" ]]; then
	printf '%s\n' "test $*" >>"$AIP_FAKE_GO_RECORD"
fi

if [[ -n "${AIP_FAKE_GO_FAIL:-}" ]]; then
	for arg in "$@"; do
		case "$arg" in
		*"${AIP_FAKE_GO_FAIL}"*)
			printf 'fake go test: injected failure for %s\n' "$arg" >&2
			exit 1
			;;
		esac
	done
fi

printf 'ok\tfake\t0.001s\n'
exit 0
`

// fakeRaceCheckGit supplies staged file paths for `git diff --cached
// --name-only` without touching the developer's index.
const fakeRaceCheckGit = `#!/usr/bin/env bash
set -uo pipefail

if [[ "${1:-}" == "diff" ]]; then
	if [[ -n "${AIP_FAKE_GIT_FILES:-}" ]]; then
		printf '%s\n' "${AIP_FAKE_GIT_FILES}"
	fi
	exit 0
fi

exit 0
`
