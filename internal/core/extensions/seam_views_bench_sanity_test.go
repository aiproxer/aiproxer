package extensions_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/extensions"
)

// TestSeamViewsBenchmarkHarness_SanityCheck verifies that the benchmark harness measures what
// it claims (Acceptance Criterion 5 for Task 1.6):
// 1. A populated snapshot yields non-zero lengths / non-nil values across all five benched accessor families.
// 2. Documented defensive clones produce distinct backing arrays (independent allocations).
// 3. Execution / direct accessors share backing arrays / return direct instances without allocations.
func TestSeamViewsBenchmarkHarness_SanityCheck(t *testing.T) {
	t.Parallel()

	snap := newBenchPopulatedSnapshot()

	// --- 1. Verify populated snapshot yields non-zero lengths / non-nil values ---

	// Family 1: Completion Gates
	gates := snap.CompletionGates()
	if len(gates) == 0 {
		t.Fatalf("expected non-empty CompletionGates on populated snapshot")
	}
	ctx := extensions.WithRequestRuntimeSnapshot(context.Background(), snap)
	ctxGates := extensions.CompletionGatesFromContext(ctx, nil)
	if len(ctxGates) == 0 {
		t.Fatalf("expected non-empty CompletionGatesFromContext with populated context snapshot")
	}
	fbGates := extensions.CompletionGatesFromContext(context.Background(), snap)
	if len(fbGates) == 0 {
		t.Fatalf("expected non-empty CompletionGatesFromContext with populated fallback")
	}

	// Family 2: Traffic Seam
	portBundle := snap.TrafficPortBundle()
	if portBundle.Obs == nil {
		t.Fatalf("expected non-nil TrafficPortBundle.Obs on populated snapshot")
	}
	if portBundle.Raw == nil {
		t.Fatalf("expected non-nil TrafficPortBundle.Raw on populated snapshot")
	}
	if len(portBundle.Red) == 0 {
		t.Fatalf("expected non-empty TrafficPortBundle.Red on populated snapshot")
	}
	if snap.TrafficObserver() == nil {
		t.Fatalf("expected non-nil TrafficObserver on populated snapshot")
	}
	redactors := snap.TrafficRedactors()
	if len(redactors) == 0 {
		t.Fatalf("expected non-empty TrafficRedactors on populated snapshot")
	}

	// Family 3: Secret Guard Plane
	sgPlane := snap.SecretGuardPlane()
	if len(sgPlane.Guards) == 0 {
		t.Fatalf("expected non-empty SecretGuardPlane.Guards on populated snapshot")
	}
	sgExecPlane := snap.SecretGuardExecutionPlane()
	if len(sgExecPlane.Guards) == 0 {
		t.Fatalf("expected non-empty SecretGuardExecutionPlane.Guards on populated snapshot")
	}

	// Family 4: Compaction Observers & Preservers
	compactObs := snap.CompactionObservers()
	if len(compactObs) == 0 {
		t.Fatalf("expected non-empty CompactionObservers on populated snapshot")
	}
	compactPres := snap.CompactionPreservers()
	if len(compactPres) == 0 {
		t.Fatalf("expected non-empty CompactionPreservers on populated snapshot")
	}

	// Family 5: Terminal Decision Provider
	termProvider := snap.TerminalDecisionProvider()
	if termProvider == nil {
		t.Fatalf("expected non-nil TerminalDecisionProvider on populated snapshot")
	}

	// Family 6: Stream Observer Factories
	streamObs := snap.StreamObserverFactories()
	if len(streamObs) == 0 {
		t.Fatalf("expected non-empty StreamObserverFactories on populated snapshot")
	}

	// --- 2. Verify defensive-cloning produces distinct backing arrays ---

	// Family 1: CompletionGates defensive clone
	gates1 := snap.CompletionGates()
	gates2 := snap.CompletionGates()
	if &gates1[0] == &gates2[0] {
		t.Fatalf("expected distinct backing array pointers for CompletionGates defensive clones")
	}
	gates1[0] = benchGate{id: "mutated"}
	if snap.CompletionGates()[0].ID() == "mutated" {
		t.Fatalf("snapshot CompletionGates mutated via cloned slice")
	}

	// Family 2: TrafficRedactors defensive clone
	red1 := snap.TrafficRedactors()
	red2 := snap.TrafficRedactors()
	if &red1[0] == &red2[0] {
		t.Fatalf("expected distinct backing array pointers for TrafficRedactors defensive clones")
	}
	red1[0] = benchTrafficRed{id: "mutated"}
	if snap.TrafficRedactors()[0].ID() == "mutated" {
		t.Fatalf("snapshot TrafficRedactors mutated via cloned slice")
	}

	// Family 3: SecretGuardPlane defensive clone (Guards slice)
	sg1 := snap.SecretGuardPlane()
	sg2 := snap.SecretGuardPlane()
	if &sg1.Guards[0] == &sg2.Guards[0] {
		t.Fatalf("expected distinct backing array pointers for SecretGuardPlane.Guards defensive clones")
	}
	sg1.Guards[0] = benchSecretGuard{id: "mutated"}
	if snap.SecretGuardPlane().Guards[0].ID() == "mutated" {
		t.Fatalf("snapshot SecretGuardPlane.Guards mutated via cloned slice")
	}

	// Family 4: CompactionObservers defensive clone
	co1 := snap.CompactionObservers()
	co2 := snap.CompactionObservers()
	if &co1[0] == &co2[0] {
		t.Fatalf("expected distinct backing array pointers for CompactionObservers defensive clones")
	}

	// Family 4: CompactionPreservers defensive clone
	cp1 := snap.CompactionPreservers()
	cp2 := snap.CompactionPreservers()
	if &cp1[0] == &cp2[0] {
		t.Fatalf("expected distinct backing array pointers for CompactionPreservers defensive clones")
	}
	cp1[0] = benchCompactionPreserver{id: "mutated"}
	if snap.CompactionPreservers()[0].ID() == "mutated" {
		t.Fatalf("snapshot CompactionPreservers mutated via cloned slice")
	}

	// Family 6: StreamObserverFactories defensive clone
	so1 := snap.StreamObserverFactories()
	so2 := snap.StreamObserverFactories()
	if &so1[0] == &so2[0] {
		t.Fatalf("expected distinct backing array pointers for StreamObserverFactories defensive clones")
	}
	so1[0] = benchStreamObsFactory{id: "mutated"}
	if snap.StreamObserverFactories()[0].ID() == "mutated" {
		t.Fatalf("snapshot StreamObserverFactories mutated via cloned slice")
	}

	// --- 3. Verify execution / direct accessors reuse backing arrays / return same instances ---

	// Family 3: SecretGuardExecutionPlane shares backing array
	exec1 := snap.SecretGuardExecutionPlane()
	exec2 := snap.SecretGuardExecutionPlane()
	if &exec1.Guards[0] != &exec2.Guards[0] {
		t.Fatalf("expected identical backing array pointers for SecretGuardExecutionPlane")
	}

	// Family 2: TrafficObserver returns identical interface instance
	to1 := snap.TrafficObserver()
	to2 := snap.TrafficObserver()
	if to1 != to2 {
		t.Fatalf("expected identical TrafficObserver instances")
	}

	// Family 5: TerminalDecisionProvider returns identical interface instance
	tp1 := snap.TerminalDecisionProvider()
	tp2 := snap.TerminalDecisionProvider()
	if tp1 != tp2 {
		t.Fatalf("expected identical TerminalDecisionProvider instances")
	}

	// --- 4. Verify empty / nil snapshot safety ---

	emptySnap := newBenchEmptySnapshot()
	if got := emptySnap.CompletionGates(); len(got) != 0 {
		t.Fatalf("expected empty CompletionGates on empty snapshot, got %v", got)
	}
	if got := emptySnap.TrafficRedactors(); len(got) != 0 {
		t.Fatalf("expected empty TrafficRedactors on empty snapshot, got %v", got)
	}
	if got := emptySnap.StreamObserverFactories(); len(got) != 0 {
		t.Fatalf("expected empty StreamObserverFactories on empty snapshot, got %v", got)
	}
	if got := emptySnap.CompactionObservers(); len(got) != 0 {
		t.Fatalf("expected empty CompactionObservers on empty snapshot, got %v", got)
	}
	if got := emptySnap.CompactionPreservers(); len(got) != 0 {
		t.Fatalf("expected empty CompactionPreservers on empty snapshot, got %v", got)
	}
	if got := emptySnap.TerminalDecisionProvider(); got != nil {
		t.Fatalf("expected nil TerminalDecisionProvider on empty snapshot, got %v", got)
	}

	var nilSnap *extensions.RequestRuntimeSnapshot
	if got := nilSnap.CompletionGates(); got != nil {
		t.Fatalf("expected nil on nil snapshot")
	}
	if got := nilSnap.TrafficPortBundle(); got.Obs != nil || got.Raw != nil || len(got.Red) != 0 {
		t.Fatalf("expected empty PortBundle on nil snapshot, got %+v", got)
	}
	if got := nilSnap.TrafficObserver(); got != nil {
		t.Fatalf("expected nil on nil snapshot")
	}
	if got := nilSnap.TrafficRedactors(); got != nil {
		t.Fatalf("expected nil on nil snapshot")
	}
	if got := nilSnap.StreamObserverFactories(); got != nil {
		t.Fatalf("expected nil on nil snapshot")
	}
	if got := nilSnap.SecretGuardPlane(); len(got.Guards) != 0 {
		t.Fatalf("expected empty SecretGuardPlane on nil snapshot")
	}
	if got := nilSnap.SecretGuardExecutionPlane(); len(got.Guards) != 0 {
		t.Fatalf("expected empty SecretGuardExecutionPlane on nil snapshot")
	}
	if got := nilSnap.CompactionObservers(); got != nil {
		t.Fatalf("expected nil on nil snapshot")
	}
	if got := nilSnap.CompactionPreservers(); got != nil {
		t.Fatalf("expected nil on nil snapshot")
	}
	if got := nilSnap.TerminalDecisionProvider(); got != nil {
		t.Fatalf("expected nil on nil snapshot")
	}
}
