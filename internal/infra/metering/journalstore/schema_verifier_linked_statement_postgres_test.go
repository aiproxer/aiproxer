//go:build integration

package journalstore_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/metering/journalstore"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

var journalIsolatedDatabaseSeq atomic.Uint64

// openIsolatedJournalPostgres gives each verifier test its own disposable
// PostgreSQL database. The admin endpoint is used only to create and drop that
// database; every migration, index mutation, and verification runs inside it,
// so the shared database schema is never modified and no per-connection schema
// selection state is involved. A unique database name per call keeps parallel
// and repeated runs isolated.
//
// The runtime DSN must be a URL DSN so the database name can be rewritten
// safely. A DSN that cannot be rewritten is reported unsupported; an endpoint
// without the CREATE DATABASE capability is reported unsupported only when the
// server says so (insufficient_privilege / feature_not_supported). Both cases
// skip when PostgreSQL is optional, but fail under AIP_REQUIRE_POSTGRES=1 so
// the required gate cannot pass with the negative cases silently absent. Any
// unexpected DDL error always fails; the helper never falls back to destructive
// shared-schema DDL.
func openIsolatedJournalPostgres(t *testing.T) *bun.DB {
	t.Helper()
	runtimeDSN := testkit.SkipUnlessPostgres(t)
	database := fmt.Sprintf("journalstore_test_%d_%d", time.Now().UnixNano(), journalIsolatedDatabaseSeq.Add(1))

	isolatedRuntimeDSN, ok := journalDSNWithDatabase(runtimeDSN, database)
	if !ok {
		refuseIsolatedJournal(t, "isolated journal verification requires a URL PostgreSQL runtime DSN whose database can be rewritten", nil)
	}

	adminDSN, ok := testkit.PostgresAdminDSN()
	if !ok {
		adminDSN = runtimeDSN
	}
	admin := testkit.OpenPostgresBunForTest(t, adminDSN, 1)
	quoted := quoteJournalPostgresIdentifier(database)
	if _, err := admin.ExecContext(context.Background(), "CREATE DATABASE "+quoted); err != nil {
		_ = admin.Close()
		refuseIsolatedJournal(t, "cannot create isolated journal database", err)
	}
	cleanup := func() {
		_, _ = admin.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+quoted+" WITH (FORCE)")
		_ = admin.Close()
	}

	bunDB, err := testkit.OpenPostgresBun(isolatedRuntimeDSN, 4)
	if err != nil {
		cleanup()
		t.Fatalf("open isolated journal database: %v", err)
	}
	var current string
	if err := bunDB.NewRaw(`SELECT current_database()`).Scan(context.Background(), &current); err != nil {
		_ = bunDB.Close()
		cleanup()
		t.Fatalf("read isolated journal current_database: %v", err)
	}
	if current != database {
		_ = bunDB.Close()
		cleanup()
		t.Fatalf("isolated database target mismatch: current_database=%q want %q", current, database)
	}
	t.Cleanup(func() {
		_ = bunDB.Close()
		cleanup()
	})
	return bunDB
}

// refuseIsolatedJournal reacts to a missing optional disposable-database
// capability. A legitimate capability absence skips only while PostgreSQL is
// optional; under AIP_REQUIRE_POSTGRES=1 it fails so the required gate cannot
// pass with the negative cases silently absent. An unexpected error always
// fails.
func refuseIsolatedJournal(t *testing.T, reason string, cause error) {
	t.Helper()
	if cause != nil && !isOptionalIsolatedDatabaseAbsence(cause) {
		t.Fatalf("%s: %v", reason, cause)
	}
	if testkit.PostgresRequired() {
		if cause != nil {
			t.Fatalf("%s (AIP_REQUIRE_POSTGRES=1 requires full negative coverage): %v", reason, cause)
		}
		t.Fatalf("%s (AIP_REQUIRE_POSTGRES=1 requires full negative coverage)", reason)
	}
	if cause != nil {
		t.Skipf("%s: %v (coverage limited to the existing non-mutating verifier tests)", reason, cause)
	}
	t.Skipf("%s (coverage limited to the existing non-mutating verifier tests)", reason)
}

func journalDSNWithDatabase(dsn, database string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		return dsn, false
	}
	u.Path = "/" + database
	return u.String(), true
}

func quoteJournalPostgresIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// TestJournalDSNWithDatabaseRewritesOnlyURLDSN pins the safety contract used by
// openIsolatedJournalPostgres: only a URL DSN whose database name can be
// rewritten is accepted; non-URL DSNs are reported unsupported so the caller
// skips before any DDL rather than mutating the shared database.
func TestJournalDSNWithDatabaseRewritesOnlyURLDSN(t *testing.T) {
	if _, ok := journalDSNWithDatabase("host=localhost user=x dbname=y", "s"); ok {
		t.Fatal("non-URL DSN must not report database rewrite support")
	}
	dsn, ok := journalDSNWithDatabase("postgresql://u:p@h:5432/db?sslmode=require", "iso_db")
	if !ok {
		t.Fatal("URL postgres DSN must report database rewrite support")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse rewritten DSN: %v", err)
	}
	if u.Path != "/iso_db" {
		t.Fatalf("rewritten DSN database = %q want %q", u.Path, "/iso_db")
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Fatalf("rewritten DSN lost query parameters: %q", dsn)
	}
}

// PostgreSQL VerifySchema must fail when a B-leg linked-statement accelerator
// index is absent: R10's bounded indexed statement lookup silently degrades to a
// large scan without it.
func TestVerifySchemaPostgresRequiresLinkedStatementIndexes(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{
		"idx_metering_facts_store_bleg",
		"idx_metering_facts_store_bleg_statement",
	} {
		t.Run(name, func(t *testing.T) {
			bunDB := openIsolatedJournalPostgres(t)
			require.NoError(t, journalstore.Migrate(ctx, bunDB))
			require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
			_, err := bunDB.ExecContext(ctx, `DROP INDEX IF EXISTS `+name)
			require.NoError(t, err)
			err = journalstore.VerifySchema(ctx, bunDB)
			require.Error(t, err, "missing %s must fail verification", name)
			require.Contains(t, err.Error(), name)
		})
	}
}

// Malformed B-leg index definitions must not satisfy verification even though
// the index name exists: wrong ordered key columns, a missing conjunct, an extra
// restriction, or a disjunction all break the production lookup shape.
func TestVerifySchemaPostgresRejectsMalformedLinkedStatementIndexes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		index  string
		mutate []string
	}{
		{
			name:  "bleg wrong columns",
			index: "idx_metering_facts_store_bleg",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg`,
				`CREATE INDEX idx_metering_facts_store_bleg ON metering_facts(store_id, b_leg_id) WHERE payload_kind = 'observation'`,
			},
		},
		{
			name:  "bleg_statement missing conjunct",
			index: "idx_metering_facts_store_bleg_statement",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg_statement`,
				`CREATE INDEX idx_metering_facts_store_bleg_statement
					ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
					WHERE payload_kind = 'observation'
						AND observation_subject_kind = 'statement_line'
						AND observation_origin = 'statement'
						AND observation_acquisition = 'statement_importer'`,
			},
		},
		{
			name:  "bleg_statement extra conjunct",
			index: "idx_metering_facts_store_bleg_statement",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg_statement`,
				`CREATE INDEX idx_metering_facts_store_bleg_statement
					ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
					WHERE payload_kind = 'observation'
						AND observation_subject_kind = 'statement_line'
						AND observation_origin = 'statement'
						AND observation_acquisition = 'statement_importer'
						AND authority = 'verified_statement'
						AND b_leg_id = 'one-leg'`,
			},
		},
		{
			name:  "bleg_statement disjunction",
			index: "idx_metering_facts_store_bleg_statement",
			mutate: []string{
				`DROP INDEX IF EXISTS idx_metering_facts_store_bleg_statement`,
				`CREATE INDEX idx_metering_facts_store_bleg_statement
					ON metering_facts(store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id)
					WHERE payload_kind = 'observation'
						AND observation_subject_kind = 'statement_line'
						AND observation_origin = 'statement'
						AND observation_acquisition = 'statement_importer'
						OR authority = 'verified_statement'`,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bunDB := openIsolatedJournalPostgres(t)
			require.NoError(t, journalstore.Migrate(ctx, bunDB))
			require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
			for _, stmt := range tc.mutate {
				_, err := bunDB.ExecContext(ctx, stmt)
				require.NoError(t, err)
			}
			err := journalstore.VerifySchema(ctx, bunDB)
			require.Error(t, err, "malformed %s must fail verification", tc.name)
			require.Contains(t, err.Error(), tc.index)
		})
	}
}

// A migration-history row alone is not proof of a live index; conversely, a
// missing linked-statement migration record must fail verification.
//
// PresenceBooleanRepairMigrationName is in this list because its history check
// used to exist on SQLite only. The PostgreSQL side hand-listed every other
// migration and silently omitted this one, so a PostgreSQL database whose row
// for the presence-boolean repair was missing still passed verification. The
// checks are now derived from RequiredMigrationNames, which is what makes the
// omission impossible to reintroduce; this case is the proof that it is closed.
func TestVerifySchemaPostgresRequiresLinkedStatementMigrationHistory(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{
		journalstore.PresenceBooleanRepairMigrationName,
		journalstore.LinkedStatementIndexMigrationName,
		journalstore.LinkedStatementOrderedIndexMigrationName,
		journalstore.LinkedStatementCandidateIndexMigrationName,
	} {
		t.Run(name, func(t *testing.T) {
			bunDB := openIsolatedJournalPostgres(t)
			require.NoError(t, journalstore.Migrate(ctx, bunDB))
			require.NoError(t, journalstore.VerifySchema(ctx, bunDB))
			_, err := bunDB.NewRaw(`DELETE FROM bun_metering_journal_migrations WHERE name = ?`, name).Exec(ctx)
			require.NoError(t, err)
			err = journalstore.VerifySchema(ctx, bunDB)
			require.Error(t, err, "missing migration history %s must fail verification", name)
			require.Contains(t, err.Error(), name)
		})
	}
}
