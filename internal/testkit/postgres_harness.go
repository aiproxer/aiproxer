package testkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"
)

var (
	postgresStoreIDSeq atomic.Int64
	postgresStoreIDRun = time.Now().UnixNano()
)

// UniquePostgresStoreID returns a process-unique store_id for pooler-safe isolation.
func UniquePostgresStoreID(prefix string) string {
	p := strings.TrimSpace(prefix)
	if p == "" {
		p = "pg"
	}
	return fmt.Sprintf("%s-%d-%d", p, postgresStoreIDRun, postgresStoreIDSeq.Add(1))
}

// PostgresStoreComponent identifies dual-plane tables cleaned by store_id.
type PostgresStoreComponent string

const (
	PostgresComponentAuthority    PostgresStoreComponent = "authority"
	PostgresComponentLease        PostgresStoreComponent = "lease"
	PostgresComponentJournal      PostgresStoreComponent = "journal"
	PostgresComponentTerminalWork PostgresStoreComponent = "terminal-work"
)

// DualPlanePostgresSearchPathGuardDirs lists strict dual-plane store packages
// whose runtime and test SQL must not depend on session search_path state.
//
// The usage-authority store moved to the Enterprise tree with task 6.1; its
// guard now lives with the store in
// enterprise/internal/infra/usageauthority/authoritystore and calls the shared
// AssertNoSearchPathInSources helper below.
func DualPlanePostgresSearchPathGuardDirs() []string {
	return []string{
		"internal/infra/concurrencyauthority/leasestore",
		"internal/infra/metering/journalstore",
		"internal/infra/terminalwork/workstore",
	}
}

// AssertNoSearchPathInSources fails the test when any Go source file under
// absDir references the session search_path setting. It is the single shared
// predicate for the open-source dual-plane store guards and for the
// Enterprise-owned usage-authority store guard, so the rule is stated once.
func AssertNoSearchPathInSources(t *testing.T, absDir string) {
	t.Helper()
	if err := noSearchPathInSources(absDir); err != nil {
		t.Fatal(err)
	}
}

// noSearchPathInSources returns an error naming the offending source file when
// a Go file under absDir references search_path.
func noSearchPathInSources(absDir string) error {
	return filepath.WalkDir(absDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "search_path") {
			return fmt.Errorf("%s must not reference search_path; use unique store_id + admin cleanup", path)
		}
		return nil
	})
}

// ClassifyForbiddenRuntimeSQL returns a short reason when query is illegal on a
// transaction-pooled runtime connection; empty means allowed DML/query.
func ClassifyForbiddenRuntimeSQL(query string) string {
	stripped := stripSQLCommentsAndStringLiterals(query)
	for _, stmt := range splitSQLStatements(stripped) {
		if reason := classifyForbiddenSQLStatement(stmt); reason != "" {
			return reason
		}
	}
	return ""
}

func classifyForbiddenSQLStatement(stmt string) string {
	tokens := sqlKeywordTokens(stmt)
	if len(tokens) == 0 {
		return ""
	}
	for _, tok := range tokens {
		switch tok {
		case "search_path":
			return "session search_path"
		case "pg_advisory_lock", "pg_try_advisory_lock", "pg_advisory_unlock",
			"pg_advisory_lock_shared", "pg_try_advisory_lock_shared", "pg_advisory_unlock_shared":
			return "session advisory lock"
		}
	}
	switch tokens[0] {
	case "create":
		for _, tok := range tokens[1:] {
			if tok == "temp" || tok == "temporary" {
				return "temporary table"
			}
		}
		return "runtime DDL"
	case "alter", "drop":
		return "runtime DDL"
	case "prepare":
		return "SQL PREPARE"
	case "deallocate":
		return "SQL DEALLOCATE"
	case "reset":
		return "session GUC"
	case "set":
		if len(tokens) > 1 && tokens[1] == "transaction" {
			return ""
		}
		return "session GUC"
	default:
		return ""
	}
}

func stripSQLCommentsAndStringLiterals(q string) string {
	var b strings.Builder
	b.Grow(len(q))
	for i := 0; i < len(q); i++ {
		c := q[i]
		if c == '-' && i+1 < len(q) && q[i+1] == '-' {
			i += 2
			for i < len(q) && q[i] != '\n' {
				i++
			}
			if i < len(q) {
				b.WriteByte('\n')
			}
			continue
		}
		if c == '/' && i+1 < len(q) && q[i+1] == '*' {
			i += 2
			for i+1 < len(q) && (q[i] != '*' || q[i+1] != '/') {
				i++
			}
			i++ // consume '/'
			b.WriteByte(' ')
			continue
		}
		if c == '\'' {
			i++
			for i < len(q) {
				if q[i] == '\'' {
					if i+1 < len(q) && q[i+1] == '\'' {
						i += 2
						continue
					}
					break
				}
				i++
			}
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func splitSQLStatements(q string) []string {
	parts := strings.Split(q, ";")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sqlKeywordTokens(stmt string) []string {
	fields := strings.FieldsFunc(strings.ToLower(stmt), func(r rune) bool {
		return unicode.IsSpace(r) || r == ',' || r == '(' || r == ')' || r == '='
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, `"'`+"`")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// RuntimeSQLGuard records forbidden runtime SQL observed through a bun query hook.
type RuntimeSQLGuard struct {
	mu         sync.Mutex
	violations []string
}

// NewRuntimeSQLGuard returns an empty guard suitable for bun.DB.AddQueryHook.
func NewRuntimeSQLGuard() *RuntimeSQLGuard {
	return &RuntimeSQLGuard{}
}

// BeforeQuery implements bun.QueryHook.
func (g *RuntimeSQLGuard) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if g == nil || event == nil {
		return ctx
	}
	if reason := ClassifyForbiddenRuntimeSQL(event.Query); reason != "" {
		g.mu.Lock()
		g.violations = append(g.violations, reason+": "+truncateSQL(event.Query, 120))
		g.mu.Unlock()
	}
	return ctx
}

// AfterQuery implements bun.QueryHook.
func (*RuntimeSQLGuard) AfterQuery(context.Context, *bun.QueryEvent) {}

// Violations returns a snapshot of recorded forbidden SQL reasons.
func (g *RuntimeSQLGuard) Violations() []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.violations))
	copy(out, g.violations)
	return out
}

// Reset clears recorded violations between tests sharing a query hook.
func (g *RuntimeSQLGuard) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.violations = nil
	g.mu.Unlock()
}

// HasRuntimeDDL reports whether any recorded violation is runtime DDL or temp DDL.
func (g *RuntimeSQLGuard) HasRuntimeDDL() bool {
	for _, v := range g.Violations() {
		if strings.HasPrefix(v, "runtime DDL") || strings.HasPrefix(v, "temporary table") {
			return true
		}
	}
	return false
}

// AssertNoViolations fails the test when forbidden runtime SQL was observed.
func (g *RuntimeSQLGuard) AssertNoViolations(t *testing.T) {
	t.Helper()
	violations := g.Violations()
	if len(violations) == 0 {
		return
	}
	t.Fatalf("pooled runtime SQL contract violated (%d): %s", len(violations), strings.Join(violations, "; "))
}

// FailRuntimeDDLRED fails with the deterministic Phase-1 RED reason when the
// constructor/open path emitted runtime DDL (no open-without-migrate API yet).
func FailRuntimeDDLRED(t *testing.T, guard *RuntimeSQLGuard, openErr error) {
	t.Helper()
	violations := guard.Violations()
	if guard.HasRuntimeDDL() {
		t.Fatalf("pooled runtime RED: runtime DDL observed (no open-without-migrate API until Phase 3): %s", strings.Join(violations, "; "))
	}
	if len(violations) > 0 {
		t.Fatalf("pooled runtime RED: forbidden session SQL observed: %s", strings.Join(violations, "; "))
	}
	if openErr != nil {
		t.Fatalf("pooled runtime open: %v", openErr)
	}
}

func truncateSQL(q string, n int) string {
	q = strings.Join(strings.Fields(q), " ")
	if len(q) <= n {
		return q
	}
	return q[:n] + "..."
}

// OpenPostgresBun opens a small owned Bun PostgreSQL pool for tests.
// Callers must Close the returned handle (or register cleanup). Errors omit DSN values.
func OpenPostgresBun(dsn string, maxOpen int) (*bun.DB, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return nil, errors.New("testkit: empty postgres DSN")
	}
	if maxOpen <= 0 {
		maxOpen = 4
	}
	ctx, cancel := context.WithTimeout(context.Background(), db.DefaultPostgresOpenMigrateTimeout)
	defer cancel()
	return db.OpenPostgresBun(ctx, trimmed, db.PoolSettings{
		MaxOpenConns: maxOpen,
		MaxIdleConns: maxOpen,
	})
}

// OpenPostgresBunForTest opens a Bun pool or fails the test.
func OpenPostgresBunForTest(t *testing.T, dsn string, maxOpen int) *bun.DB {
	t.Helper()
	bunDB, err := OpenPostgresBun(dsn, maxOpen)
	if err != nil {
		t.Fatal(err)
	}
	return bunDB
}

// CleanupPostgresStoreByID deletes dual-plane rows for storeID via the admin DSN.
// Order is dependency-safe. Shared schema objects are never dropped.
// After known admin bootstrap, unexpected errors fail the test (no text matching).
func CleanupPostgresStoreByID(t *testing.T, adminDSN, storeID string, components ...PostgresStoreComponent) {
	t.Helper()
	storeID = strings.TrimSpace(storeID)
	if adminDSN == "" || storeID == "" || len(components) == 0 {
		return
	}
	admin, err := OpenPostgresBun(adminDSN, 2)
	if err != nil {
		t.Fatalf("cleanup open admin: %v", err)
	}
	defer func() { _ = admin.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, component := range components {
		stmts := cleanupStatements(component, storeID)
		for _, stmt := range stmts {
			if _, err := admin.ExecContext(ctx, stmt.sql, stmt.args...); err != nil {
				if isUndefinedTableSQLState(err) {
					continue
				}
				t.Fatalf("cleanup %s store: %v", component, err)
			}
		}
	}
}

type cleanupStmt struct {
	sql  string
	args []any
}

func cleanupStatements(component PostgresStoreComponent, storeID string) []cleanupStmt {
	switch component {
	case PostgresComponentAuthority:
		return []cleanupStmt{
			{`DELETE FROM usage_authority_decision_filters WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM usage_authority_decisions WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM usage_authority_limit_filters WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM usage_authority_limit_rows WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM usage_authority_reservations WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM usage_authority_unreserved_usage_facts WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM usage_authority_state WHERE store_id = ?`, []any{storeID}},
		}
	case PostgresComponentLease:
		return []cleanupStmt{
			{`DELETE FROM concurrency_leases WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM concurrency_lease_capacity WHERE store_id = ?`, []any{storeID}},
		}
	case PostgresComponentJournal:
		return []cleanupStmt{
			{`DELETE FROM metering_observation_economic_outbox WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM metering_components WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM metering_fact_supersessions WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM metering_fact_filters WHERE store_id = ?`, []any{storeID}},
			{`DELETE FROM metering_facts WHERE store_id = ?`, []any{storeID}},
		}
	case PostgresComponentTerminalWork:
		return []cleanupStmt{
			{`DELETE FROM economic_terminal_work WHERE store_id = ?`, []any{storeID}},
		}
	default:
		return nil
	}
}

func isUndefinedTableSQLState(err error) bool {
	if err == nil {
		return false
	}
	var pgErr pgdriver.Error
	if errors.As(err, &pgErr) && pgErr.Field('C') == "42P01" {
		return true
	}
	return false
}
