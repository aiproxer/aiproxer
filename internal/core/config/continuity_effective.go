package config

import "strings"

// Continuity backing names accepted by the configuration validator and the runtime.
const (
	// ContinuityStoreMemory is the process-local backing. It is the default when
	// the continuity block is omitted entirely.
	ContinuityStoreMemory = "memory"
	// ContinuityStoreSQLite is the local durable backing (requires sqlite_path).
	ContinuityStoreSQLite = "sqlite"
	// ContinuityStorePostgres is the managed durable backing (requires postgres_dsn).
	ContinuityStorePostgres = "postgres"
)

// EffectiveContinuityStore returns the continuity backing name after applying the same
// rules as [Validate] (in_memory forces memory; empty store defaults to memory).
//
// It is the single resolution point for the continuity backing. The configuration
// validator, the loader defaults and the store constructor all read the backing from
// here, so an omitted `continuity` block, an omitted `in_memory` flag and an
// explicit `store: memory` all resolve to the same backing and never disagree.
//
// A configuration that pairs in_memory with a durable store is refused by
// [Validate] before it reaches this resolver, so the forced process-local
// backing is never a silent downgrade of the durable store the operator asked
// for.
func EffectiveContinuityStore(c ContinuityConfig) string {
	store := strings.ToLower(strings.TrimSpace(c.Store))
	if c.InMemory {
		return ContinuityStoreMemory
	}
	if store == "" {
		return ContinuityStoreMemory
	}
	return store
}
