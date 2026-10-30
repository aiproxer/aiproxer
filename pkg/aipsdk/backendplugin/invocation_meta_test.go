package backendplugin_test

import (
	"encoding/json"
	"errors"
	"net/url"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestApplyRestoreCallWireMetadata_roundTrip(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Session: aipapi.SessionRef{AuthoritativeSessionID: "proxy-session"},
		Invocation: aipapi.Invocation{
			Operation:     aipapi.OperationOpenAIResponses,
			DeliveryMode:  aipapi.DeliveryModeStreaming,
			TransportMode: aipapi.TransportModeStreaming,
		},
		Extensions: map[string]json.RawMessage{
			"openrouter.provider": json.RawMessage(`{"order":["a"]}`),
			"openai.extra_body.x": json.RawMessage(`1`),
		},
	}
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
	}
	if err := backendplugin.ApplyCallWireMetadata(&inv, call, map[string]string{"provider": "sambanova"}); err != nil {
		t.Fatal(err)
	}
	if inv.SafeMetadata[backendplugin.MetaOperation] != string(aipapi.OperationOpenAIResponses) {
		t.Fatalf("op=%q", inv.SafeMetadata[backendplugin.MetaOperation])
	}
	if backendplugin.RouteParam(inv.SafeMetadata, "provider") != "sambanova" {
		t.Fatalf("provider=%q", backendplugin.RouteParam(inv.SafeMetadata, "provider"))
	}
	if inv.ProxyOwnedSessionID != "" {
		t.Fatalf("legacy helper projected authoritative session=%q", inv.ProxyOwnedSessionID)
	}
	out, err := backendplugin.CallFromInvocation(backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg",
		CanonicalModelID: "m", Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: new("hi")}},
		}},
		SafeMetadata:        inv.SafeMetadata,
		ProxyOwnedSessionID: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Invocation.Operation != aipapi.OperationOpenAIResponses {
		t.Fatalf("restored op=%q", out.Invocation.Operation)
	}
	if out.Session.AuthoritativeSessionID != "" {
		t.Fatalf("legacy restore established session=%q", out.Session.AuthoritativeSessionID)
	}
	if string(out.Extensions["openrouter.provider"]) != `{"order":["a"]}` {
		t.Fatalf("ext=%v", out.Extensions)
	}
	_ = url.Values{}
}

func TestApplyCallWireMetadataWithNegotiation_NilInvocation(t *testing.T) {
	t.Parallel()
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(nil, aipapi.Call{}, nil, backendplugin.Negotiation{}); err == nil {
		t.Fatal("nil invocation must be rejected")
	}
}

func TestCallFromInvocation_IgnoresClientSafeMetadataSessionSpoof(t *testing.T) {
	t.Parallel()
	call, err := backendplugin.CallFromInvocation(backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
		SafeMetadata: map[string]string{"aip.authoritative_session_id": "client-spoof"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.Session.AuthoritativeSessionID != "" {
		t.Fatalf("client metadata established authority: %q", call.Session.AuthoritativeSessionID)
	}
}

func TestInvocationProtoRoundTrip_PreservesTypedProxySessionOnly(t *testing.T) {
	t.Parallel()
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		ProxyOwnedSessionID: "proxy-session",
		SafeMetadata:        map[string]string{"aip.authoritative_session_id": "client-spoof"},
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	if wire.GetProxyOwnedSessionId() != "proxy-session" {
		t.Fatalf("wire session=%q", wire.GetProxyOwnedSessionId())
	}
	decoded, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProxyOwnedSessionID != "proxy-session" {
		t.Fatalf("decoded session=%q", decoded.ProxyOwnedSessionID)
	}
	call, err := backendplugin.CallFromInvocation(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if call.Session.AuthoritativeSessionID != "proxy-session" {
		t.Fatalf("restored session=%q", call.Session.AuthoritativeSessionID)
	}
}

func TestCallFromInvocation_TypedProxySessionWinsOverMetadataConflict(t *testing.T) {
	t.Parallel()
	call, err := backendplugin.CallFromInvocation(backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		ProxyOwnedSessionID: "proxy-session",
		SafeMetadata:        map[string]string{"aip.authoritative_session_id": "client-conflict"},
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if call.Session.AuthoritativeSessionID != "proxy-session" {
		t.Fatalf("session conflict resolved to %q", call.Session.AuthoritativeSessionID)
	}
}

func TestApplyCallWireMetadata_OldNegotiationOmitsTypedSessionWithoutDroppingCall(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{
		Session:  aipapi.SessionRef{AuthoritativeSessionID: "proxy-session"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	}
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); !errors.Is(err, backendplugin.ErrProxyOwnedSessionUnsupported) {
		t.Fatalf("old negotiation err=%v, want proxy-owned session rejection", err)
	}
	if inv.SafeMetadata != nil && inv.SafeMetadata["aip.authoritative_session_id"] != "" {
		t.Fatal("session authority fell back to spoofable metadata")
	}
}

func TestApplyCallWireMetadata_NewNegotiationCarriesTypedSession(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "proxy-session"}}
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
	}
	neg := backendplugin.Negotiation{
		Compatible:      true,
		NegotiatedMinor: backendplugin.ProtocolMinorProxyOwnedSessionID,
		EnabledFeatures: []string{backendplugin.FeatureProxyOwnedSessionID},
	}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); err != nil {
		t.Fatalf("ApplyCallWireMetadataWithNegotiation: %v", err)
	}
	if inv.ProxyOwnedSessionID != "proxy-session" {
		t.Fatalf("new negotiation omitted typed session=%q", inv.ProxyOwnedSessionID)
	}
	out, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatal(err)
	}
	if out.Session.AuthoritativeSessionID != "proxy-session" {
		t.Fatalf("negotiated roundtrip session=%q", out.Session.AuthoritativeSessionID)
	}
}

func TestApplyCallWireMetadata_NonAuthoritativeCallRemainsBackwardCompatible(t *testing.T) {
	t.Parallel()
	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	inv := backendplugin.Invocation{}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorExactOpenResponsesFields}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); err != nil {
		t.Fatal(err)
	}
	if inv.ProxyOwnedSessionID != "" {
		t.Fatalf("unexpected session authority=%q", inv.ProxyOwnedSessionID)
	}
}

func TestContinuityMarkerSurvivesInvocationExtensionRoundTrip(t *testing.T) {
	t.Parallel()
	const markerKey = "aip.internal.openai_codex.reasoning_continuity.v1"
	const markerValue = `{"eligible":true,"dialect":"openai.responses.reasoning_item.v1"}`
	call := aipapi.Call{
		Extensions: map[string]json.RawMessage{markerKey: json.RawMessage(markerValue)},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	}
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{
			Kind: backendplugin.PartKindText, Text: strPtr("hello"),
		}}}},
	}
	neg := backendplugin.Negotiation{Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorProxyOwnedSessionID, EnabledFeatures: []string{backendplugin.FeatureProxyOwnedSessionID}}
	if err := backendplugin.ApplyCallWireMetadataWithNegotiation(&inv, call, nil, neg); err != nil {
		t.Fatal(err)
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	got, err := backendplugin.CallFromInvocation(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Extensions[markerKey]) != markerValue {
		t.Fatalf("marker extension changed across ABI roundtrip: %s", got.Extensions[markerKey])
	}
}

func strPtr(s string) *string { return new(s) }
