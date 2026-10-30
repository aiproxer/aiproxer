package aipapi_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestOutputCommitted_reasoningSignatureDelta(t *testing.T) {
	t.Parallel()
	if aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"}) {
		t.Fatal("reasoning_signature_delta must not commit for failover")
	}
}

func TestValidateEventSequence_acceptsReasoningSignatureDeltaAfterMessageStarted(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: aipapi.EventResponseFinished},
	})
	if err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestValidateEventSequence_rejectsReasoningSignatureDeltaBeforeMessageStarted(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: aipapi.EventResponseFinished},
	})
	if err == nil {
		t.Fatal("expected reject before message_started")
	}
}

func TestValidateEventEnvelope_reasoningSignatureDeltaOversized(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta, Signature: strings.Repeat("s", aipapi.MaxRefStringBytes+1)}
	if err := aipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected oversized signature to be rejected")
	}
}

func TestValidateEventEnvelope_reasoningSignatureDeltaWithinBound(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"}
	if err := aipapi.ValidateEventEnvelope(ev); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestCollect_ignoresReasoningSignatureDelta(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventReasoningSignatureDelta, Signature: "sig"},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Reasoning.String() != "think" {
		t.Fatalf("reasoning = %q, want %q", col.Reasoning.String(), "think")
	}
}

func TestOutputCommitted_reasoningOpaqueDelta(t *testing.T) {
	t.Parallel()
	if !aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking","data":"x"}`)}) {
		t.Fatal("reasoning_opaque_delta must commit for failover")
	}
}

func TestValidateEventSequence_acceptsReasoningOpaqueDeltaAfterMessageStarted(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking","data":"opaque"}`)},
		{Kind: aipapi.EventResponseFinished},
	})
	if err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestValidateEventEnvelope_reasoningOpaqueDeltaOversized(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: []byte(strings.Repeat("o", aipapi.MaxReasoningOpaqueBytes+1))}
	if err := aipapi.ValidateEventEnvelope(ev); err == nil {
		t.Fatal("expected oversized opaque to be rejected")
	}
}

func TestCollect_ignoresReasoningOpaqueDelta(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventReasoningOpaqueDelta, Opaque: []byte(`{"type":"redacted_thinking","data":"opaque"}`)},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Reasoning.String() != "think" {
		t.Fatalf("reasoning = %q, want %q", col.Reasoning.String(), "think")
	}
}
