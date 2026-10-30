package journalstore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// identityWorkRecorder counts the SQL statements a journal append actually
// issues, so identity resolution can be bounded as work rather than as
// elapsed time.
type identityWorkRecorder struct {
	mu      sync.Mutex
	queries []string
}

func (r *identityWorkRecorder) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (r *identityWorkRecorder) AfterQuery(_ context.Context, q *bun.QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, q.Query)
}

func (r *identityWorkRecorder) take() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	taken := r.queries
	r.queries = nil
	return taken
}

// TestObservationAppendResolvesDurableIdentityOnceAfterInsert pins the durable
// identity work an append performs.
//
// Resolving identity after the insert removes two lookups for fresh
// observations. This test pins that work bound without asserting elapsed
// time; the atomic batch and terminal budgets remain unchanged.
func TestObservationAppendResolvesDurableIdentityOnceAfterInsert(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)

	const total = 32
	batch := make([]metering.Observation, 0, total)
	for i := 1; i <= total; i++ {
		batch = append(batch, phase4Observation("sqlite-test", fmt.Sprintf("identity-work-%d", i), uint64(i)))
	}

	recorder := &identityWorkRecorder{}
	store.DB().AddQueryHook(recorder)
	require.NoError(t, store.AppendObservations(ctx, batch))
	queries := recorder.take()
	require.NotEmpty(t, queries, "expected the append to record SQL statements")

	// Per observation the append issues exactly one durable identity read -
	// after its own INSERT. A pre-insert read cannot find anything for a new
	// observation, so a second or third read is a pure miss that buys no
	// outcome and only multiplies the per-observation cost of the batch.
	identityReads := 0
	for _, query := range queries {
		if isDurableIdentityRead(query) {
			identityReads++
		}
	}
	require.Equalf(t, total, identityReads,
		"appending %d fresh observations issued %d durable identity reads, want exactly one per observation; "+
			"pre-insert identity lookups are pure misses for a new observation and must not run: %s",
		total, identityReads, summarizeIdentityWork(queries))

	// The bound is only meaningful if the durable outcome is unchanged: every
	// observation, and every projected component, is still written exactly once.
	page, err := store.ListObservations(ctx, ObservationQueryForPhase4(batch[0], total+8))
	require.NoError(t, err)
	require.Len(t, page.Observations, total)
	components, err := store.ListObservationComponents(ctx, structToComponentQuery(batch[0], total*4+8))
	require.NoError(t, err)
	require.Len(t, components.Components, total*4, "each observation projects its two measures and two charges exactly once")
}

// isDurableIdentityRead reports whether a statement reads an existing durable
// observation row by identity. It deliberately matches both identity forms -
// the source identity and the observation ID/revision fence - so restoring
// either pre-insert lookup is visible as extra work. Bun renders bound values
// inline, so the shapes are matched as literal SQL rather than placeholders.
func isDurableIdentityRead(query string) bool {
	normalized := strings.Join(strings.Fields(query), " ")
	return strings.HasPrefix(normalized, "SELECT") &&
		strings.Contains(normalized, "FROM metering_facts") &&
		(strings.Contains(normalized, "source_event_key = '") ||
			strings.Contains(normalized, "observation_revision = "))
}

// TestObservationAppendClassifiesSourceIdentityOccupiedByLegacyV1Fact guards
// the one behavior that depends on the durable identity read NOT filtering on
// payload_kind. A legacy V1 fact can already occupy the source identity; that
// is an identity collision, not a missing row, and must keep its own
// classification rather than degrading into a lookup failure.
func TestObservationAppendClassifiesSourceIdentityOccupiedByLegacyV1Fact(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase4Observation("sqlite-test", "legacy-v1-occupied", 1)

	// A legacy row is exactly a metering_facts row that was never lifted to the
	// additive observation projection, so payload_kind keeps its 'fact' default.
	_, err := store.DB().ExecContext(ctx, `
INSERT INTO metering_facts(store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope, recorded_at_unix, payload_json)
VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		observation.Subject.StoreID, "legacy-fact-1", observation.StreamID, 1,
		dbinfra.LegacyPersistedText(observation.SourceEventIdentity()), "delta", "operator", "backend_ingress",
		"backend_attempt", 0, `{}`)
	require.NoError(t, err, "seed legacy V1 fact occupying the observation source identity")

	err = store.AppendObservation(ctx, observation)
	require.ErrorIs(t, err, journalstore.ErrIdentityCollision)
	require.Contains(t, err.Error(), "occupied by a V1 fact",
		"a V1-occupied source identity must stay a typed identity collision, got %v", err)
}

// TestObservationAppendPrefersSourceIdentityOverRevisionFence pins the
// precedence between the two durable identities. A store can hold a row that
// matches the incoming source identity and, independently, a different row that
// matches its observation ID/revision fence. Source identity is resolved first,
// so the source row is the one that classifies the append, and the revision
// fence is only consulted when no source identity matches.
func TestObservationAppendPrefersSourceIdentityOverRevisionFence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)

	// alpha/1 owns source identity "key-a"; beta/1 owns "key-b". They are
	// independent rows: distinct observation identities, distinct source keys.
	sourceRow := phase4Observation("sqlite-test", "alpha", 1)
	sourceRow.SourceEventKey = "key-a"
	require.NoError(t, store.AppendObservation(ctx, sourceRow))
	revisionRow := phase4Observation("sqlite-test", "beta", 1)
	revisionRow.SourceEventKey = "key-b"
	require.NoError(t, store.AppendObservation(ctx, revisionRow))

	// Both identities now match: this observation carries beta/1 under the
	// source identity already held by alpha/1.
	incoming := phase4Observation("sqlite-test", "beta", 1)
	incoming.SourceEventKey = "key-a"
	err := store.AppendObservation(ctx, incoming)
	require.ErrorIs(t, err, journalstore.ErrIdentityCollision)
	require.Contains(t, err.Error(), "stored observation identity drift",
		"the source-identity row must decide the outcome, got %v", err)

	// Neither identity was disturbed by the rejected append.
	for _, want := range []metering.Observation{sourceRow, revisionRow} {
		got, err := store.GetObservation(ctx, want.ID, want.Revision)
		require.NoError(t, err)
		require.Equal(t, want.Fingerprint(), got.Fingerprint())
	}
}

// TestObservationAppendRevisionFenceFallbackStaysReachableAndIsolated covers
// the second identity form on its own: with no source-identity match, the
// observation ID/revision fence is what resolves the append. A fresh append
// beside unrelated rows must still land, and a second source identity claiming
// an occupied observation ID/revision must collide.
func TestObservationAppendRevisionFenceFallbackStaysReachableAndIsolated(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)

	unrelated := phase4Observation("sqlite-test", "unrelated", 1)
	unrelated.SourceEventKey = "key-unrelated"
	require.NoError(t, store.AppendObservation(ctx, unrelated))

	// No source identity and no revision fence match: a plain fresh append.
	fresh := phase4Observation("sqlite-test", "fresh", 1)
	fresh.SourceEventKey = "key-fresh"
	require.NoError(t, store.AppendObservation(ctx, fresh), "unrelated rows must not turn the fallback into a false collision")

	// The revision fence is occupied by "fresh"/1; a different source identity
	// claiming the same observation identity must be rejected.
	collide := phase4Observation("sqlite-test", "fresh", 1)
	collide.SourceEventKey = "key-fresh-again"
	err := store.AppendObservation(ctx, collide)
	require.ErrorIs(t, err, journalstore.ErrIdentityCollision,
		"the revision fence must still classify a different-source-identity claim")

	got, err := store.GetObservation(ctx, fresh.ID, fresh.Revision)
	require.NoError(t, err)
	require.Equal(t, fresh.Fingerprint(), got.Fingerprint(), "the rejected append must not rewrite the fenced row")
}

// TestObservationReplayPreservesFirstReceiptProjectionsAndOutbox states what a
// replay must leave untouched. Replay equivalence is a canonical semantic
// property, so a delivery that differs only in receipt metadata is accepted -
// but the durable record keeps the first envelope, the first receipt, its
// component projections, and its single economic trigger. Nothing is
// overwritten, duplicated, or re-enqueued.
func TestObservationReplayPreservesFirstReceiptProjectionsAndOutbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSQLiteJournal(t)
	sink := economicSink(t, store)

	observation := phase4Observation("sqlite-test", "replay-invariants", 1)
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, observation))

	firstPayload := selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision)
	firstFingerprint := storedFactFingerprint(t, ctx, store, observation)
	firstComponents, err := store.ListObservationComponents(ctx, structToComponentQuery(observation, 50))
	require.NoError(t, err)
	firstOutbox, err := store.ListPendingObservationOutbox(ctx, 50)
	require.NoError(t, err)
	require.Len(t, firstOutbox, 1)

	replay := observation.Clone()
	replay.ReceivedAt = replay.ReceivedAt.Add(time.Minute)
	require.NotEqual(t, observation.ReceivedAt, replay.ReceivedAt, "the replay must differ in receipt metadata to be a replay at all")
	require.NoError(t, sink.AppendEconomicObservationWithOutbox(ctx, replay), "receipt-only replay must stay idempotent")

	stored, err := store.GetObservation(ctx, observation.ID, observation.Revision)
	require.NoError(t, err)
	require.Equal(t, observation.ReceivedAt, stored.ReceivedAt, "the durable row must keep the first receipt")
	require.Equal(t, observation.Fingerprint(), stored.Fingerprint())
	require.Equal(t, firstFingerprint, storedFactFingerprint(t, ctx, store, observation))
	require.Equal(t, firstPayload, selectFactsPayload(t, ctx, store, observation.Subject.StoreID, observation.ID, observation.Revision),
		"the durable row must keep the first envelope verbatim")

	afterComponents, err := store.ListObservationComponents(ctx, structToComponentQuery(observation, 50))
	require.NoError(t, err)
	require.Len(t, afterComponents.Components, len(firstComponents.Components), "a replay must not duplicate or drop projections")
	afterOutbox, err := store.ListPendingObservationOutbox(ctx, 50)
	require.NoError(t, err)
	require.Len(t, afterOutbox, 1, "a replay must not enqueue a second economic trigger")
}

// summarizeIdentityWork renders the distinct statement shapes a batch issued so
// a failed bound explains which statement multiplied.
func summarizeIdentityWork(queries []string) string {
	counts := map[string]int{}
	order := make([]string, 0, len(queries))
	for _, query := range queries {
		shape := strings.Join(strings.Fields(query), " ")
		if len(shape) > 90 {
			shape = shape[:90] + "..."
		}
		if _, seen := counts[shape]; !seen {
			order = append(order, shape)
		}
		counts[shape]++
	}
	var b strings.Builder
	for _, shape := range order {
		fmt.Fprintf(&b, "\n  %3d x %s", counts[shape], shape)
	}
	return b.String()
}

// TestNewDurableStoreFailsClosedWhenTheJournalIdentityCannotBeMinted proves the
// opaque journal identity is fail-closed: a failed or empty mint refuses the
// open instead of publishing a journal whose identity cannot be proven, and
// the caller keeps ownership of the database handle.
func TestNewDurableStoreFailsClosedWhenTheJournalIdentityCannotBeMinted(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "metering-mint.db")
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := dbinfra.NewBunDB(sqlDB, dbinfra.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{
		StoreID: "mint-failure",
		ObservationIdentityMint: func() (metering.ObservationJournalID, error) {
			return "", errors.New("journal identity entropy unavailable")
		},
	})
	if err == nil {
		t.Fatal("a journal whose identity cannot be minted must not open")
	}
	if store != nil {
		t.Fatal("a refused journal open must not publish a store")
	}
	if !strings.Contains(err.Error(), "identity") {
		t.Fatalf("err = %v, want the identity mint failure", err)
	}
	// The failed open did not take the database handle: the caller still owns
	// it, so no handle is leaked by the refusal.
	if err := sqlDB.PingContext(context.Background()); err != nil {
		t.Fatalf("the refused open closed the caller-owned database handle: %v", err)
	}
	// An empty identity is refused as well: a journal without a provable
	// identity would be indistinguishable from another journal over the same
	// store.
	if _, err := journalstore.NewDurableStore(context.Background(), bunDB, journalstore.DurableConfig{
		StoreID: "mint-empty",
		ObservationIdentityMint: func() (metering.ObservationJournalID, error) {
			return "", nil
		},
	}); err == nil {
		t.Fatal("an empty journal identity must not open a journal")
	}
}
