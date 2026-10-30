package testkit

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func TestMakefile_AuthorityPostgresPooledUsesNormalParallelism(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	assertMakefileTarget(t, text, "test-authority-postgres-pooled:", func(t *testing.T, rest string) {
		t.Helper()
		if !strings.Contains(rest, "$(GO_TEST_FLAGS)") {
			t.Fatalf("must use $$(GO_TEST_FLAGS) for normal -parallel=8")
		}
		if strings.Contains(rest, "-parallel=1") {
			t.Fatal("must not force -parallel=1 (pooled runtime correctness proof)")
		}
		if !strings.Contains(rest, "AIP_REQUIRE_POSTGRES_POOLER") {
			t.Fatal("must fail closed via AIP_REQUIRE_POSTGRES_POOLER")
		}
		if !strings.Contains(rest, `test "$${AIP_TEST_POSTGRES_RUNTIME_IS_POOLER:-}" = "1"`) {
			t.Fatal("must require exact =1 attestation on the Unix branch")
		}
		if !strings.Contains(rest, `GetEnvironmentVariable('AIP_TEST_POSTGRES_RUNTIME_IS_POOLER','Process') -ne '1'`) {
			t.Fatal("must require exact =1 attestation on the Windows branch")
		}
		// Must cover TestPostgresPooled_* plus Phase*_PostgresPooled_* / DirectPooled_*
		// (exact ^TestPostgresPooled_ alone orphans Phase3/Phase34 pooled contracts).
		hasBroad := strings.Contains(rest, `-run 'Pooled'`) || strings.Contains(rest, `-run "Pooled"`)
		hasExact := strings.Contains(rest, `-run '^TestPostgresPooled_'`) || strings.Contains(rest, `-run "^TestPostgresPooled_"`)
		hasPhaseOrDirect := strings.Contains(rest, "Phase3") || strings.Contains(rest, "Phase34") || strings.Contains(rest, "DirectPooled")
		if !hasBroad && (!hasExact || !hasPhaseOrDirect) {
			t.Fatal("pooled gate must select TestPostgresPooled_* and Phase*_PostgresPooled_* (use -run 'Pooled' or equivalent)")
		}
		if !strings.Contains(rest, "./internal/infra/runtimebundle") {
			t.Fatal("must include runtimebundle composition proof in pooled gate")
		}
		if !strings.Contains(rest, "./internal/infra/terminalwork/workstore") {
			t.Fatal("must include terminalwork/workstore pooled contracts in pooled gate")
		}
	})
}

func TestMakefile_AuthorityPostgresDirectSkipsPooled(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	assertMakefileTarget(t, string(body), "test-authority-postgres-direct:", func(t *testing.T, rest string) {
		t.Helper()
		if !strings.Contains(rest, "$(GO_TEST_FLAGS)") {
			t.Fatalf("must use $$(GO_TEST_FLAGS) for normal -parallel=8")
		}
		// Broad -skip 'Pooled' also excludes Phase*_PostgresPooled_* helpers that
		// do not match the TestPostgresPooled_ prefix.
		hasExact := strings.Contains(rest, `-skip '^TestPostgresPooled_'`) || strings.Contains(rest, `-skip "^TestPostgresPooled_"`)
		hasBroad := strings.Contains(rest, `-skip 'Pooled'`) || strings.Contains(rest, `-skip "Pooled"`)
		if !hasExact && !hasBroad {
			t.Fatal("must skip TestPostgresPooled_ (or broader Pooled) so direct and pooled gates stay separate")
		}
		if strings.Contains(rest, `-run '^TestPostgresPooled_'`) || strings.Contains(rest, `-run "^TestPostgresPooled_"`) {
			t.Fatal("direct gate must not select only pooled tests")
		}
		if !strings.Contains(rest, "./internal/infra/terminalwork/workstore") {
			t.Fatal("must include terminalwork/workstore direct contracts in direct gate")
		}
	})
}

func TestMakefile_DBParityTargetsDelegation(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	forbiddenPkgSubstrings := []string{
		"usageauthority",
		"concurrencyauthority",
		"journalstore",
		"workstore",
		"internal/infra",
		"internal/core/continuity",
	}

	// 1. test-db-parity-sqlite delegates to runner in sqlite mode without hardcoded package lists
	assertMakefileTarget(t, text, "test-db-parity-sqlite:", func(t *testing.T, rest string) {
		t.Helper()
		if !strings.Contains(rest, "./internal/testkit/dbparity/cmd") || !strings.Contains(rest, "sqlite") {
			t.Fatalf("test-db-parity-sqlite must delegate to dbparity runner with 'sqlite' mode, got:\n%s", rest)
		}
		if strings.Contains(rest, "-flags") || strings.Contains(rest, "--flags") {
			t.Fatalf("test-db-parity-sqlite must not interpolate -flags CLI arguments into recipe (rely on exported GO_TEST_FLAGS), got:\n%s", rest)
		}
		for _, forbidden := range forbiddenPkgSubstrings {
			if strings.Contains(rest, forbidden) {
				t.Fatalf("test-db-parity-sqlite must not hardcode package list (found %q in %s)", forbidden, rest)
			}
		}
	})

	// 2. test-db-parity-postgres-direct sets AIP_REQUIRE_POSTGRES=1 and delegates to runner in postgres-direct mode
	assertMakefileTarget(t, text, "test-db-parity-postgres-direct:", func(t *testing.T, rest string) {
		t.Helper()
		assertTargetNoPrerequisites(t, rest, "test-db-parity-postgres-direct:")

		winBranch, posixBranch := splitMakefileBranches(t, rest)

		// Windows branch
		if !strings.Contains(winBranch, "./internal/testkit/dbparity/cmd") || !strings.Contains(winBranch, "postgres-direct") {
			t.Fatalf("test-db-parity-postgres-direct Windows branch must delegate to dbparity runner with 'postgres-direct' mode, got:\n%s", winBranch)
		}
		if strings.Contains(winBranch, "-flags") || strings.Contains(winBranch, "--flags") {
			t.Fatalf("test-db-parity-postgres-direct Windows branch must not interpolate -flags into recipe, got:\n%s", winBranch)
		}
		if !strings.Contains(winBranch, `[Environment]::SetEnvironmentVariable('AIP_REQUIRE_POSTGRES','1','Process')`) {
			t.Fatalf("test-db-parity-postgres-direct Windows branch must set AIP_REQUIRE_POSTGRES=1 via SetEnvironmentVariable, got:\n%s", winBranch)
		}
		if !strings.Contains(winBranch, "AIP_MANAGED_POSTGRES_DSN") || !strings.Contains(winBranch, "AIP_TEST_POSTGRES_ADMIN_DSN") {
			t.Fatalf("test-db-parity-postgres-direct Windows branch must preserve runtime DSN, alias managed DSN, and fallback to admin DSN, got:\n%s", winBranch)
		}

		// POSIX branch
		if !strings.Contains(posixBranch, "./internal/testkit/dbparity/cmd") || !strings.Contains(posixBranch, "postgres-direct") {
			t.Fatalf("test-db-parity-postgres-direct POSIX branch must delegate to dbparity runner with 'postgres-direct' mode, got:\n%s", posixBranch)
		}
		if strings.Contains(posixBranch, "-flags") || strings.Contains(posixBranch, "--flags") {
			t.Fatalf("test-db-parity-postgres-direct POSIX branch must not interpolate -flags into recipe, got:\n%s", posixBranch)
		}
		if !strings.Contains(posixBranch, "AIP_REQUIRE_POSTGRES=1") {
			t.Fatalf("test-db-parity-postgres-direct POSIX branch must set AIP_REQUIRE_POSTGRES=1 prefix, got:\n%s", posixBranch)
		}
		if !strings.Contains(posixBranch, `AIP_TEST_POSTGRES_DSN="$${AIP_TEST_POSTGRES_DSN:-$${AIP_MANAGED_POSTGRES_DSN:-$$AIP_TEST_POSTGRES_ADMIN_DSN}}"`) {
			t.Fatalf("test-db-parity-postgres-direct POSIX branch must preserve runtime DSN, alias managed DSN, and fallback to admin DSN, got:\n%s", posixBranch)
		}

		for _, forbidden := range forbiddenPkgSubstrings {
			if strings.Contains(rest, forbidden) {
				t.Fatalf("test-db-parity-postgres-direct must not hardcode package list (found %q in %s)", forbidden, rest)
			}
		}
	})

	// 3. test-db-parity delegates to canonical runner in 'all' mode with AIP_REQUIRE_POSTGRES=1 wiring per branch
	assertMakefileTarget(t, text, "test-db-parity:", func(t *testing.T, rest string) {
		t.Helper()
		assertTargetNoPrerequisites(t, rest, "test-db-parity:")

		winBranch, posixBranch := splitMakefileBranches(t, rest)

		// Windows branch: delegates with 'all' mode and fail-closed env wiring
		if !strings.Contains(winBranch, "./internal/testkit/dbparity/cmd") || !strings.Contains(winBranch, "all") {
			t.Fatalf("test-db-parity Windows branch must invoke dbparity runner with 'all' mode, got:\n%s", winBranch)
		}
		if strings.Contains(winBranch, "-flags") || strings.Contains(winBranch, "--flags") {
			t.Fatalf("test-db-parity Windows branch must not interpolate -flags into recipe, got:\n%s", winBranch)
		}
		if !strings.Contains(winBranch, `[Environment]::SetEnvironmentVariable('AIP_REQUIRE_POSTGRES','1','Process')`) {
			t.Fatalf("test-db-parity Windows branch must set AIP_REQUIRE_POSTGRES=1 via SetEnvironmentVariable, got:\n%s", winBranch)
		}
		if !strings.Contains(winBranch, "AIP_MANAGED_POSTGRES_DSN") || !strings.Contains(winBranch, "AIP_TEST_POSTGRES_ADMIN_DSN") {
			t.Fatalf("test-db-parity Windows branch must preserve runtime DSN, alias managed DSN, and fallback to admin DSN, got:\n%s", winBranch)
		}

		// POSIX branch: delegates with 'all' mode and fail-closed env wiring
		if !strings.Contains(posixBranch, "./internal/testkit/dbparity/cmd") || !strings.Contains(posixBranch, "all") {
			t.Fatalf("test-db-parity POSIX branch must invoke dbparity runner with 'all' mode, got:\n%s", posixBranch)
		}
		if strings.Contains(posixBranch, "-flags") || strings.Contains(posixBranch, "--flags") {
			t.Fatalf("test-db-parity POSIX branch must not interpolate -flags into recipe, got:\n%s", posixBranch)
		}
		if !strings.Contains(posixBranch, "AIP_REQUIRE_POSTGRES=1") {
			t.Fatalf("test-db-parity POSIX branch must set AIP_REQUIRE_POSTGRES=1 prefix, got:\n%s", posixBranch)
		}
		if !strings.Contains(posixBranch, `AIP_TEST_POSTGRES_DSN="$${AIP_TEST_POSTGRES_DSN:-$${AIP_MANAGED_POSTGRES_DSN:-$$AIP_TEST_POSTGRES_ADMIN_DSN}}"`) {
			t.Fatalf("test-db-parity POSIX branch must contain AIP_TEST_POSTGRES_DSN fallback expression, got:\n%s", posixBranch)
		}

		for _, forbidden := range forbiddenPkgSubstrings {
			if strings.Contains(rest, forbidden) {
				t.Fatalf("test-db-parity must not hardcode package list (found %q in %s)", forbidden, rest)
			}
		}
	})
}

func TestMakefile_ExportGoTestFlagsPropagation(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "export GO_TEST_FLAGS") {
		t.Fatal("Makefile must include 'export GO_TEST_FLAGS'")
	}

	t.Run("makefile_dry_run_export_marker", func(t *testing.T) {
		makePath, err := exec.LookPath("make")
		if err != nil {
			t.Skip("make executable not found on PATH; skipping make child environment probe")
		}

		// Probe make dry-run with complex flags containing quotes
		cmd := exec.Command(makePath, "-n", "test-db-parity-sqlite", `GO_TEST_FLAGS=-run "Test Name" -count=1`)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make -n probe failed: %v\nOutput: %s", err, string(out))
		}
		outStr := string(out)
		if strings.Contains(outStr, "-flags") || strings.Contains(outStr, "--flags") {
			t.Fatalf("make recipe output should not contain interpolated -flags, got:\n%s", outStr)
		}
		if !strings.Contains(outStr, "internal/testkit/dbparity/cmd sqlite") {
			t.Fatalf("make recipe should invoke dbparity runner with sqlite, got:\n%s", outStr)
		}
	})

	t.Run("runner_plan_preserves_quoted_env_groups", func(t *testing.T) {
		rawEnv := `-run "^$" -count=1`
		parsed, err := dbparity.ParseFlagWords(rawEnv)
		if err != nil {
			t.Fatalf("ParseFlagWords(%q) failed: %v", rawEnv, err)
		}
		if len(parsed) != 3 || parsed[0] != "-run" || parsed[1] != "^$" || parsed[2] != "-count=1" {
			t.Fatalf("unexpected parsed flag words: %#v", parsed)
		}

		plans, err := dbparity.Plan(dbparity.ModeSQLite, dbparity.PlanOptions{
			GoTestFlags: parsed,
			ComponentID: "concurrency-authority",
		})
		if err != nil {
			t.Fatalf("Plan failed: %v", err)
		}
		if len(plans) != 1 {
			t.Fatalf("expected 1 plan, got %d", len(plans))
		}
		plan := plans[0]
		foundGroup := false
		for _, arg := range plan.Args {
			if arg == "^$" {
				foundGroup = true
			}
		}
		if !foundGroup {
			t.Fatalf("plan.Args missing preserved quoted flag group %q: %#v", "^$", plan.Args)
		}
	})

	t.Run("runner_cli_subprocess_handles_go_test_flags_and_errors", func(t *testing.T) {
		goBin, err := exec.LookPath("go")
		if err != nil {
			t.Skip("go binary not found on PATH; skipping subprocess test")
		}

		binPath := filepath.Join(t.TempDir(), "dbparity")
		if runtime.GOOS == "windows" {
			binPath += ".exe"
		}
		buildCmd := exec.Command(goBin, "build", "-o", binPath, "./internal/testkit/dbparity/cmd")
		buildCmd.Dir = root
		if buildOut, buildErr := buildCmd.CombinedOutput(); buildErr != nil {
			t.Fatalf("failed to build dbparity: %v\nOutput: %s", buildErr, string(buildOut))
		}

		// Valid GO_TEST_FLAGS with quoted no-op regex in sqlite mode (must execute actual sqlite mode and succeed with exit code 0)
		cmdValid := exec.Command(binPath, "sqlite", "-component", "control-plane-ledger")
		cmdValid.Dir = root
		cmdValid.Env = append(os.Environ(), `GO_TEST_FLAGS=-run "^$" -count=1`)
		outValid, errValid := cmdValid.CombinedOutput()
		if errValid != nil {
			t.Fatalf("dbparity sqlite with valid quoted GO_TEST_FLAGS failed: %v\nOutput: %s", errValid, string(outValid))
		}

		// Invalid GO_TEST_FLAGS with unclosed quote (must exit with exit code 2 and actionable error)
		cmdInvalid := exec.Command(binPath, "list")
		cmdInvalid.Dir = root
		cmdInvalid.Env = append(os.Environ(), `GO_TEST_FLAGS=-run "Unclosed Quote`)
		outInvalid, errInvalid := cmdInvalid.CombinedOutput()
		if errInvalid == nil {
			t.Fatalf("expected dbparity to fail with invalid GO_TEST_FLAGS, but succeeded:\n%s", string(outInvalid))
		}
		var exitErr *exec.ExitError
		if errors.As(errInvalid, &exitErr) {
			if exitErr.ExitCode() != 2 {
				t.Fatalf("expected exit code 2 for invalid GO_TEST_FLAGS, got: %d", exitErr.ExitCode())
			}
		} else {
			t.Fatalf("expected *exec.ExitError, got: %T (%v)", errInvalid, errInvalid)
		}
		if !strings.Contains(string(outInvalid), "invalid GO_TEST_FLAGS environment variable") || !strings.Contains(string(outInvalid), "unclosed double quote") {
			t.Fatalf("expected actionable error on invalid GO_TEST_FLAGS, got: %s", string(outInvalid))
		}

		// Invalid -flags CLI argument with unclosed quote (must exit with exit code 2 and actionable error)
		cmdInvalidFlag := exec.Command(binPath, "-flags", `-run "Unclosed Quote`, "list")
		cmdInvalidFlag.Dir = root
		outInvalidFlag, errInvalidFlag := cmdInvalidFlag.CombinedOutput()
		if errInvalidFlag == nil {
			t.Fatalf("expected dbparity to fail with invalid -flags argument, but succeeded:\n%s", string(outInvalidFlag))
		}
		if errors.As(errInvalidFlag, &exitErr) {
			if exitErr.ExitCode() != 2 {
				t.Fatalf("expected exit code 2 for invalid -flags argument, got: %d", exitErr.ExitCode())
			}
		} else {
			t.Fatalf("expected *exec.ExitError, got: %T (%v)", errInvalidFlag, errInvalidFlag)
		}
		if !strings.Contains(string(outInvalidFlag), "invalid -flags argument") || !strings.Contains(string(outInvalidFlag), "unclosed double quote") {
			t.Fatalf("expected actionable error on invalid -flags argument, got: %s", string(outInvalidFlag))
		}
	})
}

func TestMakefile_DBParityHelpDistinction(t *testing.T) {
	root := moduleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	assertMakefileTarget(t, text, "help:", func(t *testing.T, rest string) {
		t.Helper()
		requiredHelpEntries := []string{
			"make test-db-parity-sqlite",
			"make test-db-parity-postgres-direct",
			"make test-db-parity",
			"make test-authority-postgres-direct",
			"make test-authority-postgres-pooled",
			"make test-postgres-migrations",
			"make billing-convergence-certify",
		}
		for _, entry := range requiredHelpEntries {
			if !strings.Contains(rest, entry) {
				t.Errorf("Makefile help must document %q", entry)
			}
		}
	})
}

func assertMakefileTarget(t *testing.T, text, target string, check func(*testing.T, string)) {
	t.Helper()
	idx := strings.Index(text, target)
	if idx < 0 {
		t.Fatalf("Makefile missing %s", target)
	}
	rest := text[idx:]
	if end := strings.Index(rest[len(target):], "\n\n"); end >= 0 {
		rest = rest[:len(target)+end]
	}
	check(t, rest)
}

func assertTargetNoPrerequisites(t *testing.T, recipe, targetPrefix string) {
	t.Helper()
	normalized := strings.ReplaceAll(recipe, "\r\n", "\n")
	firstLine, _, _ := strings.Cut(normalized, "\n")
	if !strings.HasPrefix(firstLine, targetPrefix) {
		t.Fatalf("target line does not start with %q: %q", targetPrefix, firstLine)
	}
	prereqs := strings.TrimSpace(strings.TrimPrefix(firstLine, targetPrefix))
	if prereqs != "" {
		t.Fatalf("target %q must have no prerequisites on declaration line, found: %q", targetPrefix, prereqs)
	}
}

func splitMakefileBranches(t *testing.T, recipe string) (winBranch, posixBranch string) {
	t.Helper()
	normalized := strings.ReplaceAll(recipe, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var winLines, posixLines []string
	inWin := false
	inPosix := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "ifeq ($(OS),Windows_NT)") {
			inWin = true
			inPosix = false
			continue
		}
		if trimmed == "else" {
			inWin = false
			inPosix = true
			continue
		}
		if trimmed == "endif" {
			inWin = false
			inPosix = false
			continue
		}
		if inWin {
			winLines = append(winLines, line)
		} else if inPosix {
			posixLines = append(posixLines, line)
		}
	}
	if len(winLines) == 0 || len(posixLines) == 0 {
		t.Fatalf("failed to extract both Windows and POSIX branches from recipe:\n%s", recipe)
	}
	return strings.Join(winLines, "\n"), strings.Join(posixLines, "\n")
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
