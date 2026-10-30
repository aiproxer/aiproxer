//go:build integration

package journalstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestPhase34_Postgres_DirectPooled_CorrectionAggregateIdentical(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	ensureDirectJournalSchema(t, adminDSN)
	ensurePooledJournalSchema(t, adminDSN)

	ctx := context.Background()
	storeID := testkit.UniquePostgresStoreID("p34-parity")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentJournal)
	})

	direct := newPostgresJournal(t, adminDSN, storeID)
	seedPhase34CorrectionStream(t, direct)

	pooled, _ := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)
	pageDirect, err := direct.List(ctx, metering.Query{StreamID: "stream-parity", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	pagePooled, err := pooled.List(ctx, metering.Query{StreamID: "stream-parity", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	snapD, err := aggregate.Apply(pageDirect.Facts)
	if err != nil {
		t.Fatal(err)
	}
	snapP, err := aggregate.Apply(pagePooled.Facts)
	if err != nil {
		t.Fatal(err)
	}
	if snapD.Quantities[metering.ComponentOutputToken] != 7 ||
		snapP.Quantities[metering.ComponentOutputToken] != 7 {
		t.Fatalf("direct=%v pooled=%v want output=7", snapD.Quantities, snapP.Quantities)
	}
}

func TestPhase34_Postgres_VerifySchemaV2(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ensureDirectJournalSchema(t, dsn)
	bunDB := testkit.OpenPostgresBunForTest(t, dsn, 2)
	t.Cleanup(func() { _ = bunDB.Close() })
	if err := journalstore.VerifySchema(context.Background(), bunDB); err != nil {
		t.Fatalf("VerifySchema V2: %v", err)
	}
}

func TestPhase34_PostgresPooled_VerifySchemaFailsWhenV2IndexMissing(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	pooledJournalTestMu.Lock(t)
	ensurePooledJournalSchema(t, adminDSN)
	runtime := sharedPooledJournalRuntime(t, runtimeDSN)
	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 2)
	t.Cleanup(func() { _ = admin.Close() })

	for _, name := range journalstore.V2BoundedIndexNames {
		t.Run(name, func(t *testing.T) {
			restoreSQL := restoreV2IndexSQL(name)
			dropSQL := `DROP INDEX IF EXISTS ` + name
			// metering_facts_store_id_key backs the store-scoped FK
			// metering_components(store_id, observation_row_id) ->
			// metering_facts(store_id, id). Plain DROP INDEX is rejected
			// with SQLSTATE 2BP01; drop through the owning FK constraint
			// and recreate both to preserve schema integrity.
			isFKBacked := name == "metering_facts_store_id_key"
			t.Cleanup(func() {
				restoreCtx, cancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
				defer cancel()
				if isFKBacked {
					_, _ = admin.ExecContext(restoreCtx, restoreV2IndexSQL(name))
					_, _ = admin.ExecContext(restoreCtx, `ALTER TABLE metering_components DROP CONSTRAINT IF EXISTS metering_components_store_id_observation_row_id_fkey`)
					_, _ = admin.ExecContext(restoreCtx, `ALTER TABLE metering_components ADD CONSTRAINT metering_components_store_id_observation_row_id_fkey FOREIGN KEY (store_id, observation_row_id) REFERENCES metering_facts(store_id, id)`)
					// Ensure index exists after FK recreation (FK does not create it).
					_, _ = admin.ExecContext(restoreCtx, restoreV2IndexSQL(name))
				} else {
					_, _ = admin.ExecContext(restoreCtx, restoreSQL)
				}
			})
			mutateCtx, cancel := context.WithTimeout(context.Background(), pooledJournalOpenTimeout)
			defer cancel()
			if isFKBacked {
				if _, err := admin.ExecContext(mutateCtx, `ALTER TABLE metering_components DROP CONSTRAINT IF EXISTS metering_components_store_id_observation_row_id_fkey`); err != nil {
					t.Fatalf("drop FK for %s: %v", name, err)
				}
			}
			if _, err := admin.ExecContext(mutateCtx, dropSQL); err != nil {
				t.Fatalf("drop %s: %v", name, err)
			}
			verifyCtx, verifyCancel := context.WithTimeout(t.Context(), pooledJournalOpenTimeout)
			defer verifyCancel()
			err := journalstore.VerifySchema(verifyCtx, runtime)
			if err == nil {
				t.Fatalf("VerifySchema must fail after dropping %s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("VerifySchema error %q must mention %s", err.Error(), name)
			}
			if isFKBacked {
				_, _ = admin.ExecContext(mutateCtx, restoreV2IndexSQL(name))
				_, _ = admin.ExecContext(mutateCtx, `ALTER TABLE metering_components ADD CONSTRAINT metering_components_store_id_observation_row_id_fkey FOREIGN KEY (store_id, observation_row_id) REFERENCES metering_facts(store_id, id)`)
			} else {
				_, _ = admin.ExecContext(mutateCtx, restoreSQL)
			}
		})
	}
}

func TestPhase34_Postgres_ConcurrentFactIdentityAppendRace(t *testing.T) {
	dsn := testkit.SkipUnlessPostgres(t)
	ensureDirectJournalSchema(t, dsn)
	storeID := testkit.UniquePostgresStoreID("p34-race-direct")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSNForCleanup(dsn), storeID, testkit.PostgresComponentJournal)
	})
	store := newPostgresJournal(t, dsn, storeID)
	runFactIdentityRace(t, store, storeID+"-stream", storeID+"-fact")
}

func TestPhase34_PostgresPooled_ConcurrentFactIdentityAppendRace(t *testing.T) {
	adminDSN, runtimeDSN := testkit.SkipUnlessPostgresPooled(t)
	ensurePooledJournalSchema(t, adminDSN)
	storeID := testkit.UniquePostgresStoreID("p34-race-pooled")
	t.Cleanup(func() {
		testkit.CleanupPostgresStoreByID(t, adminDSN, storeID, testkit.PostgresComponentJournal)
	})
	store, _ := openSharedPooledJournalStore(t, adminDSN, runtimeDSN, storeID)
	runFactIdentityRace(t, store, storeID+"-stream", storeID+"-fact")
}

func restoreV2IndexSQL(name string) string {
	switch name {
	case "metering_facts_store_stream_fact_id_key":
		return `CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_stream_fact_id_key ON metering_facts(store_id, stream_id, fact_id)`
	case "idx_metering_facts_store_stream_seq":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_stream_seq ON metering_facts(store_id, stream_id, sequence)`
	case "idx_metering_facts_store_attempt":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_attempt ON metering_facts(store_id, attempt_id) WHERE attempt_id <> ''`
	case "idx_metering_facts_store_recorded":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_recorded ON metering_facts(store_id, recorded_at_unix)`
	case "idx_metering_facts_store_account_window":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_account_window ON metering_facts(store_id, observation_provider_account_key, observation_pool_id, observation_window_id, observation_reset_at_unix, observation_observed_at_unix, observation_received_at_unix, stream_id, sequence, observation_id, observation_revision, id) WHERE payload_kind = 'observation' AND observation_subject_kind = 'account_window'`
	case "idx_metering_facts_store_plane":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_plane ON metering_facts(store_id, perspective, boundary, lifecycle_scope)`
	case "idx_metering_fact_supersessions_to":
		return `CREATE INDEX IF NOT EXISTS idx_metering_fact_supersessions_to ON metering_fact_supersessions(store_id, stream_id, to_fact_id)`
	case "metering_facts_store_id_key":
		return `CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_id_key ON metering_facts(store_id, id)`
	case "metering_facts_store_observation_revision_key":
		return `CREATE UNIQUE INDEX IF NOT EXISTS metering_facts_store_observation_revision_key ON metering_facts(store_id, observation_id, observation_revision) WHERE payload_kind = 'observation' AND observation_id <> ''`
	case "idx_metering_components_store_subject":
		return `CREATE INDEX IF NOT EXISTS idx_metering_components_store_subject ON metering_components(store_id, subject_kind, subject_id, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`
	case "idx_metering_components_store_component":
		return `CREATE INDEX IF NOT EXISTS idx_metering_components_store_component ON metering_components(store_id, component_key_hash, component_key, subject_kind, subject_id, observation_id, observation_revision, item_id)`
	case "idx_metering_components_store_provider_account":
		return `CREATE INDEX IF NOT EXISTS idx_metering_components_store_provider_account ON metering_components(store_id, provider_account_key, stream_id, sequence, observation_id, observation_revision, item_kind, item_id)`
	case "idx_metering_components_observation":
		return `CREATE INDEX IF NOT EXISTS idx_metering_components_observation ON metering_components(observation_row_id, item_kind, item_id)`
	case "idx_metering_facts_store_bleg":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_bleg ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id) WHERE payload_kind = 'observation'`
	case "idx_metering_facts_store_bleg_statement":
		return `CREATE INDEX IF NOT EXISTS idx_metering_facts_store_bleg_statement ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id) WHERE payload_kind = 'observation' AND observation_subject_kind = 'statement_line' AND observation_origin = 'statement' AND observation_acquisition = 'statement_importer' AND authority = 'verified_statement'`
	default:
		return `SELECT 1`
	}
}

func seedPhase34CorrectionStream(t *testing.T, store *journalstore.DurableStore) {
	t.Helper()
	ctx := context.Background()
	base := validFact("base-parity", "stream-parity", 1)
	base.IdentityVersion = metering.IdentityVersionV1
	base.SourceID = "base-parity"
	base.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: 10, Present: true,
	}}
	corr := validFact("corr-parity", "stream-parity", 2)
	corr.Kind = metering.FactKindCorrection
	corr.IdentityVersion = metering.IdentityVersionV1
	corr.SourceID = "corr-parity"
	corr.Supersedes = []string{"base-parity"}
	corr.Quantities = []metering.Quantity{{
		Component: metering.ComponentOutputToken, Unit: metering.UnitToken, Value: -3, Present: true,
	}}
	if err := store.Append(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(ctx, corr); err != nil {
		t.Fatal(err)
	}
}
