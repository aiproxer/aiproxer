package session_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestSessionViewContextDefensivelyCopiesLabels(t *testing.T) {
	t.Parallel()

	labels := map[string]string{"feature": "enabled"}
	ctx := session.WithSessionView(context.Background(), session.SessionView{
		AuthoritativeSessionID: "session-authoritative",
		ClientSessionHint:      "client-hint",
		ALegID:                 "a-leg",
		TurnID:                 "turn",
		Labels:                 labels,
	})
	labels["feature"] = "mutated"

	got, ok := session.SessionViewFromContext(ctx)
	if !ok {
		t.Fatal("session view missing")
	}
	if got.AuthoritativeSessionID != "session-authoritative" || got.ClientSessionHint != "client-hint" || got.ALegID != "a-leg" || got.TurnID != "turn" || got.Labels["feature"] != "enabled" {
		t.Fatalf("session view = %+v", got)
	}
	got.Labels["feature"] = "mutated-again"
	gotAgain, _ := session.SessionViewFromContext(ctx)
	if gotAgain.Labels["feature"] != "enabled" {
		t.Fatal("retrieved session view aliases stored labels")
	}
}

func TestSessionViewContextPreservesIndependentClassificationSnapshots(t *testing.T) {
	t.Parallel()

	want := session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   session.EvidenceCode("client.codex"),
		Revision:   3,
	}
	ctx := session.WithSessionView(context.Background(), session.SessionView{Classification: want})

	got, ok := session.SessionViewFromContext(ctx)
	if !ok || got.Classification != want {
		t.Fatalf("session classification = %+v ok=%v, want %+v", got.Classification, ok, want)
	}
	got.Classification.Evidence = "client.mutated"

	gotAgain, ok := session.SessionViewFromContext(ctx)
	if !ok || gotAgain.Classification != want {
		t.Fatalf("stored session classification changed through returned snapshot: %+v ok=%v", gotAgain.Classification, ok)
	}
}

func TestSecureTurnPolicyContextCarriesOnlyContentFreePolicy(t *testing.T) {
	t.Parallel()

	ctx := session.WithSecureTurnPolicy(context.Background(), session.SecureTurnPolicyView{TranscriptEnabled: true})
	got, ok := session.SecureTurnPolicyFromContext(ctx)
	if !ok || !got.TranscriptEnabled {
		t.Fatalf("secure turn policy = %+v ok=%v", got, ok)
	}
}

func TestWithoutSecureTurnPolicyMasksInheritedPolicy(t *testing.T) {
	t.Parallel()

	ctx := session.WithSecureTurnPolicy(context.Background(), session.SecureTurnPolicyView{TranscriptEnabled: true})
	ctx = session.WithoutSecureTurnPolicy(ctx)
	got, ok := session.SecureTurnPolicyFromContext(ctx)
	if ok || got != (session.SecureTurnPolicyView{}) {
		t.Fatalf("masked secure-turn policy = %+v ok=%v", got, ok)
	}
}
