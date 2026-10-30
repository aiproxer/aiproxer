package backendplugin_test

import (
	"errors"
	"strings"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestNegotiateWire_RoundTripAndFailClosed(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{
		Major: 1, Minor: 2, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "count_tokens", Required: false}, {Name: "alpha", Required: false}},
	}
	plugin := backendplugin.ProtocolOffer{
		Major: 1, Minor: 1, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "alpha", Required: false}, {Name: "beta", Required: false}},
	}
	req, err := backendplugin.ProtocolOfferToNegotiateRequest(host)
	if err != nil {
		t.Fatal(err)
	}
	backHost, err := backendplugin.ProtocolOfferFromNegotiateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if backHost.Major != 1 || backHost.Minor != 2 || !backHost.DisableTransportRetries || len(backHost.Features) != 2 {
		t.Fatalf("%+v", backHost)
	}

	neg, err := backendplugin.Negotiate(host, plugin)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := backendplugin.NegotiationToNegotiateResponse(neg)
	if err != nil {
		t.Fatal(err)
	}
	backNeg, err := backendplugin.NegotiationFromNegotiateResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !backNeg.Compatible || backNeg.NegotiatedMinor != 1 || len(backNeg.EnabledFeatures) != 1 || backNeg.EnabledFeatures[0] != "alpha" {
		t.Fatalf("%+v", backNeg)
	}
	if backNeg.PluginMajor != 1 || backNeg.PluginMinor != 1 || len(backNeg.PluginFeatures) != 2 {
		t.Fatalf("plugin advert %+v", backNeg)
	}

	_, err = backendplugin.ProtocolOfferFromNegotiateRequest(nil)
	if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("nil req: %v", err)
	}
	_, err = backendplugin.NegotiationFromNegotiateResponse(&backendpluginv1.NegotiateResponse{
		Compatible:  true,
		PluginMajor: 0,
	})
	if err == nil {
		t.Fatal("compatible with unspecified plugin major must fail")
	}
}

func TestServerFrame_ValidateShapeExclusivePayloads(t *testing.T) {
	t.Parallel()
	ev := &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta}
	if err := (backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameEvent, Event: ev, Diagnostic: "x",
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("event+diagnostic: %v", err)
	}
	if err := (backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameEvent, Event: ev, CancelOutcome: &backendplugin.CancelOutcome{Reason: backendplugin.CancelReasonClient},
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("event+cancel: %v", err)
	}
	if err := (backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameDiagnostic, Diagnostic: "ok", Event: ev,
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("diagnostic+event: %v", err)
	}
	if err := (backendplugin.ServerFrame{
		Kind:       backendplugin.ServerFrameTerminal,
		Terminal:   &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
		Diagnostic: "x",
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("terminal+diagnostic: %v", err)
	}
	if err := (backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}).ValidateShape(); err != nil {
		t.Fatal(err)
	}
	var v backendplugin.StreamValidator
	_ = v.Push(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
	if err := v.Push(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameEvent, Sequence: 1, Event: ev, Diagnostic: "nope",
	}); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("stream pollution: %v", err)
	}
}

func TestClientFrame_ValidateShape(t *testing.T) {
	t.Parallel()
	text := "hi"
	inv := sampleInvocation(text)
	if err := (backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1", Invocation: &inv,
	}).ValidateShape(); err != nil {
		t.Fatal(err)
	}
	if err := (backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameStart, InstanceID: "i1",
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("start without invocation: %v", err)
	}
	if err := (backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameCancel, InstanceID: "i1", CancelReason: backendplugin.CancelReasonUnspecified,
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrUnknownEnum) {
		t.Fatalf("cancel unspecified: %v", err)
	}
	if err := (backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameCancel, InstanceID: "i1", CancelReason: backendplugin.CancelReasonHost, Invocation: &inv,
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("cancel+invocation: %v", err)
	}
	if err := (backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameCloseInput, InstanceID: "i1",
	}).ValidateShape(); err != nil {
		t.Fatal(err)
	}
	if err := (backendplugin.ClientFrame{
		Kind: backendplugin.ClientFrameCloseInput, InstanceID: "i1", CancelReason: backendplugin.CancelReasonClient,
	}).ValidateShape(); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("close+cancel: %v", err)
	}
}

func TestOptionalScalarPresence_RoundTrip(t *testing.T) {
	t.Parallel()
	zeroU := uint32(0)
	zeroI := int32(0)
	empty := ""
	f := false
	text := "hi"
	inv := sampleInvocation(text)
	inv.ToolChoice = &empty
	inv.Options = backendplugin.GenerationOptions{
		MaxOutputTokens:    &zeroU,
		TemperatureMillis:  &zeroI,
		ReasoningEffort:    &empty,
		ParallelToolCalls:  &f,
		ResponseMIMEType:   &empty,
		ResponseSchemaJSON: backendplugin.RawJSONFromBytes([]byte{}),
	}

	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	if wire.ToolChoice == nil || *wire.ToolChoice != "" {
		t.Fatal("empty tool_choice must stay present")
	}
	if wire.Options.MaxOutputTokens == nil || *wire.Options.MaxOutputTokens != 0 {
		t.Fatal("zero max_output_tokens must stay present")
	}
	if wire.Options.TemperatureMillis == nil || *wire.Options.TemperatureMillis != 0 {
		t.Fatal("zero temperature must stay present")
	}
	if wire.Options.ParallelToolCalls == nil || *wire.Options.ParallelToolCalls {
		t.Fatal("false parallel must stay present")
	}
	back, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if back.ToolChoice == nil || *back.ToolChoice != "" {
		t.Fatal("tool_choice lost")
	}
	if back.Options.MaxOutputTokens == nil || *back.Options.MaxOutputTokens != 0 {
		t.Fatal("max tokens lost")
	}
	if back.Options.ParallelToolCalls == nil || *back.Options.ParallelToolCalls {
		t.Fatal("bool lost")
	}

	absent := sampleInvocation(text)
	aw, err := backendplugin.InvocationToProto(absent)
	if err != nil {
		t.Fatal(err)
	}
	if aw.ToolChoice != nil || aw.Options.MaxOutputTokens != nil || aw.Options.ParallelToolCalls != nil {
		t.Fatal("absent optionals must stay unset")
	}
	ab, err := backendplugin.InvocationFromProto(aw)
	if err != nil {
		t.Fatal(err)
	}
	if ab.ToolChoice != nil || ab.Options.MaxOutputTokens != nil || ab.Options.ParallelToolCalls != nil {
		t.Fatal("absent roundtrip polluted")
	}
}

func TestBounds_FramePayloadAndEventOpaque(t *testing.T) {
	t.Parallel()
	var v backendplugin.StreamValidator
	_ = v.Push(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
	huge := strings.Repeat("x", int(backendplugin.DefaultMaxStreamFrameBytes)+1)
	if err := v.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 1,
		Event: &backendplugin.CanonicalEvent{
			Kind:   backendplugin.EventReasoningOpaqueDelta,
			Opaque: []byte(huge),
		},
	}); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("oversize event: %v", err)
	}

	cfg := backendplugin.ConfigureRequest{
		InstanceID:  "i",
		FactoryKind: "k",
		ConfigYAML:  []byte(strings.Repeat("a", int(backendplugin.DefaultMaxConfigYAMLBytes)+1)),
		Negotiation: backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
			MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		},
	}
	if err := cfg.Validate(); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("config: %v", err)
	}

	raw := backendplugin.RawJSONFromBytes([]byte(strings.Repeat("b", int(backendplugin.DefaultMaxRawJSONBytes)+1)))
	if err := raw.Validate(backendplugin.DefaultMaxRawJSONBytes); !errors.Is(err, backendplugin.ErrOversizedRawJSON) {
		t.Fatalf("rawjson: %v", err)
	}

	sf := backendplugin.ServerFrame{
		Kind:       backendplugin.ServerFrameDiagnostic,
		Sequence:   1,
		Diagnostic: strings.Repeat("d", int(backendplugin.DefaultMaxDiagnosticBytes)+1),
	}
	if err := sf.ValidateShape(); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("diagnostic: %v", err)
	}
	if err := backendplugin.ValidateServerFrameBounds(sf); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("frame bounds: %v", err)
	}
}
