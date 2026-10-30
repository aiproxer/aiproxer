package b2bua

// ContinuityScenarioSpec links a stable scenario identifier to continuity/B2BUA store behavior
// and the primary regression test that exercises it (specification bundle).
type ContinuityScenarioSpec struct {
	ID               string
	InvariantSummary string
	TestName         string // exists in internal/core/b2bua/*_test.go
}

// SpecBundleContinuityScenarios lists memory-store and continuity invariants. Keep aligned
// with .kiro/steering/routing-and-orchestration.md and the referenced tests.
func SpecBundleContinuityScenarios() []ContinuityScenarioSpec {
	return []ContinuityScenarioSpec{
		{
			ID:               "SB-CONT-round-trip",
			InvariantSummary: "Resolve + Create round-trip preserves continuity key semantics.",
			TestName:         "TestMemoryStore_ResolveCreate_roundTripContinuity",
		},
		{
			ID:               "SB-CONT-replace-same-key",
			InvariantSummary: "Creating with the same continuity key replaces the prior A-leg.",
			TestName:         "TestMemoryStore_Create_sameContinuityKeyReplacesOldALeg",
		},
		{
			ID:               "SB-CONT-weighted-first-persisted",
			InvariantSummary: "Weighted-first consumed flag persists on the A-leg record.",
			TestName:         "TestMemoryStore_WeightedFirstConsumed_persists",
		},
		{
			ID:               "SB-CONT-bleg-monotonic",
			InvariantSummary: "B-leg ids allocate monotonic sequence numbers per A-leg.",
			TestName:         "TestMemoryStore_NextBLeg_monotonicSeq",
		},
		{
			ID:               "SB-CONT-attempt-lineage-order",
			InvariantSummary: "RecordAttempt stores attempts; LoadAttempts returns stable order.",
			TestName:         "TestMemoryStore_RecordAttempt_and_LoadAttempts_order",
		},
		{
			ID:               "SB-CONT-attempt-wrong-bleg-rejected",
			InvariantSummary: "Recording an attempt with a wrong B-leg id is rejected.",
			TestName:         "TestMemoryStore_RecordAttempt_rejectsWrongBLegID",
		},
		{
			ID:               "SB-CONT-ttl-sweep-anonymous",
			InvariantSummary: "TTL sweeps stale anonymous legs on create when configured.",
			TestName:         "TestMemoryStore_TTL_sweepsStaleAnonymousLegsOnCreate",
		},
		{
			ID:               "SB-CONT-contract-store-interface",
			InvariantSummary: "Store interface matches SDK continuity contract shape.",
			TestName:         "TestContinuityContract_StoreInterfaceMatchesSDK",
		},
		{
			ID:               "SB-CONT-interleaved-state-round-trip",
			InvariantSummary: "Thinker cycle state and memo reference round-trip on the A-leg; empty state is harmless for routes without thinker.",
			TestName:         "TestMemoryStore_InterleavedState",
		},
	}
}
