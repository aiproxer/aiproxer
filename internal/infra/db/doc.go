// Package db provides internal-only database infrastructure: opening managed connections,
// wrapping *sql.DB with Bun for supported dialects, connection pool tuning,
// driver-error classification for constraint/lock contention, and
// secret-safe DSN and error redaction.
//
// This package is not a business or domain layer. It does not define continuity,
// secure-session, or other application ports or use-case contracts. Callers that
// may import it are limited to runtime composition roots and concrete store adapters
// under internal/core; public packages (pkg/aipapi, pkg/aipsdk) and protocol plugins
// must not depend on it.
package db
