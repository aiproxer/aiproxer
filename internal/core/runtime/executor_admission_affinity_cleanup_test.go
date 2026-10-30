package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/affinity/memorystore"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestNoteCandidateAdmissionReject_ClearsStickyAffinityBinding(t *testing.T) {
	ctx := context.Background()
	affinityStore := memorystore.New()
	key := affinity.Key{Scope: affinity.ScopeSession, ID: "session-affinity-id"}
	if err := affinityStore.Set(ctx, affinity.Binding{
		Key:          key,
		BackendID:    "rejected-backend",
		CandidateKey: "rejected-backend:model",
	}); err != nil {
		t.Fatal(err)
	}

	ex := TestExecutor()
	ex.AffinityStore = affinityStore
	ex.Bus = hooks.New(hooks.Config{})
	traceID := "trace-affinity-reject"
	affinityKey := key
	affinitySet := true
	candidate := routing.AttemptCandidate{
		Key:     "rejected-backend:model",
		Primary: routing.Primary{Backend: "rejected-backend", Model: "model"},
	}
	out := candidateAdmissionOutcome{admitRes: aipapi.CandidateAdmissionResult{
		Kind:       aipapi.NegotiationReject,
		Capability: aipapi.NegotiationResult{Kind: aipapi.NegotiationReject},
	}}

	// This is the production rejection handler used by openPlannedCandidate after
	// real capability/transport admission, not the core TCK's synthetic callback.
	ex.noteCandidateAdmissionReject(ctx, traceID, affinityKey, affinitySet, candidate, "rejected-backend", true, out, "pre_open", nil)

	if _, ok, err := affinityStore.Get(ctx, key); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("sticky affinity binding survived rejected candidate admission")
	}
}
