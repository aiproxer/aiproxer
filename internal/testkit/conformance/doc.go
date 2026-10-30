// Package conformance hosts the bundled frontend × backend conformance matrix (spec tasks 12.x).
// Tests drive official reference clients against frontend handlers, the core executor, and
// official backend connectors backed by reference backend emulators or protocol-faithful error stubs.
//
// The generic cell selector ([DeploymentSpec]/[Deploy]) and the TestCellSelect_SmokeScaffold_*
// tests are Phase 7 SMOKE SCAFFOLDING: they prove the reusable harness composes any
// authoritative cell with contract-fake origins and injectable failure modes. They are NOT
// Phase 8 compatibility evidence — Phase 8 replaces the contract-fake origins with the
// independent OpenResponses refbackend/refclient emulators and certifies every cell with
// official-wire scenarios (tasks 8.1–8.5).
package conformance
