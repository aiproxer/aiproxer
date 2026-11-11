package proxymemory

// Tick-scoped phase 2 attempt bounding (issue #1728). A dirty bank whose
// publication loses the compare-and-set stays immediately claimable: the store
// correctly leaves it dirty, and failPublish correctly does not schedule a retry
// it no longer owns. Without a per-tick visited set the phase 2 batch therefore
// re-claims that one bank and buys a real paid consolidation for it on every
// remaining slot of the same tick. These cases drive a candidate stub that keeps
// re-offering a dirty bank exactly like the real store does, so the old control
// flow re-purchases the bank and the patched one does not.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// workerExtractionRef identifies one carried extraction revision.
func workerExtractionRef(e Extraction) ExtractionRef {
	return ExtractionRef{ALegID: e.ALegID, Revision: e.Revision, GeneratedAtMS: e.GeneratedAtMS}
}

// workerPhase2PoolHarness is one worker over a real candidate set: every pool
// bank stays eligible until the claim query excludes its complete owner.
func workerPhase2PoolHarness(t *testing.T, cfg Config, pool ...ClaimedBank) *workerTestHarness {
	t.Helper()
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	h.store.bankPool = pool
	return h
}

// workerBankOfOwner is one dirty claimed bank of a distinct owner.
func workerBankOfOwner(owner Owner) ClaimedBank {
	bank := workerClaimedBank()
	bank.Owner = owner
	return bank
}

// TestWorkerPhase2TickAttemptsALostCASBankOnce is T1: with a batch of four and
// one continually eligible bank whose publication loses its CAS, the tick buys
// exactly one paid consolidation, counts one lease loss, and never routes the
// stale claim through FailBank.
func TestWorkerPhase2TickAttemptsALostCASBankOnce(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2Batch = 4
	h := workerPhase2PoolHarness(t, cfg, workerClaimedBank())
	h.store.extractions = []Extraction{workerExtraction(h.base)}
	h.store.publishErrFor = func(ClaimedBank) error { return ErrLeaseLost }

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 1,
		"a lost publication CAS must not buy another paid consolidation in the same tick")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2LeaseLost))
	require.Equal(t, float64(0), captureCounter(t, h.metrics, EventPhase2Failed),
		"a lost CAS is a lease loss, not a failure")
	require.Empty(t, h.store.bankFailCalls(),
		"a claim whose CAS was lost must never be reprogrammed through FailBank")

	claims := h.store.bankClaimCalls()
	require.Len(t, claims, 2, "the tick asks for candidates only to learn that the bank is spent")
	require.Empty(t, claims[0].ExcludeBankOwners,
		"the first claim of a tick must be the historical candidate query")
	require.Equal(t, []Owner{workerOwner()}, claims[1].ExcludeBankOwners,
		"the spent bank identity must be excluded from the same tick's next candidate")
	require.Equal(t, 1, claims[1].Limit, "the exclusion must not change the one-row claim cadence")
}

// TestWorkerPhase2TickSpendsTheBatchOnDistinctBanks is T2 and T3: a bank that
// loses its CAS is never re-claimed, other identities still consume the
// remaining slots, and the exclusion keys on the COMPLETE owner triple so two
// owners that share one project key stay independent.
func TestWorkerPhase2TickSpendsTheBatchOnDistinctBanks(t *testing.T) {
	ownerA := workerOwner()
	ownerB := Owner{TenantID: workerTestTenant, PrincipalID: "bob-worker", ProjectKey: workerTestProject}
	ownerC := Owner{TenantID: "tenant-other", PrincipalID: workerTestPrincipal, ProjectKey: workerTestProject}
	ownerD := Owner{TenantID: workerTestTenant, PrincipalID: workerTestPrincipal, ProjectKey: strings.Repeat("d", 64)}
	require.Equal(t, ownerA.ProjectKey, ownerB.ProjectKey, "owners B and C share owner A's project key")
	require.Equal(t, ownerA.ProjectKey, ownerC.ProjectKey)
	cfg := workerTestConfig()
	cfg.Phase2Batch = 4
	h := workerPhase2PoolHarness(t, cfg,
		workerBankOfOwner(ownerA), workerBankOfOwner(ownerB),
		workerBankOfOwner(ownerC), workerBankOfOwner(ownerD))
	h.store.extractions = []Extraction{workerExtraction(h.base)}
	h.store.publishErrFor = func(bank ClaimedBank) error {
		if bank.Owner == ownerA {
			return ErrLeaseLost
		}
		return nil
	}

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 4, "each distinct bank must consume one batch slot")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2LeaseLost),
		"only the discarded publication lost its lease")
	require.Equal(t, float64(3), captureCounter(t, h.metrics, EventPhase2Published),
		"every other bank must still publish in the same tick")
	require.Empty(t, h.store.bankFailCalls(), "no attempt was an ordinary failure")

	inputs := h.store.inputCalls()
	require.Len(t, inputs, 4)
	require.Equal(t, []Owner{ownerA, ownerB, ownerC, ownerD}, []Owner{
		inputs[0].bank.Owner, inputs[1].bank.Owner, inputs[2].bank.Owner, inputs[3].bank.Owner,
	}, "the deterministic candidate order must be preserved and no bank may be repeated")
	claims := h.store.bankClaimCalls()
	require.Len(t, claims, 4)
	require.Empty(t, claims[0].ExcludeBankOwners)
	require.Equal(t, []Owner{ownerA}, claims[1].ExcludeBankOwners,
		"a lost-CAS bank must be excluded from the rest of the tick's candidates")
	require.Equal(t, []Owner{ownerA, ownerB, ownerC}, claims[3].ExcludeBankOwners,
		"every spent identity accumulates for the rest of the tick")
}

// TestWorkerPhase2TickRetriesTheBankOnALaterTick is T4 and T5: the visited set
// is local to one tick, so the next eligible tick — and a successor generation —
// retry the bank, and a budget-excluded extraction tail stays outstanding until
// its own later tick acknowledges exactly that revision.
func TestWorkerPhase2TickRetriesTheBankOnALaterTick(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2Batch = 4
	cfg.Phase2InputTokens = 20_000
	h := workerPhase2PoolHarness(t, cfg, workerClaimedBankWithoutNames())
	carried := workerExtractionAt(h.base, workerTestALeg2, 2*time.Hour)
	carried.RawMemory = strings.Repeat("r", 60<<10)
	tail := workerExtractionAt(h.base, workerTestALeg3, time.Hour)
	tail.RawMemory = strings.Repeat("s", 20<<10)
	h.store.extractions = []Extraction{carried, tail}
	attempts := 0
	h.store.publishErrFor = func(ClaimedBank) error {
		attempts++
		if attempts == 1 {
			return ErrLeaseLost
		}
		return nil
	}
	h.store.onPublish = func(_ ClaimedBank, p Publication) {
		// Model the store's durable progress: a publication records exactly the
		// revisions it carried, so the next claim sees them as consolidated.
		h.store.mu.Lock()
		defer h.store.mu.Unlock()
		h.store.bankPool[0].ConsolidatedNames = slices.Clone(p.ConsolidatedNames)
		h.store.bankPool[0].MemoryMD = p.MemoryMD
	}

	// Scenario precondition: the budget carries the oldest extraction only.
	_, included, err := BuildPhase2Request(cfg, Phase2Input{
		Extractions: []Extraction{carried, tail},
		WindowNames: []string{SummaryName(carried), SummaryName(tail)},
	})
	require.NoError(t, err)
	require.Equal(t, []string{SummaryName(carried)}, included.SummaryNames)
	require.True(t, included.Truncated)

	h.worker.Tick(context.Background())
	require.Len(t, h.aux.submitCalls(), 1, "a lost CAS buys one attempt in the tick")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2LeaseLost))

	h.clock.Advance(cfg.PollInterval)
	h.worker.Tick(context.Background())
	publishes := h.store.publishCalls()
	require.Len(t, publishes, 2, "the lost-CAS attempt and the recovered attempt each publish once")
	require.Equal(t, []ExtractionRef{workerExtractionRef(carried)}, publishes[1].publication.Acknowledged,
		"only the carried revision may be acknowledged")
	require.Empty(t, h.store.bankClaimCalls()[2].ExcludeBankOwners,
		"a new tick starts with an empty exclusion set")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2Published))

	h.clock.Advance(cfg.PollInterval)
	h.worker.Tick(context.Background())
	require.Len(t, h.store.publishCalls(), 3, "the tail is consolidated by its own later tick")
	require.Equal(t, []ExtractionRef{workerExtractionRef(tail)}, h.store.publishCalls()[2].publication.Acknowledged,
		"the budget-excluded tail is acknowledged exactly once, by its own tick")
	require.ElementsMatch(t, []string{SummaryName(carried), SummaryName(tail)},
		h.store.publishCalls()[2].publication.ConsolidatedNames)
	require.Len(t, h.aux.submitCalls(), 3, "each drained revision buys exactly one consolidation")

	h.clock.Advance(cfg.PollInterval)
	h.worker.Tick(context.Background())
	require.Len(t, h.aux.submitCalls(), 3, "a settled bank must not buy another consolidation")
	require.Len(t, h.store.publishCalls(), 4, "the settled bank publishes its retained identity set")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2PublishedEmpty))

	// A successor generation is a fresh worker over a fresh store: the visited
	// set belongs to an invocation, never to a process-level cache.
	successor := workerPhase2PoolHarness(t, cfg, workerClaimedBank())
	successor.store.extractions = []Extraction{workerExtraction(successor.base)}
	successor.worker.Tick(context.Background())
	require.Len(t, successor.aux.submitCalls(), 1,
		"no process-lifetime suppression may outlive one worker generation")
}

// TestWorkerPhase2TickKeepsTheOrdinaryFailureSchedule is T6: an ordinary
// publication failure still routes the claim through FailBank at
// now+RetryBackoff exactly once, and the failed bank is not re-purchased in the
// same tick. Exact retry-boundary eligibility is the store contract's
// "failing a bank clears its lease and schedules the retry" case.
func TestWorkerPhase2TickKeepsTheOrdinaryFailureSchedule(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2Batch = 4
	h := workerPhase2PoolHarness(t, cfg, workerClaimedBank())
	h.store.extractions = []Extraction{workerExtraction(h.base)}
	h.store.publishErr = errors.New("transient commit failure")

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 1)
	fails := h.store.bankFailCalls()
	require.Len(t, fails, 1, "the ordinary failure must be recorded exactly once")
	require.Equal(t, h.base.Add(cfg.RetryBackoff).UnixMilli(), fails[0].retryAtMS,
		"the retry schedule must stay now+RetryBackoff")
	require.Equal(t, workerClaimedBank(), fails[0].bank, "the failed claim must be the attempted claim")
	require.Equal(t, float64(1), captureCounter(t, h.metrics, EventPhase2Failed))
	require.Equal(t, float64(0), captureCounter(t, h.metrics, EventPhase2LeaseLost))
}

// TestWorkerPhase2TickRefusesAnExcludedClaimWithoutPayingAgain covers the
// defensive half of the contract: a store that returns an already excluded bank
// must never buy a second paid consolidation. The freshly returned claim — which
// the worker may or may not still own — is released only through the existing
// lease-owner-fenced failure path, and the phase ends.
func TestWorkerPhase2TickRefusesAnExcludedClaimWithoutPayingAgain(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2Batch = 4
	h := workerPhase2PoolHarness(t, cfg, workerClaimedBank())
	h.store.extractions = []Extraction{workerExtraction(h.base)}
	h.store.ignoreBankExclusions = true

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), 1, "a store contract violation must not buy a second paid call")
	fails := h.store.bankFailCalls()
	require.Len(t, fails, 1, "the returned claim must be released through the fenced failure path")
	require.Equal(t, h.base.Add(cfg.RetryBackoff).UnixMilli(), fails[0].retryAtMS)
	require.Len(t, h.store.bankClaimCalls(), 2, "the phase must end after the refused claim")
}

// TestWorkerPhase2TickCapsDistinctAttemptsPerTick is the T8 worker half: a
// configured batch beyond the parameter-budget cap is capped deterministically,
// and the last candidate query still carries every identity the tick spent (255
// complete owner triples, under the conservative SQLite bind limit).
func TestWorkerPhase2TickCapsDistinctAttemptsPerTick(t *testing.T) {
	cfg := workerTestConfig()
	cfg.Phase2Batch = workerPhase2MaxDistinctBanksPerTick + 8
	h := newWorkerTestHarness(t, cfg)
	h.aux.text = workerPhase2Output
	h.store.notes = []Note{{NoteID: "fits", Text: "remember this"}}
	h.store.bankPool = make([]ClaimedBank, 0, workerPhase2MaxDistinctBanksPerTick+8)
	for i := range workerPhase2MaxDistinctBanksPerTick + 8 {
		h.store.bankPool = append(h.store.bankPool, workerBankOfOwner(Owner{
			TenantID:    workerTestTenant,
			PrincipalID: workerTestPrincipal,
			ProjectKey:  strings.Repeat("0", 60) + string(rune('a'+i/26)) + string(rune('a'+i%26)),
		}))
	}

	h.worker.Tick(context.Background())

	require.Len(t, h.aux.submitCalls(), workerPhase2MaxDistinctBanksPerTick,
		"the per-tick cap must bound the distinct paid attempts")
	claims := h.store.bankClaimCalls()
	require.Len(t, claims, workerPhase2MaxDistinctBanksPerTick,
		"the capped tick must not issue a claim beyond its last allowed attempt")
	require.Len(t, claims[len(claims)-1].ExcludeBankOwners, workerPhase2MaxDistinctBanksPerTick-1,
		"the last candidate query must carry every identity the tick spent")
}
