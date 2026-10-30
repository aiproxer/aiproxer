package qa

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The development-host guard in scripts/race-check.sh must refuse full race
// scans on interactive dev machines (DESKTOP-I2CAJ6V, agent-dev) even when the
// scan is forced with --strict: a race run costs hours and agents repeatedly
// trigger it by passing the force flag. These tests execute the real script
// against fake `hostname`/`go`/`git`/`cc` binaries on a temporary PATH, so
// they assert the guard without ever building or scanning anything.
//
// The scan-scheduling contract itself (TestRaceCheckStagedScanPartitions...)
// runs with AIP_ALLOW_RACE_ON_DEV=1 so it stays green on blocked hosts; the
// guard behavior is pinned here instead.

type raceDevHostScenario struct {
	name string
	// hostnameOut is the stdout of the fake `hostname` binary.
	hostnameOut string
	// hostVar/computerVar set HOSTNAME/COMPUTERNAME (empty means unset, so
	// the inherited developer environment can never leak into the case).
	hostVar     string
	computerVar string
	policy      string
	// allowOverride sets AIP_ALLOW_RACE_ON_DEV=1.
	allowOverride bool
	// wantSkip expects the guard to trip: exit 0, a SKIP diagnostic, and no
	// recorded `go test` invocation. Otherwise the staged scan must proceed
	// to exactly one recorded invocation.
	wantSkip bool
}

func TestRaceCheckDevHostGuard(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		// Bash is not guaranteed on Windows; the authoritative race gate is
		// scripts/race-check.sh on Linux/macOS CI, and the Windows shim is
		// scripts/race-check.ps1.
		t.Skip("race-check.sh contract requires the repository's POSIX bash runtime")
	}

	scenarios := []raceDevHostScenario{
		{
			name:        "configured new development box skips",
			hostnameOut: "devbox-local",
			policy:      "development",
			wantSkip:    true,
		},
		{
			name:        "linux dev box skips even with strict",
			hostnameOut: "agent-dev",
			wantSkip:    true,
		},
		{
			name:        "windows dev box skips even with strict",
			hostnameOut: "DESKTOP-I2CAJ6V",
			wantSkip:    true,
		},
		{
			name:        "lowercase windows dev box still matches",
			hostnameOut: "desktop-i2caj6v",
			wantSkip:    true,
		},
		{
			name:        "domain-suffixed dev box still matches",
			hostnameOut: "agent-dev.corp.example.com",
			wantSkip:    true,
		},
		{
			name:        "hostname env alone trips the guard",
			hostnameOut: "some-laptop",
			hostVar:     "agent-dev",
			wantSkip:    true,
		},
		{
			name:        "computername env alone trips the guard",
			hostnameOut: "some-laptop",
			computerVar: "DESKTOP-I2CAJ6V",
			wantSkip:    true,
		},
		{
			name:        "lookalike hostname does not match",
			hostnameOut: "agent-dev2",
			wantSkip:    false,
		},
		{
			name:        "unrelated host proceeds",
			hostnameOut: "ci-runner-04",
			wantSkip:    false,
		},
		{
			name:          "explicit human override proceeds on a blocked host",
			hostnameOut:   "agent-dev",
			allowOverride: true,
			wantSkip:      false,
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			assertRaceDevHostScenario(t, scenario)
		})
	}
}

func assertRaceDevHostScenario(t *testing.T, scenario raceDevHostScenario) {
	t.Helper()

	root := t.TempDir()
	writeRaceFixture(t, filepath.Join(root, raceCheckScript), readRepositoryFile(t, "scripts", "race-check.sh"), 0o755)
	writeRaceFixture(t, filepath.Join(root, "scripts", "dev-host-policy.sh"), readRepositoryFile(t, "scripts", "dev-host-policy.sh"), 0o644)
	if scenario.policy != "" {
		writeRaceFixture(t, filepath.Join(root, "host-policy"), scenario.policy+"\n", 0o600)
	}

	binDir := filepath.Join(root, "fakebin")
	recordPath := filepath.Join(root, "go-invocations.log")
	writeRaceFixture(t, filepath.Join(binDir, "go"), fakeRaceCheckGo, 0o755)
	writeRaceFixture(t, filepath.Join(binDir, "git"), fakeRaceCheckGit, 0o755)
	writeRaceFixture(t, filepath.Join(binDir, "cc"), "#!/usr/bin/env bash\nexit 0\n", 0o755)
	writeRaceFixture(t, filepath.Join(binDir, "hostname"), fakeRaceCheckHostname, 0o755)
	if err := os.WriteFile(recordPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// Skip cases use the strict-forced full scan to prove --strict cannot
	// force execution on a dev host; proceed cases use a single staged file
	// so the assertion stays at exactly one recorded invocation.
	args := []string{raceCheckScript, "--strict"}
	stagedFiles := ""
	if !scenario.wantSkip {
		args = append(args, "--staged")
		stagedFiles = "internal/core/runtime/attempt_session.go"
	}

	allow := ""
	if scenario.allowOverride {
		allow = "1"
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", args...)
	cmd.Dir = root
	// The fake bin dir is prepended so the script can never reach the real
	// hostname, Go toolchain, Git index, or C compiler. HOSTNAME,
	// COMPUTERNAME, and AIP_ALLOW_RACE_ON_DEV are set explicitly (possibly
	// empty) so the developer's own environment cannot leak into the case.
	cmd.Env = append(os.Environ(),
		"AIP_DEV_HOST_POLICY_FILE="+filepath.Join(root, "host-policy"),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"AIP_FAKE_HOSTNAME="+scenario.hostnameOut,
		"HOSTNAME="+scenario.hostVar,
		"COMPUTERNAME="+scenario.computerVar,
		"AIP_ALLOW_RACE_ON_DEV="+allow,
		"AIP_FAKE_GO_RECORD="+recordPath,
		"AIP_FAKE_GO_FAIL=",
		"AIP_FAKE_GIT_FILES="+stagedFiles,
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
	if exitCode != 0 {
		t.Fatalf("race-check.sh exit = %d, want 0\n%s", exitCode, output)
	}

	invocations := readRaceInvocations(t, recordPath)
	if scenario.wantSkip {
		if !strings.Contains(string(output), "SKIP: race detector scan is disabled on development host") {
			t.Errorf("guard skip must emit an explicit SKIP diagnostic\nscript output:\n%s", output)
		}
		if len(invocations) != 0 {
			t.Errorf("guard skip must record no go test invocations, got %d:\n%s\nscript output:\n%s",
				len(invocations), strings.Join(raceInvocationLines(invocations), "\n"), output)
		}
		return
	}
	if strings.Contains(string(output), "SKIP: race detector scan is disabled on development host") {
		t.Errorf("unrelated host must not trip the dev-host guard\nscript output:\n%s", output)
	}
	if len(invocations) != 1 {
		t.Fatalf("proceeding scan must record exactly one go test invocation, got %d:\n%s\nscript output:\n%s",
			len(invocations), strings.Join(raceInvocationLines(invocations), "\n"), output)
	}
	if !strings.Contains(invocations[0].line, "./internal/core/runtime/...") {
		t.Errorf("proceeding scan must run the staged scope\nrecorded: %s", invocations[0].line)
	}
}

// fakeRaceCheckHostname answers `hostname` (any args, e.g. -s) with
// $AIP_FAKE_HOSTNAME so guard matching is fully deterministic.
const fakeRaceCheckHostname = `#!/usr/bin/env bash
printf '%s\n' "${AIP_FAKE_HOSTNAME:-unknown-test-host}"
`
