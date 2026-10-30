package adapter

import (
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"google.golang.org/protobuf/proto"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// Claim: terminal vocabulary crosses the connector boundary: a aipapi
// response_finished event with explicit ResponseStatus and FinishReason
// survives aipapi -> ABI DTO -> proto wire -> ABI DTO -> aipapi.
// Source: issue #1104 (CanonicalEvent lacked FinishReason/ResponseStatus).
// Mutant: drop either field in CanonicalEventFromAipapi, CanonicalEvent
// To/FromProto, or eventToAipapi -> FAIL.
func TestTerminalFields_RoundTripPreservesFinishReasonAndResponseStatus(t *testing.T) {
	t.Parallel()

	in := aipapi.Event{
		Kind:           aipapi.EventResponseFinished,
		ResponseStatus: "incomplete",
		FinishReason:   "length",
	}
	dto := backendplugin.CanonicalEventFromAipapi(in)
	wire, err := backendplugin.CanonicalEventToProto(dto)
	if err != nil {
		t.Fatalf("CanonicalEventToProto: %v", err)
	}
	encoded, err := proto.Marshal(wire)
	if err != nil {
		t.Fatalf("proto.Marshal: %v", err)
	}
	decoded := new(backendpluginv1.CanonicalEvent)
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("proto.Unmarshal: %v", err)
	}
	back, err := backendplugin.CanonicalEventFromProto(decoded)
	if err != nil {
		t.Fatalf("CanonicalEventFromProto: %v", err)
	}
	out, err := eventToAipapi(back)
	if err != nil {
		t.Fatalf("eventToAipapi: %v", err)
	}
	if out.Kind != aipapi.EventResponseFinished {
		t.Fatalf("kind = %q, want %q", out.Kind, aipapi.EventResponseFinished)
	}
	if out.FinishReason != "length" {
		t.Fatalf("finish_reason = %q, want %q", out.FinishReason, "length")
	}
	if out.ResponseStatus != "incomplete" {
		t.Fatalf("response_status = %q, want %q", out.ResponseStatus, "incomplete")
	}
}

// Claim: absent terminal vocabulary stays absent across the connector
// boundary (empty means the producer left terminal semantics to inference).
// Source: issue #1104 presence semantics (mirrors pkg/aipapi/events.go).
// Mutant: default either field to a non-empty value on any leg -> FAIL.
func TestTerminalFields_EmptyStaysEmpty(t *testing.T) {
	t.Parallel()

	in := aipapi.Event{Kind: aipapi.EventResponseFinished}
	dto := backendplugin.CanonicalEventFromAipapi(in)
	if dto.FinishReason != nil || dto.ResponseStatus != nil {
		t.Fatalf("DTO pointers = (%v, %v), want (nil, nil)", dto.FinishReason, dto.ResponseStatus)
	}
	wire, err := backendplugin.CanonicalEventToProto(dto)
	if err != nil {
		t.Fatalf("CanonicalEventToProto: %v", err)
	}
	if wire.GetFinishReason() != "" || wire.GetResponseStatus() != "" {
		t.Fatalf("wire = (%q, %q), want (\"\", \"\")", wire.GetFinishReason(), wire.GetResponseStatus())
	}
	back, err := backendplugin.CanonicalEventFromProto(wire)
	if err != nil {
		t.Fatalf("CanonicalEventFromProto: %v", err)
	}
	out, err := eventToAipapi(back)
	if err != nil {
		t.Fatalf("eventToAipapi: %v", err)
	}
	if out.FinishReason != "" || out.ResponseStatus != "" {
		t.Fatalf("aipapi = (%q, %q), want (\"\", \"\")", out.FinishReason, out.ResponseStatus)
	}
}
