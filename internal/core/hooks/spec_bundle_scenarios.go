package hooks

// HookScenarioSpec links a stable scenario identifier to hook-bus invariants
// and the primary regression test (specification bundle).
type HookScenarioSpec struct {
	ID               string
	InvariantSummary string
	TestName         string // exists in internal/core/hooks/*_test.go
}

// SpecBundleHookScenarios lists submit, request/response part, and tool-reactor
// invariants. Keep aligned with the referenced tests.
func SpecBundleHookScenarios() []HookScenarioSpec {
	return []HookScenarioSpec{
		{
			ID:               "SB-HOOK-submit-order",
			InvariantSummary: "Submit hooks run in stable order (Order, then ID, then registration index).",
			TestName:         "TestRunSubmit_ordersByOrderField",
		},
		{
			ID:               "SB-HOOK-submit-fail-open",
			InvariantSummary: "Fail-open submit hook errors are skipped; later hooks may run.",
			TestName:         "TestRunSubmit_failOpen_skipsHookError",
		},
		{
			ID:               "SB-HOOK-submit-fail-closed",
			InvariantSummary: "Fail-closed submit hook errors stop the chain.",
			TestName:         "TestRunSubmit_failClosed_stopsChain",
		},
		{
			ID:               "SB-HOOK-submit-panic-fail-open",
			InvariantSummary: "Fail-open: panic in one submit hook still runs subsequent hooks.",
			TestName:         "TestRunSubmit_failOpen_panicSecondHookStillRuns",
		},
		{
			ID:               "SB-HOOK-tool-reactor-chain",
			InvariantSummary: "Tool reactors can pass through, rewrite, or replace events in chain order.",
			TestName:         "TestApplyToolReactors_passThroughChain",
		},
		{
			ID:               "SB-HOOK-tool-reactor-invalid-canonical",
			InvariantSummary: "Rewrites that violate canonical tool-event shape fail closed.",
			TestName:         "TestApplyToolReactors_rewrite_invalidCanonicalFailsClosed",
		},
	}
}
