package sessionclassification_test

import (
	"context"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// Task 9.2 durable-guard certification.
//
// One guard in BunStore is not reachable through the store API alone, so it needs an
// explicit construction to be pinned at all.
//
// CompleteRemote's positive UPDATE carries `AND kind = ''`. Through the public
// contract that predicate can never be the deciding one:
//
//   - Promote clears remote_lease_id when it installs a positive, so a positive row
//     never holds the lease token a later completion must match.
//   - ClaimRemote refuses any key that already holds a positive.
//   - CompleteRemote clears the lease on every accepted completion.
//
// So the token guard (`AND remote_lease_id = ? AND remote_attempts = ?`) always
// rejects first, and removing `AND kind = ''` changes nothing observable through the
// API. That is precisely why an ordinary test cannot tell whether the guard is
// load-bearing.
//
// The state is nevertheless reachable at the TABLE, which is the shared boundary:
// the table is durable and multi-writer, so a row can carry a positive together with
// a live lease token written by another writer, an older generation, or a manual
// operation. The guard is what keeps the re-source invariant at that boundary. The
// test below constructs exactly that row through raw SQL - the same technique the
// established dbparity contract uses for its control row - so the guard is exercised
// on the deciding predicate instead of being masked by the token guard.

// sessionClassificationRawInsert writes one durable classification row directly,
// bypassing the store API. It is the only way to reach the states the store's own
// monotonicity guards make unreachable through its public contract.
func sessionClassificationRawInsert(
	t *testing.T,
	database *bun.DB,
	key featurestate.Key,
	kind session.Kind,
	source session.ClassificationSource,
	confidence session.ConfidenceBand,
	evidence session.EvidenceCode,
	revision int64,
	remoteAttempts int64,
	leaseID string,
	leaseUntil *time.Time,
	now time.Time,
) {
	t.Helper()
	_, err := database.NewRaw(`INSERT INTO session_classification (
		scope_kind, scope_id, kind, source, confidence, evidence_code, classification_revision,
		remote_attempts, remote_lease_id, remote_lease_until, remote_next_eligible_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
		key.Kind, key.ID, kind, source, confidence, evidence, revision,
		remoteAttempts, leaseID, leaseUntil, now,
	).Exec(context.Background())
	require.NoError(t, err)
}

// TestBunStoreCompletionCannotReSourceARowThatIsBothPositiveAndLeased pins the
// `AND kind = ”` guard on CompleteRemote's positive UPDATE.
//
// The row under test is a durable row that already holds a coding_agent positive AND
// a live, matching remote lease token. A remote completion carrying a DIFFERENT
// positive (remote source, different evidence) must be refused and must leave the
// stored source, evidence, confidence, and revision untouched.
func TestBunStoreCompletionCannotReSourceARowThatIsBothPositiveAndLeased(t *testing.T) {
	t.Parallel()

	dsn := sqliteTestDSN(t, "classification-positive-and-leased.db")
	_, database := openSQLiteBunDBWithConnections(t, dsn, 4)
	classificationStore, err := store.NewBunStore(database)
	require.NoError(t, err)
	ctx := context.Background()
	require.NoError(t, classificationStore.EnsureSchema(ctx))

	runPositiveAndLeasedCompletionGuardContract(t, database, classificationStore)
}

// runPositiveAndLeasedCompletionGuardContract is dialect-neutral so it also runs
// against direct PostgreSQL through the database-parity gate.
func runPositiveAndLeasedCompletionGuardContract(t *testing.T, database *bun.DB, classificationStore featurestate.Store) {
	t.Helper()
	ctx := context.Background()
	now := testStoreTime()
	leaseUntil := now.Add(time.Minute)
	const leaseToken = "shared-positive-and-leased-token"

	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "positive-and-leased/authority"}
	sessionClassificationRawInsert(t, database, key,
		session.KindCodingAgent, session.SourceLocalIdentity, session.ConfidenceHigh,
		"client_family.codex", 1, 1, leaseToken, &leaseUntil, now)

	before, found, err := classificationStore.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found, "the constructed row must be readable as a valid positive")
	require.True(t, before.Classification.IsCodingAgent())
	require.Equal(t, leaseToken, before.RemoteLeaseID, "the constructed row must carry the live lease")

	// A completion holding exactly the row's own live token, carrying a positive
	// with a different source AND different evidence. Every other predicate of the
	// UPDATE is satisfied, so `AND kind = ''` is the only thing that can refuse it.
	claim := featurestate.RemoteClaim{
		Key: key, LeaseID: leaseToken, Attempt: 1, RetryBackoff: 0,
	}
	attempted, err := classificationStore.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{
		Proposal: convergenceRemoteProposal("remote.re_source_attempt"),
	}, now.Add(time.Second))
	require.ErrorIs(t, err, featurestate.ErrStaleRemoteClaim,
		"a completion must not re-source a row that already holds a positive")
	assert.Equal(t, before.Classification, attempted.Classification,
		"the refused completion must return the current positive, not its own proposal")
	assert.Equal(t, before, attempted, "the refused completion must return the current record")

	after, found, err := classificationStore.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, before, after, "the refused completion must not rewrite the durable row")
	assert.Equal(t, session.SourceLocalIdentity, after.Classification.Source)
	assert.Equal(t, session.EvidenceCode("client_family.codex"), after.Classification.Evidence)
	assert.Equal(t, uint64(1), after.Classification.Revision)

	// The neutral completion path carries the same `AND kind = ''` predicate. It
	// must be refused too, so a leased positive is not silently mutated into
	// backoff bookkeeping that would misrepresent the row's remote state.
	neutral, err := classificationStore.CompleteRemote(ctx, claim, featurestate.RemoteCompletion{},
		now.Add(2*time.Second))
	require.ErrorIs(t, err, featurestate.ErrStaleRemoteClaim,
		"a neutral completion must not touch a row that already holds a positive")
	assert.Equal(t, before, neutral)
	settled, found, err := classificationStore.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, before, settled, "a refused neutral completion must leave the durable row byte-identical")

	// A promotion against the same row is the sibling guard, and it must also leave
	// the leased positive untouched rather than re-sourcing it.
	replayed, promoted, err := classificationStore.Promote(ctx, key, convergenceLocalProposal(), now.Add(3*time.Second))
	require.NoError(t, err)
	assert.False(t, promoted, "a leased positive row must refuse a further promotion")
	assert.Equal(t, before, replayed)

	final, found, err := classificationStore.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, before, final, "no completion or promotion may rewrite a leased positive row")
}

// TestBunStorePromotionCannotReSourceALeasedUnknownRow pins the `WHERE
// session_classification.kind = ”` guard on Promote's upsert against the same
// table-level state shape: a row whose classification columns are empty but which
// carries live remote bookkeeping.
//
// A promotion here must install the positive and clear the lease atomically, and
// must never leave the row holding a classification alongside a stale lease token,
// because a later completion matching that token would then re-source it.
func TestBunStorePromotionCannotReSourceALeasedUnknownRow(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := sqliteTestDSN(t, "classification-leased-unknown.db")
	_, database := openSQLiteBunDBWithConnections(t, dsn, 4)
	classificationStore, err := store.NewBunStore(database)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))

	now := testStoreTime()
	leaseUntil := now.Add(time.Minute)
	const leaseToken = "leased-unknown-token"
	key := featurestate.Key{Kind: featurestate.ScopeALeg, ID: "leased-unknown/authority"}
	sessionClassificationRawInsert(t, database, key,
		session.KindUnknown, session.ClassificationSource(""), session.ConfidenceBand(""),
		session.EvidenceCode(""), 0, 2, leaseToken, &leaseUntil, now)

	unknown, found, err := classificationStore.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, session.Classification{}, unknown.Classification)
	require.Equal(t, uint32(2), unknown.RemoteAttempts, "the promotion must preserve remote-attempt history")

	promoted, didPromote, err := classificationStore.Promote(ctx, key, convergenceLocalProposal(), now.Add(time.Second))
	require.NoError(t, err)
	require.True(t, didPromote)
	assert.Equal(t, uint64(1), promoted.Classification.Revision)
	assert.Equal(t, session.SourceLocalIdentity, promoted.Classification.Source)
	assert.Equal(t, uint32(2), promoted.RemoteAttempts, "a promotion must not reset the finite attempt budget")
	assert.Empty(t, promoted.RemoteLeaseID, "a promotion must clear the lease atomically with the classification")
	assert.True(t, promoted.RemoteLeaseUntil.IsZero())
	assert.True(t, promoted.RemoteNextEligibleAt.IsZero())

	stored, found, err := classificationStore.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, promoted, stored)
	assert.Empty(t, stored.RemoteLeaseID)

	// The now-stale token must no longer complete: the promotion released it, so the
	// re-source invariant is preserved by the cleared lease even before the
	// classification guard is consulted.
	stale, err := classificationStore.CompleteRemote(ctx, featurestate.RemoteClaim{
		Key: key, LeaseID: leaseToken, Attempt: 2, RetryBackoff: 0,
	}, featurestate.RemoteCompletion{Proposal: convergenceRemoteProposal("remote.after_promotion")},
		now.Add(2*time.Second))
	require.ErrorIs(t, err, featurestate.ErrStaleRemoteClaim)
	assert.Equal(t, stored.Classification, stale.Classification)
}
