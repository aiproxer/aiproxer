// Package b2bua holds core-owned B2BUA session store contracts: A-leg resolution,
// B-leg allocation, attempt lineage rows, and in-memory TTL semantics.
//
// The exported Store shape and ALegRecord/BLegRecord rows are mirrored in
// `pkg/aipsdk/continuity` for external documentation and tooling. Parity is
// enforced by store_contract_test.go in this package (run with
// `internal/core/b2bua` tests).
package b2bua
