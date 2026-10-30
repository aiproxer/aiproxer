package sessionclassification_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	featurestate "github.com/aiproxer/aiproxer/internal/plugins/features/sessionclassification"
	store "github.com/aiproxer/aiproxer/internal/standardplugins/featurehost/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"github.com/uptrace/bun/schema"
	_ "modernc.org/sqlite"
)

func TestBunStorePromoteSurvivesSQLiteReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dsn := sqliteTestDSN(t, "classification-reopen.db")
	sqlDB1, bunDB1 := openSQLiteBunDB(t, dsn)
	store1, err := store.NewBunStore(bunDB1)
	require.NoError(t, err)
	require.NoError(t, store1.EnsureSchema(ctx))

	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "secure-session-reopen"}
	missing, found, err := store1.Load(ctx, key)
	require.NoError(t, err)
	assert.False(t, found, "a missing key must remain unallocated")
	assert.Equal(t, featurestate.Record{}, missing)

	now := time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
	firstProposal := session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh, Evidence: "client.codex", Revision: 91,
	}
	first, promoted, err := store1.Promote(ctx, key, firstProposal, now)
	require.NoError(t, err)
	assert.True(t, promoted, "the first valid proposal must be stored")
	assert.Equal(t, key, first.Key)
	assert.Equal(t, firstProposal.Kind, first.Classification.Kind)
	assert.Equal(t, uint64(1), first.Classification.Revision, "the store owns the first positive revision")
	assert.Equal(t, firstProposal.Source, first.Classification.Source)
	assert.Equal(t, firstProposal.Confidence, first.Classification.Confidence)
	assert.Equal(t, firstProposal.Evidence, first.Classification.Evidence)

	secondProposal := session.Classification{
		Kind: session.KindCodingAgent, Source: session.SourceLocalTooling,
		Confidence: session.ConfidenceHigh, Evidence: "tooling.distinct_cluster", Revision: 7,
	}
	repeat, promoted, err := store1.Promote(ctx, key, secondProposal, now.Add(time.Second))
	require.NoError(t, err)
	assert.False(t, promoted, "a later detector must not rewrite the first positive")
	assert.Equal(t, first, repeat, "idempotent promotion must return the original row unchanged")
	require.NoError(t, sqlDB1.PingContext(ctx), "the feature store must leave its borrowed DB open")

	require.NoError(t, bunDB1.Close(), "the test owns and closes the first DB handle")
	_, bunDB2 := openSQLiteBunDB(t, dsn)
	t.Cleanup(func() { _ = bunDB2.Close() })
	store2, err := store.NewBunStore(bunDB2)
	require.NoError(t, err)
	require.NoError(t, store2.EnsureSchema(ctx), "repeated ensure must preserve existing classification rows")

	restored, found, err := store2.Load(ctx, key)
	require.NoError(t, err)
	assert.True(t, found, "the persisted classification must be restored after reopening SQLite")
	assert.Equal(t, first.Classification, restored.Classification)
	assert.Equal(t, first.UpdatedAt, restored.UpdatedAt)
	assert.Equal(t, uint32(0), restored.RemoteAttempts)
	assert.Empty(t, restored.RemoteLeaseID)
	assert.True(t, restored.RemoteLeaseUntil.IsZero())
	assert.True(t, restored.RemoteNextEligibleAt.IsZero())
}

func TestBunStoreUsesAuthorityKindAndDoesNotPersistUnknownAsNegative(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDB(t, sqliteTestDSN(t, "classification-authority.db"))
	classificationStore, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))

	secure := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "same-authority-id"}
	aLeg := featurestate.Key{Kind: featurestate.ScopeALeg, ID: "same-authority-id"}
	for _, key := range []featurestate.Key{secure, aLeg} {
		_, found, err := classificationStore.Load(ctx, key)
		require.NoError(t, err)
		assert.False(t, found, "unknown must be represented by no durable classification row")
	}

	secureProposal := positiveProposal(session.SourceLocalIdentity, "client.secure")
	secureRecord, promoted, err := classificationStore.Promote(ctx, secure, secureProposal, testStoreTime())
	require.NoError(t, err)
	assert.True(t, promoted)
	aLegRecord, promoted, err := classificationStore.Promote(ctx, aLeg, positiveProposal(session.SourceLocalTooling, "tooling.aleg"), testStoreTime())
	require.NoError(t, err)
	assert.True(t, promoted)
	assert.NotEqual(t, secureRecord.Classification.Evidence, aLegRecord.Classification.Evidence)

	loadedSecure, found, err := classificationStore.Load(ctx, secure)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, secureProposal.Evidence, loadedSecure.Classification.Evidence)
	loadedALeg, found, err := classificationStore.Load(ctx, aLeg)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "tooling.aleg", string(loadedALeg.Classification.Evidence))
}

func TestBunStoreEnsureSchemaCreatesOnlyBoundedClassificationColumns(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDB(t, sqliteTestDSN(t, "classification-schema.db"))
	classificationStore, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	var tables int
	require.NoError(t, bunDB.NewRaw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='session_classification'").Scan(ctx, &tables))
	assert.Zero(t, tables, "constructing a store must not create schema before lifecycle EnsureSchema")
	require.NoError(t, classificationStore.EnsureSchema(ctx))
	require.NoError(t, classificationStore.EnsureSchema(ctx), "schema ensure must be repeatable")

	type sqliteColumn struct {
		CID          int            `bun:"cid"`
		Name         string         `bun:"name"`
		Type         string         `bun:"type"`
		NotNull      int            `bun:"notnull"`
		DefaultValue sql.NullString `bun:"dflt_value"`
		PK           int            `bun:"pk"`
	}
	var columns []sqliteColumn
	require.NoError(t, bunDB.NewRaw("PRAGMA table_info('session_classification')").Scan(ctx, &columns))
	wantNames := []string{
		"scope_kind", "scope_id", "kind", "source", "confidence", "evidence_code",
		"classification_revision", "remote_attempts", "remote_lease_id",
		"remote_lease_until", "remote_next_eligible_at", "updated_at",
	}
	gotNames := make([]string, 0, len(columns))
	for _, column := range columns {
		gotNames = append(gotNames, column.Name)
	}
	assert.Equal(t, wantNames, gotNames, "the feature table must contain only authority, scalar classification, control, and timestamp fields")
	if len(columns) == len(wantNames) {
		assert.Equal(t, 1, columns[0].PK)
		assert.Equal(t, 2, columns[1].PK)
		assert.Equal(t, "BIGINT", strings.ToUpper(columns[6].Type), "classification revisions need a 64-bit SQL type")
		assert.Contains(t, strings.ToUpper(columns[10].Type), "TIMESTAMP")
		assert.Contains(t, strings.ToUpper(columns[11].Type), "TIMESTAMP")
	}
}

func TestBunStorePromotionClearsRemoteControlAndPreservesAttempts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDB(t, sqliteTestDSN(t, "classification-control.db"))
	classificationStore, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))

	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "control-state-session"}
	start := testStoreTime()
	leaseUntil := start.Add(time.Minute)
	backoffUntil := start.Add(2 * time.Minute)
	_, err = bunDB.NewRaw(`INSERT INTO session_classification (
		scope_kind, scope_id, kind, source, confidence, evidence_code, classification_revision,
		remote_attempts, remote_lease_id, remote_lease_until, remote_next_eligible_at, updated_at
	) VALUES (?, ?, '', '', '', '', 0, ?, ?, ?, ?, ?)`,
		key.Kind, key.ID, int64(3), "lease-token-3", leaseUntil, backoffUntil, start).Exec(ctx)
	require.NoError(t, err)

	proposal := positiveProposal(session.SourceLocalIdentity, "client.codex")
	got, promoted, err := classificationStore.Promote(ctx, key, proposal, start.Add(3*time.Minute))
	require.NoError(t, err)
	require.True(t, promoted)
	assert.Equal(t, uint32(3), got.RemoteAttempts, "promotion must keep the finite attempt history")
	assert.Empty(t, got.RemoteLeaseID)
	assert.True(t, got.RemoteLeaseUntil.IsZero())
	assert.True(t, got.RemoteNextEligibleAt.IsZero())
	assert.Equal(t, uint64(1), got.Classification.Revision)
}

func TestBunStoreRejectsInvalidProposalWithoutAllocatingOrChangingPositive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDB(t, sqliteTestDSN(t, "classification-validation.db"))
	classificationStore, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))

	now := testStoreTime()
	unknownKey := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "invalid-proposal-session"}
	_, _, err = classificationStore.Promote(ctx, unknownKey, session.Classification{}, now)
	require.ErrorIs(t, err, featurestate.ErrInvalidProposal)
	malformed := positiveProposal(session.SourceLocalIdentity, "client invalid")
	_, _, err = classificationStore.Promote(ctx, unknownKey, malformed, now)
	require.ErrorIs(t, err, featurestate.ErrInvalidProposal)
	_, found, err := classificationStore.Load(ctx, unknownKey)
	require.NoError(t, err)
	require.False(t, found, "invalid proposals must not allocate an unknown row")

	first := positiveProposal(session.SourceLocalIdentity, "client.codex")
	winner, promoted, err := classificationStore.Promote(ctx, unknownKey, first, now)
	require.NoError(t, err)
	require.True(t, promoted)

	_, _, err = classificationStore.Promote(ctx, unknownKey, session.Classification{}, now.Add(time.Second))
	require.ErrorIs(t, err, featurestate.ErrInvalidProposal)
	_, _, err = classificationStore.Promote(ctx, unknownKey, malformed, now.Add(2*time.Second))
	require.ErrorIs(t, err, featurestate.ErrInvalidProposal)
	after, found, err := classificationStore.Load(ctx, unknownKey)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, winner, after, "invalid proposals must not mutate an existing positive")
}

func TestBunStoreConcurrentPromotionHasOneWinner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	_, bunDB := openSQLiteBunDBWithConnections(t, sqliteTestDSN(t, "classification-concurrent.db"), 8)
	classificationStore, err := store.NewBunStore(bunDB)
	require.NoError(t, err)
	require.NoError(t, classificationStore.EnsureSchema(ctx))

	key := featurestate.Key{Kind: featurestate.ScopeSecureSession, ID: "concurrent-session"}
	const workerCount = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	var winnerCount atomic.Int32
	gotRecords := make(chan featurestate.Record, workerCount)
	errCh := make(chan error, workerCount)
	for i := range workerCount {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			proposal := positiveProposal(session.SourceLocalIdentity, session.EvidenceCode(fmt.Sprintf("client.evidence_%d", index)))
			got, promoted, err := classificationStore.Promote(ctx, key, proposal, testStoreTime())
			if err != nil {
				errCh <- err
				return
			}
			if promoted {
				winnerCount.Add(1)
			}
			gotRecords <- got
		}(i)
	}
	close(start)
	wg.Wait()
	close(gotRecords)
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent Promote(): %v", err)
	}
	assert.Equal(t, int32(1), winnerCount.Load(), "exactly one atomic promotion wins")

	var winner session.Classification
	for record := range gotRecords {
		assert.Equal(t, uint64(1), record.Classification.Revision)
		if winner == (session.Classification{}) {
			winner = record.Classification
		} else {
			assert.Equal(t, winner, record.Classification, "all concurrent callers must observe the same first source/evidence")
		}
	}
}

func TestNewBunStoreRejectsNilAndUnsupportedDBWithoutLeakingDetails(t *testing.T) {
	t.Parallel()

	got, err := store.NewBunStore(nil)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, store.ErrInvalidBunDB)

	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	unsupportedDB := bun.NewDB(sqlDB, unsupportedDialect{Dialect: sqlitedialect.New()})
	t.Cleanup(func() { _ = unsupportedDB.Close() })
	got, err = store.NewBunStore(unsupportedDB)
	assert.Nil(t, got)
	assert.ErrorIs(t, err, store.ErrUnsupportedBunDialect)
	if err != nil {
		assert.NotContains(t, err.Error(), ":memory:")
	}
}

func sqliteTestDSN(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	return "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
}

func openSQLiteBunDB(t *testing.T, dsn string) (*sql.DB, *bun.DB) {
	t.Helper()
	return openSQLiteBunDBWithConnections(t, dsn, 1)
}

func openSQLiteBunDBWithConnections(t *testing.T, dsn string, connections int) (*sql.DB, *bun.DB) {
	t.Helper()
	sqlDB, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(connections)
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bunDB.Close() })
	return sqlDB, bunDB
}

func positiveProposal(source session.ClassificationSource, evidence session.EvidenceCode) session.Classification {
	return session.Classification{
		Kind: session.KindCodingAgent, Source: source, Confidence: session.ConfidenceHigh,
		Evidence: evidence, Revision: 91,
	}
}

func testStoreTime() time.Time {
	return time.Date(2026, time.September, 30, 12, 0, 0, 0, time.UTC)
}

type unsupportedDialect struct {
	schema.Dialect
}

func (unsupportedDialect) Name() dialect.Name {
	return dialect.MySQL
}
