package adapter_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// A canonical json part routed to a connector must reach the plugin
// invocation with its payload intact, never be silently dropped.
func TestInvocationFromCall_JSONPartMapped(t *testing.T) {
	t.Parallel()
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.Part{
		Kind:    aipapi.PartJSON,
		Content: json.RawMessage(`{"key":"value"}`),
	})
	inv, err := adapter.InvocationFromCall(call, testCand(), backendplugin.Negotiation{})
	if err != nil {
		t.Fatal(err)
	}
	parts := inv.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("json part silently dropped: mapped parts=%d, want 2", len(parts))
	}
	if parts[1].Kind != backendplugin.PartKindJSON {
		t.Fatalf("kind=%q, want %q", parts[1].Kind, backendplugin.PartKindJSON)
	}
	if got := string(parts[1].ToolArgsJSON.Bytes()); got != `{"key":"value"}` {
		t.Fatalf("json payload=%q, want %q", got, `{"key":"value"}`)
	}
}

func TestInvocationFromCall_DecisionRejectedWithoutDroppingSemantics(t *testing.T) {
	t.Parallel()
	call := testCall()
	call.Messages = nil
	call.Decision = &aipapi.DecisionRequest{Evidence: json.RawMessage(`"data"`), Questions: []aipapi.DecisionQuestion{{ID: "q", Kind: aipapi.DecisionKindNoul}}}
	_, err := adapter.InvocationFromCall(call, testCand(), backendplugin.Negotiation{})
	if !aipapi.IsReject(err) {
		t.Fatalf("decision must fail with capability reject, got %v", err)
	}
	call = testCall()
	inv, err := adapter.InvocationFromCall(call, testCand(), backendplugin.Negotiation{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Messages) != 1 || len(inv.Messages[0].Parts) != 1 || inv.Messages[0].Parts[0].Text == nil || *inv.Messages[0].Parts[0].Text != call.Messages[0].Parts[0].Text {
		t.Fatal("chat mapping changed")
	}
}

// Golden round trip: canonical -> ABI DTO -> proto wire -> ABI DTO -> canonical
// must preserve the json part and its payload exactly.
func TestInvocationFromCall_JSONPartGoldenRoundTrip(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(`{"schema":{"type":"object","properties":{"a":{"type":"string"}}},"nested":[1,2,3]}`)
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.Part{
		Kind:    aipapi.PartJSON,
		Content: payload,
	})
	inv, err := adapter.InvocationFromCall(call, testCand(), backendplugin.Negotiation{})
	if err != nil {
		t.Fatalf("canonical->ABI: %v", err)
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatalf("ABI->proto: %v", err)
	}
	back, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatalf("proto->ABI: %v", err)
	}
	out, err := backendplugin.CallFromInvocation(back)
	if err != nil {
		t.Fatalf("ABI->canonical: %v", err)
	}
	parts := out.Messages[0].Parts
	if len(parts) != 2 {
		t.Fatalf("round-trip parts=%d, want 2", len(parts))
	}
	if parts[1].Kind != aipapi.PartJSON {
		t.Fatalf("round-trip kind=%q, want %q", parts[1].Kind, aipapi.PartJSON)
	}
	if string(parts[1].Content) != string(payload) {
		t.Fatalf("round-trip payload=%q, want %q", parts[1].Content, payload)
	}
}

func TestInvocationFromCall_ExactReasoningGoldenRoundTrip(t *testing.T) {
	t.Parallel()

	opaque := json.RawMessage(`{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"encrypted-state"}`)
	call := testCall()
	call.Messages[0].Role = aipapi.RoleAssistant
	call.Messages[0].Parts = []aipapi.Part{{
		Kind: aipapi.PartReasoning,
		Reasoning: &aipapi.ReasoningPart{
			Dialect: aipapi.ReasoningDialectOpenAIResponsesItemV1,
			Opaque:  opaque,
		},
	}}
	inv, err := adapter.InvocationFromCall(call, testCand(), backendplugin.Negotiation{})
	if err != nil {
		t.Fatal(err)
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	out, err := backendplugin.CallFromInvocation(back)
	if err != nil {
		t.Fatal(err)
	}
	got := out.Messages[0].Parts[0].Reasoning
	if got == nil || got.Dialect != aipapi.ReasoningDialectOpenAIResponsesItemV1 {
		t.Fatalf("reasoning = %+v", got)
	}
	if string(got.Opaque) != string(opaque) {
		t.Fatalf("opaque = %s, want %s", got.Opaque, opaque)
	}
}

// Empty canonical json parts must fail host-side mapping, matching reverse bridge rules.
func TestInvocationFromCall_JSONPartEmptyContentFails(t *testing.T) {
	t.Parallel()
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.Part{
		Kind: aipapi.PartJSON,
	})
	_, err := adapter.InvocationFromCall(call, testCand(), backendplugin.Negotiation{})
	if err == nil {
		t.Fatal("expected error for empty json part content")
	}
	if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("errors.Is(err, ErrInvalidInvocation)=false: %v", err)
	}
	if !strings.Contains(err.Error(), "json part requires content") {
		t.Fatalf("error=%q", err.Error())
	}
}

// A canonical part kind with no ABI mapping must fail explicitly
// instead of being silently dropped from the plugin invocation.
func TestInvocationFromCall_UnsupportedPartKindFailsClosed(t *testing.T) {
	t.Parallel()
	call := testCall()
	call.Messages[0].Parts = append(call.Messages[0].Parts, aipapi.Part{Kind: aipapi.PartKind("bogus")})
	_, err := adapter.InvocationFromCall(call, testCand(), backendplugin.Negotiation{})
	if err == nil {
		t.Fatal("expected explicit error for unmappable part kind, got nil (part silently dropped)")
	}
	if !errors.Is(err, backendplugin.ErrUnsupportedPartKind) {
		t.Fatalf("errors.Is(err, ErrUnsupportedPartKind)=false: %v", err)
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Fatalf("error must name the offending kind, got %q", err.Error())
	}
}
