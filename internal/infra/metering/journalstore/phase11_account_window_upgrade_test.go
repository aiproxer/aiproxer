package journalstore_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	aipsdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/stretchr/testify/require"
)

func TestPhase11AccountWindowProjectionUpgrade_BackfillsLegacySearchColumns(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	observation := phase11JournalAccountWindowObservation(
		"legacy-window",
		"acct-legacy",
		"pool-legacy",
		"window-legacy",
		time.Unix(1_000, 0).UTC(),
		time.Unix(10, 0).UTC(),
		phase11JournalMeasure("used_percent", "10"),
	)
	require.NoError(t, store.AppendAccountWindowObservation(ctx, observation))

	var before struct {
		Payload     string `bun:"payload_json"`
		Fingerprint string `bun:"observation_fingerprint"`
		SourceKey   string `bun:"source_event_key"`
	}
	require.NoError(t, store.DB().NewRaw(`
		SELECT payload_json, observation_fingerprint, source_event_key
		FROM metering_facts WHERE observation_id = ?`, observation.ID).Scan(ctx, &before))

	clearAccountWindowSearchColumns(t, store, ctx, observation.ID)
	removeAccountWindowMigrationRecord(t, store, ctx)

	require.NoError(t, journalstore.Migrate(ctx, store.DB()))

	page, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{
		ProviderAccountKey: observation.Subject.ProviderAccountKey,
		PoolID:             observation.Subject.PoolID,
		WindowID:           observation.Subject.WindowID,
		Limit:              10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, observation.ID, page.Observations[0].ID)

	var got struct {
		StreamID           string `bun:"stream_id"`
		Sequence           int64  `bun:"sequence"`
		ProviderAccountKey string `bun:"observation_provider_account_key"`
		PoolID             string `bun:"observation_pool_id"`
		WindowID           string `bun:"observation_window_id"`
		ResetAt            int64  `bun:"observation_reset_at_unix"`
		ObservedAt         int64  `bun:"observation_observed_at_unix"`
		ReceivedAt         int64  `bun:"observation_received_at_unix"`
	}
	require.NoError(t, store.DB().NewRaw(`
		SELECT stream_id, sequence, observation_provider_account_key,
			observation_pool_id, observation_window_id, observation_reset_at_unix,
			observation_observed_at_unix, observation_received_at_unix
		FROM metering_facts WHERE source_event_key = ?`, before.SourceKey).Scan(ctx, &got))
	require.Equal(t, observation.StreamID, got.StreamID)
	require.Equal(t, int64(observation.Sequence), got.Sequence)
	require.Equal(t, observation.Subject.ProviderAccountKey, got.ProviderAccountKey)
	require.Equal(t, observation.Subject.PoolID, got.PoolID)
	require.Equal(t, observation.Subject.WindowID, got.WindowID)
	require.Equal(t, observation.Subject.ResetAt.UnixNano(), got.ResetAt)
	require.Equal(t, observation.ObservedAt.UnixNano(), got.ObservedAt)
	require.Equal(t, observation.ReceivedAt.UnixNano(), got.ReceivedAt)

	var after struct {
		Payload     string `bun:"payload_json"`
		Fingerprint string `bun:"observation_fingerprint"`
		SourceKey   string `bun:"source_event_key"`
	}
	require.NoError(t, store.DB().NewRaw(`
		SELECT payload_json, observation_fingerprint, source_event_key
		FROM metering_facts WHERE source_event_key = ?`, before.SourceKey).Scan(ctx, &after))
	require.Equal(t, before, after, "canonical payload, fingerprint, and source identity must not be rewritten")

	// A rerun after the migration has been applied must remain harmless.
	require.NoError(t, journalstore.Migrate(ctx, store.DB()))
	require.NoError(t, store.DB().NewRaw(`
		SELECT payload_json, observation_fingerprint, source_event_key
		FROM metering_facts WHERE source_event_key = ?`, before.SourceKey).Scan(ctx, &after))
	require.Equal(t, before, after)
}

func TestPhase11AccountWindowProjectionUpgrade_InvalidCanonicalPayloadFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	valid := phase11JournalAccountWindowObservation(
		"legacy-valid",
		"acct-legacy",
		"pool-legacy",
		"window-legacy",
		time.Unix(1_000, 0).UTC(),
		time.Unix(10, 0).UTC(),
		phase11JournalMeasure("used_percent", "10"),
	)
	invalid := phase11JournalAccountWindowObservation(
		"legacy-invalid",
		"acct-invalid",
		"pool-invalid",
		"window-invalid",
		time.Unix(2_000, 0).UTC(),
		time.Unix(11, 0).UTC(),
		phase11JournalMeasure("used_percent", "20"),
	)
	require.NoError(t, store.AppendAccountWindowObservation(ctx, valid))
	require.NoError(t, store.AppendAccountWindowObservation(ctx, invalid))

	var beforeInvalidPayload, beforeInvalidFingerprint string
	require.NoError(t, store.DB().NewRaw(`
		SELECT payload_json, observation_fingerprint
		FROM metering_facts WHERE observation_id = ?`, invalid.ID).Scan(ctx, &beforeInvalidPayload, &beforeInvalidFingerprint))
	var invalidKey, validKey string
	require.NoError(t, store.DB().NewRaw(`
		SELECT source_event_key FROM metering_facts WHERE observation_id = ?`, invalid.ID).Scan(ctx, &invalidKey))
	require.NoError(t, store.DB().NewRaw(`
		SELECT source_event_key FROM metering_facts WHERE observation_id = ?`, valid.ID).Scan(ctx, &validKey))
	clearAccountWindowSearchColumns(t, store, ctx, valid.ID)
	clearAccountWindowSearchColumns(t, store, ctx, invalid.ID)
	_, err := store.DB().ExecContext(ctx,
		`UPDATE metering_facts SET payload_json = ? WHERE source_event_key = ?`,
		`{"version":2,"id":"legacy-invalid","subject":{"kind":"account_window"}}`, invalidKey)
	require.NoError(t, err)
	removeAccountWindowMigrationRecord(t, store, ctx)

	err = journalstore.Migrate(ctx, store.DB())
	require.Error(t, err, "invalid canonical account-window data must block migration")
	var migrationCount int
	require.NoError(t, store.DB().NewRaw(`
		SELECT COUNT(1) FROM bun_metering_journal_migrations WHERE name = ?`, journalstore.AccountWindowProjectionMigrationName).Scan(ctx, &migrationCount))
	require.Zero(t, migrationCount, "failed upgrade must remain retryable")

	var validProviderAccount string
	require.NoError(t, store.DB().NewRaw(`
		SELECT observation_provider_account_key
		FROM metering_facts WHERE source_event_key = ?`, validKey).Scan(ctx, &validProviderAccount))
	require.Empty(t, validProviderAccount, "failed upgrade must not partially publish search columns")

	var afterInvalidPayload, afterInvalidFingerprint string
	require.NoError(t, store.DB().NewRaw(`
		SELECT payload_json, observation_fingerprint
		FROM metering_facts WHERE source_event_key = ?`, invalidKey).Scan(ctx, &afterInvalidPayload, &afterInvalidFingerprint))
	require.Equal(t, `{"version":2,"id":"legacy-invalid","subject":{"kind":"account_window"}}`, afterInvalidPayload)
	require.Equal(t, beforeInvalidFingerprint, afterInvalidFingerprint)

	// Restoring the canonical source allows the same pending migration to be
	// retried successfully, as it would be on a later process start.
	_, err = store.DB().ExecContext(ctx,
		`UPDATE metering_facts SET payload_json = ? WHERE source_event_key = ?`,
		beforeInvalidPayload, invalidKey)
	require.NoError(t, err)
	require.NoError(t, journalstore.Migrate(ctx, store.DB()))
	require.NoError(t, store.DB().NewRaw(`
		SELECT COUNT(1) FROM bun_metering_journal_migrations WHERE name = ?`, journalstore.AccountWindowProjectionMigrationName).Scan(ctx, &migrationCount))
	require.Equal(t, 1, migrationCount)
}

func TestPhase11AccountWindowProjectionUpgrade_DoesNotPromoteGenericNonProviderRows(t *testing.T) {
	ctx := context.Background()
	store := newSQLiteJournal(t)
	provider := phase11JournalAccountWindowObservation(
		"upgrade-provider",
		"acct-upgrade",
		"pool-upgrade",
		"window-upgrade",
		time.Unix(1_000, 0).UTC(),
		time.Unix(10, 0).UTC(),
		phase11JournalMeasure("used_percent", "10"),
	)
	local := provider.Clone()
	local.ID = "upgrade-local"
	local.SourceEventKey = "upgrade-local-event"
	local.Origin = aipsdkmetering.OriginLocal
	local.Acquisition = aipsdkmetering.AcquisitionLocalTokenizer
	statement := provider.Clone()
	statement.ID = "upgrade-statement"
	statement.SourceEventKey = "upgrade-statement-event"
	statement.Origin = aipsdkmetering.OriginStatement
	statement.Acquisition = aipsdkmetering.AcquisitionStatementImporter
	statement.Authority = aipsdkmetering.AuthorityVerifiedStatement

	require.NoError(t, store.AppendAccountWindowObservation(ctx, provider))
	require.NoError(t, store.AppendObservation(ctx, local))
	require.NoError(t, store.AppendObservation(ctx, statement))
	for _, observation := range []aipsdkmetering.Observation{provider, local, statement} {
		clearAccountWindowSearchColumns(t, store, ctx, observation.ID)
	}
	removeAccountWindowMigrationRecord(t, store, ctx)

	require.NoError(t, journalstore.Migrate(ctx, store.DB()))
	page, err := store.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{
		ProviderAccountKey: provider.Subject.ProviderAccountKey,
		PoolID:             provider.Subject.PoolID,
		WindowID:           provider.Subject.WindowID,
		Limit:              10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1, "only provider-origin rows belong to the provider allowance projection")
	require.Equal(t, provider.ID, page.Observations[0].ID)
}

func TestPhase11AccountWindowProjectionUpgrade_FileReopenRunsPendingBackfill(t *testing.T) {
	ctx := context.Background()
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", filepath.ToSlash(filepath.Join(t.TempDir(), "metering.db")))
	firstSQL, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	firstBun, err := db.NewBunDB(firstSQL, db.DialectSQLite)
	require.NoError(t, err)
	first, err := journalstore.NewDurableStore(ctx, firstBun, journalstore.DurableConfig{StoreID: "upgrade-restart"})
	require.NoError(t, err)

	observation := phase11JournalAccountWindowObservation(
		"reopen-window",
		"acct-reopen",
		"pool-reopen",
		"window-reopen",
		time.Unix(3_000, 0).UTC(),
		time.Unix(12, 0).UTC(),
		phase11JournalMeasure("remaining_percent", "42.25"),
	)
	observation.Subject.StoreID = "upgrade-restart"
	observation.Correlation.StoreID = "upgrade-restart"
	require.NoError(t, first.AppendAccountWindowObservation(ctx, observation))
	clearAccountWindowSearchColumns(t, first, ctx, observation.ID)
	removeAccountWindowMigrationRecord(t, first, ctx)
	require.NoError(t, first.Close())
	require.NoError(t, firstSQL.Close())

	secondSQL, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondSQL.Close() })
	secondBun, err := db.NewBunDB(secondSQL, db.DialectSQLite)
	require.NoError(t, err)
	t.Cleanup(func() { _ = secondBun.Close() })
	second, err := journalstore.NewDurableStore(ctx, secondBun, journalstore.DurableConfig{StoreID: "upgrade-restart"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	page, err := second.ListAccountWindowObservations(ctx, journalstore.AccountWindowQuery{
		ProviderAccountKey: observation.Subject.ProviderAccountKey,
		PoolID:             observation.Subject.PoolID,
		WindowID:           observation.Subject.WindowID,
		Limit:              10,
	})
	require.NoError(t, err)
	require.Len(t, page.Observations, 1)
	require.Equal(t, observation.ID, page.Observations[0].ID)
	require.NoError(t, journalstore.Migrate(ctx, second.DB()), "reopen migration must remain idempotent")
}

//nolint:revive // test helper keeps t first per Go testing convention
func clearAccountWindowSearchColumns(t *testing.T, store *journalstore.DurableStore, ctx context.Context, observationID string) {
	t.Helper()
	_, err := store.DB().ExecContext(ctx, `
		UPDATE metering_facts SET
			stream_id = '', sequence = 0, payload_kind = 'fact', observation_id = '', observation_revision = 0,
			observation_subject_kind = '', observation_subject_id = '', observation_tenant_id = '',
			observation_origin = '', observation_acquisition = '',
			observation_provider_account_key = '', observation_pool_id = '',
			observation_window_id = '', observation_reset_at_unix = 0,
			observation_observed_at_unix = 0, observation_received_at_unix = 0
		WHERE observation_id = ?`, observationID)
	require.NoError(t, err)
}

//nolint:revive // test helper keeps t first per Go testing convention
func removeAccountWindowMigrationRecord(t *testing.T, store *journalstore.DurableStore, ctx context.Context) {
	t.Helper()
	_, err := store.DB().ExecContext(ctx,
		`DELETE FROM bun_metering_journal_migrations WHERE name = ?`,
		journalstore.AccountWindowProjectionMigrationName)
	require.NoError(t, err)
}
