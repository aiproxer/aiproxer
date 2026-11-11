package featurehost

// Issue #793 composed in-flight forget fencing: while the composed worker
// holds a durable extraction or consolidation claim, the projected HTTP
// control surface forgets the owner's scope. The claim is released only after
// the forget committed, and the test then observes that the stale claim cannot
// re-create the erased state.
//
// Topology: one real CompileGeneration over a durable topology whose
// proxy-memory store owns an isolated database, the generation's real worker
// (Started, so its claims carry the generation's lease owner), and the
// generation's projected proxy-memory HTTP handler. The worker's Tick is
// driven directly, so no wall-clock tick is involved; its auxiliary client is
// gated: when the gated phase reaches its collection the test learns the claim
// is already durable and holds the worker there until it releases it.
//
// The fixture seeds claimable state through the production Store contract
// calls (CommitFirstTurn -> UpsertSession -> ClaimSessions -> CompleteSession,
// then ClaimBanks -> PublishBank for the previously published bank), not
// through the capture plane: capture-to-extraction is the §16 sentinel's
// claim. This test owns one claim per scenario — an HTTP forget beats an
// in-flight worker claim and nothing republishes afterwards. The store's SQL
// lock and interleaving contracts stay in
// internal/infra/proxymemorystore/contract_test.go; this composed test asserts
// outcomes, never lock order.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	continuitybunstore "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

const (
	// inflightExtractLabel and inflightConsolidateLabel are distinct projects
	// of one principal, so each scenario forgets its own bank and its own
	// fence rows stay scoped to its own A-legs.
	inflightExtractLabel     = "/w/inflight-extract"
	inflightConsolidateLabel = "/w/inflight-consolidate"

	// inflightPublishedSummary is the pre-forget published content of the
	// consolidation scenario's bank: the stale in-flight publication would
	// carry the model's fresh summary, and the erased publication must not
	// come back either way.
	inflightPublishedSummary = "pre-forget published summary"

	// inflightSeedOwner is the lease owner the fixture uses for its own claims
	// and completions. It is never a generation lease owner, so it can never
	// collide with the worker's claim.
	inflightSeedOwner = "inflight-fixture"

	// inflightPhase1Role and inflightPhase2Role mirror the auxiliary request
	// roles of the two worker phases (unexported in the feature package). The
	// gated client answers by role, so a role rename fails the test loudly
	// instead of silently gating the wrong phase.
	inflightPhase1Role = "proxy_memory_phase1"
	inflightPhase2Role = "proxy_memory_phase2"

	// inflightWaitTimeout bounds every channel handshake, so a missing phase
	// fails the test instead of hanging the suite. The channels themselves are
	// the synchronization; this is only a failure bound.
	inflightWaitTimeout = 10 * time.Second

	// inflightPostgresPoolSize is the per-pool connection budget of the
	// PostgreSQL run: the worker holds one claim while the test reads the
	// other side.
	inflightPostgresPoolSize = 4
)

// inflightPhase1Reply and inflightPhase2Reply satisfy the strict parsers of
// both phases: phase 1 carries non-blank raw_memory/rollout_summary and a
// normalized slug, phase 2 stays far below the 256 KiB memory_md bound. All
// content is benign plain text.
const (
	inflightPhase1Reply = `{"raw_memory":"inflight raw memory reply","rollout_summary":"inflight rollout summary reply","rollout_slug":"inflight-forget-notes"}`
	inflightPhase2Reply = `{"memory_md":"# Memory\ninflight forget notes","memory_summary":"` + inflightPublishedSummary + `"}`
)

// SQL read models of the composed assertions. Each statement is owner-scoped
// like the production queries; the fixture values are arguments, never
// interpolated identifiers.
const (
	inflightOwnerSessionsSQL = `SELECT count(*) FROM proxy_memory_sessions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	inflightLeasedSessionsSQL = `SELECT count(*) FROM proxy_memory_sessions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND lease_owner <> ''`
	inflightOwnerExtractionsSQL = `SELECT count(*) FROM proxy_memory_extractions
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	inflightOwnerBanksSQL = `SELECT count(*) FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	inflightLeasedBanksSQL = `SELECT count(*) FROM proxy_memory_banks
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ? AND lease_owner <> ''`
	inflightOwnerNotesSQL = `SELECT count(*) FROM proxy_memory_notes
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	inflightOwnerLedgerSQL = `SELECT count(*) FROM proxy_memory_extraction_ledger
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	inflightErasureEpochSQL = `SELECT epoch FROM proxy_memory_erasure_epochs
		WHERE tenant_id = ? AND principal_id = ? AND project_key = ?`
	inflightFenceRowsSQL      = `SELECT count(*) FROM proxy_memory_fence WHERE a_leg_id = ?`
	inflightBindingRowsSQL    = `SELECT count(*) FROM proxy_memory_session_bindings WHERE a_leg_id = ?`
	inflightBindingContentSQL = `SELECT project_label, first_turn_outcome, first_turn_injection,
		first_turn_summary_version, first_turn_epoch
		FROM proxy_memory_session_bindings WHERE a_leg_id = ?`
)

// inflightGateState is one gated collection: entered signals once the gated
// phase reached the client, release unblocks it, and the context bounds both
// waits so a canceled worker never leaks the gate.
type inflightGateState struct {
	role    string
	entered chan string
	release chan struct{}
	once    sync.Once
}

func (g *inflightGateState) releaseGate() {
	if g != nil {
		g.once.Do(func() { close(g.release) })
	}
}

// inflightAux records the phase of every submission and answers the matching
// canned reply. The installed gate blocks exactly one phase, so the worker's
// claim is held for the duration of the test's forget.
type inflightAux struct {
	mu    sync.Mutex
	next  int
	gate  *inflightGateState
	roles map[auxiliary.JobID]string
}

func newInflightAux() *inflightAux {
	return &inflightAux{roles: make(map[auxiliary.JobID]string)}
}

// installGate arms the client to block the given phase on its next collection.
func (c *inflightAux) installGate(role string) *inflightGateState {
	gate := &inflightGateState{
		role:    role,
		entered: make(chan string, 1),
		release: make(chan struct{}),
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gate = gate
	return gate
}

func (c *inflightAux) SubmitCollect(_ context.Context, req auxiliary.Request, _ auxiliary.SubmitOptions) (auxiliary.JobID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := auxiliary.JobID("inflight-job-" + strconv.Itoa(c.next))
	c.roles[id] = req.Role
	return id, nil
}

func (c *inflightAux) Await(ctx context.Context, id auxiliary.JobID) (aipapi.Collected, error) {
	c.mu.Lock()
	role, gate := c.roles[id], c.gate
	c.mu.Unlock()
	if gate != nil && gate.role == role {
		select {
		case gate.entered <- role:
		case <-ctx.Done():
			return aipapi.Collected{}, ctx.Err()
		}
		select {
		case <-gate.release:
		case <-ctx.Done():
			return aipapi.Collected{}, ctx.Err()
		}
	}
	var text string
	switch role {
	case inflightPhase1Role:
		text = inflightPhase1Reply
	case inflightPhase2Role:
		text = inflightPhase2Reply
	default:
		return aipapi.Collected{}, fmt.Errorf("inflight-forget: unexpected auxiliary request role %q", role)
	}
	var collected aipapi.Collected
	collected.Text.WriteString(text)
	return collected, nil
}

func (c *inflightAux) Forget(auxiliary.JobID) {}

// inflightFixture is one composed generation shared by both scenarios. It
// carries no *testing.T: every method takes the owning test so a failure
// belongs to the subtest that observed it.
type inflightFixture struct {
	clock      *sentinelClock
	continuity *continuitybunstore.Store
	store      *proxymemorystore.Store
	worker     *proxymemory.Worker
	handler    http.Handler
	aux        *inflightAux
	registry   *prometheus.Registry
	db         *bun.DB
}

// runProxyMemoryInflightForgetFencing builds the composed generation over the
// supplied durable pools and runs the extraction and consolidation fencing
// scenarios sequentially against it.
func runProxyMemoryInflightForgetFencing(t *testing.T, continuityDB, memoryDB *bun.DB) {
	t.Helper()
	ctx := context.Background()
	clock := newSentinelClock()
	aux := newInflightAux()
	registry := prometheus.NewRegistry()

	continuity, err := continuitybunstore.New(continuityDB)
	require.NoError(t, err)
	rt, err := NewProcess(ctx, ProcessInput{
		Logger:           slog.Default(),
		BunDB:            continuityDB,
		ContinuityStore:  continuity,
		MetricsRegistry:  registry,
		FeatureDatabases: &proxyMemoryTestDatabases{database: memoryDB, identity: "inflight-forget-memory"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })

	var worker *proxymemory.Worker
	previousHook := proxyMemoryWorkerHook
	proxyMemoryWorkerHook = func(w *proxymemory.Worker) { worker = w }
	t.Cleanup(func() { proxyMemoryWorkerHook = previousHook })

	out, err := rt.CompileGeneration(ctx, GenerationInput{
		Registrations:    sentinelRegistrations(t),
		BackgroundClient: aux,
		NowFn:            clock.Now,
	})
	require.NoError(t, err)
	require.NotNil(t, out.HostProjections.ProxyMemoryHTTP,
		"the composed generation must project the memory control surface")
	require.NotNil(t, worker, "the worker hook must capture the composed worker")

	// Start publishes the generation's lease owner, so every claim in this
	// test carries a real lease. The first automatic tick is a full
	// PollInterval away, so all work below happens exactly when the test
	// asks for it.
	require.NoError(t, worker.Start(context.Background()))
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), inflightWaitTimeout)
		defer stopCancel()
		if err := worker.Stop(stopCtx); err != nil {
			t.Errorf("stop composed worker: %v", err)
		}
	})

	f := &inflightFixture{
		clock:      clock,
		continuity: continuity,
		store:      proxymemorystore.New(memoryDB, proxymemorystore.NewALegReader(continuityDB)),
		worker:     worker,
		handler:    out.HostProjections.ProxyMemoryHTTP,
		aux:        aux,
		registry:   registry,
		db:         memoryDB,
	}
	t.Run("extraction claim stops at forget", f.extractionClaimStopsAtForget)
	t.Run("consolidation claim cannot republish after forget", f.consolidationClaimCannotRepublish)
}

// ownerOf derives the owner triple the capture plane would derive for label.
func (f *inflightFixture) ownerOf(label string) proxymemory.Owner {
	return proxymemory.Owner{
		TenantID:    sentinelTenant,
		PrincipalID: sentinelPrincipalA,
		ProjectKey:  proxymemory.ProjectKey(proxymemory.NormalizePath(label)),
	}
}

// seedSession creates one active A-leg with its immutable first-turn binding
// and stores its captured session, exactly as the capture plane does, so the
// worker has one claimable session of the owner.
func (f *inflightFixture) seedSession(ctx context.Context, t *testing.T, owner proxymemory.Owner, label string, activityMS int64) string {
	t.Helper()
	leg, err := f.continuity.CreateALeg(ctx, "")
	require.NoError(t, err)
	committed, err := f.store.CommitFirstTurn(ctx, proxymemory.FirstTurnJob{
		Principal:  proxymemory.Principal{TenantID: owner.TenantID, PrincipalID: owner.PrincipalID},
		ALegID:     leg.ALegID,
		Project:    proxymemory.Project{Key: owner.ProjectKey, Label: label},
		Decision:   proxymemory.FirstTurnDecision{Outcome: proxymemory.FirstTurnNoSummary},
		EpochKnown: true,
	})
	require.NoError(t, err)
	require.True(t, committed, "the first-turn decision must persist before a capture can be stored")
	outcome, err := f.store.UpsertSession(ctx, proxymemory.SessionCapture{
		Owner:        owner,
		ProjectLabel: label,
		ALegID:       leg.ALegID,
		Transcript:   `[{"type":"message","role":"user","text":"inflight forget fencing"}]`,
		ActivityMS:   activityMS,
		SourceSeq:    1,
	})
	require.NoError(t, err)
	require.Equal(t, proxymemory.UpsertStored, outcome)
	return leg.ALegID
}

// claimAndComplete extracts one seeded session through the store's claim and
// completion contract, which leaves the owner's bank dirty and deletes the
// session row.
func (f *inflightFixture) claimAndComplete(ctx context.Context, t *testing.T, owner proxymemory.Owner, aLegID string, nowMS int64) proxymemory.Extraction {
	t.Helper()
	now := time.UnixMilli(nowMS).UTC()
	claimed, err := f.store.ClaimSessions(ctx, proxymemory.ClaimQuery{
		NowMS:        now.UnixMilli(),
		IdleBeforeMS: now.Add(-time.Hour).UnixMilli(),
		NotBeforeMS:  now.Add(-240 * time.Hour).UnixMilli(),
		LeaseUntilMS: now.Add(time.Hour).UnixMilli(),
		LeaseOwner:   inflightSeedOwner,
		Limit:        1,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1, "the fixture must claim its seeded session")
	require.Equal(t, aLegID, claimed[0].ALegID)
	extraction := proxymemory.Extraction{
		Revision:        claimed[0].Revision,
		Owner:           owner,
		ALegID:          aLegID,
		SessionRef:      proxymemory.SessionRef(aLegID),
		RawMemory:       "inflight raw memory",
		RolloutSummary:  "inflight rollout summary",
		RolloutSlug:     "inflight-slug",
		SourceUpdatedMS: claimed[0].LastActivityMS,
		GeneratedAtMS:   now.UnixMilli(),
	}
	require.NoError(t, f.store.CompleteSession(ctx, claimed[0], extraction))
	return extraction
}

// seedPublishedBank stores one extraction, publishes it, and then dirties the
// bank again with a second extraction, so the worker's in-flight consolidation
// claim is a re-consolidation of an already published bank.
func (f *inflightFixture) seedPublishedBank(ctx context.Context, t *testing.T, owner proxymemory.Owner, label string) string {
	t.Helper()
	nowMS := f.clock.Now().UnixMilli()
	now := time.UnixMilli(nowMS).UTC()
	activityMS := now.Add(-7 * time.Hour).UnixMilli()
	firstLeg := f.seedSession(ctx, t, owner, label, activityMS)
	first := f.claimAndComplete(ctx, t, owner, firstLeg, nowMS)

	claimed, err := f.store.ClaimBanks(ctx, proxymemory.ClaimQuery{
		NowMS:        nowMS,
		LeaseUntilMS: now.Add(time.Hour).UnixMilli(),
		LeaseOwner:   inflightSeedOwner,
		Limit:        1,
	})
	require.NoError(t, err)
	require.Len(t, claimed, 1, "the completed extraction must make the bank claimable")
	require.NoError(t, f.store.PublishBank(ctx, claimed[0], proxymemory.Publication{
		MemoryMD:          "# Memory",
		MemorySummary:     inflightPublishedSummary,
		ConsolidatedNames: []string{proxymemory.SummaryName(first)},
		Acknowledged: []proxymemory.ExtractionRef{{
			ALegID:        first.ALegID,
			Revision:      first.Revision,
			GeneratedAtMS: first.GeneratedAtMS,
		}},
		WindowSinceMS: now.Add(-720 * time.Hour).UnixMilli(),
		PublishedAtMS: nowMS,
	}))
	summary, _, err := f.store.PublishedSummary(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, inflightPublishedSummary, summary)

	// One pending note exists before the forget, so the scenario's "no note may
	// survive" assertion proves the erase instead of a state that never existed.
	require.NoError(t, f.store.AddNote(ctx, owner, proxymemory.Note{
		NoteID:      "inflight-prereserved-note",
		Text:        "inflight pending note",
		CreatedAtMS: nowMS,
	}))

	secondLeg := f.seedSession(ctx, t, owner, label, activityMS)
	f.claimAndComplete(ctx, t, owner, secondLeg, nowMS)
	return secondLeg
}

// startTick drives one worker iteration in its own goroutine and returns the
// gated phase's entry signal, the one-shot release and the completion signal.
func (f *inflightFixture) startTick(ctx context.Context, role string) (entered <-chan string, release func(), done <-chan struct{}) {
	gate := f.aux.installGate(role)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		f.worker.Tick(ctx)
	}()
	return gate.entered, gate.releaseGate, doneCh
}

// awaitInflightEntry blocks until the worker reached the gated collection,
// which is only possible after its claim committed.
func awaitInflightEntry(t *testing.T, entered <-chan string, role string) {
	t.Helper()
	select {
	case got := <-entered:
		require.Equal(t, role, got, "the gated phase must reach its collection")
	case <-time.After(inflightWaitTimeout):
		t.Fatalf("the worker never reached the %s collection", role)
	}
}

// awaitTickJoin joins one worker iteration or fails the test after the bound.
func awaitTickJoin(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(inflightWaitTimeout):
		t.Fatal("the worker tick did not return after the gate was released")
	}
}

// releaseAndJoin releases one gated tick and joins it exactly once.
func releaseAndJoin(t *testing.T, release func(), done <-chan struct{}) {
	t.Helper()
	release()
	awaitTickJoin(t, done)
}

// inflightOwnerCount reads one owner-scoped row count through query.
func inflightOwnerCount(t *testing.T, db *bun.DB, query string, owner proxymemory.Owner) int {
	t.Helper()
	var count int
	require.NoError(t, db.NewRaw(query, owner.TenantID, owner.PrincipalID, owner.ProjectKey).
		Scan(context.Background(), &count))
	return count
}

// inflightErasureEpoch reads the owner's durable erasure epoch.
func inflightErasureEpoch(t *testing.T, db *bun.DB, owner proxymemory.Owner) int64 {
	t.Helper()
	var epoch int64
	require.NoError(t, db.NewRaw(inflightErasureEpochSQL, owner.TenantID, owner.PrincipalID, owner.ProjectKey).
		Scan(context.Background(), &epoch))
	return epoch
}

// inflightFenceRows reads the fence rows that keep one A-leg vanished.
func inflightFenceRows(t *testing.T, db *bun.DB, aLegID string) int {
	t.Helper()
	var count int
	require.NoError(t, db.NewRaw(inflightFenceRowsSQL, aLegID).Scan(context.Background(), &count))
	return count
}

// inflightBindingRows reads the retained binding identity rows of one A-leg.
func inflightBindingRows(t *testing.T, db *bun.DB, aLegID string) int {
	t.Helper()
	var count int
	require.NoError(t, db.NewRaw(inflightBindingRowsSQL, aLegID).Scan(context.Background(), &count))
	return count
}

// requireInflightSessionStateErased states what a forget must remove from the
// extraction scenario's pre-forget state: the claimed session and the
// capture/claim columns that belong to it. The scenario never creates a bank,
// note or extraction before the forget, so an assertion for those rows would
// hold with or without the forget and prove nothing.
func requireInflightSessionStateErased(t *testing.T, db *bun.DB, owner proxymemory.Owner) {
	t.Helper()
	require.Zero(t, inflightOwnerCount(t, db, inflightOwnerSessionsSQL, owner), "no session may survive the forget")
	require.Zero(t, inflightOwnerCount(t, db, inflightOwnerExtractionsSQL, owner),
		"the released stale completion must not store an extraction")
	require.Zero(t, inflightOwnerCount(t, db, inflightLeasedSessionsSQL, owner),
		"the released stale completion must not keep a session lease")
}

// requireInflightPublishedStateErased states what a forget must remove from
// the consolidation scenario's pre-forget state: the published bank, the
// pending note, the two extractions and the extraction-identity ledger row all
// exist before the forget, so each zero below is evidence of the erasure.
func requireInflightPublishedStateErased(t *testing.T, db *bun.DB, owner proxymemory.Owner) {
	t.Helper()
	require.Zero(t, inflightOwnerCount(t, db, inflightOwnerBanksSQL, owner), "no bank may survive the forget")
	require.Zero(t, inflightOwnerCount(t, db, inflightOwnerNotesSQL, owner), "no pending note may survive the forget")
	require.Zero(t, inflightOwnerCount(t, db, inflightOwnerExtractionsSQL, owner), "no extraction may survive the forget")
	require.Zero(t, inflightOwnerCount(t, db, inflightOwnerLedgerSQL, owner), "no extraction identity may survive the forget")
}

// requireInflightBindingScrubbed asserts the retained binding keeps its
// content-free identity row while every remembered field is cleared, so an
// erased A-leg still carries its fence without carrying the owner's content.
func requireInflightBindingScrubbed(t *testing.T, db *bun.DB, aLegID string) {
	t.Helper()
	require.Equal(t, 1, inflightFenceRows(t, db, aLegID), "the erased A-leg must stay fenced")
	require.Equal(t, 1, inflightBindingRows(t, db, aLegID),
		"the erased A-leg keeps its content-free binding identity")
	var label, outcome, injection string
	var version, epoch int64
	require.NoError(t, db.NewRaw(inflightBindingContentSQL, aLegID).
		Scan(context.Background(), &label, &outcome, &injection, &version, &epoch))
	require.Empty(t, label, "the project label is remembered content")
	require.Empty(t, outcome, "the frozen first-turn outcome is remembered content")
	require.Empty(t, injection, "the frozen first-turn injection is remembered content")
	require.Zero(t, version)
	require.Zero(t, epoch)
}

// requireInflightErasedSurface asserts the composed read surface reports the
// owner's forgotten scope as absent.
func requireInflightErasedSurface(t *testing.T, f *inflightFixture, owner proxymemory.Owner) {
	t.Helper()
	require.Equal(t, http.StatusNotFound,
		sentinelMemoryStatus(t, f.handler, sentinelScope(sentinelPrincipalA), owner.ProjectKey),
		"a forgotten scope is not found to its owner either")
	require.Empty(t, sentinelListBanks(t, f.handler, sentinelScope(sentinelPrincipalA)),
		"a forgotten scope must not be listed")
	_, _, err := f.store.PublishedSummary(context.Background(), owner)
	require.ErrorIs(t, err, proxymemory.ErrNotFound)
}

// inflightEventValue reads one counter before a scenario and returns a closure
// that reads its growth afterwards.
func inflightEventValue(t *testing.T, registry *prometheus.Registry, event string) func() float64 {
	t.Helper()
	baseline := proxyMemoryCounterValue(t, registry, event)
	return func() float64 { return proxyMemoryCounterValue(t, registry, event) - baseline }
}

// extractionClaimStopsAtForget is the phase 1 scenario: the worker claims the
// session and is blocked in its extraction collection; the HTTP forget erases
// the claimed session; the released completion loses its lease, no extraction
// or bank is re-created, and neither a further tick nor a fresh capture can
// bring the erased state back.
func (f *inflightFixture) extractionClaimStopsAtForget(t *testing.T) {
	ctx := t.Context()
	owner := f.ownerOf(inflightExtractLabel)
	activityMS := f.clock.Now().Add(-7 * time.Hour).UnixMilli()
	aLeg := f.seedSession(ctx, t, owner, inflightExtractLabel, activityMS)

	phase1Succeeded := inflightEventValue(t, f.registry, proxymemory.EventPhase1Succeeded)
	phase1Failed := inflightEventValue(t, f.registry, proxymemory.EventPhase1Failed)
	phase1LeaseLost := inflightEventValue(t, f.registry, proxymemory.EventPhase1LeaseLost)
	controlForget := inflightEventValue(t, f.registry, proxymemory.EventControlForget)

	entered, release, done := f.startTick(ctx, inflightPhase1Role)
	t.Cleanup(func() { releaseAndJoin(t, release, done) })
	awaitInflightEntry(t, entered, inflightPhase1Role)

	// Pre-forget state: exactly the one claimed session and its binding exist,
	// and no extraction has been produced yet. These are the counts the
	// post-forget assertions below drive to zero; stating them first is what
	// makes those zeros evidence instead of tautology. The scenario has no
	// bank, note or extraction identity to assert about afterwards: asserting
	// them here would only restate that they never existed.
	require.Equal(t, 1, inflightOwnerCount(t, f.db, inflightOwnerSessionsSQL, owner),
		"the scenario must start with one captured session")
	require.Equal(t, 1, inflightOwnerCount(t, f.db, inflightLeasedSessionsSQL, owner),
		"the tick must hold the generation's session lease before the forget")
	require.Equal(t, 1, inflightBindingRows(t, f.db, aLeg),
		"the scenario must start with the A-leg's first-turn binding")
	require.Zero(t, inflightOwnerCount(t, f.db, inflightOwnerExtractionsSQL, owner),
		"the extraction must not have completed before the forget")

	forget := sentinelMemoryRequest(f.handler, http.MethodDelete, "/v1/aip/memory/"+owner.ProjectKey, sentinelScope(sentinelPrincipalA))
	require.Equal(t, http.StatusNoContent, forget.Code, "body: %s", forget.Body.Bytes())

	// The forget wins while the claim is held: the claimed row and every
	// artifact are already gone before the worker is released.
	requireInflightSessionStateErased(t, f.db, owner)
	require.Equal(t, int64(1), inflightErasureEpoch(t, f.db, owner),
		"the forget must advance the owner's erasure epoch")

	releaseAndJoin(t, release, done)

	// The released stale completion cannot recreate the erased state.
	requireInflightSessionStateErased(t, f.db, owner)
	requireInflightBindingScrubbed(t, f.db, aLeg)
	requireInflightErasedSurface(t, f, owner)
	require.Equal(t, 1.0, phase1LeaseLost(), "a completion behind the forget is a lost lease, not a success")
	require.Zero(t, phase1Succeeded())
	require.Zero(t, phase1Failed())
	require.Equal(t, 1.0, controlForget())

	// A further tick finds nothing to claim and keeps the erasure intact.
	f.worker.Tick(ctx)
	requireInflightSessionStateErased(t, f.db, owner)
	require.Zero(t, phase1Succeeded())

	// The capture plane cannot recreate the fenced A-leg either: a capture that
	// arrives after the forget is refused instead of authorizing new work.
	// The capture carries the epoch the request path would observe now, so the
	// refusal is attributable to the durable fence and not to a stale epoch.
	outcome, err := f.store.UpsertSession(ctx, proxymemory.SessionCapture{
		Owner:        owner,
		ProjectLabel: inflightExtractLabel,
		ALegID:       aLeg,
		Transcript:   `[{"type":"message","role":"user","text":"after forget"}]`,
		ActivityMS:   f.clock.Now().Add(-7 * time.Hour).UnixMilli(),
		SourceSeq:    2,
		ErasureEpoch: inflightErasureEpoch(t, f.db, owner),
	})
	require.NoError(t, err)
	require.Equal(t, proxymemory.UpsertForgotten, outcome,
		"a post-forget capture must be refused by the durable fence")
	requireInflightSessionStateErased(t, f.db, owner)
	require.Zero(t, phase1Succeeded())
}

// consolidationClaimCannotRepublish is the phase 2 scenario: the worker claims
// a dirty bank that already published a summary, reads its pre-forget
// consolidation input and is blocked in the model collection; the HTTP forget
// erases the bank and the extractions; the released publication loses its
// version compare-and-set and the erased bank does not come back.
func (f *inflightFixture) consolidationClaimCannotRepublish(t *testing.T) {
	ctx := t.Context()
	owner := f.ownerOf(inflightConsolidateLabel)
	aLeg := f.seedPublishedBank(ctx, t, owner, inflightConsolidateLabel)

	phase2Published := inflightEventValue(t, f.registry, proxymemory.EventPhase2Published)
	phase2Failed := inflightEventValue(t, f.registry, proxymemory.EventPhase2Failed)
	phase2LeaseLost := inflightEventValue(t, f.registry, proxymemory.EventPhase2LeaseLost)
	controlForget := inflightEventValue(t, f.registry, proxymemory.EventControlForget)

	entered, release, done := f.startTick(ctx, inflightPhase2Role)
	t.Cleanup(func() { releaseAndJoin(t, release, done) })
	awaitInflightEntry(t, entered, inflightPhase2Role)

	// Pre-forget state: exactly the artifacts the forget must erase exist, so
	// each post-forget zero below is evidence rather than a tautology.
	require.Equal(t, 1, inflightOwnerCount(t, f.db, inflightOwnerBanksSQL, owner),
		"the scenario must start with the published bank")
	require.Equal(t, 1, inflightOwnerCount(t, f.db, inflightLeasedBanksSQL, owner),
		"the tick must hold the generation's bank lease before the forget")
	require.Equal(t, 2, inflightOwnerCount(t, f.db, inflightOwnerExtractionsSQL, owner),
		"the in-flight input must be the pre-forget extraction set")
	require.Equal(t, 1, inflightOwnerCount(t, f.db, inflightOwnerNotesSQL, owner),
		"the scenario must start with the pending note")
	require.Equal(t, 2, inflightOwnerCount(t, f.db, inflightOwnerLedgerSQL, owner),
		"the scenario must start with the extraction identities of both A-legs")
	summary, _, err := f.store.PublishedSummary(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, inflightPublishedSummary, summary, "the in-flight claim reads the pre-forget publication")

	forget := sentinelMemoryRequest(f.handler, http.MethodDelete, "/v1/aip/memory/"+owner.ProjectKey, sentinelScope(sentinelPrincipalA))
	require.Equal(t, http.StatusNoContent, forget.Code, "body: %s", forget.Body.Bytes())

	requireInflightPublishedStateErased(t, f.db, owner)
	require.Equal(t, int64(1), inflightErasureEpoch(t, f.db, owner))

	releaseAndJoin(t, release, done)

	// The released publication must not resurrect the bank or its documents.
	requireInflightPublishedStateErased(t, f.db, owner)
	requireInflightBindingScrubbed(t, f.db, aLeg)
	requireInflightErasedSurface(t, f, owner)
	require.Equal(t, 1.0, phase2LeaseLost(), "the publication behind the forget is a lost lease")
	require.Zero(t, phase2Published(),
		"the stale claim must not publish a replacement document set")
	require.Zero(t, phase2Failed())
	require.Equal(t, 1.0, controlForget())

	// Neither a further tick nor a new capture may rebuild the erased bank.
	f.worker.Tick(ctx)
	requireInflightPublishedStateErased(t, f.db, owner)
	require.Zero(t, phase2Published())
	outcome, err := f.store.UpsertSession(ctx, proxymemory.SessionCapture{
		Owner:        owner,
		ProjectLabel: inflightConsolidateLabel,
		ALegID:       aLeg,
		Transcript:   `[{"type":"message","role":"user","text":"after forget"}]`,
		ActivityMS:   f.clock.Now().Add(-7 * time.Hour).UnixMilli(),
		SourceSeq:    2,
		ErasureEpoch: inflightErasureEpoch(t, f.db, owner),
	})
	require.NoError(t, err)
	require.Equal(t, proxymemory.UpsertForgotten, outcome,
		"a post-forget capture must be refused by the durable fence")
	requireInflightPublishedStateErased(t, f.db, owner)
	require.Zero(t, phase2Published())
}

// TestProxyMemoryInflightForgetFencing is the default composed topology:
// separate durable SQLite databases for continuity and the isolated memory
// store, as the production composition does.
func TestProxyMemoryInflightForgetFencing(t *testing.T) {
	continuityDB := certificationOpenSQLite(t,
		certificationSQLiteDSN(filepath.Join(t.TempDir(), "inflight-forget-continuity.db")))
	memoryDB := certificationOpenSQLite(t,
		certificationSQLiteDSN(filepath.Join(t.TempDir(), "inflight-forget-memory.db")))
	runProxyMemoryInflightForgetFencing(t, continuityDB, memoryDB)
}
