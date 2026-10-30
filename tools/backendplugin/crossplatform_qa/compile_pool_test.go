package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func makeCompileJobs(n int) []claimJob {
	oss := []string{"linux", "darwin", "windows"}
	arches := []string{"amd64", "arm64"}
	jobs := make([]claimJob, 0, n)
	for i := range n {
		jobs = append(jobs, claimJob{
			r: discoveredRelease{DirName: fmt.Sprintf("conn-%03d", i)},
			c: platformClaim{OS: oss[i%len(oss)], Arch: arches[(i/len(oss))%len(arches)]},
		})
	}
	return jobs
}

func resultKey(cr compileResult) string {
	return cr.Connector + "/" + cr.OS + "/" + cr.Arch
}

func TestCompileWorkerCount_CapFloorAndJobsBound(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		procs int
		jobs  int
		want  int
	}{
		{"zero procs floors at one", 0, 10, 1},
		{"negative procs floors at one", -4, 10, 1},
		{"high core host is capped", 256, 60, maxCompileWorkers},
		{"typical host is uncapped", 4, 60, 4},
		{"never exceeds jobs", 16, 3, 3},
		{"single job keeps one worker", 16, 1, 1},
		{"no jobs keeps sizing", 16, 0, maxCompileWorkers},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := compileWorkerCount(tc.procs, tc.jobs); got != tc.want {
				t.Fatalf("compileWorkerCount(%d, %d) = %d, want %d", tc.procs, tc.jobs, got, tc.want)
			}
		})
	}
}

func TestCompilePhase_BoundedConcurrency(t *testing.T) {
	t.Parallel()
	jobs := makeCompileJobs(24)
	const workers = 4
	var active, maxActive int64
	compile := func(ctx context.Context, j claimJob) compileResult {
		cur := atomic.AddInt64(&active, 1)
		for {
			old := atomic.LoadInt64(&maxActive)
			if cur <= old || atomic.CompareAndSwapInt64(&maxActive, old, cur) {
				break
			}
		}
		defer atomic.AddInt64(&active, -1)
		time.Sleep(20 * time.Millisecond)
		return compileResult{Connector: j.r.DirName, OS: j.c.OS, Arch: j.c.Arch, OK: true}
	}

	results := runCompilePhase(jobs, compile, compilePhaseConfig{workers: workers, timeout: time.Minute})

	if got := atomic.LoadInt64(&maxActive); got > workers {
		t.Fatalf("max concurrency %d exceeds worker bound %d", got, workers)
	}
	if got := atomic.LoadInt64(&maxActive); got < 2 {
		t.Fatalf("expected pooled concurrent execution, saw max concurrency %d", got)
	}
	if len(results) != len(jobs) {
		t.Fatalf("reported %d results for %d jobs", len(results), len(jobs))
	}
	for _, cr := range results {
		if !cr.OK {
			t.Fatalf("unexpected failure for %s: %s", resultKey(cr), cr.Error)
		}
	}
}

func TestCompilePhase_AllJobsReportedIncludingFailures(t *testing.T) {
	t.Parallel()
	jobs := makeCompileJobs(10)
	compile := func(ctx context.Context, j claimJob) compileResult {
		cr := compileResult{Connector: j.r.DirName, OS: j.c.OS, Arch: j.c.Arch}
		if j.c.OS == "linux" {
			cr.OK = true
		} else {
			cr.Error = "synthetic failure"
		}
		return cr
	}

	results := runCompilePhase(jobs, compile, compilePhaseConfig{workers: 3, timeout: time.Minute})

	if len(results) != len(jobs) {
		t.Fatalf("reported %d results for %d jobs", len(results), len(jobs))
	}
	seen := map[string]bool{}
	failures := 0
	for _, cr := range results {
		if seen[resultKey(cr)] {
			t.Fatalf("duplicate result %s", resultKey(cr))
		}
		seen[resultKey(cr)] = true
		if !cr.OK {
			failures++
		}
	}
	for _, j := range jobs {
		key := j.r.DirName + "/" + j.c.OS + "/" + j.c.Arch
		if !seen[key] {
			t.Fatalf("missing result for claimed job %s", key)
		}
	}
	if failures == 0 {
		t.Fatal("expected some failing results to be reported")
	}
}

func TestCompilePhase_FailFastCancelsSiblings(t *testing.T) {
	t.Parallel()
	jobs := makeCompileJobs(6)
	compile := func(ctx context.Context, j claimJob) compileResult {
		if j.r.DirName == "conn-000" {
			return compileResult{Connector: j.r.DirName, OS: j.c.OS, Arch: j.c.Arch, Error: "boom"}
		}
		<-ctx.Done()
		return compileResult{Connector: j.r.DirName, OS: j.c.OS, Arch: j.c.Arch, Error: "sibling_canceled: " + ctx.Err().Error()}
	}

	results := runCompilePhase(jobs, compile, compilePhaseConfig{workers: 4, timeout: time.Minute})

	if len(results) != len(jobs) {
		t.Fatalf("reported %d results for %d jobs", len(results), len(jobs))
	}
	byConn := map[string]compileResult{}
	for _, cr := range results {
		byConn[cr.Connector] = cr
	}
	boom := byConn["conn-000"]
	if boom.OK || !strings.Contains(boom.Error, "boom") {
		t.Fatalf("failing job must report its own error, got %+v", boom)
	}
	for i := 1; i < len(jobs); i++ {
		name := fmt.Sprintf("conn-%03d", i)
		cr, ok := byConn[name]
		if !ok {
			t.Fatalf("missing canceled sibling result %s", name)
		}
		if cr.OK {
			t.Fatalf("sibling %s must be canceled, got ok", name)
		}
		if !strings.Contains(cr.Error, "sibling_canceled") {
			t.Fatalf("sibling %s must observe fail-fast cancellation, got %q", name, cr.Error)
		}
	}
}

func TestCompilePhase_AggregateDeadlineCancelsAllJobs(t *testing.T) {
	t.Parallel()
	jobs := makeCompileJobs(5)
	compile := func(ctx context.Context, j claimJob) compileResult {
		<-ctx.Done()
		return compileResult{Connector: j.r.DirName, OS: j.c.OS, Arch: j.c.Arch, Error: "phase_" + ctx.Err().Error()}
	}

	start := time.Now()
	results := runCompilePhase(jobs, compile, compilePhaseConfig{workers: 2, timeout: 50 * time.Millisecond})
	elapsed := time.Since(start)

	if len(results) != len(jobs) {
		t.Fatalf("reported %d results for %d jobs", len(results), len(jobs))
	}
	for _, cr := range results {
		if cr.OK {
			t.Fatalf("job %s must not succeed under the aggregate deadline", resultKey(cr))
		}
		if !strings.Contains(cr.Error, "phase_") {
			t.Fatalf("job %s must report deadline cancellation, got %q", resultKey(cr), cr.Error)
		}
	}
	if elapsed > 10*time.Second {
		t.Fatalf("aggregate deadline must bound the phase, took %s", elapsed)
	}
}

func TestCompilePhase_StableOrder(t *testing.T) {
	t.Parallel()
	jobs := makeCompileJobs(12)
	compile := func(ctx context.Context, j claimJob) compileResult {
		time.Sleep(time.Duration(rand.IntN(15)) * time.Millisecond)
		return compileResult{Connector: j.r.DirName, OS: j.c.OS, Arch: j.c.Arch, OK: true}
	}

	cfg := compilePhaseConfig{workers: 5, timeout: time.Minute}
	first := runCompilePhase(jobs, compile, cfg)
	second := runCompilePhase(jobs, compile, cfg)

	if len(first) != len(jobs) || len(second) != len(jobs) {
		t.Fatalf("result counts %d/%d want %d", len(first), len(second), len(jobs))
	}
	for i := range first {
		if resultKey(first[i]) != resultKey(second[i]) {
			t.Fatalf("unstable order at %d: %s vs %s", i, resultKey(first[i]), resultKey(second[i]))
		}
		if i > 0 && resultKey(first[i-1]) >= resultKey(first[i]) {
			t.Fatalf("results not sorted at %d: %s before %s", i, resultKey(first[i-1]), resultKey(first[i]))
		}
	}
}
