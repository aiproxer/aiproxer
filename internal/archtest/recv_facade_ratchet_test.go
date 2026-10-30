package archtest

import "testing"

// TestRecvFacadeRatchet enforces the P2 architecture gate from the
// adversarial review: the receive façade must not grow its coupling.
// It is a lightweight wrapper around the turn/recv ownership inventory
// that pins the 5 metrics the review asked for:
//
//   - receiver method count on retryRecvStream
//   - domain package fan-out (imports of receive-loop)
//   - cross-domain method count
//   - state-copy assignment count (field-by-field reconstruction)
//   - sync-primitive count
//
// Targets are set to the post-refactor baseline (turn_recv_ownership_baseline.json
// current values). Any increase fails the ratchet and requires an ADR.
func TestRecvFacadeRatchet(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	current, err := scanTurnRecvOwnership(root)
	if err != nil {
		t.Fatalf("scan turn/recv ownership: %v", err)
	}
	// Baselines captured at 2026-08-21 after P1 AbortBeforeReturn fix.
	// See internal/archtest/testdata/turn_recv_ownership_baseline.json
	const (
		maxMethodCount          = 91
		maxDomainFanout         = 34
		maxCrossDomainMethods   = 89
		maxStateCopyAssignments = 62
		maxSyncPrimitives       = 12
		maxExecutorReachable    = 39
		maxFieldCount           = 89
	)
	if current.MethodCount > maxMethodCount {
		t.Fatalf("retryRecvStream method count %d > ratchet %d (new cross-domain logic must move to owners)", current.MethodCount, maxMethodCount)
	}
	if current.DomainPackageFanoutCount > maxDomainFanout {
		t.Fatalf("domain package fan-out %d > ratchet %d (receive-loop must not import new domains)", current.DomainPackageFanoutCount, maxDomainFanout)
	}
	if current.CrossDomainMethodCount > maxCrossDomainMethods {
		t.Fatalf("cross-domain method count %d > ratchet %d", current.CrossDomainMethodCount, maxCrossDomainMethods)
	}
	if current.StateCopyAssignmentCount > maxStateCopyAssignments {
		t.Fatalf("state-copy assignments %d > ratchet %d (field-by-field reconstruction must not grow)", current.StateCopyAssignmentCount, maxStateCopyAssignments)
	}
	if current.SyncPrimitiveCount > maxSyncPrimitives {
		t.Fatalf("sync primitive count %d > ratchet %d", current.SyncPrimitiveCount, maxSyncPrimitives)
	}
	if current.ExecutorReachability.MethodCount > maxExecutorReachable {
		t.Fatalf("executor-reachable method count %d > ratchet %d (facade must not re-acquire *Executor)", current.ExecutorReachability.MethodCount, maxExecutorReachable)
	}
	if current.FieldCount > maxFieldCount {
		t.Fatalf("field count %d > ratchet %d", current.FieldCount, maxFieldCount)
	}
	// Raw inner access is already fenced by TestAttemptSessionInnerFence, but
	// we double-check here as a P2 gate.
	if current.FieldCount == 0 {
		t.Fatal("invariant: field count must be non-zero")
	}
}
