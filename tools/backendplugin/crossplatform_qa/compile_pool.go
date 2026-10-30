package main

import (
	"context"
	"runtime"
	"sort"
	"sync"
	"time"
)

// compilePhaseTimeout is the aggregate deadline for the whole cross-compile
// phase across every claimed job. Individual commands keep qaCommandTimeout;
// this bound fails the phase closed and cancels siblings instead of letting
// queued builds drift past the budget.
const compilePhaseTimeout = 45 * time.Minute

// maxCompileWorkers caps the compile worker pool so cross-compiling never
// oversubscribes the host, even on high-core CI runners. Each worker owns one
// concurrent `go build` subprocess tree.
const maxCompileWorkers = 8

// compileFunc runs one claimed cross-compile job. It must always return a
// compileResult, including when ctx is canceled (the task runner tears down
// the child process tree as part of its cleanup).
type compileFunc func(ctx context.Context, j claimJob) compileResult

type compilePhaseConfig struct {
	// workers bounds concurrent compiles; <=0 means default sizing.
	workers int
	// timeout is the aggregate phase deadline; <=0 means compilePhaseTimeout.
	timeout time.Duration
}

func defaultCompilePhaseConfig(jobs int) compilePhaseConfig {
	return compilePhaseConfig{
		workers: compileWorkerCount(runtime.GOMAXPROCS(0), jobs),
		timeout: compilePhaseTimeout,
	}
}

// compileWorkerCount sizes the pool conservatively from the host's logical
// processors, capped at maxCompileWorkers, floored at one, and never exceeding
// the number of jobs.
func compileWorkerCount(procs, jobs int) int {
	workers := max(min(procs, maxCompileWorkers), 1)
	if jobs > 0 && workers > jobs {
		workers = jobs
	}
	return workers
}

// runCompilePhase runs every claimed job on a bounded worker pool. It reports
// a result for every job -- including canceled siblings -- cancels the phase
// context as soon as any job fails (fail-fast), and enforces an aggregate
// deadline. Results are returned sorted deterministically by
// (connector, os, arch). The pool is fully drained before returning.
func runCompilePhase(jobs []claimJob, compile compileFunc, cfg compilePhaseConfig) []compileResult {
	if cfg.workers <= 0 {
		cfg = defaultCompilePhaseConfig(len(jobs))
	}
	if cfg.timeout <= 0 {
		cfg.timeout = compilePhaseTimeout
	}
	results := make([]compileResult, len(jobs))
	if len(jobs) == 0 {
		return results
	}
	if cfg.workers > len(jobs) {
		cfg.workers = len(jobs)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()

	jobCh := make(chan int)
	var wg sync.WaitGroup
	wg.Add(cfg.workers)
	for range cfg.workers {
		go func() {
			defer wg.Done()
			for idx := range jobCh {
				res := compile(ctx, jobs[idx])
				results[idx] = res
				if !res.OK {
					cancel()
				}
			}
		}()
	}
	for i := range jobs {
		jobCh <- i
	}
	close(jobCh)
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		if results[i].Connector != results[j].Connector {
			return results[i].Connector < results[j].Connector
		}
		if results[i].OS != results[j].OS {
			return results[i].OS < results[j].OS
		}
		return results[i].Arch < results[j].Arch
	})
	return results
}
