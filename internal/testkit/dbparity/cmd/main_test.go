package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
)

func TestRunCLI_ListMode_Formats(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantJSON bool
	}{
		{
			name:     "list positional default text",
			args:     []string{"list"},
			wantJSON: false,
		},
		{
			name:     "list with spaced -format json trailing",
			args:     []string{"list", "-format", "json"},
			wantJSON: true,
		},
		{
			name:     "list with equals --format=json trailing",
			args:     []string{"list", "--format=json"},
			wantJSON: true,
		},
		{
			name:     "list with spaced -list-format json trailing",
			args:     []string{"list", "-list-format", "json"},
			wantJSON: true,
		},
		{
			name:     "list with equals --list-format=json trailing",
			args:     []string{"list", "--list-format=json"},
			wantJSON: true,
		},
		{
			name:     "list with boolean -json trailing",
			args:     []string{"list", "-json"},
			wantJSON: true,
		},
		{
			name:     "list with boolean --json trailing",
			args:     []string{"list", "--json"},
			wantJSON: true,
		},
		{
			name:     "-mode list flag with -json",
			args:     []string{"-mode", "list", "-json"},
			wantJSON: true,
		},
		{
			name:     "--mode=list flag with text format",
			args:     []string{"--mode=list", "-format", "text"},
			wantJSON: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(context.Background(), tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("runCLI(%v) returned exit code %d, stderr: %s", tc.args, code, stderr.String())
			}

			out := stdout.String()
			if tc.wantJSON {
				var parsed []dbparity.ComponentListEntry
				if err := json.Unmarshal([]byte(out), &parsed); err != nil {
					t.Fatalf("expected valid JSON output for %v, got: %s (err: %v)", tc.args, out, err)
				}
				if len(parsed) == 0 {
					t.Errorf("expected non-empty JSON component list")
				}
			} else {
				if !strings.Contains(out, "concurrency-authority:") || !strings.Contains(out, "TestPackages:") {
					t.Errorf("expected text format output for %v, got: %s", tc.args, out)
				}
			}
		})
	}
}

func TestRunCLI_HelpFlag(t *testing.T) {
	for _, flagArg := range []string{"-h", "--help", "-help"} {
		t.Run(flagArg, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(context.Background(), []string{flagArg}, &stdout, &stderr)
			if code != 0 {
				t.Errorf("runCLI(%q) returned code %d, want 0", flagArg, code)
			}
			if !strings.Contains(stderr.String(), "Usage: dbparity") {
				t.Errorf("expected usage message in stderr for %q, got: %s", flagArg, stderr.String())
			}
		})
	}
}

func TestRunCLI_FlagReorderingAndResolution(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
	}{
		{
			name:     "trailing spaced component with list",
			args:     []string{"list", "-component", "billing"},
			wantCode: 0,
		},
		{
			name:     "trailing equals component with list",
			args:     []string{"list", "--component=billing"},
			wantCode: 0,
		},
		{
			name:     "trailing spaced alias only with list",
			args:     []string{"list", "-only", "billing"},
			wantCode: 0,
		},
		{
			name:     "trailing spaced flags with list",
			args:     []string{"list", "-flags", "-count=1 -parallel=4"},
			wantCode: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(context.Background(), tc.args, &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("runCLI(%v) = %d, want %d (stderr: %s)", tc.args, code, tc.wantCode, stderr.String())
			}
		})
	}
}

func TestRunCLI_Errors_ExitCode2(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		errorContains string
	}{
		{
			name:          "unknown single dash flag after mode",
			args:          []string{"sqlite", "-unknown"},
			errorContains: "flag provided but not defined: -unknown",
		},
		{
			name:          "unknown double dash flag after mode",
			args:          []string{"sqlite", "--bogus"},
			errorContains: "flag provided but not defined: --bogus",
		},
		{
			name:          "unknown equals flag after mode",
			args:          []string{"sqlite", "--unknown=value"},
			errorContains: "flag provided but not defined: --unknown=value",
		},
		{
			name:          "missing value for -component at end",
			args:          []string{"sqlite", "-component"},
			errorContains: "flag needs an argument: -component",
		},
		{
			name:          "missing value for --component at end",
			args:          []string{"sqlite", "--component"},
			errorContains: "flag needs an argument: --component",
		},
		{
			name:          "missing value for -component followed by flag",
			args:          []string{"sqlite", "-component", "-json"},
			errorContains: "flag needs an argument: -component",
		},
		{
			name:          "missing value for -format at end",
			args:          []string{"list", "-format"},
			errorContains: "flag needs an argument: -format",
		},
		{
			name:          "missing value for -list-format at end",
			args:          []string{"list", "-list-format"},
			errorContains: "flag needs an argument: -list-format",
		},
		{
			name:          "missing value for -only at end",
			args:          []string{"sqlite", "-only"},
			errorContains: "flag needs an argument: -only",
		},
		{
			name:          "missing value for -flags at end",
			args:          []string{"sqlite", "-flags"},
			errorContains: "flag needs an argument: -flags",
		},
		{
			name:          "missing value for -mode at end",
			args:          []string{"-mode"},
			errorContains: "flag needs an argument: -mode",
		},
		{
			name:          "multiple positional modes",
			args:          []string{"sqlite", "postgres-direct"},
			errorContains: "unexpected extra positional argument",
		},
		{
			name:          "conflicting -mode flag and positional mode",
			args:          []string{"-mode", "sqlite", "all"},
			errorContains: "cannot specify both -mode flag",
		},
		{
			name:          "unknown runner mode string",
			args:          []string{"invalid-mode"},
			errorContains: "unknown runner mode",
		},
		{
			name:          "malformed unclosed quote in -flags",
			args:          []string{"list", "-flags", `-run "unclosed quote`},
			errorContains: "invalid -flags argument",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(context.Background(), tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("runCLI(%v) returned code %d, want exit code 2 (stderr: %s)", tc.args, code, stderr.String())
			}
			errOut := stderr.String()
			if !strings.Contains(errOut, tc.errorContains) {
				t.Errorf("runCLI(%v) stderr %q does not contain expected substring %q", tc.args, errOut, tc.errorContains)
			}
		})
	}
}

func TestRunCLI_ComponentAndOnly_OrderPrecedence(t *testing.T) {
	root := repoRoot(t)
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(origDir)
	})

	t.Run("-component then -only: only wins", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		// -component control-plane-ledger followed by -only nonexistent should fail on nonexistent because -only wins
		code := runCLI(context.Background(), []string{"sqlite", "-component", "control-plane-ledger", "-only", "nonexistent"}, &stdout, &stderr)
		if code == 0 {
			t.Fatalf("expected error for nonexistent component")
		}
		if !strings.Contains(stderr.String(), `unknown component "nonexistent"`) {
			t.Errorf("expected stderr to fail on nonexistent, got: %s", stderr.String())
		}
	})

	t.Run("-only then -component: component wins", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		// -only nonexistent followed by -component control-plane-ledger should resolve to control-plane-ledger (-only is overwritten)
		code := runCLI(context.Background(), []string{"sqlite", "-flags", `-run "^$"`, "-only", "nonexistent", "-component", "control-plane-ledger"}, &stdout, &stderr)
		if strings.Contains(stderr.String(), `unknown component "nonexistent"`) {
			t.Errorf("expected component control-plane-ledger to overwrite only nonexistent, but got error: %s", stderr.String())
		}
		if code != 0 {
			t.Errorf("expected exit code 0 for control-plane-ledger sqlite parity run, got %d (stderr: %s)", code, stderr.String())
		}
	})

	t.Run("--component=... then --only=... equals form: only wins", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCLI(context.Background(), []string{"sqlite", "--component=control-plane-ledger", "--only=nonexistent"}, &stdout, &stderr)
		if code == 0 {
			t.Fatalf("expected error for nonexistent component")
		}
		if !strings.Contains(stderr.String(), `unknown component "nonexistent"`) {
			t.Errorf("expected stderr to fail on nonexistent, got: %s", stderr.String())
		}
	})

	t.Run("--only=... then --component=... equals form: component wins", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := runCLI(context.Background(), []string{"sqlite", "-flags", `-run "^$"`, "--only=nonexistent", "--component=control-plane-ledger"}, &stdout, &stderr)
		if strings.Contains(stderr.String(), `unknown component "nonexistent"`) {
			t.Errorf("expected component control-plane-ledger to overwrite only nonexistent, but got error: %s", stderr.String())
		}
		if code != 0 {
			t.Errorf("expected exit code 0 for control-plane-ledger sqlite parity run, got %d (stderr: %s)", code, stderr.String())
		}
	})
}

func TestRunCLI_PositionalDiagnostics_RedactsSecrets(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantContained string
		secret        string
	}{
		{
			name:          "extra positional argument with URL DSN",
			args:          []string{"sqlite", "postgres://user:supersecretpass@localhost:5432/testdb"},
			wantContained: "postgres://user:***@localhost:5432/testdb",
			secret:        "supersecretpass",
		},
		{
			name:          "extra positional argument with KV DSN password",
			args:          []string{"sqlite", "host=localhost password='supersecretpass' dbname=testdb"},
			wantContained: "password='***'",
			secret:        "supersecretpass",
		},
		{
			name:          "extra positional argument with escaped quote in password",
			args:          []string{"sqlite", "password = 'supersecret\\'pass'"},
			wantContained: "password = '***'",
			secret:        "supersecret",
		},
		{
			name:          "mode conflict with URL DSN",
			args:          []string{"-mode", "sqlite", "postgres://user:supersecretpass@localhost:5432/testdb"},
			wantContained: "postgres://user:***@localhost:5432/testdb",
			secret:        "supersecretpass",
		},
		{
			name:          "mode conflict with double-quoted KV DSN password",
			args:          []string{"-mode", "sqlite", `PASSWORD="supersecretpass" dbname=testdb`},
			wantContained: `PASSWORD=***`,
			secret:        "supersecretpass",
		},
		{
			name:          "unknown mode with URL DSN",
			args:          []string{"postgres://user:supersecretpass@localhost:5432/testdb"},
			wantContained: "postgres://user:***@localhost:5432/testdb",
			secret:        "supersecretpass",
		},
		{
			name:          "unknown mode with KV DSN password",
			args:          []string{"password=supersecretpass"},
			wantContained: "password=***",
			secret:        "supersecretpass",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(context.Background(), tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("runCLI(%v) = %d, want exit code 2 (stderr: %s)", tc.args, code, stderr.String())
			}
			errOut := stderr.String()
			if !strings.Contains(errOut, tc.wantContained) {
				t.Errorf("stderr missing expected redacted output %q: %s", tc.wantContained, errOut)
			}
			if strings.Contains(errOut, tc.secret) {
				t.Errorf("stderr leaked secret %q: %s", tc.secret, errOut)
			}
		})
	}
}

func TestRunCLI_ReorderCLIArgs_ErrorRedaction(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantContained string
		secret        string
	}{
		{
			name:          "unknown flag with URL secret in value",
			args:          []string{"sqlite", "-unknown=postgres://user:supersecretpass@localhost/db"},
			wantContained: "postgres://user:***@localhost/db",
			secret:        "supersecretpass",
		},
		{
			name:          "unknown flag with KV password in value",
			args:          []string{"sqlite", "-unknown=password='supersecretpass'"},
			wantContained: "password='***'",
			secret:        "supersecretpass",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLI(context.Background(), tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("runCLI(%v) = %d, want exit code 2 (stderr: %s)", tc.args, code, stderr.String())
			}
			errOut := stderr.String()
			if !strings.Contains(errOut, tc.wantContained) {
				t.Errorf("stderr missing expected redacted output %q: %s", tc.wantContained, errOut)
			}
			if strings.Contains(errOut, tc.secret) {
				t.Errorf("stderr leaked secret %q: %s", tc.secret, errOut)
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from %s", dir)
		}
		dir = parent
	}
}

func TestSubprocess_CLIExecution(t *testing.T) {
	root := repoRoot(t)

	tempDir := t.TempDir()
	binPath := filepath.Join(tempDir, "dbparity.exe")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./internal/testkit/dbparity/cmd")
	buildCmd.Dir = root
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build dbparity binary: %v, output: %s", err, string(out))
	}

	t.Run("subprocess list mode json", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, binPath, "list", "-format", "json")
		cmd.Dir = root
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err != nil {
			t.Fatalf("command failed with %v, stderr: %s", err, stderr.String())
		}

		var parsed []dbparity.ComponentListEntry
		if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
			t.Fatalf("failed to parse JSON from subprocess stdout: %s (err: %v)", stdout.String(), err)
		}
		if len(parsed) == 0 {
			t.Errorf("expected non-empty JSON components")
		}
	})

	t.Run("subprocess list mode boolean json", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, binPath, "list", "--json")
		cmd.Dir = root
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err != nil {
			t.Fatalf("command failed with %v, stderr: %s", err, stderr.String())
		}

		var parsed []dbparity.ComponentListEntry
		if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
			t.Fatalf("failed to parse JSON from subprocess stdout: %s (err: %v)", stdout.String(), err)
		}
	})

	t.Run("subprocess unknown trailing flag returns exit code 2", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, binPath, "sqlite", "-unknown")
		cmd.Dir = root
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			t.Fatal("expected command with unknown flag to fail")
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Errorf("expected exit status 2, got: %v (stderr: %s)", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "flag provided but not defined: -unknown") {
			t.Errorf("expected stderr to contain unknown flag error, got: %s", stderr.String())
		}
	})

	t.Run("subprocess missing flag argument returns exit code 2", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, binPath, "sqlite", "-component")
		cmd.Dir = root
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			t.Fatal("expected command with missing flag value to fail")
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Errorf("expected exit status 2, got: %v (stderr: %s)", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "flag needs an argument: -component") {
			t.Errorf("expected stderr to contain missing value error, got: %s", stderr.String())
		}
	})

	t.Run("subprocess extra positional arguments returns exit code 2", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, binPath, "sqlite", "postgres-direct")
		cmd.Dir = root
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		if err == nil {
			t.Fatal("expected command with extra positional arguments to fail")
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 2 {
			t.Errorf("expected exit status 2, got: %v (stderr: %s)", err, stderr.String())
		}
		if !strings.Contains(stderr.String(), "unexpected extra positional argument") {
			t.Errorf("expected stderr to contain extra positional argument error, got: %s", stderr.String())
		}
	})
}
