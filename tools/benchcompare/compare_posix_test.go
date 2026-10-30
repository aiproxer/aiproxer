//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/gitscope"
)

func TestComparison_RetainsOrderedSamplesAndPinnedToolReport(t *testing.T) {
	root := t.TempDir()
	dirs := []string{filepath.Join(root, "baseline"), filepath.Join(root, "candidate")}
	for _, dir := range dirs {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range map[string]string{"go.mod": "module fixture\n\ngo 1.26.9\n", "fixture.json": "{}\n"} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = dir
			cmd.Env = gitscope.Environ()
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git: %v %s", err, output)
			}
		}
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$1" in
env) echo '{"GOVERSION":"go1.26.9","GOOS":"fixture","GOARCH":"fixture","CGO_ENABLED":"0","GOFLAGS":""}' ;;
test) echo 'BenchmarkFixture-1 20 100 ns/op 0 B/op 0 allocs/op' ;;
run) echo "$2" ;;
*) exit 3 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out := filepath.Join(root, "evidence")
	opts := options{Baseline: dirs[0], Candidate: dirs[1], Module: ".", Packages: ".", Bench: "BenchmarkFixture$", Fixtures: "fixture.json", Out: out, Samples: 2, Benchtime: "20x", CPU: "1", Timeout: time.Second}
	if err := compare(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(out, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record report
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Outcome != "passed" || record.TimingEvidence || len(record.Samples) != 4 {
		t.Fatalf("wrong comparison evidence: %+v", record)
	}
	want := []string{"baseline", "candidate", "candidate", "baseline"}
	for index, sample := range record.Samples {
		if sample.Revision != want[index] {
			t.Fatalf("execution order %d = %s", index, sample.Revision)
		}
		if _, err := os.Stat(sample.Log); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := os.ReadFile(filepath.Join(out, "benchstat.txt"))
	if err != nil || !strings.Contains(string(stats), "@"+benchstatVersion) {
		t.Fatalf("comparison tool was not pinned: %s %v", stats, err)
	}
	if err := compare(t.Context(), opts); err == nil {
		t.Fatal("overwrote previous evidence directory")
	}
}
