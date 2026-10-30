// Package journalstore provides append-only metering fact journal adapters:
// in-memory for tests/local use, and Bun-backed SQLite/Postgres for durable
// deployments. Adapters implement pkg/aipsdk/metering Recorder and Querier
// without coupling to live authority counters or proprietary financial ledgers
// (requirements 13.1, 13.5, 13.8).
package journalstore
