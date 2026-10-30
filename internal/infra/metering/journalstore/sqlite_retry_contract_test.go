package journalstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/uptrace/bun/dialect"
)

func TestSQLiteRetry_BusyEventuallySucceeds(t *testing.T) {
	t.Parallel()
	cfg := DurableConfig{StoreID: "retry-busy"}
	store, holder, release := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	var events []SQLiteRetryEvent
	var busyAttempts int
	store.cfg.SQLiteRetryObserver = func(event SQLiteRetryEvent) {
		events = append(events, event)
		if event.Classification == "busy" {
			busyAttempts++
			if busyAttempts == 7 {
				release()
			}
		}
	}

	if err := store.Append(context.Background(), conflictTestFact("retry-busy", "retry", 1)); err != nil {
		t.Fatal(err)
	}
	if busyAttempts != 7 || len(events) != 8 || events[0].Attempt != 1 || events[6].Attempt != 7 || events[7].Attempt != 8 || events[7].Classification != "success" {
		t.Fatalf("events=%+v", events)
	}
}

func TestSQLiteRetry_WholeTransactionRestarts(t *testing.T) {
	t.Parallel()
	cfg := DurableConfig{StoreID: "retry-whole"}
	store, holder, release := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	var attempts []int
	var once sync.Once
	store.cfg.SQLiteRetryObserver = func(event SQLiteRetryEvent) {
		if event.Classification == "busy" {
			attempts = append(attempts, event.Attempt)
			once.Do(release)
		}
	}
	fact := conflictTestFact("retry-whole", "retry-whole-stream", 1)
	if err := store.Append(context.Background(), fact); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 || attempts[0] != 1 {
		t.Fatalf("attempts=%v", attempts)
	}
	var count int
	if err := store.db.NewRaw(`SELECT COUNT(1) FROM metering_facts WHERE store_id = ?`, cfg.StoreID).Scan(context.Background(), &count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("facts=%d", count)
	}
}

func TestSQLiteRetry_CancellationStopsBeforeNextAttempt(t *testing.T) {
	t.Parallel()
	cfg := DurableConfig{StoreID: "retry-cancel"}
	store, holder, release := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts int
	store.cfg.SQLiteRetryObserver = func(SQLiteRetryEvent) {
		attempts++
		cancel()
		release()
	}
	err := store.Append(ctx, conflictTestFact("retry-cancel", "retry-cancel-stream", 1))
	if !errors.Is(err, ErrSQLiteRetryCanceled) {
		t.Fatalf("err=%v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestSQLiteRetry_DeadlineStopsPromptly(t *testing.T) {
	t.Parallel()
	cfg := DurableConfig{StoreID: "retry-deadline"}
	store, holder, release := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	var attempts int
	store.cfg.SQLiteRetryObserver = func(SQLiteRetryEvent) { attempts++ }
	store.cfg.SQLiteRetrySleep = func(ctx context.Context, _ time.Duration) error {
		release()
		<-ctx.Done()
		return ctx.Err()
	}
	if err := store.Append(ctx, conflictTestFact("retry-deadline", "retry-deadline-stream", 1)); !errors.Is(err, ErrSQLiteRetryCanceled) {
		t.Fatalf("err=%v", err)
	}
	if attempts > 1 {
		t.Fatalf("attempts=%d", attempts)
	}
}

func TestSQLiteRetry_BudgetAndAttemptLimit(t *testing.T) {
	t.Parallel()
	clock := time.Unix(10, 0)
	var sleeps []time.Duration
	cfg := DurableConfig{
		StoreID:        "retry-budget",
		SQLiteRetryNow: func() time.Time { return clock },
		SQLiteRetrySleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			clock = clock.Add(delay)
			return nil
		},
	}
	store, holder, _ := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	var events []SQLiteRetryEvent
	store.cfg.SQLiteRetryObserver = func(event SQLiteRetryEvent) { events = append(events, event) }
	err := store.Append(context.Background(), conflictTestFact("retry-budget", "retry-budget-stream", 1))
	if !errors.Is(err, ErrSQLiteBusyRetryExhausted) {
		t.Fatalf("err=%v", err)
	}
	if len(events) != 12 || len(sleeps) != 11 {
		t.Fatalf("events=%+v sleeps=%v", events, sleeps)
	}
	want := []time.Duration{5 * time.Millisecond, 10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 160 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleep[%d]=%s want %s", i, sleeps[i], want[i])
		}
	}
	last := events[len(events)-1]
	if last.Attempt != 12 || last.TerminalOutcome != "attempt_limit" {
		t.Fatalf("terminal event = %+v, want attempt 12 attempt_limit", last)
	}
	if !strings.Contains(err.Error(), "after 12 attempts") {
		t.Fatalf("attempt-limit error omits the attempted count: %v", err)
	}
}

func TestSQLiteRetry_BudgetExhaustedReportsExecutedCount(t *testing.T) {
	t.Parallel()
	clock := time.Unix(10, 0)
	cfg := DurableConfig{
		StoreID:        "retry-budget-exhausted",
		SQLiteRetryNow: func() time.Time { return clock },
		SQLiteRetrySleep: func(_ context.Context, delay time.Duration) error {
			clock = clock.Add(3 * time.Second)
			return nil
		},
	}
	store, holder, _ := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	var events []SQLiteRetryEvent
	store.cfg.SQLiteRetryObserver = func(event SQLiteRetryEvent) { events = append(events, event) }
	err := store.Append(context.Background(), conflictTestFact("retry-budget-exhausted", "retry-budget-exhausted-stream", 1))
	if !errors.Is(err, ErrSQLiteBusyRetryExhausted) {
		t.Fatalf("err=%v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events=%+v", events)
	}
	if events[0].Attempt != 1 || events[0].Classification != "busy" || events[0].Backoff != 5*time.Millisecond {
		t.Fatalf("first busy event = %+v", events[0])
	}
	terminal := events[1]
	if terminal.Attempt != 1 || terminal.Classification != "busy" || terminal.TerminalOutcome != "budget_exhausted" {
		t.Fatalf("terminal event = %+v, want one executed attempt reported", terminal)
	}
	if !strings.Contains(err.Error(), "after 1 attempts") {
		t.Fatalf("budget-exhausted error omits the attempted count: %v", err)
	}
}

func TestSQLiteRetry_ObserverUsesDeterministicClock(t *testing.T) {
	t.Parallel()
	clock := time.Unix(10, 0)
	var sleeps []time.Duration
	cfg := DurableConfig{
		StoreID:        "retry-clock",
		SQLiteRetryNow: func() time.Time { return clock },
		SQLiteRetrySleep: func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			clock = clock.Add(delay)
			return nil
		},
	}
	store, holder, release := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	var events []SQLiteRetryEvent
	var once sync.Once
	store.cfg.SQLiteRetryObserver = func(event SQLiteRetryEvent) {
		events = append(events, event)
		once.Do(release)
	}
	if err := store.Append(context.Background(), conflictTestFact("retry-clock", "retry-clock-stream", 1)); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || len(sleeps) != 1 || sleeps[0] != 5*time.Millisecond {
		t.Fatalf("events=%+v sleeps=%v", events, sleeps)
	}
}

func TestSQLiteRetry_NonBusyDoesNotRetry(t *testing.T) {
	t.Parallel()
	cfg := DurableConfig{StoreID: "retry-nonbusy"}
	store, holder, release := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	release()
	if _, err := store.db.ExecContext(context.Background(), `DROP TABLE metering_fact_filters`); err != nil {
		t.Fatal(err)
	}
	var attempts int
	store.cfg.SQLiteRetryObserver = func(SQLiteRetryEvent) { attempts++ }
	err := store.Append(context.Background(), conflictTestFact("retry-nonbusy", "retry-nonbusy-stream", 1))
	if err == nil || attempts != 0 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestSQLiteRetry_PostgresDoesNotRetry(t *testing.T) {
	// The dialect gate stays at the retry call sites; only SQLite lock
	// contention classified by the shared database helper may retry.
	busy := func(name dialect.Name, err error) bool {
		return name == dialect.SQLite && db.IsSQLiteBusy(err)
	}
	if busy(dialect.PG, errors.New("database is locked")) {
		t.Fatal("postgres lock text was classified as SQLite busy")
	}
	if busy(dialect.SQLite, errors.New("sqlite_busy")) {
		t.Fatal("generic sqlite_busy text was classified as SQLite busy")
	}
	if busy(dialect.SQLite, errors.New("constraint failed")) {
		t.Fatal("constraint error was classified as SQLite busy")
	}
}

func TestSQLiteRetry_LockReleased(t *testing.T) {
	t.Parallel()
	cfg := DurableConfig{StoreID: "retry-release"}
	store, holder, release := newRetrySQLiteJournal(t, cfg)
	t.Cleanup(func() { _ = holder.Close() })
	var once sync.Once
	store.cfg.SQLiteRetryObserver = func(SQLiteRetryEvent) { once.Do(release) }
	if err := store.Append(context.Background(), conflictTestFact("retry-release", "retry-release-stream", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(context.Background(), `SELECT 1`); err != nil {
		t.Fatal(err)
	}
}

func newRetrySQLiteJournal(t *testing.T, cfg DurableConfig) (*DurableStore, *sql.Conn, func()) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "retry.db")
	sqlDB, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(0)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	t.Cleanup(func() { _ = sqlDB.Close() })
	bunDB, err := db.NewBunDB(sqlDB, db.DialectSQLite)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDurableStore(context.Background(), bunDB, DurableConfig{StoreID: cfg.StoreID})
	if err != nil {
		t.Fatal(err)
	}
	store.cfg = cfg
	t.Cleanup(func() { _ = store.Close() })
	holder, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		_ = holder.Close()
		t.Fatal(err)
	}
	return store, holder, func() { _, _ = holder.ExecContext(context.Background(), `ROLLBACK`) }
}
