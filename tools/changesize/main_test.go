package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
	"gopkg.in/yaml.v3"
)

func TestRun_StagedRejectsOverLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 3)
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, noOverrideGetenv)
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "3 modified Go files") || !strings.Contains(stderr.String(), "2-file") {
		t.Fatalf("reject message missing counts: %q", stderr.String())
	}
}

func TestRun_StagedIgnoresNonGoPaths(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	for _, name := range []string{"README.md", "notes.txt", "config.json"} {
		path := filepath.Join(repo, name)
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "1"}, &stderr, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
}

func TestRun_StagedAllowsAtLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 2)
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, os.Getenv)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
}

func TestRun_EnvOverrideAllowsOverLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 3)
	getenv := func(key string) string {
		if key == OverrideEnv {
			return "1"
		}
		return ""
	}
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, getenv)
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), OverrideEnv) {
		t.Fatalf("override warning missing env name: %q", stderr.String())
	}
}

func TestRun_GitConfigOverrideAllowsOverLimit(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 3)
	git(t, repo, "config", OverrideGitConfig, "true")
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--staged", "--limit", "2"}, &stderr, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("exit=%d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), OverrideGitConfig) {
		t.Fatalf("override warning missing git config key: %q", stderr.String())
	}
}

func TestRun_RangeCountsCommits(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 1)
	git(t, repo, "-c", "user.email=qa@example.com", "-c", "user.name=QA", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	base := strings.TrimSpace(string(git(t, repo, "rev-parse", "HEAD")))
	for i := range 3 {
		path := filepath.Join(repo, fmt.Sprintf("extra-%d.go", i))
		if err := os.WriteFile(path, []byte("y\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.email=qa@example.com", "-c", "user.name=QA", "-c", "commit.gpgsign=false", "commit", "-qm", "more")
	var stderr bytes.Buffer
	code := run([]string{"--repo", repo, "--base", base, "--head", "HEAD", "--limit", "2"}, &stderr, func(string) string { return "" })
	if code != 1 {
		t.Fatalf("exit=%d, want 1; stderr=%q", code, stderr.String())
	}
}

func TestRun_UsageError(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	if code := run([]string{"--staged", "--base", "HEAD"}, &stderr, os.Getenv); code != 2 {
		t.Fatalf("mixed flags exit=%d, want 2; stderr=%q", code, stderr.String())
	}
}

// PR size belongs to the event's head, not a merge checkout regenerated on newer main.
// Source: #1006 preflight counted 77 feature files plus 28 unrelated base files.
func TestWorkflow_PRSizeExcludesMovedBaseButRejectsOversizedHead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux preflight shell fixture uses POSIX executable and PATH semantics")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("workflow shell requires bash")
	}
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string
				Env  map[string]string
				Run  string
			}
		}
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	repo := initTempRepo(t)
	commit := func(message string) string {
		git(t, repo, "-c", "user.email=qa@example.com", "-c", "user.name=QA", "-c", "commit.gpgsign=false", "commit", "-qm", message)
		return strings.TrimSpace(string(git(t, repo, "rev-parse", "HEAD")))
	}
	stageFiles(t, repo, 1)
	base := commit("event base")
	git(t, repo, "checkout", "-qb", "feature")
	stageFiles(t, repo, 2)
	head := commit("one feature file")
	git(t, repo, "checkout", "-qb", "advanced-base", base)
	for _, name := range []string{"unrelated-a.go", "unrelated-b.go"} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("package fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
	commit("base moves after event")
	git(t, repo, "-c", "user.email=qa@example.com", "-c", "user.name=QA", "-c", "commit.gpgsign=false", "merge", "--no-ff", "-qm", "synthetic merge", head)
	for _, step := range workflow.Jobs["preflight"].Steps {
		if step.Name != "Check change size" {
			continue
		}
		bin := t.TempDir()
		argsFile := filepath.Join(bin, "args")
		// Capture the actual workflow shell arguments, then execute the real gate.
		if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf '%s\\0' \"$@\" > \"$ARGS_FILE\"\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), "bash", "-e", "-c", step.Run)
		cmd.Dir = repo
		cmd.Env = append(gitscope.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "ARGS_FILE="+argsFile)
		for name, value := range step.Env {
			switch value {
			case "${{ github.event.pull_request.base.sha }}":
				cmd.Env = append(cmd.Env, name+"="+base)
			case "${{ github.event.pull_request.head.sha }}":
				cmd.Env = append(cmd.Env, name+"="+head)
			default:
				t.Fatalf("unrecognized size-check input: %s=%s", name, value)
			}
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("workflow command: %v: %s", err, out)
		}
		raw, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		args := splitGitNames(raw)
		if len(args) < 2 || args[0] != "run" || args[1] != "./tools/changesize" {
			t.Fatalf("workflow did not invoke size gate: %q", args)
		}
		args = append(args[2:], "--repo", repo, "--limit", "2")
		var stderr bytes.Buffer
		if code := run(args, &stderr, noOverrideGetenv); code != 0 {
			t.Fatalf("unrelated base files polluted PR size: exit=%d: %s", code, &stderr)
		}
		git(t, repo, "checkout", "feature")
		stageFiles(t, repo, 4)
		oversized := commit("three feature files")
		for i := range args {
			if args[i] == head {
				args[i] = oversized
			}
		}
		stderr.Reset()
		if code := run(args, &stderr, noOverrideGetenv); code != 1 || !strings.Contains(stderr.String(), "3 modified Go files") {
			t.Fatalf("oversized PR must still fail: exit=%d: %s", code, &stderr)
		}
		return
	}
	t.Fatal("preflight size gate missing")
}

func noOverrideGetenv(key string) string {
	if key == OverrideEnv {
		return ""
	}
	return os.Getenv(key)
}

func initTempRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git(t, dir, "-c", "core.autocrlf=false", "init", "-q")
	return dir
}

func stageFiles(t *testing.T, repo string, n int) {
	t.Helper()
	for i := range n {
		path := filepath.Join(repo, fmt.Sprintf("f-%02d.go", i))
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, repo, "add", ".")
}

func git(t *testing.T, repo string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	// Repo points at a throwaway fixture repository. Git exports GIT_DIR to every
	// hook it runs, so an inherited GIT_DIR would initialise and commit inside the
	// real repository instead of the fixture, destroying its index and refs.
	cmd.Env = gitscope.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}
