package db

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite" // register "sqlite" driver for test databases
)

func openStubSQLite(t *testing.T) (*sql.DB, error) {
	t.Helper()
	return sql.Open("sqlite", "file::memory:?cache=shared")
}
