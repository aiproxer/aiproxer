package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitGoFlagsUsesGoQuotedFieldSyntax(t *testing.T) {
	t.Parallel()
	got, err := splitGoFlags(`-tags=precommit '-skip=^TestParent$/slow[ _]case$' "-count=1"`)
	want := []string{"-tags=precommit", "-skip=^TestParent$/slow[ _]case$", "-count=1"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("splitGoFlags() = %q, %v; want %q", got, err, want)
	}

	got, err = splitGoFlags(`prefix"quoted value"`)
	want = []string{`prefix"quoted`, `value"`}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("quotes inside an unquoted field were special: got %q, %v", got, err)
	}

	got, err = splitGoFlags(`'-skip=^Test\d+$'`)
	want = []string{`-skip=^Test\d+$`}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("quoted field was unescaped: got %q, %v", got, err)
	}
}

func TestLastGoFlagsSkipWinsIncludingEmptyClear(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		input   string
		want    string
		found   bool
		wantErr bool
	}{
		{name: "last short and long form", input: `-skip=first '--skip=second pattern'`, want: "second pattern", found: true},
		{name: "empty clears earlier skip", input: `--skip=first '-skip='`, want: "", found: true},
		{name: "bare skip requires equals", input: `-skip pattern`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields, err := splitGoFlags(tc.input)
			if err != nil {
				t.Fatal(err)
			}
			got, found, err := lastGoFlagsSkip(fields)
			if (err != nil) != tc.wantErr || got != tc.want || found != tc.found {
				t.Fatalf("lastGoFlagsSkip(%q) = %q, %v, %v; want %q, %v, error=%v", tc.input, got, found, err, tc.want, tc.found, tc.wantErr)
			}
		})
	}
}

func TestValidateExplicitTestSkipScopeAndName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		set       bool
		testName  string
		task      string
		scope     string
		full      bool
		base      string
		planOnly  bool
		wantError bool
	}{
		{name: "not set", task: "build", scope: "explicit"},
		{name: "explicit test", set: true, testName: "TestOwner", task: "test", scope: "explicit"},
		{name: "empty name", set: true, task: "test", scope: "explicit", wantError: true},
		{name: "regexp rejected", set: true, testName: "TestOwner|TestOther", task: "test", scope: "explicit", wantError: true},
		{name: "subtest rejected", set: true, testName: "TestOwner/Child", task: "test", scope: "explicit", wantError: true},
		{name: "changed rejected", set: true, testName: "TestOwner", task: "test", scope: "changed", wantError: true},
		{name: "full rejected", set: true, testName: "TestOwner", task: "test", scope: "explicit", full: true, wantError: true},
		{name: "build rejected", set: true, testName: "TestOwner", task: "build", scope: "explicit", wantError: true},
		{name: "lint rejected", set: true, testName: "TestOwner", task: "lint", scope: "explicit", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateExplicitTestSkip(tc.set, tc.testName, tc.task, tc.scope, tc.full, tc.base, tc.planOnly)
			if (err != nil) != tc.wantError {
				t.Fatalf("validateExplicitTestSkip() error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}

func TestExplicitTestSkipCombinesGOENVQuarantineAndExactName(t *testing.T) {
	root := t.TempDir()
	goEnvFile := filepath.Join(root, "goenv")
	if err := os.WriteFile(goEnvFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/skipfixture\n\ngo 1.25.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := `package skipfixture

import "testing"

func TestTarget(t *testing.T) { t.Fatal("requested test was not skipped") }
func TestTargetSimilar(t *testing.T) { t.Fatal("similarly named test must still run") }
func TestQuarantined(t *testing.T) { t.Fatal("quarantined test was not skipped") }
func TestParent(t *testing.T) {
	t.Run("slow case", func(t *testing.T) { t.Fatal("inherited subtest skip was lost") })
	t.Run("fast", func(t *testing.T) {})
}
`
	if err := os.WriteFile(filepath.Join(root, "skip_test.go"), []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	env := withoutEnvironment(os.Environ(), "GOFLAGS", "GOENV", "GOWORK")
	env = append(env, "GOENV="+goEnvFile, "GOWORK=off", "GOPROXY=off")
	setFlags := exec.Command("go", "env", "-w", `GOFLAGS='-skip=^TestParent$/slow[ _]case$'`)
	setFlags.Dir = root
	setFlags.Env = env
	if output, err := setFlags.CombinedOutput(); err != nil {
		t.Fatalf("write GOENV GOFLAGS: %v\n%s", err, output)
	}

	goFlags, err := effectiveGOFlags(t.Context(), root, env)
	if err != nil {
		t.Fatal(err)
	}
	pattern, err := combinedTestSkipPattern(goFlags, []quarantineEntry{{Test: "TestQuarantined"}}, "TestTarget")
	wantPattern := `^TestParent$/slow[ _]case$|^(TestQuarantined)$|^TestTarget$`
	if err != nil || pattern != wantPattern {
		t.Fatalf("combinedTestSkipPattern() = %q, %v; want %q", pattern, err, wantPattern)
	}

	command, err := commandFor("test", ".", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	command = withExplicitTestSkip(command, pattern)
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = root
	cmd.Env = env
	output, runErr := cmd.Output()
	if runErr == nil {
		t.Fatal("similar named test did not fail as expected")
	}
	seen := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		var event struct {
			Action string
			Test   string
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode go test event: %v", err)
		}
		if event.Test != "" && (event.Action == "skip" || event.Action == "pass" || event.Action == "fail") {
			seen[event.Test] = event.Action
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	for test, wantAction := range map[string]string{
		"TestTarget":           "",
		"TestTargetSimilar":    "fail",
		"TestQuarantined":      "",
		"TestParent/slow_case": "",
		"TestParent/fast":      "pass",
	} {
		if got := seen[test]; got != wantAction {
			t.Errorf("%s action = %q, want %q; events=%v\n%s", test, got, wantAction, seen, output)
		}
	}
}

func withoutEnvironment(env []string, keys ...string) []string {
	wanted := make(map[string]bool, len(keys))
	for _, key := range keys {
		wanted[key] = true
	}
	filtered := make([]string, 0, len(env)+len(keys))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !wanted[key] {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
