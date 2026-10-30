package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestExecutionPropagatesFailures(t *testing.T) {
	t.Parallel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"fail", "malformed"} {
		cmd := exec.Command(exe, "-test.run=^TestDevcheckHelperProcess$")
		cmd.Env = append(os.Environ(), "DEVCHECK_TEST_HELPER="+mode)
		var output strings.Builder
		if _, err := execute(t.Context(), cmd, true, &output, nil); err == nil {
			t.Errorf("reported success for child mode %s", mode)
		}
	}
}

func TestDevcheckHelperProcess(t *testing.T) {
	mode := os.Getenv("DEVCHECK_TEST_HELPER")
	if mode == "" {
		return
	}
	if mode == "fail" {
		fmt.Println(`{"Action":"output","Package":"broken","Output":"check failed\n"}`)
		fmt.Println(`{"Action":"fail","Package":"broken"}`)
		os.Exit(7)
	}
	fmt.Println("invalid output")
	os.Exit(0)
}

func TestScopedCommands(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		task string
		want []string
	}{
		{"test", []string{"go", "test", "-mod=readonly", "-p=2", "-parallel=2", "-timeout=10m", "-json", "./pkg/one", "./pkg/two/..."}},
		{"build", []string{"go", "build", "-mod=readonly", "-p=2", "-buildvcs=false", "./pkg/one", "./pkg/two/..."}},
		{"lint", []string{"golangci-lint", "run", "--allow-parallel-runners", "--concurrency=2", "--disable=modernize,paralleltest,thelper,gocognit", "./pkg/one", "./pkg/two/..."}},
	} {
		t.Run(tc.task, func(t *testing.T) {
			t.Parallel()
			got, err := commandFor(tc.task, "./pkg/one ./pkg/two/...", 2, false)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("command = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestLocalArchTrimpathCommandGroups(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, task, module, scope string
		localArchTrimpath         bool
		wantCommands              int
		wantTrimmed               []bool
		wantPackages              []string
	}{
		{
			name:              "mixed root test scope",
			task:              "test",
			module:            ".",
			scope:             "./pkg/one ./internal/archtest ./pkg/two/...",
			localArchTrimpath: true,
			wantCommands:      2,
			wantTrimmed:       []bool{false, true},
			wantPackages:      []string{"./pkg/one", "./pkg/two/...", "./internal/archtest"},
		},
		{
			name:              "root arch build",
			task:              "build",
			module:            ".",
			scope:             "./internal/archtest/...",
			localArchTrimpath: true,
			wantCommands:      1,
			wantTrimmed:       []bool{true},
			wantPackages:      []string{"./internal/archtest/..."},
		},
		{
			name:              "nested module stays unchanged",
			task:              "test",
			module:            "connectors/nested",
			scope:             "./internal/archtest",
			localArchTrimpath: true,
			wantCommands:      1,
			wantTrimmed:       []bool{false},
			wantPackages:      []string{"./internal/archtest"},
		},
		{
			name:              "broad root scope stays unchanged",
			task:              "test",
			module:            ".",
			scope:             "./...",
			localArchTrimpath: true,
			wantCommands:      1,
			wantTrimmed:       []bool{false},
			wantPackages:      []string{"./..."},
		},
		{
			name:              "mixed root broad scope stays unchanged",
			task:              "test",
			module:            ".",
			scope:             "./... ./internal/archtest",
			localArchTrimpath: true,
			wantCommands:      1,
			wantTrimmed:       []bool{false},
			wantPackages:      []string{"./...", "./internal/archtest"},
		},
		{
			name:              "mixed internal broad scope stays unchanged",
			task:              "build",
			module:            ".",
			scope:             "./internal/... ./internal/archtest",
			localArchTrimpath: true,
			wantCommands:      1,
			wantTrimmed:       []bool{false},
			wantPackages:      []string{"./internal/...", "./internal/archtest"},
		},
		{
			name:         "local opt in is required",
			task:         "build",
			module:       ".",
			scope:        "./internal/archtest",
			wantCommands: 1,
			wantTrimmed:  []bool{false},
			wantPackages: []string{"./internal/archtest"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands, err := commandGroupsFor(tc.task, tc.module, tc.scope, 2, true, tc.localArchTrimpath)
			if err != nil {
				t.Fatal(err)
			}
			if len(commands) != tc.wantCommands {
				t.Fatalf("got %d commands, want %d: %v", len(commands), tc.wantCommands, commands)
			}
			var gotPackages []string
			for i, command := range commands {
				trimmed := slices.Contains(command, "-trimpath")
				if trimmed != tc.wantTrimmed[i] {
					t.Errorf("command %d trimpath = %t, want %t: %v", i, trimmed, tc.wantTrimmed[i], command)
				}
				if tc.task == "test" && !slices.Contains(command, "-count=1") {
					t.Errorf("fresh execution flag missing from command %d: %v", i, command)
				}
				for _, arg := range command {
					if arg == "." || strings.HasPrefix(arg, "./") {
						gotPackages = append(gotPackages, arg)
					}
				}
			}
			if !reflect.DeepEqual(gotPackages, tc.wantPackages) {
				t.Errorf("package coverage/order = %v, want %v", gotPackages, tc.wantPackages)
			}
		})
	}
}

func TestRejectUnboundedOrAmbiguousScope(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"", "-race", "../other", "/tmp/pkg", "example.com/remote", "./pkg/../../other"} {
		if _, err := commandFor("test", scope, 2, false); err == nil {
			t.Errorf("accepted scope %q", scope)
		}
	}
	if _, err := commandFor("unknown", "./...", 2, false); err == nil {
		t.Fatal("accepted unknown operation")
	}
}

func TestFreshExecutionIsExplicit(t *testing.T) {
	t.Parallel()
	cmd, err := commandFor("test", "./...", 2, true)
	if err != nil || !strings.Contains(strings.Join(cmd, " "), "-count=1") {
		t.Fatalf("fresh command = %v, %v", cmd, err)
	}
}

func TestModuleBoundary(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "connectors", "one")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "go.mod"), []byte("module example.com/one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := moduleDirectory(root, "connectors/one"); err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"..", "missing", filepath.Dir(root)} {
		if _, err := moduleDirectory(root, module); err == nil {
			t.Errorf("accepted module %q", module)
		}
	}
}

func TestCacheTelemetry(t *testing.T) {
	t.Parallel()
	input := `{"Action":"output","Package":"one","Output":"ok  one (cached)\n"}
{"Action":"pass","Package":"one","Elapsed":0}
{"Action":"pass","Package":"two","Test":"TestOne","Elapsed":1}
{"Action":"pass","Package":"two","Elapsed":1}
{"Action":"fail","Package":"three","Elapsed":2}
{"Action":"skip","Package":"four"}
`
	var output strings.Builder
	stats, err := consumeTests(strings.NewReader(input), &output)
	if err != nil || stats.Passed != 2 || stats.Cached != 1 || stats.Failed != 1 || stats.Skipped != 1 {
		t.Fatalf("stats = %+v, %v", stats, err)
	}
	if !strings.Contains(output.String(), "(cached)") {
		t.Fatal("discarded test output")
	}
	if _, err := consumeTests(strings.NewReader("broken\n"), &output); err == nil {
		t.Fatal("accepted malformed test stream")
	}
}
