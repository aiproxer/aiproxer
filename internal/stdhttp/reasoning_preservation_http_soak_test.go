//go:build precommit

package stdhttp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/testkit/reasoninge2e"
)

// TestReasoningPreservationHTTP_Soak runs the env-gated full HTTP soak.
// Skipped unless AIP_REASONING_E2E_SOAK=1. Defaults: 1000 seeds × 100 turns
// with 25%/25%/50% mode allocation. Uses a fixed worker pool (exactly N workers)
// and the same ClientEmulator/OracleLedger/stdhttp stack as the precommit matrix.
func TestReasoningPreservationHTTP_Soak(t *testing.T) {
	cfg, err := reasoninge2e.LoadSoakConfigFromEnv()
	if err != nil {
		t.Fatalf("soak config: %v", err)
	}
	if !cfg.Enabled {
		t.Skip("reasoning E2E soak disabled; set AIP_REASONING_E2E_SOAK=1 to run")
	}

	cases := reasoninge2e.SoakCases(cfg)
	if cfg.Replay == nil {
		if len(cases) != cfg.Seeds {
			t.Fatalf("soak case count=%d want=%d", len(cases), cfg.Seeds)
		}
		if cfg.Seeds == reasoninge2e.DefaultSoakSeeds {
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
			if dropAll != 250 || always != 250 || combined != 500 {
				t.Fatalf("soak split drop=%d always=%d combined=%d want 250/250/500", dropAll, always, combined)
			}
		}
	} else if len(cases) != 1 {
		t.Fatalf("soak replay cases=%d want=1", len(cases))
	}

	workers := min(cfg.Workers, len(cases))
	perSeed := soakPerSeedTimeout(cfg.Turns)
	t.Logf("soak start cases=%d turns=%d workers=%d per_seed_timeout=%s", len(cases), cfg.Turns, workers, perSeed)

	jobs := make([]reasoninge2e.SoakPoolJob, len(cases))
	for i, c := range cases {
		jobs[i] = reasoninge2e.SoakPoolJob{Index: i, Case: c}
	}
	results := reasoninge2e.RunSoakWorkerPool(workers, jobs, func(job reasoninge2e.SoakPoolJob) reasoninge2e.SoakPoolResult {
		n, err := executeReasoningMatrixSeed(job.Case.Mode, job.Case.Seed, cfg.Turns, perSeed, soakFail)
		return reasoninge2e.SoakPoolResult{
			Index:     job.Index,
			Case:      job.Case,
			HTTPTurns: n,
			Err:       err,
		}
	})

	var totalHTTP int
	var completed int
	for _, res := range results {
		if res.Err != nil {
			t.Errorf("%s/seed_%d: %v", res.Case.Mode, res.Case.Seed, res.Err)
			continue
		}
		completed++
		totalHTTP += res.HTTPTurns
		t.Logf("soak progress done=%d/%d mode=%s seed=%d http_turns=%d", completed, len(cases), res.Case.Mode, res.Case.Seed, res.HTTPTurns)
	}
	if t.Failed() {
		return
	}
	if completed != len(cases) {
		t.Fatalf("completed seeds=%d want=%d", completed, len(cases))
	}
	wantHTTP := len(cases) * cfg.Turns
	if totalHTTP != wantHTTP {
		t.Fatalf("total ledger HTTP turns=%d want=%d", totalHTTP, wantHTTP)
	}
}

func soakPerSeedTimeout(turns int) time.Duration {
	d := time.Duration(turns)*3*time.Second + time.Minute
	if d < time.Minute {
		return time.Minute
	}
	if d > 30*time.Minute {
		return 30 * time.Minute
	}
	return d
}

func soakFail(tp reasoninge2e.TranscriptPlan, idx int, reasonCode string, err error) string {
	code := reasonCode
	if err != nil {
		msg := err.Error()
		if _, after, ok := strings.Cut(msg, "structural mismatch:"); ok {
			code = strings.TrimSpace(after)
			if j := strings.IndexAny(code, " \t"); j >= 0 {
				code = code[:j]
			}
		}
	}
	return reasoninge2e.FormatSoakFail(tp, idx, code) + reasoninge2e.FormatRetentionDiag(err)
}
