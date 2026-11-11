//go:build integration

package featurehost

// PostgreSQL frozen-overlay replay acceptance for proxy memory V1 (issue #793).
//
// This is a composed acceptance run, not a unit test. It drives the real
// PostgreSQL topology through production code only:
//
//   - real featurehost processes over real PostgreSQL continuity pools and the
//     production isolated-feature-database opener, so the proxy-memory store
//     owns its own dedicated pool inside one isolated schema;
//   - real compiled generations (`CompileGeneration`) and a real generic-core
//     executor with the real secure-session manager, so every turn below is an
//     authenticated request that resumes through a proxy-issued resume proof;
//   - the composed capture writer (the generation's lifecycle) persists the
//     frozen first-turn decision into PostgreSQL;
//   - a recording stub backend, so the assertions observe exactly the
//     backend-bound canonical request.
//
// Fixture ownership: the isolated-schema fixture is shared with the composed
// PostgreSQL sentinel (PR #1778): `proxyMemoryPostgresSchema` creates one
// bounded disposable schema and scopes the DSN, and `proxyMemoryPostgresPool`
// opens each owned pool and proves it resolves that schema. This file therefore
// owns only its scenario-specific pieces: the stub-backed composed wiring, the
// seeded V1/V2 publications and the direct durable-state readers.
//
// It also reuses the package-local composed sentinel fixtures from
// proxy_memory_sentinel_test.go and proxy_memory_test.go (injected clock,
// recording backend, canned auxiliary client, registration, authenticated
// scope, turn builders, overlay readers, executor wiring, store seeder,
// capture-transform finder, counter reader), exactly as proxy_memory_test.go's
// backend fixture already does. It touches no other test file.
//
// The bounded scenario:
//
//  1. V1 is published through the real store path
//     (CommitFirstTurn/ClaimSessions/CompleteSession/ClaimBanks/PublishBank).
//     Extraction and consolidation model calls are not part of this slice;
//  2. an authoritative first turn freezes the V1 injection and persists it;
//  3. V2 is published through the same store path, so the current bank
//     publishes a different summary and a different version;
//  4. a second generation compiles in the same process (a real generation
//     reload): the proxy-issued resume proof alone continues the same A-leg and
//     resends the exact frozen V1, never V2;
//  5. the process then restarts over the same schema: the process-owned replay
//     cache is gone, so the same proof must be served from the durable
//     PostgreSQL binding — same secure session, same A-leg, same exact V1;
//  6. a fresh eligible session first turn in the restarted process receives
//     the current V2;
//  7. a first turn with no derivable project commits the explicit no_project
//     decision, injects nothing and captures nothing.
//
// Mutation sensitivity is intentionally NOT claimed: the fresh-bank and
// process-cache paths keep several assertions green even when the durable
// read is broken, so only the post-restart replay assertion is pinned to the
// durable PostgreSQL binding.
//
// The preexisting-search_path guard is owned by the shared fixture, not
// re-tested here; see the discrepancy note in the PR body: the fixture refuses
// `search_path` in any spelling and value including an empty one, but it does
// not refuse the libpq `options=-csearch_path=…` spelling, which the
// repository's pgdriver path rejects at pool open anyway ("unrecognized
// configuration parameter options", SQLSTATE 42704).
//
// It is stub-backed and bounded: it is not a live-provider test, not a
// native-CLI session-continuity certificate, and it does not exercise
// extraction or consolidation model calls.

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	continuitybunstore "github.com/aiproxer/aiproxer/internal/core/continuity/bunstore"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

const (
	// Distinct, non-overlapping summaries: a decision re-derived from the
	// current bank is observable as V2 text reaching the resumed request.
	proxyMemoryReplayV1Summary = "kale standup summary version one"
	proxyMemoryReplayV2Summary = "parrot standup summary version two"

	proxyMemoryReplaySession   = "proxy-memory-replay-session"
	proxyMemoryReplayFresh     = "proxy-memory-replay-fresh"
	proxyMemoryReplayNoProject = "proxy-memory-replay-no-project"
)

// TestProxyMemoryFrozenReplay_PostgresDirect certifies the PostgreSQL half of
// issue #793: a frozen first-turn overlay replays exactly across a generation
// reload and a process restart while the current bank moves on, and a fresh
// session uses the new bank. It fails closed when PostgreSQL is required but
// unavailable.
func TestProxyMemoryFrozenReplay_PostgresDirect(t *testing.T) {
	ctx := context.Background()
	// The isolated schema is the #1778 shared fixture: it validates the DSN
	// (URL-only, no preexisting search_path in any spelling), bounds the
	// create/probe/drop steps and names the schema from this run's prefix.
	scopedDSN, schema := proxyMemoryPostgresSchema(t, "pm_replay")
	owner := proxymemory.Owner{
		TenantID:    sentinelTenant,
		PrincipalID: sentinelPrincipalA,
		ProjectKey:  proxymemory.ProjectKey(proxymemory.NormalizePath(sentinelProjectPath)),
	}
	label := proxymemory.NormalizePath(sentinelProjectPath)
	clock := newSentinelClock()

	// ---- process 1: publish V1, freeze it on the authoritative first turn ----
	registry1 := prometheus.NewRegistry()
	rt1, continuityDB1, continuity1 := proxyMemoryReplayProcess(t, ctx, scopedDSN, schema, registry1)
	out1, captureLife1 := proxyMemoryReplayCompile(t, rt1, clock)
	require.NoError(t, captureLife1.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = captureLife1.Stop(stopCtx)
	})

	// The independent observer store reads through its own pool, so the
	// assertions observe committed state rather than composition-local caches.
	observerDB1 := proxyMemoryPostgresPool(t, scopedDSN, schema, "observer", 8)
	observerStore := proxymemorystore.New(observerDB1, proxymemorystore.NewALegReader(continuityDB1))

	seedV1, err := continuity1.CreateALeg(ctx, "")
	require.NoError(t, err)
	seedProxyMemorySummary(t, observerStore, owner, label, proxyMemoryReplayV1Summary,
		clock.Now().UnixMilli(), seedV1.ALegID)
	published, version, err := observerStore.PublishedSummary(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, proxyMemoryReplayV1Summary, published)
	require.Equal(t, int64(1), version, "the first publication must be bank version 1")

	backend1 := &sentinelBackend{}
	ex1 := sentinelExecutor(t, continuityDB1, out1, backend1)
	firstCall := sentinelCodexTurn(proxyMemoryReplaySession)
	firstBound := sentinelServe(t, ex1, sentinelScope(sentinelPrincipalA), firstCall, backend1)
	require.False(t, sentinelCallCarries(firstBound, proxyMemoryReplayV2Summary),
		"sanity: V2 does not exist yet")
	require.True(t, sentinelCallCarries(firstBound, proxyMemoryReplayV1Summary),
		"the authoritative first turn must freeze the published V1 injection")
	firstOverlay, ok := sentinelInstructionOverlay(firstBound, proxyMemoryReplayV1Summary)
	require.True(t, ok, "the frozen V1 injection must be a system instruction message")
	require.Equal(t, aipapi.RoleSystem, firstOverlay.Role)
	require.Len(t, firstOverlay.Parts, 1)
	require.True(t, sentinelCallCarries(firstBound, owner.ProjectKey),
		"the frozen injection must carry the opaque project scope identifier")
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry1, proxymemory.EventInjectMatched))

	firstALegID := firstCall.Session.ALegID
	require.NotEmpty(t, firstALegID, "the executor must bind the turn to a durable A-leg")
	require.NotEmpty(t, firstCall.Session.AuthoritativeSessionID,
		"the secure session layer must issue a proxy session identifier")
	require.NotEmpty(t, firstCall.Session.ResumeToken,
		"the secure session layer must issue a proxy resume proof on the first turn")

	// Retiring the generation drains its writer, so the frozen decision is
	// durable before the reload and the restart: this is the same stop path a
	// real generation retirement uses.
	proxyMemoryReplayStopCapture(t, captureLife1)
	require.Equal(t, 1, proxyMemoryReplayCapturedSessions(t, observerDB1, firstALegID),
		"the first turn's admitted decision must authorize its capture")
	binding := proxyMemoryReplayReadBinding(t, observerDB1, firstALegID)
	require.Equal(t, string(proxymemory.FirstTurnMatched), binding.Outcome)
	require.Equal(t, owner.ProjectKey, binding.ProjectKey)
	require.Equal(t, int64(1), binding.SummaryVersion)
	require.Equal(t, firstOverlay.Parts[0].Text, binding.Injection,
		"the durable decision must be the exact bytes the first turn sent to the model")

	// ---- V2 is published through the same real store path ----
	//
	// The seed advances the activity clock past the live session's captured
	// turn, so the seed can only claim its own fresh row: the shared owner
	// already holds a session for the first turn.
	seedV2, err := continuity1.CreateALeg(ctx, "")
	require.NoError(t, err)
	seedProxyMemorySummary(t, observerStore, owner, label, proxyMemoryReplayV2Summary,
		clock.Now().Add(2*time.Second).UnixMilli(), seedV2.ALegID)
	published, version, err = observerStore.PublishedSummary(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, proxyMemoryReplayV2Summary, published, "the current bank must move on to V2")
	require.Equal(t, int64(2), version, "V2 is the next bank version")

	// ---- generation reload in the same process: the resume proof replays V1 ----
	out2, captureLife2 := proxyMemoryReplayCompile(t, rt1, clock)
	require.NoError(t, captureLife2.Start(ctx))
	backend2 := &sentinelBackend{}
	ex2 := sentinelExecutor(t, continuityDB1, out2, backend2)

	resumedCall := sentinelCodexTurn(proxyMemoryReplaySession)
	resumedCall.Session.ResumeToken = firstCall.Session.ResumeToken
	resumedBound := sentinelServe(t, ex2, sentinelScope(sentinelPrincipalA), resumedCall, backend2)
	require.Equal(t, firstALegID, resumedCall.Session.ALegID,
		"the proxy-issued proof must resume the same A-leg, not allocate a new one")
	require.Equal(t, firstCall.Session.AuthoritativeSessionID, resumedCall.Session.AuthoritativeSessionID,
		"the resumed turn must continue the same proxy-issued secure session")
	resumedOverlay, ok := sentinelInstructionOverlay(resumedBound, proxyMemoryReplayV1Summary)
	require.True(t, ok, "the reloaded generation must replay the frozen V1 injection")
	require.Equal(t, firstOverlay, resumedOverlay,
		"the replay must be the exact frozen first-turn decision")
	require.False(t, sentinelCallCarries(resumedBound, proxyMemoryReplayV2Summary),
		"the resumed request must not be reconstructed from the newer bank snapshot")
	require.Equal(t, 2.0, proxyMemoryCounterValue(t, registry1, proxymemory.EventInjectMatched),
		"the replayed frozen decision is counted as its own injection")

	// Negative control of the proof itself: a forged token is a client-supplied
	// continuation value, not proxy authority, so it must be refused before any
	// backend work and must not allocate a fresh session.
	forged := sentinelCodexTurn(proxyMemoryReplaySession)
	forged.Session.ResumeToken = "forged-resume-proof"
	before := backend2.callCount()
	_, err = ex2.Execute(scope.WithScope(context.Background(), sentinelScope(sentinelPrincipalA)), forged)
	require.Error(t, err, "a forged resume proof must never continue a proxy session")
	require.Equal(t, before, backend2.callCount(), "a refused proof must never reach the backend")
	require.Empty(t, forged.Session.ALegID, "a refused proof must not allocate an A-leg")
	proxyMemoryReplayStopCapture(t, captureLife2)

	// ---- process restart: the replay state is gone, the durable binding is not ----
	require.NoError(t, rt1.Close())
	require.NoError(t, continuityDB1.Close())
	require.NoError(t, observerDB1.Close())

	registry3 := prometheus.NewRegistry()
	rt3, continuityDB3, _ := proxyMemoryReplayProcess(t, ctx, scopedDSN, schema, registry3)
	out3, captureLife3 := proxyMemoryReplayCompile(t, rt3, clock)
	require.NoError(t, captureLife3.Start(ctx))
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = captureLife3.Stop(stopCtx)
	})
	observerDB3 := proxyMemoryPostgresPool(t, scopedDSN, schema, "observer-restarted", 8)
	backend3 := &sentinelBackend{}
	ex3 := sentinelExecutor(t, continuityDB3, out3, backend3)

	// The restarted process has no process-owned replay cache, so the same
	// proxy-issued resume proof must be served from the durable PostgreSQL
	// binding, on the same secure session and the same A-leg.
	afterRestartCall := sentinelCodexTurn(proxyMemoryReplaySession)
	afterRestartCall.Session.ResumeToken = firstCall.Session.ResumeToken
	afterRestartBound := sentinelServe(t, ex3, sentinelScope(sentinelPrincipalA), afterRestartCall, backend3)
	require.Equal(t, firstALegID, afterRestartCall.Session.ALegID,
		"the resume proof must survive a process restart and resume the same A-leg")
	require.Equal(t, firstCall.Session.AuthoritativeSessionID, afterRestartCall.Session.AuthoritativeSessionID,
		"the proxy-issued secure session must survive the restart")
	afterRestartOverlay, ok := sentinelInstructionOverlay(afterRestartBound, proxyMemoryReplayV1Summary)
	require.True(t, ok, "the restarted process must replay V1 from the durable binding")
	require.Equal(t, firstOverlay, afterRestartOverlay,
		"the durable replay must be the exact frozen first-turn decision")
	require.False(t, sentinelCallCarries(afterRestartBound, proxyMemoryReplayV2Summary),
		"the restarted process must not reconstruct the decision from the current bank")
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry3, proxymemory.EventInjectMatched))
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry3, proxymemory.EventInjectNoSummary),
		"the durable replay is never re-frozen from the current bank")

	// ---- a fresh eligible session first turn uses the current V2 ----
	freshCall := sentinelCodexTurn(proxyMemoryReplayFresh)
	freshBound := sentinelServe(t, ex3, sentinelScope(sentinelPrincipalA), freshCall, backend3)
	require.NotEqual(t, firstALegID, freshCall.Session.ALegID,
		"a fresh session must allocate its own A-leg")
	require.True(t, sentinelCallCarries(freshBound, proxyMemoryReplayV2Summary),
		"a fresh first turn must receive the current published V2")
	require.False(t, sentinelCallCarries(freshBound, proxyMemoryReplayV1Summary),
		"a fresh first turn must not receive the older V1")
	freshOverlay, ok := sentinelInstructionOverlay(freshBound, proxyMemoryReplayV2Summary)
	require.True(t, ok, "the fresh V2 injection must be a system instruction message")
	require.Equal(t, aipapi.RoleSystem, freshOverlay.Role)
	require.Equal(t, 2.0, proxyMemoryCounterValue(t, registry3, proxymemory.EventInjectMatched))
	freshALegID := freshCall.Session.ALegID
	require.NotEmpty(t, freshALegID)
	require.NotEmpty(t, freshCall.Session.ResumeToken,
		"the fresh session must also receive a proxy-issued resume proof")

	// ---- first turn with no derivable project: the explicit negative ----
	noProjectCall := &aipapi.Call{
		Route:      aipapi.RouteIntent{Selector: "only:model"},
		Session:    aipapi.SessionRef{ClientSessionID: proxyMemoryReplayNoProject},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello there")},
		}},
	}
	noProjectBound := sentinelServe(t, ex3, sentinelScope(sentinelPrincipalA), noProjectCall, backend3)
	require.False(t, sentinelCallCarries(noProjectBound, proxyMemoryReplayV1Summary))
	require.False(t, sentinelCallCarries(noProjectBound, proxyMemoryReplayV2Summary))
	require.Empty(t, noProjectBound.Instructions,
		"a no-project first turn must not add a memory instruction")
	require.Equal(t, 1.0, proxyMemoryCounterValue(t, registry3, proxymemory.EventInjectNoProject))
	require.Equal(t, 0.0, proxyMemoryCounterValue(t, registry3, proxymemory.EventInjectNoSession))
	noProjectALegID := noProjectCall.Session.ALegID
	require.NotEmpty(t, noProjectALegID)

	// Draining the restarted generation commits the decisions of the fresh and
	// no-project first turns, so the assertions read durable PostgreSQL state.
	proxyMemoryReplayStopCapture(t, captureLife3)

	freshBinding := proxyMemoryReplayReadBinding(t, observerDB3, freshALegID)
	require.Equal(t, string(proxymemory.FirstTurnMatched), freshBinding.Outcome)
	require.Equal(t, int64(2), freshBinding.SummaryVersion)
	require.True(t, strings.Contains(freshBinding.Injection, proxyMemoryReplayV2Summary),
		"the fresh session's durable decision must freeze V2")
	require.False(t, strings.Contains(freshBinding.Injection, proxyMemoryReplayV1Summary))
	require.Equal(t, 1, proxyMemoryReplayCapturedSessions(t, observerDB3, freshALegID))

	noProjectBinding := proxyMemoryReplayReadBinding(t, observerDB3, noProjectALegID)
	require.Equal(t, string(proxymemory.FirstTurnNoProject), noProjectBinding.Outcome)
	require.Empty(t, noProjectBinding.ProjectKey, "a no-project decision carries no project")
	require.Empty(t, noProjectBinding.Injection, "a no-project decision injects nothing")
	require.Zero(t, noProjectBinding.SummaryVersion)
	require.Zero(t, proxyMemoryReplayCapturedSessions(t, observerDB3, noProjectALegID),
		"a no-project turn must not capture anything")

	// The frozen V1 binding is untouched by the reload, the restart and V2's
	// publication.
	after := proxyMemoryReplayReadBinding(t, observerDB3, firstALegID)
	require.Equal(t, binding, after, "the frozen first-turn decision must never be rewritten")
}

// proxyMemoryReplayProcess builds one real featurehost process the way the
// composition root does on PostgreSQL: the continuity pool plus the production
// isolated-feature-database opener, so the memory store owns its own pool.
func proxyMemoryReplayProcess(t *testing.T, ctx context.Context, scopedDSN, schema string, registry *prometheus.Registry) (*Runtime, *bun.DB, *continuitybunstore.Store) {
	t.Helper()
	continuityDB := proxyMemoryPostgresPool(t, scopedDSN, schema, "continuity", 8)
	continuity, err := continuitybunstore.NewWithContext(ctx, continuityDB)
	require.NoError(t, err)
	rt, err := NewProcess(ctx, ProcessInput{
		Logger:          slog.Default(),
		BunDB:           continuityDB,
		ContinuityStore: continuity,
		MetricsRegistry: registry,
		FeatureDatabases: db.NewFeatureDatabaseOpener(db.FeatureDatabaseConfig{
			Store:       "postgres",
			PostgresDSN: scopedDSN,
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close() })
	return rt, continuityDB, continuity
}

// proxyMemoryReplayCompile compiles one enabled proxy-memory generation over
// the process and returns it with its capture lifecycle, which the caller
// starts and drains.
func proxyMemoryReplayCompile(t *testing.T, rt *Runtime, clock *sentinelClock) (GenerationOutput, aipplugin.Lifecycle) {
	t.Helper()
	out, err := rt.CompileGeneration(context.Background(), GenerationInput{
		Registrations:    sentinelRegistrations(t),
		BackgroundClient: &sentinelAuxClient{},
		NowFn:            clock.Now,
	})
	require.NoError(t, err)
	require.NotNil(t, out.HostProjections.ProxyMemoryHTTP)
	require.GreaterOrEqual(t, len(out.Lifecycles), 2)
	require.NotNil(t, proxyMemoryCaptureTransform(out),
		"the enabled generation must carry the capture transform")
	return out, out.Lifecycles[len(out.Lifecycles)-2]
}

// proxyMemoryReplayStopCapture drains the generation's capture writer so the
// first-turn decisions it queued are durable before the assertions read them.
func proxyMemoryReplayStopCapture(t *testing.T, lifecycle aipplugin.Lifecycle) {
	t.Helper()
	stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	require.NoError(t, lifecycle.Stop(stopCtx))
}

type proxyMemoryReplayBinding struct {
	ProjectKey     string
	Outcome        string
	Injection      string
	SummaryVersion int64
}

// proxyMemoryReplayReadBinding reads the durable frozen first-turn decision of
// one A-leg directly from PostgreSQL.
func proxyMemoryReplayReadBinding(t *testing.T, database *bun.DB, aLegID string) proxyMemoryReplayBinding {
	t.Helper()
	var row proxyMemoryReplayBinding
	err := database.NewRaw(`SELECT project_key, first_turn_outcome, first_turn_injection,
		first_turn_summary_version
		FROM proxy_memory_session_bindings WHERE a_leg_id = ?`, aLegID).Scan(context.Background(),
		&row.ProjectKey, &row.Outcome, &row.Injection, &row.SummaryVersion)
	require.NoError(t, err, "read the durable first-turn binding of %s", aLegID)
	return row
}

// proxyMemoryReplayCapturedSessions counts the durable captured sessions of
// one A-leg.
func proxyMemoryReplayCapturedSessions(t *testing.T, database *bun.DB, aLegID string) int {
	t.Helper()
	var count int
	require.NoError(t, database.NewRaw(
		`SELECT count(*) FROM proxy_memory_sessions WHERE a_leg_id = ?`, aLegID).
		Scan(context.Background(), &count))
	return count
}
