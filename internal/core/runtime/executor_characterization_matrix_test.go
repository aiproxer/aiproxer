package runtime_test

// Phase 4.1 characterization matrix: maps each resolution-plan Execute scenario to
// an existing regression test. This file is the reviewer checklist; behavior is
// asserted in the referenced tests, not duplicated here.

import "testing"

func TestExecutor_characterizationMatrix_documented(t *testing.T) {
	t.Parallel()
	matrix := map[string]string{
		"call validation failure":                "TestExecutor_execute_callValidateFailure",
		"nil context behavior":                   "TestExecutor_execute_nilContext",
		"secure-session required":                "TestExecutor_secureSession_*",
		"selector alias resolution":              "TestExecutor_selectorAlias_opensMappedBackend",
		"model-only selector defaulting":         "TestBuildRoutePlan_modelOnlyAppliesDefaultBackend",
		"unresolved model-only failure":          "TestBuildRoutePlan_unresolvedModelOnlyFails",
		"pre-output failover":                    "TestExecutor_parallel_failover / TestReplayLineage_recvFailoverIncrementsBLegs",
		"no retry after output begins":           "TestRetryRecvStream_tryReplacement_blockedAfterMandatoryRecorderFailure",
		"B-leg lifecycle registration/cancel":    "TestLifecycle_*",
		"route preference behavior":              "TestExecutor_routePreference_*",
		"affinity identity behavior":             "TestExecutorSessionAffinity* / TestBuildRoutePlan_affinityIdentityError",
		"stream recovery behavior":               "TestAttemptStream_recovery*",
		"accounting preflight/ledger failure":    "TestExecutor_tokenAccounting*",
		"interleaved-thinking wrapper selection": "TestExecutor_interleaved* / TestInterleavedStream_*",
	}
	if len(matrix) < 14 {
		t.Fatalf("characterization matrix incomplete: %d entries", len(matrix))
	}
	for scenario, testRef := range matrix {
		if scenario == "" || testRef == "" {
			t.Fatalf("empty matrix entry: scenario=%q testRef=%q", scenario, testRef)
		}
	}
}
