// Package leasestore provides memory, SQLite, and PostgreSQL backends for the
// concurrency-authority LeaseStore port.
//
// Memory is single-process only. SQLite serializes local writers and reports
// single-node readiness limits. PostgreSQL is the distributed strict reference.
// Journals are not used as live lease authority (requirement 13.5).
package leasestore
