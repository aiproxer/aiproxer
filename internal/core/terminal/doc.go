// Package terminal implements the pure-domain request/attempt terminal owner
// state machine (requirements 7.1–7.8, design D8, D13).
//
// One CAS Claim(command) wins per owner; the winner snapshots accumulators
// once and publishes an Outcome. Concurrent losers observe the same result
// without re-running effects. Domain code has no SQL, HTTP, or filesystem I/O.
package terminal
