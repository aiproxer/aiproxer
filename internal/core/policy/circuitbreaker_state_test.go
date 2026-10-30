package policy_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestCircuitBreakerState_SharedAcrossPolicies proves two policy views built
// from the same shared observation state see each other's recorded failures
// (compatible-generation health observation must survive candidate recompile;
// design "Health policy reload" / req 7.4, 9.1).
func TestCircuitBreakerState_SharedAcrossPolicies(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()
	state := policy.NewCircuitBreakerState(policy.CircuitBreakerStateOptions{})
	p1 := policy.NewCircuitBreakerPolicy(state, policy.CircuitBreakerOptions{
		FailureThreshold: 2,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	p2 := policy.NewCircuitBreakerPolicy(state, policy.CircuitBreakerOptions{
		FailureThreshold: 2,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})

	key := "be:model"
	p1.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	if u := p2.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("unexpected unhealthy before threshold on p2: %v", u)
	}
	p1.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)

	u2 := p2.UnhealthyCandidateKeys()
	if _, ok := u2[key]; !ok {
		t.Fatalf("p2 must observe failures recorded via p1 (shared state), got %v", u2)
	}
}

// TestCircuitBreakerPolicy_IndependentThresholdPerGeneration proves each
// policy view evaluates using its own threshold against the same shared
// observation counters (req 7.4, 9.1).
func TestCircuitBreakerPolicy_IndependentThresholdPerGeneration(t *testing.T) {
	t.Parallel()
	now := time.Unix(2000, 0).UTC()
	state := policy.NewCircuitBreakerState(policy.CircuitBreakerStateOptions{})
	strict := policy.NewCircuitBreakerPolicy(state, policy.CircuitBreakerOptions{
		FailureThreshold: 1,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	lenient := policy.NewCircuitBreakerPolicy(state, policy.CircuitBreakerOptions{
		FailureThreshold: 5,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})

	key := "be:model"
	// Lenient writer records one failure without opening (threshold=5).
	lenient.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)

	if u := strict.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("strict policy (threshold=1) must open after 1 shared failure, got %v", u)
	}
	if u := lenient.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("lenient policy (threshold=5) must not open after 1 failure, got %v", u)
	}
}

// TestCircuitBreakerPolicy_SuccessResetsSharedState proves a success recorded
// through one policy view clears the shared counter observed by another.
func TestCircuitBreakerPolicy_SuccessResetsSharedState(t *testing.T) {
	t.Parallel()
	now := time.Unix(3000, 0).UTC()
	state := policy.NewCircuitBreakerState(policy.CircuitBreakerStateOptions{})
	p1 := policy.NewCircuitBreakerPolicy(state, policy.CircuitBreakerOptions{
		FailureThreshold: 3,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	p2 := policy.NewCircuitBreakerPolicy(state, policy.CircuitBreakerOptions{
		FailureThreshold: 3,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})

	key := "x:y"
	p1.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	p2.OnRoutingAttemptOutcome(key, aipapi.AttemptSuccess)
	p1.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	if u := p1.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("success via p2 must reset streak observed by p1: %v", u)
	}
}

func TestCircuitBreakerPolicy_NilStateUsesFreshPrivateState(t *testing.T) {
	t.Parallel()
	p := policy.NewCircuitBreakerPolicy(nil, policy.CircuitBreakerOptions{
		FailureThreshold: 1,
		OpenDuration:     time.Hour,
	})
	if p == nil {
		t.Fatal("expected non-nil policy with nil state")
	}
	p.OnRoutingAttemptOutcome("a:b", aipapi.AttemptSurfacedFailure)
	if u := p.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("expected key to open, got %v", u)
	}
}

var (
	_ policy.CandidateHealth           = (*policy.CircuitBreakerPolicy)(nil)
	_ policy.RoutingAttemptOutcomeSink = (*policy.CircuitBreakerPolicy)(nil)
)
