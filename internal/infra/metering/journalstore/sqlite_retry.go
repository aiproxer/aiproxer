package journalstore

import (
	"context"
	"errors"
	"time"
)

const (
	sqliteRetryMaxAttempts = 12
	sqliteRetryBudget      = 2 * time.Second
)

var sqliteRetryBackoffs = [...]time.Duration{
	5 * time.Millisecond,
	10 * time.Millisecond,
	20 * time.Millisecond,
	40 * time.Millisecond,
	80 * time.Millisecond,
	160 * time.Millisecond,
	250 * time.Millisecond,
	250 * time.Millisecond,
	250 * time.Millisecond,
	250 * time.Millisecond,
	250 * time.Millisecond,
}

// SQLiteRetryEvent describes one append attempt from the narrow SQLite retry policy.
type SQLiteRetryEvent struct {
	Attempt         int
	Classification  string
	Backoff         time.Duration
	TerminalOutcome string
}

// SQLiteRetryObserver receives bounded append retry diagnostics.
type SQLiteRetryObserver func(SQLiteRetryEvent)

// ErrSQLiteRetryCanceled classifies cancellation while a busy append is waiting to retry.
var ErrSQLiteRetryCanceled = errors.New("sqlite_retry_canceled")

// ErrSQLiteBusyRetryExhausted classifies a busy append that used its retry budget.
var ErrSQLiteBusyRetryExhausted = errors.New("sqlite_busy_retry_exhausted")

func sqliteRetryNow(cfg DurableConfig) func() time.Time {
	if cfg.SQLiteRetryNow != nil {
		return cfg.SQLiteRetryNow
	}
	return time.Now
}

func sqliteRetrySleep(cfg DurableConfig) func(context.Context, time.Duration) error {
	if cfg.SQLiteRetrySleep != nil {
		return cfg.SQLiteRetrySleep
	}
	return func(ctx context.Context, delay time.Duration) error {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *DurableStore) notifySQLiteRetry(event SQLiteRetryEvent) {
	if s.cfg.SQLiteRetryObserver != nil {
		s.cfg.SQLiteRetryObserver(event)
	}
}
