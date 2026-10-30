package sessionclassification_test

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/internal/testkit/dbparity"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

// TestDBParity_SQLite is the canonical SQLite entry point for the
// session-classification durable Load/Promote and schema contract.
func TestDBParity_SQLite(t *testing.T) {
	t.Parallel()

	dsn := sqliteTestDSN(t, "classification-db-parity.db")
	currentDB := openSQLiteDBForParity(t, dsn)
	openAgain := func() *bun.DB {
		require.NoError(t, currentDB.Close())
		currentDB = openSQLiteDBForParity(t, dsn)
		return currentDB
	}

	classificationStore, err := store.NewBunStore(currentDB)
	require.NoError(t, err)
	runSessionClassificationParityContract(t, currentDB, classificationStore, openAgain)
}

func openSQLiteDBForParity(t *testing.T, dsn string) *bun.DB {
	t.Helper()
	_, bunDB := openSQLiteBunDBWithConnections(t, dsn, 8)
	return bunDB
}

func runSessionClassificationParityContract(
	t *testing.T,
	database *bun.DB,
	classificationStore *store.BunStore,
	reopen func() *bun.DB,
) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, classificationStore.EnsureSchema(ctx))
	runSessionClassificationRemoteLeaseContract(t, func() featurestate.Store {
		candidate, err := store.NewBunStore(database)
		require.NoError(t, err)
		return candidate
	})
	// Task 9.2: the authority-isolation contract and the durable monotonicity-guard
	// contract are named subtests so the parity run records that both dialects
	// executed them, rather than folding them invisibly into the parent test.
	t.Run("authority isolation", func(t *testing.T) {
		runSessionClassificationAuthorityIsolationContract(t, func(t *testing.T) featurestate.Store {
			t.Helper()
			candidate, err := store.NewBunStore(database)
			require.NoError(t, err)
			return candidate
		})
	})
	t.Run("positive and leased row cannot be re-sourced", func(t *testing.T) {
		runPositiveAndLeasedCompletionGuardContract(t, database, classificationStore)
	})
	require.NoError(t, dbparity.VerifySchema(ctx, database, sessionClassificationLogicalSchemaSpec()))
	assertSessionClassificationMigrationHistory(t, database)

	key := parityKey("restart")
	missing, found, err := classificationStore.Load(ctx, key)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, featurestate.Record{}, missing)

	proposed := session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh, Evidence: "client.codex", Revision: 91,
	}
	first, promoted, err := classificationStore.Promote(ctx, key, proposed, parityTime())
	require.NoError(t, err)
	require.True(t, promoted, "the first positive proposal must be stored")
	assert.Equal(t, key, first.Key)
	assert.Equal(t, session.KindCodingAgent, first.Classification.Kind)
	assert.Equal(t, session.SourceLocalIdentity, first.Classification.Source)
	assert.Equal(t, session.ConfidenceHigh, first.Classification.Confidence)
	assert.Equal(t, session.EvidenceCode("client.codex"), first.Classification.Evidence)
	assert.Equal(t, uint64(1), first.Classification.Revision)

	replayed, promoted, err := classificationStore.Promote(ctx, key, session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh, Evidence: "tooling.distinct_cluster", Revision: 7,
	}, parityTime().Add(time.Second))
	require.NoError(t, err)
	assert.False(t, promoted, "a later positive must not rewrite the first classification")
	assert.Equal(t, first, replayed)

	otherScope := featurestate.Key{Kind: featurestate.ScopeALeg, ID: key.ID}
	other, promoted, err := classificationStore.Promote(ctx, otherScope, session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh, Evidence: "tooling.distinct_cluster", Revision: 7,
	}, parityTime())
	require.NoError(t, err)
	require.True(t, promoted)
	assert.Equal(t, otherScope, other.Key)
	assert.NotEqual(t, first.Classification.Evidence, other.Classification.Evidence,
		"authority kind must be part of the durable key")

	controlKey := parityKey("control")
	leaseUntil := parityTime().Add(time.Minute)
	backoffUntil := parityTime().Add(2 * time.Minute)
	_, err = database.NewRaw(`INSERT INTO session_classification (
		scope_kind, scope_id, kind, source, confidence, evidence_code, classification_revision,
		remote_attempts, remote_lease_id, remote_lease_until, remote_next_eligible_at, updated_at
	) VALUES (?, ?, '', '', '', '', 0, ?, ?, ?, ?, ?)`,
		controlKey.Kind, controlKey.ID, int64(3), "lease-token-3", leaseUntil, backoffUntil, parityTime()).Exec(ctx)
	require.NoError(t, err)
	control, promoted, err := classificationStore.Promote(ctx, controlKey, proposed, parityTime().Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, promoted)
	assert.Equal(t, uint32(3), control.RemoteAttempts, "promotion must preserve remote attempt history")
	assert.Empty(t, control.RemoteLeaseID)
	assert.True(t, control.RemoteLeaseUntil.IsZero())
	assert.True(t, control.RemoteNextEligibleAt.IsZero())

	winner := runConcurrentPromotions(t, classificationStore, parityKey("race"))
	assert.Equal(t, uint64(1), winner.Classification.Revision)

	require.NoError(t, classificationStore.EnsureSchema(ctx), "schema migration must be idempotent")
	require.NoError(t, dbparity.VerifySchema(ctx, database, sessionClassificationLogicalSchemaSpec()))
	assertSessionClassificationMigrationHistory(t, database)

	reopenedDB := reopen()
	reopenedStore, err := store.NewBunStore(reopenedDB)
	require.NoError(t, err)
	require.NoError(t, reopenedStore.EnsureSchema(ctx), "reopen must retain the applied feature migration")
	require.NoError(t, dbparity.VerifySchema(ctx, reopenedDB, sessionClassificationLogicalSchemaSpec()))
	assertSessionClassificationMigrationHistory(t, reopenedDB)

	restored, found, err := reopenedStore.Load(ctx, key)
	require.NoError(t, err)
	require.True(t, found, "a positive classification must be restored after reopening the database")
	assert.Equal(t, first.Classification, restored.Classification)
	assert.Equal(t, first.UpdatedAt, restored.UpdatedAt)

	remoteKey := parityKey("remote-restart")
	remoteStart := parityTime().Add(24 * time.Hour).Add(123 * time.Nanosecond)
	remoteClaim, claimed, ok, err := reopenedStore.ClaimRemote(ctx, remoteKey, remoteStart, 2, time.Minute, 0)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint32(1), claimed.RemoteAttempts)

	remoteReopenedDB := reopen()
	remoteReopenedStore, err := store.NewBunStore(remoteReopenedDB)
	require.NoError(t, err)
	require.NoError(t, remoteReopenedStore.EnsureSchema(ctx))
	active, found, err := remoteReopenedStore.Load(ctx, remoteKey)
	require.NoError(t, err)
	require.True(t, found, "an unknown claimed row must survive a durable reopen")
	require.Equal(t, uint32(1), active.RemoteAttempts)
	require.Equal(t, remoteClaim.LeaseID, active.RemoteLeaseID)

	completedAt := active.RemoteLeaseUntil.Add(-time.Nanosecond)
	completed, err := remoteReopenedStore.CompleteRemote(ctx, remoteClaim, featurestate.RemoteCompletion{}, completedAt)
	require.NoError(t, err, "the active lease token must remain valid after reopen")
	require.Equal(t, uint32(1), completed.RemoteAttempts)
	require.Empty(t, completed.RemoteLeaseID)

	stale, err := remoteReopenedStore.CompleteRemote(ctx, remoteClaim, featurestate.RemoteCompletion{}, active.RemoteLeaseUntil)
	require.ErrorIs(t, err, featurestate.ErrStaleRemoteClaim, "the lease must be single-use across reopen")
	require.Equal(t, completed, stale, "stale completion after reopen must return current state")
}

func runConcurrentPromotions(t *testing.T, classificationStore *store.BunStore, key featurestate.Key) featurestate.Record {
	t.Helper()
	const workerCount = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winners atomic.Int32
	records := make(chan featurestate.Record, workerCount)
	errs := make(chan error, workerCount)
	for i := range workerCount {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			source := session.SourceLocalIdentity
			evidence := session.EvidenceCode("client.codex")
			if index%2 == 1 {
				source = session.SourceLocalTooling
				evidence = session.EvidenceCode("tooling.distinct_cluster")
			}
			record, promoted, err := classificationStore.Promote(context.Background(), key, session.Classification{
				Kind: session.KindCodingAgent, Source: source,
				Confidence: session.ConfidenceHigh, Evidence: evidence, Revision: 1,
			}, parityTime())
			if err != nil {
				errs <- err
				return
			}
			if promoted {
				winners.Add(1)
			}
			records <- record
		}(i)
	}
	close(start)
	wg.Wait()
	close(records)
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Promote: %v", err)
	}
	require.Equal(t, int32(1), winners.Load(), "exactly one proposal must win atomically")

	var winner featurestate.Record
	for record := range records {
		if winner.Classification == (session.Classification{}) {
			winner = record
			continue
		}
		assert.Equal(t, winner, record, "all concurrent callers must observe the same winner")
	}
	return winner
}

func assertSessionClassificationMigrationHistory(t *testing.T, database *bun.DB) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	migrations, err := dbparity.DiscoverMigrations(filepath.Dir(file))
	require.NoError(t, err)
	require.NotEmpty(t, migrations, "the feature-owned versioned migration must be discoverable")

	rows, err := database.QueryContext(context.Background(), "SELECT name FROM bun_session_classification_migrations")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	recorded := make(map[string]bool)
	count := 0
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		if len(name) >= 14 {
			recorded[name[:14]] = true
		}
		count++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 1, count, "EnsureSchema must apply the feature baseline exactly once")
	require.NoError(t, dbparity.AssertMigrationHistoryIDs(dbparity.MigrationIDs(migrations), recorded))
}

func sessionClassificationLogicalSchemaSpec() dbparity.LogicalSchemaSpec {
	return dbparity.LogicalSchemaSpec{
		ComponentID: "session-classification",
		Tables: []dbparity.TableSpec{{
			Name: "session_classification",
			Columns: []dbparity.ColumnSpec{
				{Name: "scope_kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
				{Name: "scope_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false), PrimaryKey: true},
				{Name: "kind", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				{Name: "source", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				{Name: "confidence", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				{Name: "evidence_code", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				{Name: "classification_revision", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				{Name: "remote_attempts", Type: dbparity.TypeInteger, Nullable: dbparity.PtrBool(false)},
				{Name: "remote_lease_id", Type: dbparity.TypeText, Nullable: dbparity.PtrBool(false)},
				{Name: "remote_lease_until", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(true)},
				{Name: "remote_next_eligible_at", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(true)},
				{Name: "updated_at", Type: dbparity.TypeTimestamp, Nullable: dbparity.PtrBool(false)},
			},
			PrimaryKey: []string{"scope_kind", "scope_id"},
		}},
	}
}

func parityKey(suffix string) featurestate.Key {
	return featurestate.Key{
		Kind: featurestate.ScopeSecureSession,
		ID:   fmt.Sprintf("dbparity-%d-%s", time.Now().UnixNano(), suffix),
	}
}

func parityTime() time.Time {
	return time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
}
