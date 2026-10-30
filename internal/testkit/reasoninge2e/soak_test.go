package reasoninge2e_test

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

func TestDefaultSoakCases_exact1000Split(t *testing.T) {
	t.Parallel()
	cases := reasoninge2e.SoakCases(reasoninge2e.SoakConfig{
		Enabled: true,
		Seeds:   reasoninge2e.DefaultSoakSeeds,
		Turns:   reasoninge2e.DefaultSoakTurns,
		Workers: reasoninge2e.DefaultSoakWorkers,
	})
	if len(cases) != 1000 {
		t.Fatalf("cases=%d want=1000", len(cases))
	}
	var dropAll, always, combined int
	for _, c := range cases {
		switch c.Mode {
		case reasoninge2e.MatrixModeRandomBackendDropAll:
			dropAll++
		case reasoninge2e.MatrixModeAlwaysReasonRandomClient:
			always++
		case reasoninge2e.MatrixModeCombined:
			combined++
		default:
			t.Fatalf("unexpected mode %q", c.Mode)
		}
	}
	if dropAll != 250 || always != 250 || combined != 500 {
		t.Fatalf("split drop=%d always=%d combined=%d want 250/250/500", dropAll, always, combined)
	}
}

func TestSoakCases_allocationPreservesRatios(t *testing.T) {
	t.Parallel()
	cases := reasoninge2e.SoakCases(reasoninge2e.SoakConfig{Enabled: true, Seeds: 4, Turns: 10, Workers: 2})
	if len(cases) != 4 {
		t.Fatalf("cases=%d want=4", len(cases))
	}
	var dropAll, always, combined int
	for _, c := range cases {
		switch c.Mode {
		case reasoninge2e.MatrixModeRandomBackendDropAll:
			dropAll++
		case reasoninge2e.MatrixModeAlwaysReasonRandomClient:
			always++
		case reasoninge2e.MatrixModeCombined:
			combined++
		}
	}
	if dropAll != 1 || always != 1 || combined != 2 {
		t.Fatalf("split drop=%d always=%d combined=%d want 1/1/2", dropAll, always, combined)
	}
}

func TestParseSoakConfig_disabledByDefault(t *testing.T) {
	t.Parallel()
	cfg, err := reasoninge2e.ParseSoakConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("soak must be disabled when AIP_REASONING_E2E_SOAK unset")
	}
}

func TestParseSoakConfig_defaultsWhenEnabled(t *testing.T) {
	t.Parallel()
	cfg, err := reasoninge2e.ParseSoakConfig(func(k string) string {
		if k == reasoninge2e.EnvSoakGate {
			return "1"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("expected enabled")
	}
	if cfg.Seeds != 1000 || cfg.Turns != 100 || cfg.Workers != reasoninge2e.DefaultSoakWorkers {
		t.Fatalf("defaults seeds=%d turns=%d workers=%d", cfg.Seeds, cfg.Turns, cfg.Workers)
	}
	if cfg.Replay != nil {
		t.Fatal("replay must be nil by default")
	}
}

func TestParseSoakConfig_envOverrides(t *testing.T) {
	t.Parallel()
	env := map[string]string{
		reasoninge2e.EnvSoakGate:    "1",
		reasoninge2e.EnvSoakSeeds:   "3",
		reasoninge2e.EnvSoakTurns:   "4",
		reasoninge2e.EnvSoakWorkers: "2",
	}
	cfg, err := reasoninge2e.ParseSoakConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Seeds != 3 || cfg.Turns != 4 || cfg.Workers != 2 {
		t.Fatalf("got seeds=%d turns=%d workers=%d", cfg.Seeds, cfg.Turns, cfg.Workers)
	}
}

func TestParseSoakConfig_rejectsNonPositive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "seeds_zero", env: map[string]string{reasoninge2e.EnvSoakGate: "1", reasoninge2e.EnvSoakSeeds: "0"}},
		{name: "turns_neg", env: map[string]string{reasoninge2e.EnvSoakGate: "1", reasoninge2e.EnvSoakTurns: "-1"}},
		{name: "workers_zero", env: map[string]string{reasoninge2e.EnvSoakGate: "1", reasoninge2e.EnvSoakWorkers: "0"}},
		{name: "seeds_non_int", env: map[string]string{reasoninge2e.EnvSoakGate: "1", reasoninge2e.EnvSoakSeeds: "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := reasoninge2e.ParseSoakConfig(func(k string) string { return tt.env[k] })
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseSoakConfig_rejectsUnbounded(t *testing.T) {
	t.Parallel()
	_, err := reasoninge2e.ParseSoakConfig(func(k string) string {
		switch k {
		case reasoninge2e.EnvSoakGate:
			return "1"
		case reasoninge2e.EnvSoakWorkers:
			return "9999"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("expected workers upper bound error")
	}
}

func TestParseSoakConfig_replayRequiresModeAndSeed(t *testing.T) {
	t.Parallel()
	_, err := reasoninge2e.ParseSoakConfig(func(k string) string {
		switch k {
		case reasoninge2e.EnvSoakGate:
			return "1"
		case reasoninge2e.EnvSoakMode:
			return string(reasoninge2e.MatrixModeCombined)
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("expected mode/seed pairing error")
	}
	_, err = reasoninge2e.ParseSoakConfig(func(k string) string {
		switch k {
		case reasoninge2e.EnvSoakGate:
			return "1"
		case reasoninge2e.EnvSoakSeed:
			return "7"
		default:
			return ""
		}
	})
	if err == nil {
		t.Fatal("expected mode/seed pairing error")
	}
}

func TestParseSoakConfig_replayPair(t *testing.T) {
	t.Parallel()
	cfg, err := reasoninge2e.ParseSoakConfig(func(k string) string {
		switch k {
		case reasoninge2e.EnvSoakGate:
			return "1"
		case reasoninge2e.EnvSoakMode:
			return string(reasoninge2e.MatrixModeCombined)
		case reasoninge2e.EnvSoakSeed:
			return "42"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Replay == nil || cfg.Replay.Mode != reasoninge2e.MatrixModeCombined || cfg.Replay.Seed != 42 {
		t.Fatalf("replay=%+v", cfg.Replay)
	}
	cases := reasoninge2e.SoakCases(cfg)
	if len(cases) != 1 || cases[0].Mode != reasoninge2e.MatrixModeCombined || cases[0].Seed != 42 {
		t.Fatalf("replay cases=%+v", cases)
	}
}

func TestSoakReplayCommand_contentSafeSingleCommand(t *testing.T) {
	t.Parallel()
	cmd := reasoninge2e.SoakReplayCommand(reasoninge2e.MatrixModeCombined, 99)
	if strings.Count(cmd, "go test") != 1 {
		t.Fatalf("want exactly one go test command: %s", cmd)
	}
	for _, frag := range []string{
		"AIP_REASONING_E2E_SOAK=1",
		"AIP_REASONING_E2E_MODE=combined",
		"AIP_REASONING_E2E_SEED=99",
		"TestReasoningPreservationHTTP_Soak",
		"-tags=precommit",
	} {
		if !strings.Contains(cmd, frag) {
			t.Fatalf("replay missing %q in %s", frag, cmd)
		}
	}
	leak := "reason-s99-t0-payload"
	fail := reasoninge2e.FormatSoakFail(
		mustSoakPlan(t, reasoninge2e.MatrixModeCombined, 99, 4),
		0,
		"oracle_mismatch",
	)
	if strings.Contains(fail, leak) || strings.Contains(cmd, leak) {
		t.Fatal("soak fail/replay must not include reasoning payload text")
	}
	if !strings.Contains(fail, "replay=") {
		t.Fatalf("fail missing replay: %s", fail)
	}
}

func TestParseSoakConfig_disabledIgnoresInvalidOverrides(t *testing.T) {
	t.Parallel()
	cfg, err := reasoninge2e.ParseSoakConfig(func(k string) string {
		if k == reasoninge2e.EnvSoakSeeds {
			return "nope"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("disabled soak must not validate overrides: %v", err)
	}
	if cfg.Enabled {
		t.Fatal("expected disabled")
	}
}

func TestErrSoakDisabled_sentinel(t *testing.T) {
	t.Parallel()
	if !errors.Is(reasoninge2e.ErrSoakDisabled, reasoninge2e.ErrSoakDisabled) {
		t.Fatal("sentinel mismatch")
	}
}

func TestRunSoakWorkerPool_boundsConcurrency(t *testing.T) {
	t.Parallel()
	const workers = 3
	const jobsN = 40
	var current, maxSeen atomic.Int64
	jobs := make([]reasoninge2e.SoakPoolJob, jobsN)
	for i := range jobs {
		jobs[i] = reasoninge2e.SoakPoolJob{Index: i, Case: reasoninge2e.MatrixCase{Mode: reasoninge2e.MatrixModeCombined, Seed: uint64(i)}}
	}
	results := reasoninge2e.RunSoakWorkerPool(workers, jobs, func(job reasoninge2e.SoakPoolJob) reasoninge2e.SoakPoolResult {
		c := current.Add(1)
		for {
			m := maxSeen.Load()
			if c <= m || maxSeen.CompareAndSwap(m, c) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		current.Add(-1)
		return reasoninge2e.SoakPoolResult{Index: job.Index, Case: job.Case, HTTPTurns: 1}
	})
	if len(results) != jobsN {
		t.Fatalf("results=%d want=%d", len(results), jobsN)
	}
	if got := maxSeen.Load(); got > workers {
		t.Fatalf("max concurrent=%d want<=%d", got, workers)
	}
	if got := maxSeen.Load(); got < 1 {
		t.Fatal("expected workers to run")
	}
	var sum int
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected err: %v", r.Err)
		}
		sum += r.HTTPTurns
	}
	if sum != jobsN {
		t.Fatalf("http sum=%d want=%d", sum, jobsN)
	}
}

func TestRunSoakWorkerPool_noPerJobGoroutineGrowth(t *testing.T) {
	t.Parallel()
	const workers = 4
	const jobsN = 500
	jobs := make([]reasoninge2e.SoakPoolJob, jobsN)
	for i := range jobs {
		jobs[i] = reasoninge2e.SoakPoolJob{Index: i, Case: reasoninge2e.MatrixCase{Seed: uint64(i)}}
	}
	var executorStarts atomic.Int64
	var executorActive, executorMax atomic.Int64
	results, stats := reasoninge2e.RunSoakWorkerPoolWithStats(workers, jobs, func(job reasoninge2e.SoakPoolJob) reasoninge2e.SoakPoolResult {
		executorStarts.Add(1)
		c := executorActive.Add(1)
		for {
			m := executorMax.Load()
			if c <= m || executorMax.CompareAndSwap(m, c) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		executorActive.Add(-1)
		return reasoninge2e.SoakPoolResult{Index: job.Index, Case: job.Case, HTTPTurns: 1}
	})
	if stats.WorkersStarted != int64(workers) {
		t.Fatalf("workers started=%d want=%d (fixed pool must not spawn one goroutine per job)", stats.WorkersStarted, workers)
	}
	if stats.JobsStarted != int64(jobsN) {
		t.Fatalf("pool jobs started=%d want=%d", stats.JobsStarted, jobsN)
	}
	if got := executorStarts.Load(); got != int64(jobsN) {
		t.Fatalf("executor starts=%d want=%d", got, jobsN)
	}
	if got := stats.MaxActive; got > int64(workers) {
		t.Fatalf("pool max active=%d want<=%d", got, workers)
	}
	if got := executorMax.Load(); got > int64(workers) {
		t.Fatalf("executor max active=%d want<=%d", got, workers)
	}
	if got := stats.MaxActive; got < 1 {
		t.Fatal("expected pool workers to run")
	}
	if len(results) != jobsN {
		t.Fatalf("results=%d want=%d", len(results), jobsN)
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("result[%d] err: %v", i, r.Err)
		}
		if r.Index != i || r.HTTPTurns != 1 {
			t.Fatalf("result[%d]=%+v incomplete", i, r)
		}
	}
}

func TestSoakCases_finalTurnNoTool(t *testing.T) {
	t.Parallel()
	cases := reasoninge2e.SoakCases(reasoninge2e.SoakConfig{Enabled: true, Seeds: 32, Turns: 20, Workers: 2})
	for _, c := range cases {
		plan, err := reasoninge2e.GenerateTranscriptPlan(c.Mode, c.Seed, 20)
		if err != nil {
			t.Fatal(err)
		}
		dec := plan.Decisions()
		if dec[len(dec)-1].HasTool {
			t.Fatalf("soak case final turn tool mode=%s seed=%d", c.Mode, c.Seed)
		}
	}
}

func mustSoakPlan(t *testing.T, mode reasoninge2e.MatrixMode, seed uint64, turns int) reasoninge2e.TranscriptPlan {
	t.Helper()
	p, err := reasoninge2e.GenerateTranscriptPlan(mode, seed, turns)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
