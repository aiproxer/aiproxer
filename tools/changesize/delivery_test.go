package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDeliveryReport_TestRatioGuidelineIsFiveTimes(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	if err := os.WriteFile(filepath.Join(repo, "code.go"), []byte("package example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, testLines := range []int{3, 5, 6} {
		if err := os.WriteFile(filepath.Join(repo, "code_test.go"), []byte(strings.Repeat("// test\n", testLines)), 0o600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := writeDeliveryReport(repo, "HEAD", "", "example", &out); err != nil {
			t.Fatal(err)
		}
		var report deliveryReport
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if (len(report.Advisory) != 0) != (testLines > 5) {
			t.Errorf("%d test lines per production line: %v", testLines, report.Advisory)
		}
	}
}

func TestDeliveryReport_FinalWorktreeAndStackBase(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	commit := func() {
		t.Helper()
		git(t, repo, "add", ".")
		git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	}
	write("a.go", "package example\nconst A = 1\n")
	commit()
	git(t, repo, "branch", "foundation")
	write("b.go", "package example\n")
	commit()
	write("a.go", "package example\nconst A = 2\n")
	git(t, repo, "add", "a.go")
	write("a.go", "package example\nconst A = 3\nconst B = 4\n")
	write("a_test.go", "package example\n// test\n")
	write("notes with space.md", "note\n")
	var out bytes.Buffer
	if code := runWithOutput([]string{"--repo", repo, "--report", "--base", "foundation", "--consumer", "example feature"}, &out, &out, noOverrideGetenv); code != 0 {
		t.Fatalf("exit=%d: %s", code, &out)
	}
	var report deliveryReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Files != 4 || report.GoFiles != 3 || report.Production.Added != 3 || report.Production.Deleted != 1 || report.Tests.Added != 2 {
		t.Fatalf("wrong final-state counts: %+v", report)
	}
	if report.Consumer != "example feature" || report.BuildVerified || report.HardLimit.Limit != DefaultLimit {
		t.Fatalf("report invented certification or limit: %+v", report)
	}
	out.Reset()
	if code := runWithOutput([]string{"--repo", repo, "--report", "--base", "foundation", "--head", "HEAD"}, &out, &out, noOverrideGetenv); code != 0 {
		t.Fatalf("exit=%d: %s", code, &out)
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Files != 1 || report.Production.Added != 1 || report.Tests.Added != 0 {
		t.Fatalf("committed slice included dirty successor edits: %+v", report)
	}
}

func TestDeliveryReport_RenamesRetainDependencySourcesAndBinaryPaths(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "old.go"), []byte("package example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "add", ".")
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	git(t, repo, "mv", "old.go", "new\tname.go")
	if err := os.WriteFile(filepath.Join(repo, "binary.go"), []byte{'x', 0, 'y'}, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := writeDeliveryReport(repo, "HEAD", "", "", &out); err != nil {
		t.Fatal(err)
	}
	var report deliveryReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Files != 2 || report.GoFiles != 2 || report.Production.Added != 0 || !slices.Equal(report.PreviousPaths, []string{"old.go"}) || !slices.Equal(report.Binary, []string{"binary.go"}) {
		t.Fatalf("rename/binary metrics wrong: %+v", report)
	}
}

func TestDeliveryReport_SeparatesHardLimitFromAdvisorySignals(t *testing.T) {
	t.Parallel()
	repo := initTempRepo(t)
	stageFiles(t, repo, 1)
	git(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	stageFiles(t, repo, 102)
	var out bytes.Buffer
	if err := writeDeliveryReport(repo, "HEAD", "", "", &out); err != nil {
		t.Fatal(err)
	}
	var report deliveryReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.HardLimit.Exceeded || report.GoFiles != 101 || len(report.Advisory) != 1 || len(report.Pending) != 3 {
		t.Fatalf("hard/advisory/dependency signals missing: %+v", report)
	}
	var gate bytes.Buffer
	if code := run([]string{"--repo", repo, "--staged"}, &gate, func(string) string { return "" }); code != 1 {
		t.Fatalf("report weakened existing hard gate: exit=%d %s", code, &gate)
	}
	for _, args := range [][]string{{"--report", "--staged"}, {"--consumer", "feature"}, {"--report", "--base", "missing"}} {
		out.Reset()
		if code := runWithOutput(append([]string{"--repo", repo}, args...), &out, &out, noOverrideGetenv); code != 2 {
			t.Fatalf("invalid report flags accepted: %v", args)
		}
	}
}
