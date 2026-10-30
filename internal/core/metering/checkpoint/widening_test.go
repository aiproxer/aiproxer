package checkpoint_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestBillableWidened_DetectsAddedMessage(t *testing.T) {
	t.Parallel()
	base := aipapi.Call{
		ID: "r",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("a")},
		}},
	}
	wide := aipapi.CloneCall(base)
	wide.Messages = append(wide.Messages, aipapi.Message{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("extra")},
	})
	ok, err := checkpoint.BillableWidened(base, wide)
	if err != nil || !ok {
		t.Fatalf("widened=%v err=%v", ok, err)
	}
	if err := checkpoint.AssertNotWidened(base, wide); !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("err=%v", err)
	}
	same := aipapi.CloneCall(base)
	if err := checkpoint.AssertNotWidened(base, same); err != nil {
		t.Fatal(err)
	}
}

func TestBillableWidened_AllowsMaxOutputNarrowing(t *testing.T) {
	t.Parallel()
	limit := 100
	base := aipapi.Call{
		ID: "r",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("a")},
		}},
		Options: aipapi.GenerationOptions{MaxOutputTokens: &limit},
	}
	narrowed := aipapi.CloneCall(base)
	lower := 40
	narrowed.Options.MaxOutputTokens = &lower
	if err := checkpoint.AssertNotWidened(base, narrowed); err != nil {
		t.Fatalf("narrowing MaxOutputTokens must be allowed: %v", err)
	}
	raised := aipapi.CloneCall(base)
	higher := 200
	raised.Options.MaxOutputTokens = &higher
	if err := checkpoint.AssertNotWidened(base, raised); !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("raising MaxOutputTokens must widen: %v", err)
	}
}

func TestCaptureBackendIngress(t *testing.T) {
	t.Parallel()
	snap, err := checkpoint.CaptureBackendIngress(checkpoint.BackendIngressInput{
		Call: aipapi.Call{
			ID: "req-1",
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart("final")},
			}},
			Session: aipapi.SessionRef{ALegID: "a-1"},
		},
		AttemptID:    "att-1",
		BLegID:       "b-1",
		BackendID:    "openai",
		Model:        "gpt",
		CheckpointID: "be-in-1",
		StreamID:     "be-stream-1",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Public.Boundary != metering.BoundaryBackendIngress {
		t.Fatal(snap.Public.Boundary)
	}
	if snap.Public.Lifecycle != metering.LifecycleBackendAttempt {
		t.Fatal(snap.Public.Lifecycle)
	}
	if snap.Public.Correlation.AttemptID != "att-1" || snap.Public.Correlation.BLegID != "b-1" {
		t.Fatalf("%+v", snap.Public.Correlation)
	}
	if err := snap.Public.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestHolder_ParallelBackendIngressShareFEStream(t *testing.T) {
	t.Parallel()
	h := &checkpoint.RequestHolder{}
	fe, err := h.CaptureOrReuseFrontendIngress(checkpoint.FrontendIngressInput{
		Call:         aipapi.Call{ID: "req-1"},
		CheckpointID: "fe-1",
		StreamID:     "fe-stream",
		Now:          time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := h.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:         aipapi.Call{ID: "req-1", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("a")}}}},
		AttemptID:    "att-a",
		BLegID:       "b-a",
		CheckpointID: "be-a",
		StreamID:     "be-a",
		Now:          time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.StoreBackendIngress(checkpoint.BackendIngressInput{
		Call:         aipapi.Call{ID: "req-1", Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("b")}}}},
		AttemptID:    "att-b",
		BLegID:       "b-b",
		CheckpointID: "be-b",
		StreamID:     "be-b",
		Now:          time.Unix(3, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Public.StreamID == b.Public.StreamID {
		t.Fatal("parallel legs need independent backend streams")
	}
	if h.FrontendIngress.Public.StreamID != fe.Public.StreamID {
		t.Fatal("FE stream must remain shared")
	}
}

func TestBillableFingerprint_ignoresReasoningUnlessKindReasoning(t *testing.T) {
	t.Parallel()
	base := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("visible")},
		}},
	}
	leaky := aipapi.CloneCall(base)
	leaky.Messages[0].Parts[0].Reasoning = &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
		Text:    "should-not-fingerprint",
	}
	nilReasoningKind := aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleAssistant,
			Parts: []aipapi.Part{{Kind: aipapi.PartReasoning}},
		}},
	}
	fpBase, err := checkpoint.BillableFingerprint(base)
	if err != nil {
		t.Fatal(err)
	}
	fpLeaky, err := checkpoint.BillableFingerprint(leaky)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fpBase, fpLeaky) {
		t.Fatal("non-reasoning Kind must ignore leaked Reasoning pointer for fingerprint")
	}
	if _, err := checkpoint.BillableFingerprint(nilReasoningKind); err != nil {
		t.Fatalf("nil Reasoning payload must be defensive: %v", err)
	}
}

func TestBillableFingerprint_includesReasoningPayload(t *testing.T) {
	t.Parallel()
	opaqueA := json.RawMessage(`{"data":"a"}`)
	opaqueB := json.RawMessage(`{"data":"b"}`)
	if !json.Valid(opaqueA) || !json.Valid(opaqueB) {
		t.Fatal("opaque must be valid JSON")
	}
	left := aipapi.Call{
		Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect:   aipapi.ReasoningDialectAnthropicThinkingV1,
					Text:      "think-a",
					Signature: "sig-a",
					Opaque:    opaqueA,
				},
			}},
		}},
	}
	right := aipapi.CloneCall(left)
	right.Messages[0].Parts[0].Reasoning.Text = "think-b"
	right.Messages[0].Parts[0].Reasoning.Signature = "sig-b"
	right.Messages[0].Parts[0].Reasoning.Opaque = append(json.RawMessage(nil), opaqueB...)

	fpLeft, err := checkpoint.BillableFingerprint(left)
	if err != nil {
		t.Fatal(err)
	}
	fpRight, err := checkpoint.BillableFingerprint(right)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(fpLeft, fpRight) {
		t.Fatal("billable fingerprint equality must include reasoning text/signature/opaque")
	}
}

func TestBillableFingerprint_reasoningLengthFrameNoCollision(t *testing.T) {
	t.Parallel()

	mk := func(dialect aipapi.ReasoningDialect, text, sig string, opaque json.RawMessage) aipapi.Call {
		return aipapi.Call{Messages: []aipapi.Message{{
			Role: aipapi.RoleAssistant,
			Parts: []aipapi.Part{{
				Kind: aipapi.PartReasoning,
				Reasoning: &aipapi.ReasoningPart{
					Dialect:   dialect,
					Text:      text,
					Signature: sig,
					Opaque:    opaque,
				},
			}},
		}}}
	}

	t.Run("signature_opaque_prior_collision_pair", func(t *testing.T) {
		t.Parallel()
		opaque := json.RawMessage(`{"a":1}`)
		if !json.Valid(opaque) {
			t.Fatal("opaque must be valid JSON")
		}
		a := mk(aipapi.ReasoningDialectAnthropicThinkingV1, "t", "x", opaque)
		b := mk(aipapi.ReasoningDialectAnthropicThinkingV1, "t", `x:{"a":1}`, nil)
		if err := a.Validate(); err != nil {
			t.Fatalf("a.Validate: %v", err)
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate: %v", err)
		}
		fa, err := checkpoint.BillableFingerprint(a)
		if err != nil {
			t.Fatal(err)
		}
		fb, err := checkpoint.BillableFingerprint(b)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(fa, fb) {
			t.Fatal("length framing must distinguish Signature+Opaque from Signature containing colon+JSON")
		}
	})

	t.Run("dialect_text_prior_collision_pair", func(t *testing.T) {
		t.Parallel()
		a := mk("a:b", "c", "s", nil)
		b := mk("a", "b:c", "s", nil)
		if err := a.Validate(); err != nil {
			t.Fatalf("a.Validate: %v", err)
		}
		if err := b.Validate(); err != nil {
			t.Fatalf("b.Validate: %v", err)
		}
		fa, err := checkpoint.BillableFingerprint(a)
		if err != nil {
			t.Fatal(err)
		}
		fb, err := checkpoint.BillableFingerprint(b)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(fa, fb) {
			t.Fatal("length framing must distinguish Dialect/Text pairs that collide under colon join")
		}
	})

	t.Run("equivalent_copies_remain_equal", func(t *testing.T) {
		t.Parallel()
		opaque := json.RawMessage(`{"k":"v"}`)
		orig := mk(aipapi.ReasoningDialectAnthropicRedactedThinkingV1, "think", "sig", opaque)
		if err := orig.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		cl := aipapi.CloneCall(orig)
		fo, err := checkpoint.BillableFingerprint(orig)
		if err != nil {
			t.Fatal(err)
		}
		fc, err := checkpoint.BillableFingerprint(cl)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(fo, fc) {
			t.Fatal("equivalent cloned calls must share billable fingerprint")
		}
	})
}
