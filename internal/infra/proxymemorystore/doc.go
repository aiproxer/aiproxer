// Package proxymemorystore is the Bun persistence adapter for the proxy
// memory feature (design §12): session captures, extraction results, explicit
// notes, published memory banks and forget fences over SQLite and PostgreSQL.
// It also holds the owner-scoped per-phase settings of design §4.
//
// Every statement is owner-scoped: it includes tenant_id, principal_id and
// project_key, or it reaches a row by primary key and re-checks the owner
// columns or the lease owner. The settings statements carry tenant_id and
// principal_id without a project, because the settings are global for their
// owner. No method can read or change another owner's artifact
// (Requirement 1.5).
//
// EnsureSchema applies the registered schema through the bunmigrate registry
// (history table bun_proxy_memory_migrations) so a failed upgrade stays
// retryable on the next open.
package proxymemorystore
