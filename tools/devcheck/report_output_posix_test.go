//go:build linux || darwin

package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/tools/devcheck/internal/evidence"
)

func TestCLI_SummaryPreservesBothStreamsAndVerdict(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name               string
		failure, telemetry bool
	}{
		{name: "success"}, {name: "failure", failure: true}, {name: "test telemetry", telemetry: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "logs", "manifest.json")
			script := "#!/bin/sh\n[ \"$1\" = build ] || exit 0\necho verbose-stdout\necho diagnostic-stderr >&2\n"
			task := "build"
			if scenario.telemetry {
				task = "test"
				if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, quarantineFile), []byte("TestExcluded https://github.com/aiproxer/aiproxer-enterprise/issues/1\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				script = "#!/bin/sh\n[ \"$1\" = test ] || exit 0\nprintf '%s\\n' '{\"Action\":\"output\",\"Package\":\"fixture\",\"Output\":\"verbose-stdout\\n\"}' '{\"Action\":\"pass\",\"Package\":\"fixture\"}'\necho diagnostic-stderr >&2\n"
			}
			if scenario.failure {
				script += "exit 3\n"
			}
			if scenario.telemetry {
				script = strings.Replace(script, "#!/bin/sh\n", "#!/bin/sh\nif [ \"$1\" = env ]; then echo \"$GOFLAGS\"; exit 0; fi\n", 1)
			}
			for name, data := range map[string]string{"go.mod": "module example.invalid/report\n\ngo 1.26.9\n", "go": script} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestDevcheckSignalHelper$")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "DEVCHECK_CLI_FIXTURE="+path, "DEVCHECK_CLI_OUTPUT=summary", "DEVCHECK_CLI_TASK="+task)
			if scenario.telemetry {
				cmd.Env = append(cmd.Env, "GOFLAGS=-skip=^TestInherited$")
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != scenario.failure {
				t.Fatalf("summary lost verdict: %v\n%s", err, output)
			}
			if strings.Contains(string(output), "verbose-stdout") || strings.Contains(string(output), "diagnostic-stderr") || !strings.Contains(string(output), path) {
				t.Fatalf("summary flooded output or hid evidence location:\n%s", output)
			}
			manifest := readManifest(t, path)
			want := "passed"
			if scenario.failure {
				want = "failed"
			}
			if manifest.Outcome != want || len(manifest.Steps) == 0 {
				t.Fatalf("report verdict=%+v", manifest)
			}
			if scenario.telemetry && (len(manifest.Skips) != 2 || manifest.Skips[0].Target != "^TestInherited$" || manifest.Skips[1].Target != "TestExcluded") {
				t.Fatalf("requested exclusion absent from report: %+v", manifest.Skips)
			}
			for _, step := range manifest.Steps {
				if step.Scope.Module != "." || len(step.Scope.Packages) != 1 || step.Scope.Packages[0] != "./..." {
					t.Fatalf("actual scope not recorded: %+v", step.Scope)
				}
				log, err := os.ReadFile(step.LogPath)
				if err != nil || !strings.Contains(string(log), "verbose-stdout") || !strings.Contains(string(log), "diagnostic-stderr") {
					t.Fatalf("full stdout/stderr log missing: %q, %v", log, err)
				}
				if scenario.telemetry && (!strings.Contains(string(log), `"Action":"pass"`) || step.Tests == nil || step.Tests.Passed != 1) {
					t.Fatalf("raw telemetry/counters lost: %q %+v", log, step.Tests)
				}
			}
		})
	}
}

// Claim: a failed summary-mode step reports the failing test's identity and
// its own failure text, with the excerpt capped and the full log named, so a
// summary failure stays actionable without opening the log files. Mutants:
// skip the excerpt (identity and failure text absent) or drop the cap
// (uncapped filler lines, no truncation trailer) -> FAIL.
func TestSummaryFailureExcerptShowsTestNameTextAndCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	var report strings.Builder
	recorder := newEvidenceRecorder(t.Context(), path, "test", evidence.Scope{Kind: "explicit", Module: ".", Packages: []string{"./..."}}, dir)
	recorder.quiet, recorder.report = true, &report

	step := recorder.start("repeat-1-command-1-of-1", []string{"go", "test", "-json", "./..."}, dir, &strings.Builder{})
	var logLines strings.Builder
	logLines.WriteString(`{"Action":"run","Package":"example.invalid/fixture","Test":"TestBroken"}` + "\n")
	logLines.WriteString(`{"Action":"output","Package":"example.invalid/fixture","Test":"TestBroken","Output":"=== RUN   TestBroken\n"}` + "\n")
	logLines.WriteString(`{"Action":"output","Package":"example.invalid/fixture","Test":"TestBroken","Output":"    broken_test.go:12: deliberate failure probe message\n"}` + "\n")
	logLines.WriteString(`{"Action":"output","Package":"example.invalid/fixture","Test":"TestBroken","Output":"--- FAIL: TestBroken (0.00s)\n"}` + "\n")
	logLines.WriteString(`{"Action":"fail","Package":"example.invalid/fixture","Test":"TestBroken","Elapsed":0}` + "\n")
	logLines.WriteString(`{"Action":"output","Package":"example.invalid/fixture","Output":"FAIL\n"}` + "\n")
	logLines.WriteString(`{"Action":"fail","Package":"example.invalid/fixture","Elapsed":0}` + "\n")
	logLines.WriteString("FAIL\texample.invalid/other [build failed]\n")
	for range 100 {
		logLines.WriteString(`{"Action":"output","Package":"example.invalid/fixture","Test":"TestBroken","Output":"filler line ` + strings.Repeat("x", 20) + `\n"}` + "\n")
	}
	if _, err := strings.NewReader(logLines.String()).WriteTo(step.output); err != nil {
		t.Fatalf("write step log: %v", err)
	}
	recorder.finishRun(step, nil, errors.New("exit status 1"))

	out := report.String()
	if !strings.Contains(out, "FAIL package=example.invalid/fixture test=TestBroken") {
		t.Errorf("summary missing failing test identity:\n%s", out)
	}
	if !strings.Contains(out, "deliberate failure probe message") {
		t.Errorf("summary missing failing test output text:\n%s", out)
	}
	if !strings.Contains(out, "FAIL\texample.invalid/other [build failed]") {
		t.Errorf("summary missing non-JSON build failure line:\n%s", out)
	}
	if got := strings.Count(out, "filler line"); got == 0 || got >= 100 {
		t.Errorf("filler lines = %d, want a capped non-empty excerpt", got)
	}
	if !strings.Contains(out, "... truncated; full log: "+step.logPath) {
		t.Errorf("summary missing truncation trailer naming the full log:\n%s", out)
	}
}
