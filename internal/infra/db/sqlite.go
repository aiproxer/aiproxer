package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/uptrace/bun"
	_ "modernc.org/sqlite" // register "sqlite" driver for OpenSQLiteBun
)

// SQLiteFileDSN builds a modernc.org/sqlite driver URI with pragma query parameters.
func SQLiteFileDSN(path string) (string, error) {
	p := strings.ReplaceAll(strings.TrimSpace(path), `\`, `/`)
	if strings.ContainsAny(p, "\x00?#&") {
		return "", fmt.Errorf("db: sqlite path contains invalid character")
	}
	return "file:" + p + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", nil
}

// OpenSQLiteBun opens (creating if needed) a SQLite file as an owned *bun.DB.
// ctx bounds ping; Close on the returned *bun.DB closes the underlying sql.DB.
func OpenSQLiteBun(ctx context.Context, path string) (*bun.DB, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("db: empty sqlite path")
	}
	dsn, err := SQLiteFileDSN(path)
	if err != nil {
		return nil, err
	}
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open sqlite: %w", err)
	}
	sqldb.SetMaxOpenConns(1)
	if err := sqldb.PingContext(ctx); err != nil {
		pingErr := fmt.Errorf("db: sqlite ping: %w", err)
		if cerr := sqldb.Close(); cerr != nil {
			return nil, errors.Join(pingErr, fmt.Errorf("db: close after failed ping: %w", cerr))
		}
		return nil, pingErr
	}
	bunDB, err := NewBunDB(sqldb, DialectSQLite)
	if err != nil {
		wrapped := fmt.Errorf("db: new bun db: %w", err)
		if cerr := sqldb.Close(); cerr != nil {
			return nil, errors.Join(wrapped, fmt.Errorf("db: close after bun init: %w", cerr))
		}
		return nil, wrapped
	}
	return bunDB, nil
}
