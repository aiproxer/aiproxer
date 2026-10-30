package featurehost

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// proxyMemoryPhase2AuxClient records the auxiliary submissions of the phase 2
// attempts and answers them with one canned consolidation document.
type proxyMemoryPhase2AuxClient struct {
	mu       sync.Mutex
	submits  []auxiliary.SubmitOptions
	requests []auxiliary.Request
	reply    string
}

func (c *proxyMemoryPhase2AuxClient) SubmitCollect(_ context.Context, req auxiliary.Request, opts auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.submits = append(c.submits, opts)
	c.requests = append(c.requests, req)
	return auxiliary.JobID("phase2-job"), nil
}

func (c *proxyMemoryPhase2AuxClient) Await(context.Context, auxiliary.JobID) (aipapi.Collected, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var collected aipapi.Collected
	collected.Text.WriteString(c.reply)
	return collected, nil
}

func (c *proxyMemoryPhase2AuxClient) Forget(auxiliary.JobID) {}

func (c *proxyMemoryPhase2AuxClient) submissionCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.submits)
}

// requestsCarrying counts the recorded requests whose rendered text contains
// needle.
func (c *proxyMemoryPhase2AuxClient) requestsCarrying(needle string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, req := range c.requests {
		if req.Call == nil {
			continue
		}
		found := false
		for _, msg := range req.Call.Messages {
			for _, part := range msg.Parts {
				if strings.Contains(part.Text, needle) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			count++
		}
	}
	return count
}

// proxyMemoryPhase2Owner is the owner of the seeded bank. Its project key is
// derived the way the capture plane derives it, so the fixture carries no
// key-shaped literal.
var proxyMemoryPhase2Owner = proxymemory.Owner{
	TenantID:    "tenant-phase2",
	PrincipalID: "alice-phase2",
	ProjectKey:  proxymemory.ProjectKey(proxymemory.NormalizePath("/w/phase2")),
}

// proxyMemoryPhase2Clock is the worker's injected bookkeeping clock. The tests
// advance it past the retry backoff to prove the bank stays independently
// eligible without a new capture or note.
type proxyMemoryPhase2Clock struct{ now time.Time }

func (c *proxyMemoryPhase2Clock) Now() time.Time          { return c.now }
func (c *proxyMemoryPhase2Clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// openProxyMemoryPhase2Store opens one real SQLite-backed store with the proxy
// memory schema applied.
func openProxyMemoryPhase2Store(t *testing.T) (*proxymemorystore.Store, *bun.DB, *proxyMemoryPhase2Clock) {
	t.Helper()
	bunDB := openProxyMemoryBunDB(t)
	require.NoError(t, proxymemorystore.EnsureSchema(context.Background(), bunDB))
	return proxymemorystore.New(bunDB, proxyMemoryPhase2Core{}), bunDB, &proxyMemoryPhase2Clock{now: time.UnixMilli(1_750_000_000_000).UTC()}
}

// These phase-2 tests exercise the real memory store, not core retirement.
// Their seeded sessions remain active throughout the test.
type proxyMemoryPhase2Core struct{}

func (proxyMemoryPhase2Core) ALegState(context.Context, string) (proxymemorystore.ALegState, error) {
	return proxymemorystore.ALegState{Active: true, NextBSeq: 1}, nil
}

func (proxyMemoryPhase2Core) ActiveALegs(_ context.Context, ids []string) (map[string]bool, error) {
	active := make(map[string]bool, len(ids))
	for _, id := range ids {
		active[id] = true
	}
	return active, nil
}

// newProxyMemoryPhase2Worker builds the phase 2 worker over the real store and
// the recording auxiliary client.
func newProxyMemoryPhase2Worker(t *testing.T, store proxymemory.Store, aux proxymemoryPhase2AuxClientLike, clock *proxyMemoryPhase2Clock, inputTokens int) *proxymemory.Worker {
	t.Helper()
	return newProxyMemoryPhase2WorkerWithRawCap(t, store, aux, clock, inputTokens, 0)
}

// newProxyMemoryPhase2WorkerWithRawCap is newProxyMemoryPhase2Worker with an
// explicit per-attempt extraction cap. A zero cap keeps the default.
func newProxyMemoryPhase2WorkerWithRawCap(t *testing.T, store proxymemory.Store, aux proxymemoryPhase2AuxClientLike, clock *proxyMemoryPhase2Clock, inputTokens, maxRawMemories int) *proxymemory.Worker {
	t.Helper()
	cfg := proxymemory.DefaultConfig()
	cfg.Principals = []string{proxyMemoryPhase2Owner.PrincipalID}
	cfg.ExtractRoute = "local:extract"
	cfg.ConsolidateRoute = "local:consolidate"
	cfg.Phase2InputTokens = inputTokens
	cfg.Phase2Batch = 1
	if maxRawMemories > 0 {
		cfg.MaxRawMemories = maxRawMemories
	}
	return proxymemory.NewWorker(cfg, store, aux, proxymemory.NewPrometheusCollector(), clock.Now, slog.New(slog.DiscardHandler))
}

// proxymemoryPhase2AuxClientLike is the narrow auxiliary surface the worker
// needs, so the helper accepts the recording client without naming it twice.
type proxymemoryPhase2AuxClientLike = auxiliary.BackgroundClient

// seedProxyMemoryExtraction stores one extraction of a distinct session,
// generated generatedAge before the clock, and leaves its bank dirty. It
// returns the stored extraction so a caller can derive its summary name.
func seedProxyMemoryExtraction(t *testing.T, store *proxymemorystore.Store, aLegID, rawMemory string, generatedAge time.Duration, clock *proxyMemoryPhase2Clock) proxymemory.Extraction {
	t.Helper()
	ctx := context.Background()
	activity := clock.now.Add(-2 * time.Hour)
	committed, err := store.CommitFirstTurn(ctx, proxymemory.FirstTurnJob{
		Principal:  proxymemory.Principal{TenantID: proxyMemoryPhase2Owner.TenantID, PrincipalID: proxyMemoryPhase2Owner.PrincipalID},
		ALegID:     aLegID,
		Project:    proxymemory.Project{Key: proxyMemoryPhase2Owner.ProjectKey, Label: "/w/phase2"},
		Decision:   proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
		EpochKnown: true,
	})
	require.NoError(t, err)
	require.True(t, committed)
	outcome, err := store.UpsertSession(ctx, proxymemory.SessionCapture{
		Owner:        proxyMemoryPhase2Owner,
		ProjectLabel: "/w/phase2",
		ALegID:       aLegID,
		Transcript:   `[{"type":"message","role":"user","text":"hello"}]`,
		ActivityMS:   activity.UnixMilli(),
		SourceSeq:    1,
	})
	require.NoError(t, err)
	require.Equal(t, proxymemory.UpsertStored, outcome)
	claimed, err := store.ClaimSessions(ctx, proxymemory.ClaimQuery{
		NowMS:        clock.now.UnixMilli(),
		IdleBeforeMS: clock.now.Add(-time.Hour).UnixMilli(),
		LeaseUntilMS: clock.now.Add(time.Hour).UnixMilli(),
		LeaseOwner:   "phase2-seed",
		Limit:        1,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	extraction := proxymemory.Extraction{
		Revision:        1,
		Owner:           proxyMemoryPhase2Owner,
		ALegID:          aLegID,
		SessionRef:      proxymemory.SessionRef(aLegID),
		RawMemory:       rawMemory,
		RolloutSummary:  "summary",
		RolloutSlug:     "slug",
		SourceUpdatedMS: activity.UnixMilli(),
		GeneratedAtMS:   clock.now.Add(-generatedAge).UnixMilli(),
	}
	require.NoError(t, store.CompleteSession(ctx, claimed[0], extraction))
	return extraction
}

// publishProxyMemoryBank publishes the owner's dirty bank with the given
// consolidation names, acknowledged extraction revisions and memory document,
// which records them as the previous publication for the next worker attempt.
func publishProxyMemoryBank(t *testing.T, store *proxymemorystore.Store, clock *proxyMemoryPhase2Clock, names []string, acked []proxymemory.ExtractionRef, memoryMD string) {
	t.Helper()
	ctx := context.Background()
	claimed, err := store.ClaimBanks(ctx, proxymemory.ClaimQuery{
		NowMS:        clock.now.UnixMilli(),
		LeaseUntilMS: clock.now.Add(time.Hour).UnixMilli(),
		LeaseOwner:   "phase2-seed",
		Limit:        1,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, store.PublishBank(ctx, claimed[0], proxymemory.Publication{
		MemoryMD:          memoryMD,
		MemorySummary:     "summary",
		ConsolidatedNames: names,
		Acknowledged:      acked,
		PublishedAtMS:     clock.now.UnixMilli(),
	}))
}

// proxyMemoryPhase2BankState is the bank row a later consolidation sees.
type proxyMemoryPhase2BankState struct {
	dirtySeq        int64
	consolidatedSeq int64
	version         int64
	attempts        int64
	names           []string
}

func readProxyMemoryPhase2Bank(t *testing.T, db *bun.DB) proxyMemoryPhase2BankState {
	t.Helper()
	var state proxyMemoryPhase2BankState
	var names string
	err := db.NewRaw(`
		SELECT dirty_seq, consolidated_seq, version, attempts, consolidated_names FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, proxyMemoryPhase2Owner.TenantID, proxyMemoryPhase2Owner.PrincipalID, proxyMemoryPhase2Owner.ProjectKey).
		Scan(t.Context(), &state.dirtySeq, &state.consolidatedSeq, &state.version, &state.attempts, &names)
	require.NoError(t, err)
	if names != "" {
		require.NoError(t, json.Unmarshal([]byte(names), &state.names))
	}
	return state
}

// proxyMemoryPhase2NoteCount counts the owner's pending notes.
func proxyMemoryPhase2NoteCount(t *testing.T, db *bun.DB) int {
	t.Helper()
	var count int
	err := db.NewRaw(`
		SELECT count(*) FROM proxy_memory_notes
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, proxyMemoryPhase2Owner.TenantID, proxyMemoryPhase2Owner.PrincipalID, proxyMemoryPhase2Owner.ProjectKey).
		Scan(t.Context(), &count)
	require.NoError(t, err)
	return count
}

// proxyMemoryPhase2ExtractionCount counts the owner's stored extractions.
func proxyMemoryPhase2ExtractionCount(t *testing.T, db *bun.DB) int {
	t.Helper()
	var count int
	err := db.NewRaw(`
		SELECT count(*) FROM proxy_memory_extractions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?
	`, proxyMemoryPhase2Owner.TenantID, proxyMemoryPhase2Owner.PrincipalID, proxyMemoryPhase2Owner.ProjectKey).
		Scan(t.Context(), &count)
	require.NoError(t, err)
	return count
}

// TestProxyMemoryPhase2_LimitedInputDrainsResidualExtractionAgainstRealStore
// pins the #1383 obligation against the real store: with a per-attempt cap of
// one, the extraction the cap excludes must not be lost. The first attempt
// consolidates the oldest extraction and leaves the bank dirty; the next
// attempt, with no new capture or note, consolidates the remaining one and
// settles the bank.
func TestProxyMemoryPhase2_LimitedInputDrainsResidualExtractionAgainstRealStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db, clock := openProxyMemoryPhase2Store(t)
	aux := &proxyMemoryPhase2AuxClient{reply: `{"memory_md":"# Memory","memory_summary":"summary"}`}
	worker := newProxyMemoryPhase2WorkerWithRawCap(t, store, aux, clock, 100_000, 1)
	seedProxyMemoryExtraction(t, store, "aleg-residual-old", "raw memory older", 2*time.Hour, clock)
	seedProxyMemoryExtraction(t, store, "aleg-residual-new", "raw memory newer", time.Hour, clock)
	require.Equal(t, 2, proxyMemoryPhase2ExtractionCount(t, db))

	worker.Tick(ctx)

	require.Equal(t, 1, aux.submissionCount(), "the first attempt consolidates one extraction")
	require.Equal(t, 1, aux.requestsCarrying("raw memory older"), "the oldest outstanding extraction is scheduled first")
	require.Zero(t, aux.requestsCarrying("raw memory newer"), "the capped extraction must not be sent early")
	state := readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, int64(2), state.dirtySeq)
	require.Equal(t, int64(0), state.consolidatedSeq,
		"the extraction excluded by the cap must keep the bank dirty")
	require.Equal(t, 2, proxyMemoryPhase2ExtractionCount(t, db), "nothing is deleted")

	clock.Advance(2 * time.Hour)
	worker.Tick(ctx)

	require.Equal(t, 2, aux.submissionCount(), "the second attempt consolidates the residue")
	require.Equal(t, 1, aux.requestsCarrying("raw memory newer"))
	state = readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, state.dirtySeq, state.consolidatedSeq,
		"the bank settles only once every outstanding extraction is acknowledged")
	require.Len(t, state.names, 2, "both extractions keep their consolidated identity")
}

// TestProxyMemoryPhase2_TokenBudgetDrainsResidualExtractionAgainstRealStore
// reproduces #1450: at 6,000 input tokens, two ~12 KiB extractions each fit
// individually but do not fit together. Publication must acknowledge only the
// oldest carried revision, retain the younger obligation and drain it on the
// next claim without any new session, note or capture.
func TestProxyMemoryPhase2_TokenBudgetDrainsResidualExtractionAgainstRealStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db, clock := openProxyMemoryPhase2Store(t)
	aux := &proxyMemoryPhase2AuxClient{reply: `{"memory_md":"# Memory","memory_summary":"summary"}`}
	worker := newProxyMemoryPhase2Worker(t, store, aux, clock, 6_000)
	older := seedProxyMemoryExtraction(t, store, "aleg-budget-older",
		"older budget extraction\n"+strings.Repeat("a", 12*1024), 2*time.Hour, clock)
	newer := seedProxyMemoryExtraction(t, store, "aleg-budget-newer",
		"newer budget extraction\n"+strings.Repeat("b", 12*1024), time.Hour, clock)
	require.Equal(t, 2, proxyMemoryPhase2ExtractionCount(t, db))

	worker.Tick(ctx)

	require.Equal(t, 1, aux.submissionCount(), "the oldest whole pair must make progress")
	require.Equal(t, 1, aux.requestsCarrying("older budget extraction"))
	require.Zero(t, aux.requestsCarrying("newer budget extraction"), "the younger pair did not fit")
	state := readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, int64(2), state.dirtySeq)
	require.Equal(t, int64(0), state.consolidatedSeq, "the omitted revision keeps the bank dirty")
	require.Equal(t, int64(1), state.version)
	require.Equal(t, []string{proxymemory.SummaryName(older)}, state.names)
	require.True(t, proxyMemoryPhase2ExtractionAcknowledged(t, db, older.ALegID))
	require.False(t, proxyMemoryPhase2ExtractionAcknowledged(t, db, newer.ALegID),
		"an omitted extraction must remain durably outstanding")

	clock.Advance(24 * time.Hour)
	worker.Tick(ctx)

	require.Equal(t, 2, aux.submissionCount(), "the residual revision must consolidate without new capture")
	require.Equal(t, 1, aux.requestsCarrying("older budget extraction"), "acked work is not resubmitted")
	require.Equal(t, 1, aux.requestsCarrying("newer budget extraction"))
	state = readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, state.dirtySeq, state.consolidatedSeq, "the bank settles only after the tail is acked")
	require.Equal(t, int64(2), state.version)
	require.Len(t, state.names, 2)
	require.Contains(t, state.names, proxymemory.SummaryName(older))
	require.Contains(t, state.names, proxymemory.SummaryName(newer))
	require.True(t, proxyMemoryPhase2ExtractionAcknowledged(t, db, newer.ALegID))
	require.Equal(t, 2, proxyMemoryPhase2ExtractionCount(t, db), "neither extraction is deleted")

	clock.Advance(24 * time.Hour)
	worker.Tick(ctx)
	require.Equal(t, 2, aux.submissionCount(), "a settled bank must not buy a third inference")
}

// proxyMemoryPhase2ExtractionAcknowledged inspects the exact persisted row,
// rather than inferring acknowledgment from names or dirty sequence alone.
func proxyMemoryPhase2ExtractionAcknowledged(t *testing.T, db *bun.DB, aLegID string) bool {
	t.Helper()
	var acknowledgedAtMS int64
	require.NoError(t, db.NewRaw(`
		SELECT consolidated_at_ms FROM proxy_memory_extractions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND a_leg_id = ?
	`, proxyMemoryPhase2Owner.TenantID, proxyMemoryPhase2Owner.PrincipalID,
		proxyMemoryPhase2Owner.ProjectKey, aLegID).Scan(t.Context(), &acknowledgedAtMS))
	return acknowledgedAtMS != 0
}

// TestProxyMemoryPhase2_ExcludedExtractionStaysPendingAgainstRealStore pins
// the phase 2 budget refusal against the real store: a bank whose only new
// extraction does not fit the input budget must not be published as
// consolidated. The bank keeps its dirty sequence, the extraction stays
// pending consolidation work, and no inference is bought.
func TestProxyMemoryPhase2_ExcludedExtractionStaysPendingAgainstRealStore(t *testing.T) {
	t.Parallel()
	store, db, clock := openProxyMemoryPhase2Store(t)
	aux := &proxyMemoryPhase2AuxClient{reply: `{"memory_md":"# Memory","memory_summary":"summary"}`}
	worker := newProxyMemoryPhase2Worker(t, store, aux, clock, 100_000)
	seedProxyMemoryExtraction(t, store, "aleg-phase2-oversized", strings.Repeat("r", 512*1024), time.Hour, clock)

	worker.Tick(context.Background())

	require.Zero(t, aux.submissionCount(), "an excluded extraction must not buy an inference")
	state := readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, int64(1), state.dirtySeq, "the bank must keep its dirty sequence")
	require.Equal(t, int64(0), state.consolidatedSeq, "the bank must not be marked consolidated")
	require.Equal(t, int64(0), state.version, "the bank must not be published")
	require.Equal(t, 1, proxyMemoryPhase2ExtractionCount(t, db), "the extraction must stay pending")
}

// TestProxyMemoryPhase2_PartialExtractionExclusionStaysEligibleAgainstRealStore
// pins the zero-progress prefix guard: when the oldest extraction is too large,
// the builder cannot skip it to process the smaller newer extraction. Neither
// fits the admissible oldest prefix; both stay outstanding and repeated claims
// refuse inference even after retry backoff.
func TestProxyMemoryPhase2_PartialExtractionExclusionStaysEligibleAgainstRealStore(t *testing.T) {
	t.Parallel()
	store, db, clock := openProxyMemoryPhase2Store(t)
	aux := &proxyMemoryPhase2AuxClient{reply: `{"memory_md":"# Memory","memory_summary":"summary"}`}
	worker := newProxyMemoryPhase2Worker(t, store, aux, clock, 100_000)
	seedProxyMemoryExtraction(t, store, "aleg-phase2-dropped", strings.Repeat("r", 512*1024), 2*time.Hour, clock)
	seedProxyMemoryExtraction(t, store, "aleg-phase2-carried", "raw memory", time.Hour, clock)
	require.Equal(t, 2, proxyMemoryPhase2ExtractionCount(t, db))

	worker.Tick(context.Background())

	require.Zero(t, aux.submissionCount(), "a dropped extraction must not buy an inference")
	state := readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, int64(2), state.dirtySeq, "the bank must keep both dirty sequences")
	require.Equal(t, int64(0), state.consolidatedSeq, "the bank must not be marked consolidated")
	require.Equal(t, int64(0), state.version, "the bank must not be published")
	require.Equal(t, 2, proxyMemoryPhase2ExtractionCount(t, db), "both extractions must stay pending")

	// The dropped extraction stays independently eligible: the bank is claimed
	// again after its backoff, with no new capture and no new note, and the
	// same refusal must not spend inference.
	clock.Advance(2 * time.Hour)
	worker.Tick(context.Background())

	require.Zero(t, aux.submissionCount(), "the repeat claim must not buy an inference")
	again := readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, int64(2), again.attempts, "both claims must be recorded")
	require.Equal(t, int64(0), again.consolidatedSeq, "the bank must still not be consolidated")
	require.Equal(t, int64(0), again.version, "the bank must never be published")
}

// TestProxyMemoryPhase2_ExtractionProgressKeepsUnprocessableNoteAgainstRealStore
// pins the phase 2 progress against the real store: a pending note the input
// budget cannot fit must not block the consolidation of new extraction
// content. The publication advances the bank once and the excluded note stays
// pending because it was not consumed.
func TestProxyMemoryPhase2_ExtractionProgressKeepsUnprocessableNoteAgainstRealStore(t *testing.T) {
	t.Parallel()
	store, db, clock := openProxyMemoryPhase2Store(t)
	aux := &proxyMemoryPhase2AuxClient{reply: `{"memory_md":"# Memory","memory_summary":"summary"}`}
	worker := newProxyMemoryPhase2Worker(t, store, aux, clock, 2_000)
	seedProxyMemoryExtraction(t, store, "aleg-phase2-fitting", "raw memory", time.Hour, clock)
	require.NoError(t, store.AddNote(context.Background(), proxyMemoryPhase2Owner, proxymemory.Note{
		NoteID:      "note-oversized-for-input",
		Text:        strings.Repeat("n", 8_192),
		CreatedAtMS: clock.now.Add(-time.Hour).UnixMilli(),
	}))

	before := readProxyMemoryPhase2Bank(t, db)
	worker.Tick(context.Background())

	require.Equal(t, 1, aux.submissionCount(), "the fitting extraction must buy exactly one inference")
	state := readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, before.dirtySeq, state.consolidatedSeq, "the publication acknowledges the claimed work")
	require.Equal(t, state.consolidatedSeq+1, state.dirtySeq, "the residual note keeps an independent obligation")
	require.Equal(t, int64(1), state.version, "the bank must be published once")
	require.Equal(t, 1, proxyMemoryPhase2NoteCount(t, db), "the excluded note must stay pending")
	for range 3 {
		clock.Advance(time.Minute)
		worker.Tick(context.Background())
	}
	require.Equal(t, 1, aux.submissionCount(), "residual-note retries must not pay to replay the extraction")
	require.Equal(t, int64(1), readProxyMemoryPhase2Bank(t, db).version)
}

// TestProxyMemoryPhase2_AlreadyConsolidatedReplayDoesNotPayAgainstRealStore
// pins the review finding on repeated billing against the real store: replaying
// an already consolidated extraction is not progress, so an unprocessable note
// must not drive paid consolidations. Every claim refuses, the note and the
// previous publication survive, and no inference is bought.
func TestProxyMemoryPhase2_AlreadyConsolidatedReplayDoesNotPayAgainstRealStore(t *testing.T) {
	t.Parallel()
	store, db, clock := openProxyMemoryPhase2Store(t)
	aux := &proxyMemoryPhase2AuxClient{reply: `{"memory_md":"# Memory","memory_summary":"summary"}`}
	worker := newProxyMemoryPhase2Worker(t, store, aux, clock, 2_000)
	extraction := seedProxyMemoryExtraction(t, store, "aleg-phase2-consolidated", "raw memory", time.Hour, clock)
	publishProxyMemoryBank(t, store, clock, []string{proxymemory.SummaryName(extraction)},
		[]proxymemory.ExtractionRef{{ALegID: extraction.ALegID, Revision: extraction.Revision, GeneratedAtMS: extraction.GeneratedAtMS}}, "# Memory")
	require.NoError(t, store.AddNote(context.Background(), proxyMemoryPhase2Owner, proxymemory.Note{
		NoteID:      "note-oversized-for-input",
		Text:        strings.Repeat("n", 8_192),
		CreatedAtMS: clock.now.Add(-time.Hour).UnixMilli(),
	}))
	published := readProxyMemoryPhase2Bank(t, db)

	for range 3 {
		worker.Tick(context.Background())
		clock.Advance(2 * time.Hour)
	}

	require.Zero(t, aux.submissionCount(), "replaying an unchanged extraction must not buy an inference")
	state := readProxyMemoryPhase2Bank(t, db)
	require.Equal(t, published.dirtySeq, state.dirtySeq, "the bank must keep its dirty sequence")
	require.Equal(t, published.version, state.version, "the previous publication must survive")
	require.Equal(t, published.consolidatedSeq, state.consolidatedSeq, "the consolidated sequence must not advance")
	require.Equal(t, int64(3), state.attempts, "every claim must be recorded")
	require.Equal(t, 1, proxyMemoryPhase2NoteCount(t, db), "the note must stay pending")
}

// TestProxyMemoryPhase2_BudgetOmittedExtractionKeepsIdentityAgainstRealStore
// pins the review finding on the progress baseline against the real store: an
// already consolidated extraction that no longer fits one prompt must keep its
// consolidated identity, so two successive notes are still processed and the
// extraction is never treated as new work or re-sent to the provider.
func TestProxyMemoryPhase2_BudgetOmittedExtractionKeepsIdentityAgainstRealStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, db, clock := openProxyMemoryPhase2Store(t)
	aux := &proxyMemoryPhase2AuxClient{reply: `{"memory_md":"# Memory","memory_summary":"summary"}`}
	worker := newProxyMemoryPhase2Worker(t, store, aux, clock, 4_000)
	rawMemory := strings.Repeat("r", 100*1024)
	extraction := seedProxyMemoryExtraction(t, store, "aleg-phase2-consolidated-large", rawMemory, time.Hour, clock)
	// The published memory is large enough that the already consolidated
	// extraction no longer fits one prompt.
	publishProxyMemoryBank(t, store, clock, []string{proxymemory.SummaryName(extraction)},
		[]proxymemory.ExtractionRef{{ALegID: extraction.ALegID, Revision: extraction.Revision, GeneratedAtMS: extraction.GeneratedAtMS}}, strings.Repeat("m", 64*1024))
	seeded := readProxyMemoryPhase2Bank(t, db)

	for i, noteID := range []string{"note-1", "note-2"} {
		require.NoError(t, store.AddNote(ctx, proxyMemoryPhase2Owner, proxymemory.Note{
			NoteID:      noteID,
			Text:        "note " + noteID,
			CreatedAtMS: clock.now.Add(time.Duration(i-2) * time.Hour).UnixMilli(),
		}))
		worker.Tick(ctx)

		state := readProxyMemoryPhase2Bank(t, db)
		require.Equal(t, i+1, aux.submissionCount(), "each fitting note must buy exactly one inference")
		require.Equal(t, []string{proxymemory.SummaryName(extraction)}, state.names,
			"the omitted already consolidated extraction must keep its identity")
		require.Equal(t, state.dirtySeq, state.consolidatedSeq, "each note must advance the bank once")
		require.Zero(t, proxyMemoryPhase2NoteCount(t, db), "the fitting note must be consumed")
	}

	require.Equal(t, seeded.version+2, readProxyMemoryPhase2Bank(t, db).version, "both notes must publish")
	require.Zero(t, aux.requestsCarrying(strings.Repeat("r", 1024)),
		"the omitted extraction's content must never be re-sent to the provider")
}
