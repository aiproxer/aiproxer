package aipapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func exactPart(dialect aipapi.ReasoningDialect, opaque string) *aipapi.ReasoningPart {
	// Opaque is opaque JSON bytes at the canonical layer; provider schema is adapter-owned.
	return &aipapi.ReasoningPart{
		Dialect: dialect,
		Opaque:  json.RawMessage(opaque),
	}
}

func opaqueBlob(id string) string {
	return `{"v":1,"k":` + strconv.Quote(id) + `}`
}

func TestValidateEventSequence_acceptsReasoningPartAfterMessageStarted(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, opaqueBlob("a"))},
		{Kind: aipapi.EventResponseFinished},
	})
	if err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestValidateEventSequence_rejectsReasoningPartBeforeMessageStarted(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventSequence([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: exactPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, opaqueBlob("a"))},
		{Kind: aipapi.EventResponseFinished},
	})
	if err == nil {
		t.Fatal("expected reject before message_started")
	}
}

func TestOutputCommitted_reasoningPart(t *testing.T) {
	t.Parallel()
	if !aipapi.OutputCommitted(aipapi.Event{
		Kind:      aipapi.EventReasoningPart,
		Reasoning: exactPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, opaqueBlob("a")),
	}) {
		t.Fatal("reasoning_part must commit for failover")
	}
}

func TestValidateEventEnvelope_reasoningPartNilRejected(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventEnvelope(&aipapi.Event{Kind: aipapi.EventReasoningPart})
	if err == nil {
		t.Fatal("expected nil Reasoning rejected")
	}
	if _, ok := errors.AsType[*aipapi.ValidationError](err); !ok {
		t.Fatalf("expected ValidationError, got %T %v", err, err)
	}
}

func TestValidateEventEnvelope_reasoningPartEmptyDialectRejected(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventEnvelope(&aipapi.Event{
		Kind:      aipapi.EventReasoningPart,
		Reasoning: &aipapi.ReasoningPart{Opaque: json.RawMessage(opaqueBlob("a"))},
	})
	if err == nil {
		t.Fatal("expected empty dialect rejected")
	}
}

func TestValidateEventEnvelope_reasoningPartEmptyPayloadRejected(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventEnvelope(&aipapi.Event{
		Kind:      aipapi.EventReasoningPart,
		Reasoning: &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1},
	})
	if err == nil {
		t.Fatal("expected empty payload rejected")
	}
}

func TestValidateEventEnvelope_reasoningPartRejectsUnnormalizedDialectWithoutMutation(t *testing.T) {
	t.Parallel()
	const raw = "  OpenAI.Responses.Reasoning_Item.V1  "
	ev := &aipapi.Event{
		Kind: aipapi.EventReasoningPart,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialect(raw),
			Opaque:  json.RawMessage(opaqueBlob("a")),
		},
	}
	err := aipapi.ValidateEventEnvelope(ev)
	if err == nil {
		t.Fatal("expected unnormalized dialect rejected (validator must not mutate)")
	}
	if ev.Reasoning.Dialect != aipapi.ReasoningDialect(raw) {
		t.Fatalf("validator mutated Dialect: got %q", ev.Reasoning.Dialect)
	}
	if _, ok := errors.AsType[*aipapi.ValidationError](err); !ok {
		t.Fatalf("expected ValidationError, got %T %v", err, err)
	}
}

func TestValidateEventEnvelope_reasoningPartOversizedOpaqueRejectedContentSafe(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("S", aipapi.MaxReasoningOpaqueBytes+1)
	ev := &aipapi.Event{
		Kind: aipapi.EventReasoningPart,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(secret),
		},
	}
	err := aipapi.ValidateEventEnvelope(ev)
	if err == nil {
		t.Fatal("expected oversized opaque rejected")
	}
	if strings.Contains(err.Error(), "SSS") {
		t.Fatalf("error must not echo opaque bytes: %v", err)
	}
}

func TestValidateEventEnvelope_reasoningPartErrorsDoNotLeakTextOrSignature(t *testing.T) {
	t.Parallel()
	err := aipapi.ValidateEventEnvelope(&aipapi.Event{
		Kind: aipapi.EventReasoningPart,
		Reasoning: &aipapi.ReasoningPart{
			Dialect:   aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:      "SECRET_TEXT_PAYLOAD",
			Signature: "SECRET_SIGNATURE_PAYLOAD",
			Opaque:    json.RawMessage(strings.Repeat("x", aipapi.MaxReasoningOpaqueBytes+1)),
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "SECRET_TEXT_PAYLOAD") || strings.Contains(msg, "SECRET_SIGNATURE_PAYLOAD") {
		t.Fatalf("error leaked payload: %v", err)
	}
}

func TestValidateEventEnvelope_rejectsReasoningOnWrongKind(t *testing.T) {
	t.Parallel()
	if err := aipapi.ValidateEventEnvelope(&aipapi.Event{
		Kind:      aipapi.EventTextDelta,
		Delta:     "hi",
		Reasoning: exactPart(aipapi.ReasoningDialectOpenAIChatTextV1, opaqueBlob("x")),
	}); err == nil {
		t.Fatal("expected Reasoning payload rejected on text_delta")
	}
}

func TestValidateEventEnvelope_reasoningPartAcceptsNormalized(t *testing.T) {
	t.Parallel()
	ev := &aipapi.Event{
		Kind:      aipapi.EventReasoningPart,
		Reasoning: exactPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, opaqueBlob("ok")),
	}
	if err := aipapi.ValidateEventEnvelope(ev); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

func TestValidateEventEnvelope_reasoningPartRejectsUnrelatedPayloadFields(t *testing.T) {
	t.Parallel()
	base := exactPart(aipapi.ReasoningDialectOpenAIResponsesItemV1, opaqueBlob("ok"))
	cases := []struct {
		name  string
		ev    aipapi.Event
		field string
	}{
		{
			name:  "delta",
			field: "Delta",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, Delta: "secret-text"},
		},
		{
			name:  "signature",
			field: "Signature",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, Signature: "secret-sig"},
		},
		{
			name:  "event_opaque",
			field: "Opaque",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, Opaque: []byte(`secret-opaque`)},
		},
		{
			name:  "tool_call_id",
			field: "ToolCallID",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, ToolCallID: "call_1"},
		},
		{
			name:  "tool_name",
			field: "ToolName",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, ToolName: "fn"},
		},
		{
			name:  "assistant_ref",
			field: "AssistantRef",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, AssistantRef: "file_1"},
		},
		{
			name:  "assistant_mime",
			field: "AssistantMIME",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, AssistantMIME: "image/png"},
		},
		{
			name:  "assistant_name",
			field: "AssistantName",
			ev:    aipapi.Event{Kind: aipapi.EventReasoningPart, Reasoning: base, AssistantName: "x.png"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := aipapi.ValidateEventEnvelope(&tc.ev)
			if err == nil {
				t.Fatalf("RED: reasoning_part must reject unrelated %s payload field", tc.field)
			}
			var ve *aipapi.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %T %v", err, err)
			}
			if ve.Field != tc.field {
				t.Fatalf("ValidationError.Field=%q want %q", ve.Field, tc.field)
			}
			msg := err.Error()
			for _, leak := range []string{"secret-text", "secret-sig", "secret-opaque", opaqueBlob("ok")} {
				if strings.Contains(msg, leak) {
					t.Fatalf("error must not echo payload: %v", err)
				}
			}
		})
	}
}

func TestCollect_reasoningPartsOrderedAndDeepCopied(t *testing.T) {
	t.Parallel()
	opaque1 := json.RawMessage(opaqueBlob("rs_1"))
	opaque2 := json.RawMessage(opaqueBlob("rs_2"))
	want1 := string(opaque1)
	want2 := string(opaque2)
	rp1 := &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: opaque1}
	rp2 := &aipapi.ReasoningPart{Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1, Opaque: opaque2}
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventReasoningPart, Reasoning: rp1},
		{Kind: aipapi.EventReasoningDelta, Delta: "think"},
		{Kind: aipapi.EventReasoningPart, Reasoning: rp2},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if col.Text.String() != "hi" {
		t.Fatalf("text=%q", col.Text.String())
	}
	if col.Reasoning.String() != "think" {
		t.Fatalf("reasoning text=%q want think", col.Reasoning.String())
	}
	if len(col.ReasoningParts) != 2 {
		t.Fatalf("ReasoningParts len=%d", len(col.ReasoningParts))
	}
	if string(col.ReasoningParts[0].Opaque) != want1 || string(col.ReasoningParts[1].Opaque) != want2 {
		t.Fatalf("opaque order mismatch: %#v", col.ReasoningParts)
	}
	opaque1[2] = 'Z'
	if string(col.ReasoningParts[0].Opaque) != want1 {
		t.Fatalf("collected opaque aliased caller mutation: %s", col.ReasoningParts[0].Opaque)
	}
}

func TestCollect_reasoningByteLimitAcrossTextAndExactParts(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "abcd"},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  json.RawMessage(opaqueBlob("rs")), // len > remaining budget
		}},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.CollectWithLimits(context.Background(), stream, aipapi.CollectLimits{MaxReasoningBytes: 14})
	if err == nil {
		t.Fatal("expected aggregate reasoning limit exceeded")
	}
	if !errors.Is(err, aipapi.ErrCollectLimitExceeded) {
		t.Fatalf("got %v", err)
	}
	if col.Reasoning.String() != "abcd" {
		t.Fatalf("partial text reasoning retained on error: %q", col.Reasoning.String())
	}
	if len(col.ReasoningParts) != 0 {
		t.Fatalf("rejected exact part must not be appended: %#v", col.ReasoningParts)
	}
}

func TestCollect_rejectsUnnormalizedDialectWithoutMutation(t *testing.T) {
	t.Parallel()
	const raw = "  openai.responses.reasoning_item.v1  "
	rp := &aipapi.ReasoningPart{
		Dialect: aipapi.ReasoningDialect(raw),
		Opaque:  json.RawMessage(opaqueBlob("a")),
	}
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: rp},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), stream)
	if err == nil {
		t.Fatal("expected unnormalized dialect rejected")
	}
	if rp.Dialect != aipapi.ReasoningDialect(raw) {
		t.Fatalf("collect mutated caller Dialect: %q", rp.Dialect)
	}
	if len(col.ReasoningParts) != 0 {
		t.Fatalf("invalid part must not be collected: %#v", col.ReasoningParts)
	}
}

func TestCollect_reasoningExactPartsDoNotDoubleCountIntoTextBuilder(t *testing.T) {
	t.Parallel()
	stream := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIChatTextV1,
			Text:    "exact-text",
		}},
		{Kind: aipapi.EventResponseFinished},
	})
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatal(err)
	}
	if col.Reasoning.String() != "" {
		t.Fatalf("exact part must not append to Reasoning builder, got %q", col.Reasoning.String())
	}
	if len(col.ReasoningParts) != 1 || col.ReasoningParts[0].Text != "exact-text" {
		t.Fatalf("parts=%#v", col.ReasoningParts)
	}
}

func TestCollect_anthropicOpaqueDeltaStillIgnoredForReasoningParts(t *testing.T) {
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
		t.Fatal(err)
	}
	if col.Reasoning.String() != "think" {
		t.Fatalf("reasoning=%q", col.Reasoning.String())
	}
	if len(col.ReasoningParts) != 0 {
		t.Fatalf("opaque delta must not populate ReasoningParts: %#v", col.ReasoningParts)
	}
}

func TestFixedEventStream_clonesReasoningPartOpaque(t *testing.T) {
	t.Parallel()
	opaque := json.RawMessage(opaqueBlob("rs_1"))
	want := string(opaque)
	s := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventReasoningPart, Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		}},
	})
	ev, err := s.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	opaque[2] = 'Z'
	if string(ev.Reasoning.Opaque) != want {
		t.Fatalf("stream clone aliased: %s", ev.Reasoning.Opaque)
	}
}
