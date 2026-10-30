package journalstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	dbinfra "github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
	_ "modernc.org/sqlite" // register sqlite driver for durable metering journals
)

// DurableConfig configures a Bun-backed metering journal.
type DurableConfig struct {
	StoreID         string
	DefaultPageSize int
	MaxPageSize     int
	// ObservationOutboxMaxPending bounds unacknowledged observation relay
	// entries. Zero uses the safe default; a negative value is rejected.
	ObservationOutboxMaxPending int
	Now                         func() time.Time
	SQLiteRetryNow              func() time.Time
	SQLiteRetrySleep            func(context.Context, time.Duration) error
	SQLiteRetryObserver         SQLiteRetryObserver
	// ObservationFaultHook is an infra-only test/failpoint seam. Returning an
	// error rolls back the caller-owned observation transaction.
	ObservationFaultHook func(string) error
	// ObservationIdentityMint mints this journal's opaque observation
	// identity. Nil uses the crypto-backed default. It is a constructor
	// seam so an owner can fail the open closed deterministically instead of
	// publishing a journal whose identity cannot be proven.
	ObservationIdentityMint func() (metering.ObservationJournalID, error)
}

// DurableStore persists metering facts via Bun (SQLite or Postgres).
type DurableStore struct {
	cfg             DurableConfig
	db              *bun.DB
	defaultPageSize int
	maxPageSize     int
	now             func() time.Time
	nonOwning       bool
	// journalID is the opaque identity of this opened journal. It is minted
	// per open and never derived from the store identity, so two distinct
	// journals over the same store never share it.
	journalID metering.ObservationJournalID
	// legacyObservationKeysProbe runs once per store instance and decides
	// whether per-append legacy identity lookups are needed at all; a store
	// that never saw legacy keys pays nothing for them after the probe.
	legacyObservationKeysProbe   sync.Once
	mayHoldLegacyObservationKeys bool
}

// Migrate applies the metering-journal schema on an admin connection.
func Migrate(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return fmt.Errorf("metering/journalstore: nil bun db")
	}
	return runSchemaMigrate(ctx, db)
}

// RequiredMigrationNames are bun history names that must be present after a
// successful Migrate. Collapsed legacy histories may retain extra baseline rows.
var RequiredMigrationNames = []string{
	BaselineMigrationName,
	StoreScopedSourceKeyMigrationName,
	StoreScopedFiltersMigrationName,
	SchemaV2MigrationName,
	ObservationProjectionMigrationName,
	AccountWindowProjectionMigrationName,
	ObservationEconomicOutboxMigrationName,
	PresenceBooleanRepairMigrationName,
	LinkedStatementIndexMigrationName,
	LinkedStatementOrderedIndexMigrationName,
	LinkedStatementCandidateIndexMigrationName,
}

// VerifySchema checks required runtime relations without applying migrations.
func VerifySchema(ctx context.Context, db *bun.DB) error {
	if ctx == nil {
		return fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return fmt.Errorf("metering/journalstore: nil bun db")
	}
	if db.Dialect().Name() != dialect.PG {
		for _, probe := range []string{
			`SELECT identity_version, source_revision, source_event_kind, source_id, payload_kind, observation_id, observation_revision, observation_fingerprint, observation_subject_kind, observation_subject_id, observation_tenant_id, observation_origin, observation_acquisition, observation_provider_account_key, observation_pool_id, observation_window_id, observation_reset_at_unix, observation_observed_at_unix, observation_received_at_unix FROM metering_facts WHERE 1 = 0`,
			`SELECT store_id FROM metering_fact_filters WHERE 1 = 0`,
			`SELECT 1 FROM metering_fact_supersessions WHERE 1 = 0`,
			`SELECT store_id, observation_row_id, item_kind, component_key, component_key_hash, coefficient, scale, value_present, money_present, charge_coverage_json, subject_kind, subject_id, tenant_id, provider_account_key, projection_version FROM metering_components WHERE 1 = 0`,
			`SELECT store_id, observation_id, observation_revision, observation_fingerprint, payload_json, status, attempt_count, next_attempt_at_unix, lease_owner, lease_until_unix, last_error, created_at_unix, updated_at_unix FROM metering_observation_economic_outbox WHERE 1 = 0`,
		} {
			if _, err := db.ExecContext(ctx, probe); err != nil {
				return fmt.Errorf("metering/journalstore: schema verification failed: %w", err)
			}
		}
		var filtersStoreID int
		if err := db.NewRaw(
			`SELECT COUNT(1) FROM pragma_table_info('metering_fact_filters') WHERE name = 'store_id'`,
		).Scan(ctx, &filtersStoreID); err != nil {
			return fmt.Errorf("metering/journalstore: schema verification failed: metering_fact_filters.store_id: %w", err)
		}
		if filtersStoreID != 1 {
			return fmt.Errorf("metering/journalstore: schema verification failed: missing column metering_fact_filters.store_id")
		}
		for _, name := range V2BoundedIndexNames {
			var n int
			if err := db.NewRaw(
				`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`,
				name,
			).Scan(ctx, &n); err != nil {
				return fmt.Errorf("metering/journalstore: schema verification failed: %s: %w", name, err)
			}
			if n != 1 {
				return fmt.Errorf("metering/journalstore: schema verification failed: missing index %s", name)
			}
		}
		var outboxIndex int
		if err := db.NewRaw(`SELECT COUNT(1) FROM sqlite_master WHERE type = 'index' AND name = ?`, ObservationEconomicOutboxPendingIndexName).Scan(ctx, &outboxIndex); err != nil {
			return fmt.Errorf("metering/journalstore: schema verification failed: %s: %w", ObservationEconomicOutboxPendingIndexName, err)
		}
		if outboxIndex != 1 {
			return fmt.Errorf("metering/journalstore: schema verification failed: missing index %s", ObservationEconomicOutboxPendingIndexName)
		}
		for _, name := range RequiredMigrationNames {
			var n int
			if err := db.NewRaw(
				`SELECT COUNT(1) FROM bun_metering_journal_migrations WHERE name = ?`,
				name,
			).Scan(ctx, &n); err != nil {
				return fmt.Errorf("metering/journalstore: schema verification failed: migration %s: %w", name, err)
			}
			if n < 1 {
				return fmt.Errorf("metering/journalstore: schema verification failed: missing migration %s", name)
			}
		}
		return nil
	}
	for _, probe := range []string{
		`SELECT identity_version, source_revision, source_event_kind, source_id, payload_kind, observation_id, observation_revision, observation_fingerprint, observation_subject_kind, observation_subject_id, observation_tenant_id, observation_origin, observation_acquisition, observation_provider_account_key, observation_pool_id, observation_window_id, observation_reset_at_unix, observation_observed_at_unix, observation_received_at_unix FROM metering_facts WHERE 1 = 0`,
		`SELECT * FROM metering_fact_filters WHERE 1 = 0`,
		`SELECT * FROM metering_fact_supersessions WHERE 1 = 0`,
		`SELECT * FROM metering_components WHERE 1 = 0`,
		`SELECT * FROM metering_observation_economic_outbox WHERE 1 = 0`,
	} {
		if _, err := db.ExecContext(ctx, probe); err != nil {
			return fmt.Errorf("metering/journalstore: schema verification failed: %w", err)
		}
	}
	checks := []struct {
		description string
		query       string
		args        []any
		fragments   []string
	}{
		{
			description: "metering_facts store-scoped source_event_key unique constraint",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'metering_facts'
  AND c.contype = 'u'
  AND c.conname = 'metering_facts_store_source_event_key_key'
LIMIT 1`,
			fragments: []string{"unique (store_id, source_event_key)"},
		},
		{
			description: "idx_metering_facts_stream_seq",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_stream_seq'
LIMIT 1`,
			fragments: []string{"(stream_id, sequence)"},
		},
		{
			description: "idx_metering_facts_request",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_request'
LIMIT 1`,
			fragments: []string{"(request_id)", "request_id <> ''"},
		},
		{
			description: "idx_metering_fact_filters_field",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_fact_filters'
  AND indexname = 'idx_metering_fact_filters_field'
LIMIT 1`,
			fragments: []string{"(store_id, field_name, field_value, stream_id)"},
		},
		{
			description: "metering_fact_filters.store_id",
			query: `SELECT lower(column_name) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'metering_fact_filters'
  AND column_name = 'store_id'
LIMIT 1`,
			fragments: []string{"store_id"},
		},
		{
			description: "metering_facts V2 identity columns",
			query: `SELECT lower(string_agg(column_name, ',' ORDER BY column_name)) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'metering_facts'
  AND column_name IN ('identity_version','source_revision','source_event_kind','source_id')`,
			fragments: []string{"identity_version", "source_event_kind", "source_id", "source_revision"},
		},
		{
			description: "metering_facts_store_stream_fact_id_key",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'metering_facts_store_stream_fact_id_key'
LIMIT 1`,
			fragments: []string{"unique index", "(store_id, stream_id, fact_id)"},
		},
		{
			description: "idx_metering_facts_store_stream_seq",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_store_stream_seq'
LIMIT 1`,
			fragments: []string{"(store_id, stream_id, sequence)"},
		},
		{
			description: "idx_metering_facts_store_attempt",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_store_attempt'
LIMIT 1`,
			fragments: []string{"(store_id, attempt_id)"},
		},
		{
			description: "idx_metering_facts_store_recorded",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_store_recorded'
LIMIT 1`,
			fragments: []string{"(store_id, recorded_at_unix)"},
		},
		{
			description: "idx_metering_facts_store_plane",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = 'idx_metering_facts_store_plane'
LIMIT 1`,
			fragments: []string{"(store_id, perspective, boundary, lifecycle_scope)"},
		},
		{
			description: "idx_metering_fact_supersessions_to",
			query: `SELECT lower(indexdef) FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_fact_supersessions'
  AND indexname = 'idx_metering_fact_supersessions_to'
LIMIT 1`,
			fragments: []string{"(store_id, stream_id, to_fact_id)"},
		},
		{
			description: "metering_fact_supersessions edge unique",
			query: `SELECT lower(pg_get_constraintdef(c.oid)) FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = current_schema()
  AND t.relname = 'metering_fact_supersessions'
  AND c.contype = 'u'
  AND c.conname = 'metering_fact_supersessions_edge_key'
LIMIT 1`,
			fragments: []string{"unique (store_id, stream_id, from_fact_id, to_fact_id)"},
		},
		{
			description: "metering observation economic outbox table",
			query:       `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'metering_observation_economic_outbox' LIMIT 1`,
			fragments:   []string{"metering_observation_economic_outbox"},
		},
		{
			description: "metering observation economic outbox pending index",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'metering_observation_economic_outbox' AND indexname = '` + ObservationEconomicOutboxPendingIndexName + `' LIMIT 1`,
			fragments:   []string{"store_id", "status", "next_attempt_at_unix", "lease_until_unix", "created_at_unix", "id"},
		},
		{
			description: "metering_facts V2 observation columns",
			query: `SELECT lower(string_agg(column_name, ',' ORDER BY column_name)) FROM information_schema.columns
WHERE table_schema = current_schema()
  AND table_name = 'metering_facts'
  AND column_name IN ('payload_kind','observation_id','observation_revision','observation_fingerprint','observation_subject_kind','observation_subject_id','observation_tenant_id','observation_origin','observation_acquisition','observation_provider_account_key','observation_pool_id','observation_window_id','observation_reset_at_unix','observation_observed_at_unix','observation_received_at_unix')`,
			fragments: []string{"observation_acquisition", "observation_fingerprint", "observation_id", "observation_origin", "observation_observed_at_unix", "observation_pool_id", "observation_provider_account_key", "observation_received_at_unix", "observation_reset_at_unix", "observation_revision", "observation_subject_id", "observation_subject_kind", "observation_tenant_id", "observation_window_id", "payload_kind"},
		},
		{
			description: "metering_components table",
			query:       `SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'metering_components' LIMIT 1`,
			fragments:   []string{"metering_components"},
		},
		{
			description: "metering_facts_store_id_key",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'metering_facts_store_id_key' LIMIT 1`,
			fragments:   []string{"unique index", "(store_id, id)"},
		},
		{
			description: "metering_facts_store_observation_revision_key",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'metering_facts_store_observation_revision_key' LIMIT 1`,
			fragments:   []string{"unique index", "(store_id, observation_id, observation_revision)"},
		},
		{
			description: "idx_metering_components_store_subject",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_metering_components_store_subject' LIMIT 1`,
			fragments:   []string{"store_id", "subject_kind", "subject_id", "stream_id"},
		},
		{
			description: "idx_metering_components_store_component",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_metering_components_store_component' LIMIT 1`,
			fragments:   []string{"store_id", "component_key_hash", "component_key"},
		},
		{
			description: "idx_metering_components_store_provider_account",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_metering_components_store_provider_account' LIMIT 1`,
			fragments:   []string{"store_id", "provider_account_key", "stream_id"},
		},
		{
			description: "idx_metering_components_observation",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND indexname = 'idx_metering_components_observation' LIMIT 1`,
			fragments:   []string{"(observation_row_id, item_kind, item_id)"},
		},
		{
			description: "idx_metering_facts_store_account_window",
			query:       `SELECT lower(indexdef) FROM pg_indexes WHERE schemaname = current_schema() AND tablename = 'metering_facts' AND indexname = 'idx_metering_facts_store_account_window' LIMIT 1`,
			fragments:   []string{"store_id", "observation_provider_account_key", "observation_pool_id", "observation_window_id", "observation_reset_at_unix", "observation_observed_at_unix", "observation_received_at_unix"},
		},
	}
	// One migration-history check per RequiredMigrationNames entry, generated
	// rather than written out. This list and RequiredMigrationNames are the same
	// requirement stated twice, once per dialect, and maintaining both by hand let
	// them drift: PresenceBooleanRepairMigrationName was required on SQLite and
	// absent here, so a PostgreSQL database whose history row for that repair was
	// missing still passed verification. Deriving this side from
	// RequiredMigrationNames makes that impossible to reintroduce, because a new
	// migration becomes verified on both dialects by being added in one place.
	//
	// A history row is not proof that the migration's effects exist - the index
	// and column checks above and below are - but the converse matters: a
	// database missing the row never ran that repair, and saying so is what stops
	// a partially migrated database from being certified.
	for _, name := range RequiredMigrationNames {
		checks = append(checks, struct {
			description string
			query       string
			args        []any
			fragments   []string
		}{
			description: name + " migration history",
			query:       `SELECT name FROM bun_metering_journal_migrations WHERE name = ? LIMIT 1`,
			args:        []any{name},
			fragments:   []string{name},
		})
	}
	for _, check := range checks {
		if err := dbinfra.VerifyPostgresQueryRowContains(ctx, db, check.description, check.query, check.args, check.fragments...); err != nil {
			return fmt.Errorf("metering/journalstore: schema verification failed: %w", err)
		}
	}
	// R10's bounded linked-statement lookup needs its accelerators to be exactly
	// the ordered partial indexes the production query was planned against.
	// Fragment/containment checks are unsafe here: an extra restriction (AND
	// b_leg_id = 'one-leg') or a disjunction would keep the name and leading
	// columns yet break the general B-leg lookup, so compare the whole ordered
	// key list and the complete conjunctive predicate.
	indexChecks := []struct {
		description string
		query       string
		columns     string
		predicates  []string
	}{
		{
			description: meteringFactsStoreBLegIndex,
			query: `SELECT indexdef FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = '` + meteringFactsStoreBLegIndex + `'
LIMIT 1`,
			columns:    "store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id",
			predicates: []string{"payload_kind = 'observation'"},
		},
		{
			description: meteringFactsStoreBLegStatementIndex,
			query: `SELECT indexdef FROM pg_indexes
WHERE schemaname = current_schema()
  AND tablename = 'metering_facts'
  AND indexname = '` + meteringFactsStoreBLegStatementIndex + `'
LIMIT 1`,
			columns: "store_id, b_leg_id, stream_id, sequence, observation_id, observation_revision, id",
			predicates: []string{
				"payload_kind = 'observation'",
				"observation_subject_kind = 'statement_line'",
				"observation_origin = 'statement'",
				"observation_acquisition = 'statement_importer'",
				"authority = 'verified_statement'",
			},
		},
	}
	for _, check := range indexChecks {
		if err := verifyPostgresIndexDefinition(ctx, db, check.description, check.query, check.columns, check.predicates); err != nil {
			return fmt.Errorf("metering/journalstore: schema verification failed: %w", err)
		}
	}
	return nil
}

// postgresIndexCastPattern strips PostgreSQL's rendered type casts (for example
// 'observation'::text) so index expressions can be compared semantically.
var postgresIndexCastPattern = regexp.MustCompile(`::[a-z_][a-z0-9_]*`)

// verifyPostgresIndexDefinition checks that a pg_indexes indexdef declares
// exactly the wantColumns ordered key list and exactly the wantPredicates
// conjunctive partial predicate. It deliberately avoids substring matching so
// that an extra restriction or a disjunction cannot satisfy verification.
func verifyPostgresIndexDefinition(ctx context.Context, db *bun.DB, description, query, wantColumns string, wantPredicates []string) error {
	var raw string
	if err := db.QueryRowContext(ctx, query).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("verify postgres %s: missing", description)
		}
		return fmt.Errorf("verify postgres %s: %w", description, err)
	}
	if err := checkPostgresIndexDefinition(raw, wantColumns, wantPredicates); err != nil {
		return fmt.Errorf("verify postgres %s: %w", description, err)
	}
	return nil
}

// checkPostgresIndexDefinition is the pure core of
// verifyPostgresIndexDefinition: it compares a rendered index definition against
// the expected ordered key list, the complete conjunctive predicate, and the
// required btree access method.
func checkPostgresIndexDefinition(raw, wantColumns string, wantPredicates []string) error {
	columns, predicates, err := parsePostgresIndexDefinition(raw)
	if err != nil {
		return err
	}
	wantColumns = canonicalizePostgresIndexExpression(wantColumns)
	if columns != wantColumns {
		return fmt.Errorf("key columns %q, want %q", columns, wantColumns)
	}
	want := make(map[string]struct{}, len(wantPredicates))
	for _, predicate := range wantPredicates {
		want[canonicalizePostgresIndexExpression(predicate)] = struct{}{}
	}
	got := make(map[string]struct{}, len(predicates))
	for _, predicate := range predicates {
		got[predicate] = struct{}{}
	}
	if len(got) != len(predicates) || len(got) != len(wantPredicates) {
		return fmt.Errorf("partial predicate %v, want %v", predicates, wantPredicates)
	}
	for predicate := range want {
		if _, ok := got[predicate]; !ok {
			return fmt.Errorf("partial predicate %v, want %v", predicates, wantPredicates)
		}
	}
	return nil
}

// postgresIndexBtreeMethod is the only access method that can serve the ordered
// partial B-leg lookups VerifySchema pins. brin/gist/hash/gin indexes answer
// containment or equality probes but cannot return rows in key order, so the
// planned bounded "ORDER BY sequence, ..., id" scan is not available to them and
// the lookup silently degrades to a store scan.
const postgresIndexBtreeMethod = "btree"

// postgresIndexAccessMethod reports the access method named by the "USING
// <method>" clause of an already lower-cased indexdef prefix (the text preceding
// the key list). The clause is optional: PostgreSQL defaults to btree when it is
// omitted, and "using" is a reserved word so an unquoted occurrence can only be
// the clause keyword. An omitted clause is therefore reported as btree instead of
// failing verification, so definitions that render without it still verify.
func postgresIndexAccessMethod(lowerPrefix string) string {
	fields := strings.Fields(lowerPrefix)
	for i, field := range fields {
		if field == "using" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return postgresIndexBtreeMethod
}

// parsePostgresIndexDefinition extracts the ordered key columns and the
// conjunctive partial predicate from a pg_indexes indexdef. Both are returned
// canonicalized; the predicate is split only on top-level "and" conjunctions, so
// a disjunction stays inside one element and fails set comparison. The access
// method declared before the key list is validated too: a non-btree index with
// an otherwise identical ordered key list and predicate cannot serve the
// ordered lookup, so it must fail rather than pass.
func parsePostgresIndexDefinition(raw string) (string, []string, error) {
	lower := strings.ToLower(raw)
	open := strings.IndexByte(lower, '(')
	if open < 0 {
		return "", nil, fmt.Errorf("unsupported index definition %q", raw)
	}
	if method := postgresIndexAccessMethod(lower[:open]); method != postgresIndexBtreeMethod {
		return "", nil, fmt.Errorf(
			"index access method %q is not supported, want %q: %q",
			method, postgresIndexBtreeMethod, raw,
		)
	}
	closeIdx := matchingParenIndex(lower, open)
	if closeIdx < 0 {
		return "", nil, fmt.Errorf("unsupported index definition %q", raw)
	}
	columns := canonicalizePostgresIndexExpression(raw[open+1 : closeIdx])
	rest := raw[closeIdx+1:]
	whereIdx := strings.Index(strings.ToLower(rest), " where ")
	if whereIdx < 0 {
		return columns, nil, nil
	}
	predicate := canonicalizePostgresIndexExpression(rest[whereIdx+len(" where "):])
	if predicate == "" {
		return columns, nil, fmt.Errorf("empty partial predicate in %q", raw)
	}
	return columns, strings.Split(predicate, " and "), nil
}

func matchingParenIndex(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// canonicalizePostgresIndexExpression lower-cases, drops rendered type casts,
// removes parentheses, and collapses whitespace so pg_indexes rendering can be
// compared without depending on its exact formatting. Syntax normalization is
// applied only outside single-quoted SQL literals: literal bytes (case,
// embedded cast-looking text, doubled quotes, and spaces) are preserved exactly.
func canonicalizePostgresIndexExpression(expr string) string {
	masked, literals := maskPostgresStringLiterals(expr)
	masked = strings.ToLower(masked)
	masked = postgresIndexCastPattern.ReplaceAllString(masked, "")
	masked = strings.NewReplacer("(", " ", ")", " ").Replace(masked)
	masked = strings.Join(strings.Fields(masked), " ")
	for i, literal := range literals {
		masked = strings.ReplaceAll(masked, postgresLiteralPlaceholder(i), literal)
	}
	return masked
}

func postgresLiteralPlaceholder(i int) string {
	return "\x00lit" + strconv.Itoa(i) + "\x00"
}

// maskPostgresStringLiterals replaces each single-quoted SQL literal with a
// whitespace-free placeholder and returns the literal bytes verbatim. A doubled
// single quote inside a literal is treated as an escaped quote. An unterminated
// literal is preserved to end of input.
func maskPostgresStringLiterals(expr string) (string, []string) {
	var b strings.Builder
	var literals []string
	for i := 0; i < len(expr); {
		if expr[i] != '\'' {
			b.WriteByte(expr[i])
			i++
			continue
		}
		j := i + 1
		for j < len(expr) {
			if expr[j] != '\'' {
				j++
				continue
			}
			if j+1 < len(expr) && expr[j+1] == '\'' {
				j += 2
				continue
			}
			break
		}
		end := min(j, len(expr)-1)
		literals = append(literals, expr[i:end+1])
		b.WriteString(postgresLiteralPlaceholder(len(literals) - 1))
		i = end + 1
	}
	return b.String(), literals
}

// NewDurableStore migrates schema and returns a durable journal. Caller owns closing db
// via DurableStore.Close (closes the bun handle).
func NewDurableStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("metering/journalstore: nil bun db")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("metering/journalstore: durable store id is required")
	}
	if err := runSchemaMigrate(ctx, db); err != nil {
		return nil, fmt.Errorf("metering/journalstore: migrate: %w", err)
	}
	return openStore(ctx, db, cfg, false)
}

// OpenStore opens a journal without migrations and without taking ownership
// of db. The composition root owns the shared runtime pool.
func OpenStore(ctx context.Context, db *bun.DB, cfg DurableConfig) (*DurableStore, error) {
	return openStore(ctx, db, cfg, true)
}

func openStore(ctx context.Context, db *bun.DB, cfg DurableConfig, nonOwning bool) (*DurableStore, error) {
	if ctx == nil {
		return nil, fmt.Errorf("metering/journalstore: nil context")
	}
	if db == nil {
		return nil, fmt.Errorf("metering/journalstore: nil bun db")
	}
	if strings.TrimSpace(cfg.StoreID) == "" {
		return nil, fmt.Errorf("metering/journalstore: durable store id is required")
	}
	def := cfg.DefaultPageSize
	if def <= 0 {
		def = 100
	}
	maxPageSize := cfg.MaxPageSize
	if maxPageSize <= 0 {
		maxPageSize = 500
	}
	if maxPageSize < def {
		return nil, fmt.Errorf("metering/journalstore: max page size %d < default %d", maxPageSize, def)
	}
	if cfg.ObservationOutboxMaxPending < 0 {
		return nil, fmt.Errorf("metering/journalstore: observation outbox max pending must not be negative")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	journalID, err := mintJournalIdentity(cfg)
	if err != nil {
		return nil, err
	}
	return &DurableStore{
		cfg:             cfg,
		db:              db,
		defaultPageSize: def,
		maxPageSize:     maxPageSize,
		now:             now,
		nonOwning:       nonOwning,
		journalID:       journalID,
	}, nil
}

// mintJournalIdentity mints this journal's opaque identity. The configured
// factory is the constructor seam an owner uses to fail the open closed
// deterministically; a nil factory uses the crypto-backed default. A failed
// mint never yields a published journal.
func mintJournalIdentity(cfg DurableConfig) (metering.ObservationJournalID, error) {
	if cfg.ObservationIdentityMint != nil {
		journalID, err := cfg.ObservationIdentityMint()
		if err != nil {
			return "", fmt.Errorf("metering/journalstore: mint observation journal identity: %w", err)
		}
		if strings.TrimSpace(string(journalID)) == "" {
			return "", fmt.Errorf("metering/journalstore: mint observation journal identity: empty identity")
		}
		return journalID, nil
	}
	return mintObservationJournalID()
}

// ObservationJournalIdentity reports the opaque identity of this journal. The
// observation sink built over the same store reports the same identity, so a
// recorder/sink pair built from one journal is provably one journal.
func (s *DurableStore) ObservationJournalIdentity() metering.ObservationJournalID {
	if s == nil {
		return ""
	}
	return s.journalID
}

// mintObservationJournalID returns a fresh opaque journal identity. The value
// is random per opened journal: it is never derived from the store identity,
// so a second journal over the same store never collides with the first.
//
// A failed mint is reported, never swallowed: a journal without a distinct
// identity would be indistinguishable from another journal over the same
// store, so the open must fail before the journal is published.
func mintObservationJournalID() (metering.ObservationJournalID, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("metering/journalstore: mint observation journal identity: %w", err)
	}
	return metering.ObservationJournalID("journal-" + hex.EncodeToString(buf[:])), nil
}

// Close closes the underlying bun DB handle.
func (s *DurableStore) Close() error {
	if s == nil || s.db == nil || s.nonOwning {
		return nil
	}
	return s.db.Close()
}

// CheckReadiness pings the database.
func (s *DurableStore) CheckReadiness(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.PingContext(ctx)
}

// Append inserts one fact or applies idempotent/collision rules on UNIQUE source_event_key.
// SameFactReplay → no-op; same source key otherwise → ErrIdentityCollision.
func (s *DurableStore) Append(ctx context.Context, fact metering.Fact) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("metering/journalstore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := fact.Validate(); err != nil {
		return fmt.Errorf("metering/journalstore: %w", err)
	}
	cloned, err := cloneFact(fact)
	if err != nil {
		return err
	}
	if cloned.RecordedAt.IsZero() {
		cloned.RecordedAt = s.now().UTC()
	}
	key := cloned.SourceEventKey()
	payload, err := json.Marshal(cloned)
	if err != nil {
		return fmt.Errorf("metering/journalstore: marshal payload: %w", err)
	}

	return s.appendWithSQLiteRetry(ctx, cloned, key, payload)
}

func (s *DurableStore) appendWithSQLiteRetry(ctx context.Context, cloned metering.Fact, key string, payload []byte) error {
	if s.db.Dialect().Name() != dialect.SQLite {
		return s.appendAttempt(ctx, cloned, key, payload)
	}

	now := sqliteRetryNow(s.cfg)
	started := now()
	deadline := started.Add(sqliteRetryBudget)
	sleep := sqliteRetrySleep(s.cfg)
	for attempt := 1; attempt <= sqliteRetryMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrSQLiteRetryCanceled, err)
		}
		if attempt > 1 && !now().Before(deadline) {
			attempted := attempt - 1
			s.notifySQLiteRetry(SQLiteRetryEvent{Attempt: attempted, Classification: "busy", TerminalOutcome: "budget_exhausted"})
			return fmt.Errorf("%w after %d attempts: retry budget elapsed", ErrSQLiteBusyRetryExhausted, attempted)
		}

		err := s.appendAttempt(ctx, cloned, key, payload)
		if err == nil {
			s.notifySQLiteRetry(SQLiteRetryEvent{Attempt: attempt, Classification: "success", TerminalOutcome: "success"})
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v", ErrSQLiteRetryCanceled, ctx.Err())
		}
		if s.db.Dialect().Name() != dialect.SQLite || !dbinfra.IsSQLiteBusy(err) {
			return err
		}
		if attempt == sqliteRetryMaxAttempts {
			s.notifySQLiteRetry(SQLiteRetryEvent{Attempt: attempt, Classification: "busy", TerminalOutcome: "attempt_limit"})
			return fmt.Errorf("%w after %d attempts: %v", ErrSQLiteBusyRetryExhausted, attempt, err)
		}

		backoff := sqliteRetryBackoffs[attempt-1]
		remaining := time.Until(deadline)
		if s.cfg.SQLiteRetryNow != nil {
			remaining = deadline.Sub(now())
		}
		if remaining <= 0 {
			s.notifySQLiteRetry(SQLiteRetryEvent{Attempt: attempt, Classification: "busy", TerminalOutcome: "budget_exhausted"})
			return fmt.Errorf("%w after %d attempts: retry budget elapsed", ErrSQLiteBusyRetryExhausted, attempt)
		}
		if backoff > remaining {
			backoff = remaining
		}
		s.notifySQLiteRetry(SQLiteRetryEvent{Attempt: attempt, Classification: "busy", Backoff: backoff})
		if err := sleep(ctx, backoff); err != nil {
			return fmt.Errorf("%w: %v", ErrSQLiteRetryCanceled, err)
		}
	}
	panic("unreachable")
}

func (s *DurableStore) appendAttempt(ctx context.Context, cloned metering.Fact, key string, payload []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("metering/journalstore: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := s.validateDurableSupersession(ctx, tx, cloned); err != nil {
		return err
	}

	existingPayload, found, lerr := lookupDurableSourcePayload(ctx, tx, s.cfg.StoreID, cloned.SourceEventLookupKeys())
	if lerr != nil {
		return lerr
	}
	if found {
		return resolveExistingPayload(existingPayload, cloned)
	}
	existingPayload, found, lerr = lookupDurableFactIdentity(ctx, tx, s.cfg.StoreID, cloned.StreamID, cloned.FactID)
	if lerr != nil {
		return lerr
	}
	if found {
		return resolveExistingPayload(existingPayload, cloned)
	}

	ref := cloned.SourceEventRef()
	_, err = tx.NewRaw(
		`
INSERT INTO metering_facts(
	store_id, fact_id, stream_id, sequence, source_event_key, fact_kind,
	perspective, boundary, lifecycle_scope,
	request_id, a_leg_id, b_leg_id, attempt_id,
	frontend_id, backend_id, model, presence, source, authority,
	recorded_at_unix, payload_json,
	identity_version, source_revision, source_event_kind, source_id
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
`,
		s.cfg.StoreID,
		cloned.FactID,
		cloned.StreamID,
		cloned.Sequence,
		dbinfra.LegacyPersistedText(key),
		string(cloned.Kind),
		string(cloned.Perspective),
		string(cloned.Boundary),
		string(cloned.Lifecycle),
		cloned.Correlation.RequestID,
		cloned.Correlation.ALegID,
		cloned.Correlation.BLegID,
		cloned.Correlation.AttemptID,
		cloned.FrontendID,
		cloned.BackendID,
		cloned.Model,
		string(cloned.Presence),
		string(cloned.Source),
		string(cloned.Authority),
		cloned.RecordedAt.UnixNano(),
		string(payload),
		ref.EffectiveIdentityVersion(),
		ref.SourceRevision,
		ref.EventKind,
		ref.SourceID,
	).Exec(ctx)
	if err != nil {
		if dbinfra.IsUniqueViolation(err) {
			// Postgres aborts the transaction on unique violation; release the
			// connection before resolving on a fresh read (also safe under MaxOpenConns=1).
			_ = tx.Rollback()
			return s.resolveAppendConflict(ctx, cloned)
		}
		return fmt.Errorf("metering/journalstore: insert fact: %w", err)
	}
	for _, p := range filterPairs(cloned) {
		if _, ferr := tx.NewRaw(`
INSERT INTO metering_fact_filters(store_id, fact_id, stream_id, field_name, field_value)
VALUES (?,?,?,?,?)
`, s.cfg.StoreID, cloned.FactID, cloned.StreamID, p[0], p[1]).Exec(ctx); ferr != nil {
			return fmt.Errorf("metering/journalstore: insert filter: %w", ferr)
		}
	}
	for _, raw := range cloned.Supersedes {
		to := strings.TrimSpace(raw)
		if to == "" {
			continue
		}
		if _, serr := tx.NewRaw(`
INSERT INTO metering_fact_supersessions(store_id, stream_id, from_fact_id, to_fact_id)
VALUES (?,?,?,?)
`, s.cfg.StoreID, cloned.StreamID, cloned.FactID, to).Exec(ctx); serr != nil {
			return fmt.Errorf("metering/journalstore: insert supersession: %w", serr)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("metering/journalstore: commit: %w", err)
	}
	return nil
}

// resolveAppendConflict reads the winning row after a unique-constraint race.
// It must not reuse the aborted insert transaction (Postgres 25P02).
// Races may hit either (store_id, source_event_key) or (store_id, stream_id, fact_id).
func (s *DurableStore) resolveAppendConflict(ctx context.Context, cloned metering.Fact) error {
	key := cloned.SourceEventKey()
	existingPayload, found, err := lookupDurableSourcePayload(ctx, s.db, s.cfg.StoreID, cloned.SourceEventLookupKeys())
	if err != nil {
		return fmt.Errorf("metering/journalstore: insert fact unique race lookup: %w", err)
	}
	if !found {
		existingPayload, found, err = lookupDurableFactIdentity(ctx, s.db, s.cfg.StoreID, cloned.StreamID, cloned.FactID)
		if err != nil {
			return fmt.Errorf("metering/journalstore: insert fact identity race lookup: %w", err)
		}
	}
	if !found {
		return fmt.Errorf("%w: source_event_key=%q (retry append)", ErrUniqueRaceMissingRow, key)
	}
	return resolveExistingPayload(existingPayload, cloned)
}

func resolveExistingPayload(existingPayload string, cloned metering.Fact) error {
	var existing metering.Fact
	if uerr := json.Unmarshal([]byte(existingPayload), &existing); uerr != nil {
		return fmt.Errorf("metering/journalstore: decode existing: %w", uerr)
	}
	if metering.SameFactReplay(existing, cloned) {
		return nil
	}
	return fmt.Errorf("%w: stream_id=%q fact_id=%q stored_seq=%d new_seq=%d",
		ErrIdentityCollision, cloned.StreamID, cloned.FactID, existing.Sequence, cloned.Sequence)
}

// lookupDurableSourcePayload finds a row via SourceEventLookupKeys order:
// canonical, phase-3.1 NUL (literal version), V0/V1 NUL aliases when effective
// V1, then IdempotencyKey.
func lookupDurableSourcePayload(ctx context.Context, q bun.IDB, storeID string, keys []string) (string, bool, error) {
	seen := make(map[string]struct{}, len(keys))
	for i, key := range keys {
		key = dbinfra.LegacyPersistedText(key)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		var payload string
		err := q.NewRaw(
			`SELECT payload_json FROM metering_facts WHERE store_id = ? AND source_event_key = ?`,
			storeID, key,
		).Scan(ctx, &payload)
		if err == nil {
			return payload, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			if i == 0 {
				return "", false, fmt.Errorf("metering/journalstore: lookup: %w", err)
			}
			return "", false, fmt.Errorf("metering/journalstore: legacy lookup: %w", err)
		}
	}
	return "", false, nil
}

func lookupDurableFactIdentity(ctx context.Context, q bun.IDB, storeID, streamID, factID string) (string, bool, error) {
	var payload string
	err := q.NewRaw(
		`SELECT payload_json FROM metering_facts WHERE store_id = ? AND stream_id = ? AND fact_id = ?`,
		storeID, streamID, factID,
	).Scan(ctx, &payload)
	if err == nil {
		return payload, true, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("metering/journalstore: fact identity lookup: %w", err)
}

func (s *DurableStore) validateDurableSupersession(ctx context.Context, tx bun.Tx, fact metering.Fact) error {
	if !fact.Kind.RequiresSupersedes() {
		return nil
	}
	lookup := func(factID string) (metering.Fact, bool) {
		var payload string
		err := tx.NewRaw(
			`SELECT payload_json FROM metering_facts WHERE store_id = ? AND stream_id = ? AND fact_id = ?`,
			s.cfg.StoreID, fact.StreamID, factID,
		).Scan(ctx, &payload)
		if err != nil {
			return metering.Fact{}, false
		}
		var existing metering.Fact
		if uerr := json.Unmarshal([]byte(payload), &existing); uerr != nil {
			return metering.Fact{}, false
		}
		return existing, true
	}
	type edgeRow struct {
		From string `bun:"from_fact_id"`
		To   string `bun:"to_fact_id"`
	}
	var edgeRows []edgeRow
	err := tx.NewRaw(
		`SELECT from_fact_id, to_fact_id FROM metering_fact_supersessions WHERE store_id = ? AND stream_id = ?`,
		s.cfg.StoreID, fact.StreamID,
	).Scan(ctx, &edgeRows)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("metering/journalstore: supersession edge scan: %w", err)
	}
	edges := make(map[string][]string, len(edgeRows))
	for _, e := range edgeRows {
		from := strings.TrimSpace(e.From)
		to := strings.TrimSpace(e.To)
		if from == "" || to == "" {
			continue
		}
		edges[from] = append(edges[from], to)
	}
	return validateSupersessionGraph(fact, lookup, edges)
}

// List returns a bounded page filtered by indexed selective bounds.
func (s *DurableStore) List(ctx context.Context, q metering.Query) (metering.Page, error) {
	if s == nil || s.db == nil {
		return metering.Page{}, fmt.Errorf("metering/journalstore: nil store")
	}
	if err := ctx.Err(); err != nil {
		return metering.Page{}, err
	}
	unsupported := metering.QueryUnsupported(q)
	if err := metering.ValidateQuery(q); err != nil {
		return metering.Page{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = s.defaultPageSize
	}
	if limit > s.maxPageSize {
		limit = s.maxPageSize
	}
	offset := 0
	if cur := strings.TrimSpace(q.Cursor); cur != "" {
		n, err := strconv.Atoi(cur)
		if err != nil || n < 0 {
			return metering.Page{}, fmt.Errorf("metering/journalstore: invalid cursor")
		}
		offset = n
	}

	query, args := buildDurableListQuery(s.cfg.StoreID, q, limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return metering.Page{}, fmt.Errorf("metering/journalstore: list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	facts := make([]metering.Fact, 0, limit)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return metering.Page{}, fmt.Errorf("metering/journalstore: scan: %w", err)
		}
		var f metering.Fact
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			return metering.Page{}, fmt.Errorf("metering/journalstore: decode: %w", err)
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return metering.Page{}, err
	}
	page := metering.Page{Unsupported: append([]metering.UnsupportedFilter(nil), unsupported...)}
	if len(facts) > limit {
		page.NextCursor = strconv.Itoa(offset + limit)
		facts = facts[:limit]
	}
	page.Facts = facts
	return page, nil
}

var (
	_ metering.Recorder = (*DurableStore)(nil)
	_ metering.Querier  = (*DurableStore)(nil)
)
