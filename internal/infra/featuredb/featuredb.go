// Package featuredb carries the narrow contract of the isolated durable
// database a feature-owned advisory store uses.
//
// The isolated database is always separate from the core continuity
// database: an advisory write must never hold the connection or the write
// lock the mandatory core path needs. The composition root owns the engine,
// the connection settings and the handle lifetime; a feature host only names
// the database it needs.
package featuredb

import (
	"context"

	"github.com/uptrace/bun"
)

// Spec names the isolated durable database one feature-owned store uses.
type Spec struct {
	// FeatureID names the feature that owns the database. It selects the
	// default file name of the isolated SQLite database.
	FeatureID string
	// SQLitePath is the operator-selected database file. An empty path
	// selects the default file next to the continuity database.
	SQLitePath string
}

// Database is one opened isolated durable database.
type Database struct {
	// DB is the opened database handle.
	DB *bun.DB
	// Identity is a stable, non-secret name of the opened database. Two
	// requests with the same identity share one handle.
	Identity string
}

// Opener opens the isolated durable database of a feature-owned store.
type Opener interface {
	OpenFeatureDatabase(ctx context.Context, spec Spec) (Database, error)
}
