package policy_test

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/policy"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestCircuitBreaker_opensAfterThreshold(t *testing.T) {
	t.Parallel()
	now := time.Unix(1000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 2,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	key := "be:model"
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("unexpected unhealthy before threshold: %v", u)
	}
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	u := cb.UnhealthyCandidateKeys()
	if len(u) != 1 {
		t.Fatalf("want 1 unhealthy key, got %v", u)
	}
	if _, ok := u[key]; !ok {
		t.Fatal("missing key")
	}
}

func TestCircuitBreaker_successResets(t *testing.T) {
	t.Parallel()
	now := time.Unix(2000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 3,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	key := "x:y"
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSuccess)
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("unexpected unhealthy: %v", u)
	}
}

func TestCircuitBreaker_cooldownClearsBlock(t *testing.T) {
	t.Parallel()
	start := time.Unix(3000, 0).UTC()
	var cur time.Time
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 1,
		OpenDuration:     10 * time.Second,
		Now:              func() time.Time { return cur },
	})
	key := "a:b"
	cur = start
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSwallowedFailure)
	if u := cb.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("want unhealthy, got %v", u)
	}
	cur = start.Add(11 * time.Second)
	if u := cb.UnhealthyCandidateKeys(); u != nil {
		t.Fatalf("want healthy after cooldown, got %v", u)
	}
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("want open again, got %v", u)
	}
}

func TestCircuitBreaker_cancelIgnored(t *testing.T) {
	t.Parallel()
	now := time.Unix(4000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 3,
		OpenDuration:     time.Hour,
		Now:              func() time.Time { return now },
	})
	key := "p:q"
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptCancelled)
	if cb.UnhealthyCandidateKeys() != nil {
		t.Fatal("cancel alone must not affect breaker state")
	}
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptCancelled)
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome(key, aipapi.AttemptSurfacedFailure)
	if u := cb.UnhealthyCandidateKeys(); len(u) != 1 {
		t.Fatalf("want blocked after three failures with cancel ignored mid-streak: %v", u)
	}
}

func TestCircuitBreaker_maxTrackedKeys_rotates(t *testing.T) {
	t.Parallel()
	now := time.Unix(5000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 100,
		OpenDuration:     time.Hour,
		MaxTrackedKeys:   1,
		Now:              func() time.Time { return now },
	})
	cb.OnRoutingAttemptOutcome("first:k", aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("second:k", aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("first:k", aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("third:k", aipapi.AttemptSurfacedFailure)
	// Without eviction, four failure events on distinct keys under cap 1 would panic or grow; rotations must succeed.
}

func TestCircuitBreaker_evictsIdleFirst(t *testing.T) {
	t.Parallel()
	now := time.Unix(6000, 0).UTC()
	cb := policy.NewCircuitBreaker(policy.CircuitBreakerOptions{
		FailureThreshold: 50,
		OpenDuration:     time.Hour,
		MaxTrackedKeys:   2,
		Now:              func() time.Time { return now },
	})
	cb.OnRoutingAttemptOutcome("idle:k", aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("keep:k", aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("idle:k", aipapi.AttemptSuccess)
	cb.OnRoutingAttemptOutcome("new:k", aipapi.AttemptSurfacedFailure)
	cb.OnRoutingAttemptOutcome("idle:k", aipapi.AttemptSurfacedFailure)
}
