package db

import (
	"database/sql"
	"fmt"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"
)

// Dialect names supported by NewBunDB.
type Dialect string

const (
	DialectSQLite   Dialect = "sqlite"
	DialectPostgres Dialect = "postgres"
)

// NewBunDB returns a bun.DB for the given dialect. sqldb must be non-nil; dialect
// must be DialectPostgres or DialectSQLite.
func NewBunDB(sqldb *sql.DB, dialect Dialect) (*bun.DB, error) {
	if sqldb == nil {
		return nil, ErrNilDB
	}
	switch dialect {
	case DialectPostgres:
		return bun.NewDB(sqldb, pgdialect.New()), nil
	case DialectSQLite:
		return bun.NewDB(sqldb, sqlitedialect.New()), nil
	default:
		return nil, fmt.Errorf("db: unknown dialect %q", string(dialect))
	}
}
