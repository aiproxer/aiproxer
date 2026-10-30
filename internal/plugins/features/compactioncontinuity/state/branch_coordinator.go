// Package state contains the process-owned state coordination
// used by compaction preservation. It deliberately does not interpret capsule
// facts or execute background work; it only binds bounded state to the
// authoritative parent branch and serializes lifecycle updates.
package state
