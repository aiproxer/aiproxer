package runtime

import (
	"sync"
	"testing"
)

// TestStructuralCauseClaims_SameRequestPublishesOncePerKey proves the
// publication rule the prompt-cache diagnostic depends on: a request that
// changed the cached prefix publishes that change once per cache key, so a
// later leg of the same request cannot re-arm a cause that a healthy sample
// already proved stale.
func TestStructuralCauseClaims_SameRequestPublishesOncePerKey(t *testing.T) {
	t.Parallel()

	facts := requestFacts{structuralCause: newStructuralCauseClaims()}

	if got := facts.claimStructuralCause("compaction", "backend", "model"); got != "compaction" {
		t.Fatalf("first claim = %q, want the cause", got)
	}
	// The same request, the same key: the change happened once.
	if got := facts.claimStructuralCause("compaction", "backend", "model"); got != "" {
		t.Fatalf("second claim on the same key = %q, want empty", got)
	}
	// The same request, another key: every candidate key is attributed.
	if got := facts.claimStructuralCause("compaction", "backend", "other-model"); got != "compaction" {
		t.Fatalf("claim on another key = %q, want the cause", got)
	}
	// A leg without a change claims nothing and consumes no publication.
	if got := facts.claimStructuralCause("", "backend", "third-model"); got != "" {
		t.Fatalf("claim without a cause = %q, want empty", got)
	}
	if got := facts.claimStructuralCause("compaction", "backend", "third-model"); got != "compaction" {
		t.Fatalf("claim after an empty cause = %q, want the cause", got)
	}
}

// TestStructuralCauseClaims_InterleavedRequests proves the rule holds when
// requests interleave: a second request publishing its own cause never
// re-arms the first request's cause, and the first request's later leg stays
// suppressed.
func TestStructuralCauseClaims_InterleavedRequests(t *testing.T) {
	t.Parallel()

	first := requestFacts{structuralCause: newStructuralCauseClaims()}
	second := requestFacts{structuralCause: newStructuralCauseClaims()}
	key := structuralCauseKey{backend: "backend", model: "model"}

	if got := first.claimStructuralCause("compaction", key.backend, key.model); got != "compaction" {
		t.Fatalf("first request claim = %q, want the cause", got)
	}
	// A healthy sample of the first request clears the cause in the
	// diagnostic; a different request publishes its own cause meanwhile.
	if got := second.claimStructuralCause("compaction", key.backend, key.model); got != "compaction" {
		t.Fatalf("second request claim = %q, want the cause", got)
	}
	// The first request's later leg must not resurrect its own stale cause.
	if got := first.claimStructuralCause("compaction", key.backend, key.model); got != "" {
		t.Fatalf("interleaved first request claim = %q, want empty", got)
	}
	// The second request's later leg is equally suppressed.
	if got := second.claimStructuralCause("compaction", key.backend, key.model); got != "" {
		t.Fatalf("interleaved second request claim = %q, want empty", got)
	}
}

// TestStructuralCauseClaims_ConcurrentClaimsHaveOneWinner proves the claim is
// safe under the parallel fan-out: exactly one attempt of a request publishes
// the cause for a cache key, whatever order the attempts reach the claim.
func TestStructuralCauseClaims_ConcurrentClaimsHaveOneWinner(t *testing.T) {
	t.Parallel()

	claims := newStructuralCauseClaims()
	const attempts = 64
	var wg sync.WaitGroup
	winners := make([]bool, attempts)
	start := make(chan struct{})
	for i := range attempts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			winners[i] = claims.publish(structuralCauseKey{backend: "backend", model: "model"})
		}(i)
	}
	close(start)
	wg.Wait()

	count := 0
	for _, won := range winners {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("winners = %d, want exactly 1", count)
	}
}

// TestStructuralCauseClaims_NilClaimsPublish proves a request without a claim
// set still publishes: the publication rule never drops a cause.
func TestStructuralCauseClaims_NilClaimsPublish(t *testing.T) {
	t.Parallel()

	var facts requestFacts
	if got := facts.claimStructuralCause("compaction", "backend", "model"); got != "compaction" {
		t.Fatalf("claim without a claim set = %q, want the cause", got)
	}
}

// TestRecoveryControllerOwnsOnePublicationClaimSet proves a request owns one
// claim set however many times its controller is reused: an attempt opened
// through the controller and a later replacement publish against the same keys.
func TestRecoveryControllerOwnsOnePublicationClaimSet(t *testing.T) {
	t.Parallel()

	budget := &attemptBudget{max: 3}
	first := newRecoveryController(recoveryControllerInput{budget: budget})
	again := newRecoveryController(recoveryControllerInput{budget: budget})
	if first != again {
		t.Fatal("a request reuses its recovery controller")
	}
	if first.causeClaims() == nil {
		t.Fatal("a request must own a publication claim set")
	}
	firstAttempt := requestFacts{structuralCause: first.causeClaims()}
	laterAttempt := requestFacts{structuralCause: again.causeClaims()}
	if got := firstAttempt.claimStructuralCause("compaction", "backend", "model"); got != "compaction" {
		t.Fatalf("first attempt claim = %q, want the cause", got)
	}
	if got := laterAttempt.claimStructuralCause("compaction", "backend", "model"); got != "" {
		t.Fatalf("a later attempt of the same request claim = %q, want empty", got)
	}
}
