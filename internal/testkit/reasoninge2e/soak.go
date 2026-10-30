package reasoninge2e

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Soak environment contract (opt-in; never a default/PR gate).
const (
	EnvSoakGate    = "AIP_REASONING_E2E_SOAK"
	EnvSoakSeeds   = "AIP_REASONING_E2E_SEEDS"
	EnvSoakTurns   = "AIP_REASONING_E2E_TURNS"
	EnvSoakWorkers = "AIP_REASONING_E2E_WORKERS"
	EnvSoakMode    = "AIP_REASONING_E2E_MODE"
	EnvSoakSeed    = "AIP_REASONING_E2E_SEED"
)

const (
	// DefaultSoakSeeds is the default seed count (1000) for a full soak.
	DefaultSoakSeeds = 1000
	// DefaultSoakTurns is the default HTTP turns per seed (100).
	DefaultSoakTurns = 100
	// DefaultSoakWorkers is a conservative fixed-pool default.
	DefaultSoakWorkers = 4

	maxSoakSeeds   = 100_000
	maxSoakTurns   = 10_000
	maxSoakWorkers = 32
)

// ErrSoakDisabled is returned by helpers that require an enabled soak gate.
var ErrSoakDisabled = errors.New("reasoninge2e soak: disabled (set AIP_REASONING_E2E_SOAK=1)")

// SoakReplay identifies a single mode/seed pair for reproduction.
type SoakReplay struct {
	Mode MatrixMode
	Seed uint64
}

// SoakConfig is the validated soak driver configuration.
type SoakConfig struct {
	Enabled bool
	Seeds   int
	Turns   int
	Workers int
	Replay  *SoakReplay
}

// LoadSoakConfigFromEnv parses soak settings from the process environment.
func LoadSoakConfigFromEnv() (SoakConfig, error) {
	return ParseSoakConfig(os.Getenv)
}

// ParseSoakConfig parses soak settings from getenv. When the soak gate is unset,
// overrides are ignored and Enabled is false (no error).
func ParseSoakConfig(getenv func(string) string) (SoakConfig, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	gate := strings.TrimSpace(getenv(EnvSoakGate))
	if gate != "1" {
		return SoakConfig{}, nil
	}
	cfg := SoakConfig{
		Enabled: true,
		Seeds:   DefaultSoakSeeds,
		Turns:   DefaultSoakTurns,
		Workers: DefaultSoakWorkers,
	}
	var err error
	if cfg.Seeds, err = parsePositiveBound(getenv(EnvSoakSeeds), DefaultSoakSeeds, maxSoakSeeds, EnvSoakSeeds); err != nil {
		return SoakConfig{}, err
	}
	if cfg.Turns, err = parsePositiveBound(getenv(EnvSoakTurns), DefaultSoakTurns, maxSoakTurns, EnvSoakTurns); err != nil {
		return SoakConfig{}, err
	}
	if cfg.Workers, err = parsePositiveBound(getenv(EnvSoakWorkers), DefaultSoakWorkers, maxSoakWorkers, EnvSoakWorkers); err != nil {
		return SoakConfig{}, err
	}
	modeEnv := strings.TrimSpace(getenv(EnvSoakMode))
	seedEnv := strings.TrimSpace(getenv(EnvSoakSeed))
	if modeEnv == "" && seedEnv == "" {
		return cfg, nil
	}
	if modeEnv == "" || seedEnv == "" {
		return SoakConfig{}, fmt.Errorf("reasoninge2e soak: replay requires both %s and %s", EnvSoakMode, EnvSoakSeed)
	}
	seed, err := strconv.ParseUint(seedEnv, 10, 64)
	if err != nil {
		return SoakConfig{}, fmt.Errorf("reasoninge2e soak: %s: %w", EnvSoakSeed, err)
	}
	mode := MatrixMode(modeEnv)
	switch mode {
	case MatrixModeRandomBackendDropAll, MatrixModeAlwaysReasonRandomClient, MatrixModeCombined:
	default:
		return SoakConfig{}, fmt.Errorf("reasoninge2e soak: unknown mode %q", mode)
	}
	cfg.Replay = &SoakReplay{Mode: mode, Seed: seed}
	return cfg, nil
}

func parsePositiveBound(raw string, def, maxBound int, name string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("reasoninge2e soak: %s: %w", name, err)
	}
	if v <= 0 {
		return 0, fmt.Errorf("reasoninge2e soak: %s must be > 0", name)
	}
	if v > maxBound {
		return 0, fmt.Errorf("reasoninge2e soak: %s=%d exceeds max %d", name, v, maxBound)
	}
	return v, nil
}

// SoakCases returns the mode/seed allocation for cfg.
// Defaults preserve 25%/25%/50% (250/250/500 of 1000). Replay returns one case.
func SoakCases(cfg SoakConfig) []MatrixCase {
	if cfg.Replay != nil {
		return []MatrixCase{{Mode: cfg.Replay.Mode, Seed: cfg.Replay.Seed}}
	}
	n := cfg.Seeds
	if n <= 0 {
		return nil
	}
	dropAll := n / 4
	always := n / 4
	combined := n - dropAll - always
	out := make([]MatrixCase, 0, n)
	for seed := range dropAll {
		out = append(out, MatrixCase{Mode: MatrixModeRandomBackendDropAll, Seed: uint64(seed)})
	}
	for seed := range always {
		out = append(out, MatrixCase{Mode: MatrixModeAlwaysReasonRandomClient, Seed: uint64(seed)})
	}
	for seed := range combined {
		out = append(out, MatrixCase{Mode: MatrixModeCombined, Seed: uint64(seed)})
	}
	return out
}

// SoakReplayCommand returns one content-safe command to reproduce a soak seed.
func SoakReplayCommand(mode MatrixMode, seed uint64) string {
	return fmt.Sprintf(
		"AIP_REASONING_E2E_SOAK=1 AIP_REASONING_E2E_MODE=%s AIP_REASONING_E2E_SEED=%d go test -tags=precommit -run TestReasoningPreservationHTTP_Soak -count=1 ./internal/stdhttp/",
		mode, seed,
	)
}

// FormatSoakFail builds a content-safe soak failure line with a single replay command.
func FormatSoakFail(plan TranscriptPlan, turnIndex int, reasonCode string) string {
	turnID := ""
	if turnIndex >= 0 && turnIndex < len(plan.decisions) {
		turnID = plan.decisions[turnIndex].TurnID
	}
	return fmt.Sprintf(
		"reasoninge2e soak fail: mode=%s seed=%d turn=%s idx=%d reason_code=%s decisions=%d trace=%s replay=%s",
		plan.mode, plan.seed, turnID, turnIndex, reasonCode, len(plan.decisions), plan.trace, SoakReplayCommand(plan.mode, plan.seed),
	)
}

// SoakPoolJob is one unit of work for the fixed soak worker pool.
type SoakPoolJob struct {
	Index int
	Case  MatrixCase
}

// SoakPoolResult is the outcome of one soak job (content-safe Err only).
type SoakPoolResult struct {
	Index     int
	Case      MatrixCase
	HTTPTurns int
	Err       error
}

// SoakPoolRunStats captures fixed-pool execution metrics (worker spawn vs job count).
type SoakPoolRunStats struct {
	WorkersStarted int64
	JobsStarted    int64
	MaxActive      int64
}

// RunSoakWorkerPool runs jobs with exactly workers long-lived goroutines.
// It does not spawn one goroutine per job. run must not call testing.T methods.
func RunSoakWorkerPool(workers int, jobs []SoakPoolJob, run func(SoakPoolJob) SoakPoolResult) []SoakPoolResult {
	out, _ := RunSoakWorkerPoolWithStats(workers, jobs, run)
	return out
}

// RunSoakWorkerPoolWithStats is RunSoakWorkerPool plus worker/job concurrency stats.
func RunSoakWorkerPoolWithStats(workers int, jobs []SoakPoolJob, run func(SoakPoolJob) SoakPoolResult) ([]SoakPoolResult, SoakPoolRunStats) {
	out := make([]SoakPoolResult, len(jobs))
	var stats SoakPoolRunStats
	if len(jobs) == 0 {
		return out, stats
	}
	if workers <= 0 {
		workers = 1
	}
	if workers > len(jobs) {
		workers = len(jobs)
	}
	ch := make(chan SoakPoolJob)
	var wg sync.WaitGroup
	var active atomic.Int64
	for range workers {
		wg.Go(func() {
			atomic.AddInt64(&stats.WorkersStarted, 1)
			for job := range ch {
				atomic.AddInt64(&stats.JobsStarted, 1)
				c := active.Add(1)
				for {
					m := atomic.LoadInt64(&stats.MaxActive)
					if c <= m || atomic.CompareAndSwapInt64(&stats.MaxActive, m, c) {
						break
					}
				}
				res := run(job)
				active.Add(-1)
				res.Index = job.Index
				res.Case = job.Case
				out[job.Index] = res
			}
		})
	}
	for _, job := range jobs {
		ch <- job
	}
	close(ch)
	wg.Wait()
	return out, stats
}
