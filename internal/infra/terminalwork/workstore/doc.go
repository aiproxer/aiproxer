// Package workstore provides memory, SQLite, and PostgreSQL backends for durable
// terminal-work intent, claims, retry, and quarantine (requirements 8.1–8.9).
//
// Migrations use direct/admin connections; runtime pools verify schema only
// (design D12).
package workstore
