package routing

// RoutingScenarioSpec links a stable scenario identifier to route-selector and planning
// invariants and the primary regression test (specification bundle).
type RoutingScenarioSpec struct {
	ID               string
	InvariantSummary string
	TestName         string // exists in internal/core/routing/*_test.go
}

// SpecBundleRoutingScenarios lists selector, alias, and planner invariants. Keep aligned
// with .kiro/steering/routing-and-orchestration.md and the referenced tests.
func SpecBundleRoutingScenarios() []RoutingScenarioSpec {
	return []RoutingScenarioSpec{
		{
			ID:               "SB-ROUTE-parse-primaries",
			InvariantSummary: "Route selector parsing extracts primary backend ids in order.",
			TestName:         "TestParsePrimaries",
		},
		{
			ID:               "SB-ROUTE-parse-failover-chain",
			InvariantSummary: "Failover order syntax expands backup primaries after `=>`.",
			TestName:         "TestParseFailoverOrder",
		},
		{
			ID:               "SB-ROUTE-parse-weighted-first",
			InvariantSummary: "Weighted and `[first]` arms parse without ambiguity.",
			TestName:         "TestParseWeightedAndFirst",
		},
		{
			ID:               "SB-ROUTE-parse-ttft-timeouts",
			InvariantSummary: "Global and per-leaf TTFT timeout annotations parse into routing metadata.",
			TestName:         "TestParseTTFTTimeoutAnnotations",
		},
		{
			ID:               "SB-ROUTE-parse-affinity",
			InvariantSummary: "Global route affinity annotations parse as selector-wide metadata and leaf-scoped stickiness is rejected.",
			TestName:         "TestParseGlobalAffinity",
		},
		{
			ID:               "SB-ROUTE-alias-exact",
			InvariantSummary: "Model alias resolver applies exact pattern matches before routing.",
			TestName:         "TestAliasResolver_exactMatch",
		},
		{
			ID:               "SB-ROUTE-planner-failover-order",
			InvariantSummary: "Failover expansion preserves left-to-right primaries when eligible.",
			TestName:         "TestExpandFailoverLeftToRightPrimaries",
		},
		{
			ID:               "SB-ROUTE-weighted-deterministic",
			InvariantSummary: "Weighted selection is deterministic for a fixed branch key and weight table.",
			TestName:         "TestWeightedDeterministic",
		},
		{
			ID:               "SB-ROUTE-model-only-backends",
			InvariantSummary: "Model-only backend hints apply to the resolved route list.",
			TestName:         "TestApplyModelOnlyBackends",
		},
		{
			ID:               "SB-ROUTE-planner-ttft-metadata",
			InvariantSummary: "Failover expansion preserves TTFT timeout metadata without changing candidate identity.",
			TestName:         "TestExpandFailoverPreservesTTFTTimeoutMetadata",
		},
		{
			ID:               "SB-ROUTE-parse-parallel-basic",
			InvariantSummary: "Parallel '!' separator produces a parallel group with correct branch count and targets.",
			TestName:         "TestParseParallelBasic",
		},
		{
			ID:               "SB-ROUTE-parse-parallel-handicap",
			InvariantSummary: "Per-leg [handicap=N] annotations parse into parallel branch metadata.",
			TestName:         "TestParseParallelHandicap",
		},
		{
			ID:               "SB-ROUTE-parse-parallel-user-example",
			InvariantSummary: "Full user-provided parallel selector with mixed handicap and ttft_timeout annotations parses correctly.",
			TestName:         "TestParseParallelUserExample",
		},
		{
			ID:               "SB-ROUTE-parse-parallel-failover-of-groups",
			InvariantSummary: "Failover '|' of parallel groups produces separate parallel arms.",
			TestName:         "TestParseParallelFailoverOfParallelGroups",
		},
		{
			ID:               "SB-ROUTE-parse-parallel-rejects-weighted-mix",
			InvariantSummary: "Parallel '!' mixed with weighted '^'/[weight]/[first] is rejected.",
			TestName:         "TestParseParallelRejectsMixedWithWeighted",
		},
		{
			ID:               "SB-ROUTE-planner-parallel-handicap-metadata",
			InvariantSummary: "Failover expansion preserves handicap metadata on parallel legs.",
			TestName:         "TestExpandFailoverParallelPreservesHandicapMetadata",
		},
		{
			ID:               "SB-ROUTE-model-only-parallel",
			InvariantSummary: "Model-only backend fill applies to parallel branches.",
			TestName:         "TestApplyModelOnlyBackendsParallelBranches",
		},
		{
			ID:               "SB-ROUTE-parse-thinker-forms",
			InvariantSummary: "[thinker] accepts bare and true-valued forms on weighted branches.",
			TestName:         "TestParseThinkerAcceptedForms",
		},
		{
			ID:               "SB-ROUTE-parse-thinker-hybrid",
			InvariantSummary: "One thinker weighted branch plus one non-thinker branch with an embedded parallel executor group parses.",
			TestName:         "TestParseThinkerParallelHybridAccepted",
		},
		{
			ID:               "SB-ROUTE-planner-thinker-cycle",
			InvariantSummary: "Thinker-weighted cycle picks executor branches by weight, then thinker, wrapping NextIndex after each lap.",
			TestName:         "TestPickThinkerCycle_AdvancesCursorAndWraps",
		},
		{
			ID:               "SB-ROUTE-planner-thinker-suppression",
			InvariantSummary: "SuppressThinker skips the thinker cycle position and returns the next executor candidate.",
			TestName:         "TestPickThinkerCycle_SuppressThinkerPicksExecutor",
		},
		{
			ID:               "SB-ROUTE-regress-non-thinker-noninterference",
			InvariantSummary: "Selectors without [thinker] preserve weighted, failover, parallel, [first], health, and context-size behavior with empty interleaved role and selector key.",
			TestName:         "TestNonThinkerSelectorsPreserveExistingBehavior",
		},
	}
}
