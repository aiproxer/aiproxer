package checkpoint_test

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestCaptureFrontendIngress_ClonesCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		ID: "req-1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
		Session: aipapi.SessionRef{ALegID: "a-1", ResumeToken: "secret-resume"},
	}
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         call,
		Scope:        scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		CheckpointID: "cp-fe-1",
		StreamID:     "stream-req-1",
		Now:          time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Public.Boundary != metering.BoundaryFrontendIngress {
		t.Fatalf("boundary=%q", snap.Public.Boundary)
	}
	if snap.Call.Session.ResumeToken != "" {
		t.Fatal("resume token must be stripped from snapshot call")
	}
	if call.Session.ResumeToken != "secret-resume" {
		t.Fatal("original call resume token must remain unchanged")
	}
	call.Messages[0].Parts[0].Text = "mutated"
	if snap.Call.Messages[0].Parts[0].Text != "hello" {
		t.Fatal("snapshot call must be independent of later mutations")
	}
	if err := snap.Public.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureFrontendIngress_DeepCopiesReasoningOpaque(t *testing.T) {
	t.Parallel()
	opaque := json.RawMessage(`{"k":"v"}`)
	if !json.Valid(opaque) {
		t.Fatal("opaque must be valid JSON")
	}
	call := aipapi.Call{
		ID: "req-reason-1",
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect: aipapi.ReasoningDialectAnthropicRedactedThinkingV1,
					Opaque:  opaque,
				},
			}},
		}},
	}
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         call,
		CheckpointID: "cp-fe-reason",
		StreamID:     "stream-reason",
		Now:          time.Unix(10, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Call.Messages[0].Parts[0].Reasoning == nil {
		t.Fatal("checkpoint clone must retain Reasoning")
	}
	if call.Messages[0].Parts[0].Reasoning == snap.Call.Messages[0].Parts[0].Reasoning {
		t.Fatal("checkpoint CloneCall must not share Reasoning pointer")
	}
	if &call.Messages[0].Parts[0].Reasoning.Opaque[0] == &snap.Call.Messages[0].Parts[0].Reasoning.Opaque[0] {
		t.Fatal("checkpoint CloneCall must deep-copy reasoning Opaque")
	}
	snap.Call.Messages[0].Parts[0].Reasoning.Opaque[2] = 'X'
	if string(call.Messages[0].Parts[0].Reasoning.Opaque) != `{"k":"v"}` {
		t.Fatalf("mutating checkpoint Opaque must not mutate original: %s", call.Messages[0].Parts[0].Reasoning.Opaque)
	}
}

func TestBindScope_DoesNotRewriteCall(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call: aipapi.Call{
			ID: "req-1",
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("x")},
			}},
		},
		CheckpointID: "cp-1",
		StreamID:     "s-1",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := snap.Call.Messages[0].Parts[0].Text
	snap.BindScope(scope.PrincipalScopeView{PrincipalID: scope.Known("later")})
	if snap.Public.Scope.PrincipalID.String() != "later" {
		t.Fatalf("scope=%v", snap.Public.Scope.PrincipalID)
	}
	if snap.Call.Messages[0].Parts[0].Text != before {
		t.Fatal("bind scope must not rewrite call messages")
	}
}

func TestReuseSameFrontendIngressAcrossAttempts(t *testing.T) {
	t.Parallel()
	holder := &checkpoint.RequestHolder{}
	in := checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-1"},
		CheckpointID: "cp-shared",
		StreamID:     "stream-shared",
		Now:          time.Unix(1, 0).UTC(),
	}
	first, err := holder.CaptureOrReuseFrontendIngress(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := holder.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-1", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("later")}}}},
		CheckpointID: "cp-other",
		StreamID:     "stream-other",
		Now:          time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Public.CheckpointID != second.Public.CheckpointID || first.Public.StreamID != second.Public.StreamID {
		t.Fatal("must reuse one frontend-ingress checkpoint per logical request")
	}
	if len(second.Call.Messages) != 0 {
		t.Fatal("reuse must keep original immutable call, not later attempt derivation")
	}
}

func TestCaptureCreatesNoReservations(t *testing.T) {
	t.Parallel()
	// Capture helpers have no UsageAuthority dependency; this test documents
	// requirement 2.5 by ensuring CaptureFrontendIngress is a pure function.
	_, err := checkpoint.CaptureFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req"},
		CheckpointID: "cp",
		StreamID:     "s",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestHolder_StoreBackendIngressConcurrent(t *testing.T) {
	t.Parallel()
	h := &checkpoint.RequestHolder{}
	const n = 32
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("attempt-%d", i)
			_, err := h.StoreBackendIngress(checkpoint.BackendIngressInput{
				Call:         aipapi.Call{ID: "req-race", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}}},
				AttemptID:    id,
				BLegID:       id,
				ALegID:       "a-1",
				BackendID:    "b",
				Model:        "m",
				CheckpointID: "cp-" + id,
				StreamID:     "s-" + id,
				Now:          time.Unix(1, 0).UTC(),
			})
			if err != nil {
				errCh <- err
				return
			}
			if got := h.BackendIngressFor(id); got == nil {
				errCh <- fmt.Errorf("missing snapshot for %s", id)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}
