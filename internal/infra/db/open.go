package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"
)

// OpenPostgres opens a PostgreSQL *sql.DB using the Bun pgdriver connector, pings
// with ctx, and returns an owned handle. Empty DSN is rejected. Errors do not
// include raw credentials. Common libpq DSN parameters that pgdriver does not
// honor (for example channel_binding=require emitted by Neon) are stripped
// before the connector is built so operators can paste managed-provider DSNs
// directly into config.
func OpenPostgres(ctx context.Context, dsn string) (*sql.DB, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return nil, ErrEmptyDSN
	}
	sanitized, err := SanitizePostgresDSN(trimmed)
	if err != nil {
		return nil, fmt.Errorf("db: open postgres: sanitize dsn: %w", err)
	}
	connector := pgdriver.NewConnector(pgdriver.WithDSN(sanitized))
	sqldb := sql.OpenDB(connector)
	if err := sqldb.PingContext(ctx); err != nil {
		openErr := redactOpenError(trimmed, "open postgres: ping", err)
		if cerr := sqldb.Close(); cerr != nil {
			return nil, errors.Join(openErr, fmt.Errorf("db: close after failed ping: %w", cerr))
		}
		return nil, openErr
	}
	return sqldb, nil
}

// OpenPostgresBun opens PostgreSQL, applies pool settings, and returns an owned *bun.DB
// (Close closes the underlying sql.DB). dsn must be non-empty after trim.
func OpenPostgresBun(ctx context.Context, dsn string, pool PoolSettings) (*bun.DB, error) {
	sqldb, err := OpenPostgres(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := ApplyPoolSettings(sqldb, pool); err != nil {
		poolErr := fmt.Errorf("db: postgres pool: %w", err)
		if cerr := sqldb.Close(); cerr != nil {
			return nil, errors.Join(poolErr, fmt.Errorf("db: close after pool settings: %w", cerr))
		}
		return nil, poolErr
	}
	bunDB, err := NewBunDB(sqldb, DialectPostgres)
	if err != nil {
		wrapped := fmt.Errorf("db: new bun db: %w", err)
		if cerr := sqldb.Close(); cerr != nil {
			return nil, errors.Join(wrapped, fmt.Errorf("db: close after bun init: %w", cerr))
		}
		return nil, wrapped
	}
	return bunDB, nil
}
