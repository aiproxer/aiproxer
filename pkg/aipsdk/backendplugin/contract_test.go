package backendplugin_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestNegotiate_IncompatibleMajorRejectedBeforeConfigure(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{Major: 1, Minor: 2, DisableTransportRetries: true}
	plugin := backendplugin.ProtocolOffer{Major: 2, Minor: 0, DisableTransportRetries: true}
	neg, err := backendplugin.Negotiate(host, plugin)
	if !errors.Is(err, backendplugin.ErrIncompatibleMajor) {
		t.Fatalf("err=%v want ErrIncompatibleMajor", err)
	}
	if neg.Compatible {
		t.Fatal("expected incompatible")
	}
	cfg := backendplugin.ConfigureRequest{
		InstanceID:  "i1",
		FactoryKind: "localstub",
		Negotiation: neg,
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
			MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		},
	}
	if err := cfg.Validate(); !errors.Is(err, backendplugin.ErrConfigureBeforeNegotiate) {
		t.Fatalf("configure err=%v", err)
	}
}

func TestNegotiate_HostRequiredUnknownFailClosed(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{
		Major: 1, Minor: 1, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "future.mandatory", Required: true}},
	}
	plugin := backendplugin.ProtocolOffer{
		Major: 1, Minor: 1, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "count_tokens", Required: false}},
	}
	_, err := backendplugin.Negotiate(host, plugin)
	if !errors.Is(err, backendplugin.ErrUnknownRequiredFeature) {
		t.Fatalf("err=%v", err)
	}
}

func TestNegotiate_PluginRequiredUnknownFailClosed(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{Major: 1, Minor: 1, DisableTransportRetries: true}
	plugin := backendplugin.ProtocolOffer{
		Major: 1, Minor: 1, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "plugin.required", Required: true}},
	}
	_, err := backendplugin.Negotiate(host, plugin)
	if !errors.Is(err, backendplugin.ErrUnknownRequiredFeature) {
		t.Fatalf("err=%v", err)
	}
}

func TestNegotiate_DuplicateAndEmptyFeatureNames(t *testing.T) {
	t.Parallel()
	base := backendplugin.ProtocolOffer{Major: 1, Minor: 1, DisableTransportRetries: true}
	dup := base
	dup.Features = []backendplugin.Feature{{Name: "a"}, {Name: "a"}}
	if _, err := backendplugin.Negotiate(dup, base); !errors.Is(err, backendplugin.ErrDuplicateFeature) {
		t.Fatalf("host dup err=%v", err)
	}
	if _, err := backendplugin.Negotiate(base, dup); !errors.Is(err, backendplugin.ErrDuplicateFeature) {
		t.Fatalf("plugin dup err=%v", err)
	}
	empty := base
	empty.Features = []backendplugin.Feature{{Name: "  "}}
	if _, err := backendplugin.Negotiate(empty, base); !errors.Is(err, backendplugin.ErrEmptyFeatureName) {
		t.Fatalf("empty err=%v", err)
	}
}

func TestNegotiate_EnabledIntersectionDeterministic(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{
		Major: 1, Minor: 3, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: "zeta", Required: false},
			{Name: "alpha", Required: false},
			{Name: "missing.optional", Required: false},
		},
	}
	plugin := backendplugin.ProtocolOffer{
		Major: 1, Minor: 2, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: "alpha", Required: false},
			{Name: "zeta", Required: false},
			{Name: "beta", Required: false},
		},
	}
	neg, err := backendplugin.Negotiate(host, plugin)
	if err != nil {
		t.Fatal(err)
	}
	if neg.NegotiatedMinor != 2 {
		t.Fatalf("minor=%d", neg.NegotiatedMinor)
	}
	if len(neg.EnabledFeatures) != 2 || neg.EnabledFeatures[0] != "alpha" || neg.EnabledFeatures[1] != "zeta" {
		t.Fatalf("enabled=%v", neg.EnabledFeatures)
	}
}

func TestNegotiate_OptionalUnknownFeatureIgnored(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{
		Major: 1, Minor: 1, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "future.optional", Required: false}},
	}
	plugin := backendplugin.ProtocolOffer{Major: 1, Minor: 3, DisableTransportRetries: true}
	neg, err := backendplugin.Negotiate(host, plugin)
	if err != nil {
		t.Fatal(err)
	}
	if !neg.Compatible || neg.NegotiatedMinor != 1 || len(neg.EnabledFeatures) != 0 {
		t.Fatalf("neg=%+v", neg)
	}
}

func TestValidateNegotiationResult_RejectsPeerFeatureEscalation(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{Major: 1, Minor: 2, DisableTransportRetries: true}
	neg := backendplugin.Negotiation{
		Compatible: true, PluginMajor: 1, PluginMinor: 2, NegotiatedMinor: 2,
		TransportPolicy: backendplugin.DefaultTransportPolicy(),
		PluginFeatures:  []backendplugin.Feature{{Name: "plugin-only"}},
		EnabledFeatures: []string{"plugin-only"},
	}
	if err := backendplugin.ValidateNegotiationResult(host, neg); !errors.Is(err, backendplugin.ErrUnknownRequiredFeature) {
		t.Fatalf("err=%v, want feature escalation rejection", err)
	}
}

func TestValidateNegotiationResult_RequiresAllRequiredFeatures(t *testing.T) {
	t.Parallel()
	host := backendplugin.ProtocolOffer{
		Major: 1, Minor: 2, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: "required", Required: true}},
	}
	neg := backendplugin.Negotiation{
		Compatible: true, PluginMajor: 1, PluginMinor: 2, NegotiatedMinor: 2,
		TransportPolicy: backendplugin.DefaultTransportPolicy(),
		PluginFeatures:  []backendplugin.Feature{{Name: "required", Required: true}},
	}
	if err := backendplugin.ValidateNegotiationResult(host, neg); !errors.Is(err, backendplugin.ErrUnknownRequiredFeature) {
		t.Fatalf("err=%v, want required feature rejection", err)
	}
}

func TestTransportPolicy_RetriesMustBeDisabled(t *testing.T) {
	t.Parallel()
	p := backendplugin.DefaultTransportPolicy()
	p.DisableAutomaticRetries = false
	if err := p.Validate(); !errors.Is(err, backendplugin.ErrTransportRetriesRequiredDisabled) {
		t.Fatalf("err=%v", err)
	}
}

func TestRawJSON_AbsentNullEmptyPreservedAndFailClosed(t *testing.T) {
	t.Parallel()
	absent := backendplugin.RawJSONAbsentValue()
	null := backendplugin.RawJSONNullValue()
	empty := backendplugin.RawJSONFromBytes([]byte{})
	obj := backendplugin.RawJSONFromBytes([]byte(`{}`))

	if absent.State() != backendplugin.RawJSONAbsent {
		t.Fatal("absent")
	}
	if null.State() != backendplugin.RawJSONNull {
		t.Fatal("null")
	}
	if empty.State() != backendplugin.RawJSONValue || len(empty.Bytes()) != 0 {
		t.Fatal("empty value")
	}
	if obj.State() != backendplugin.RawJSONValue || string(obj.Bytes()) != "{}" {
		t.Fatal("object")
	}

	got, err := backendplugin.RawJSONFromProto(nil)
	if err != nil || got.State() != backendplugin.RawJSONAbsent {
		t.Fatalf("nil proto: %v %#v", err, got)
	}
	got, err = backendplugin.RawJSONFromProto(backendplugin.RawJSONToProto(null))
	if err != nil || got.State() != backendplugin.RawJSONNull {
		t.Fatal("proto null roundtrip")
	}
	got, err = backendplugin.RawJSONFromProto(backendplugin.RawJSONToProto(empty))
	if err != nil || got.State() != backendplugin.RawJSONValue || len(got.Bytes()) != 0 {
		t.Fatal("proto empty roundtrip")
	}
	if backendplugin.RawJSONToProto(absent) != nil {
		t.Fatal("absent encodes as unset proto")
	}
	_, err = backendplugin.RawJSONFromProto(&backendpluginv1.RawJSONValue{})
	if !errors.Is(err, backendplugin.ErrInvalidRawJSON) {
		t.Fatalf("unset oneof err=%v", err)
	}
	_, err = backendplugin.RawJSONFromProto(&backendpluginv1.RawJSONValue{
		State: &backendpluginv1.RawJSONValue_IsNull{IsNull: false},
	})
	if !errors.Is(err, backendplugin.ErrInvalidRawJSON) {
		t.Fatalf("is_null=false err=%v", err)
	}
}

func TestUsagePresence_AllCountersAndExplicitZero(t *testing.T) {
	t.Parallel()
	zero := int64(0)
	u := backendplugin.UsageEvidence{
		InputTokens:      &zero,
		OutputTokens:     &zero,
		CacheReadTokens:  &zero,
		CacheWriteTokens: &zero,
		ReasoningTokens:  &zero,
		TotalTokens:      &zero,
		Presence: backendplugin.UsagePresence{
			InputTokens: true, OutputTokens: true, CacheReadTokens: true,
			CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
		},
		RawUsageJSON: backendplugin.RawJSONAbsentValue(),
	}
	if err := u.ValidatePresence(); err != nil {
		t.Fatal(err)
	}
	wire, err := backendplugin.UsageEvidenceToProto(u)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.UsageEvidenceFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if back.InputTokens == nil || *back.InputTokens != 0 || !back.Presence.InputTokens {
		t.Fatalf("roundtrip=%+v", back)
	}
	u.Presence.InputTokens = false
	if err := u.ValidatePresence(); err == nil {
		t.Fatal("value without presence must fail")
	}
}

func TestBounds_ConfigYAMLRawJSONDiagnosticInventory(t *testing.T) {
	t.Parallel()
	if err := backendplugin.ValidateSize(backendplugin.DefaultMaxConfigYAMLBytes+1, backendplugin.DefaultMaxConfigYAMLBytes); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("config err=%v", err)
	}
	if err := backendplugin.ValidateRawJSONSize(backendplugin.DefaultMaxRawJSONBytes+1, backendplugin.DefaultMaxRawJSONBytes); !errors.Is(err, backendplugin.ErrOversizedRawJSON) {
		t.Fatalf("rawjson err=%v", err)
	}
	if err := backendplugin.ValidateSize(backendplugin.DefaultMaxDiagnosticBytes+1, backendplugin.DefaultMaxDiagnosticBytes); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("diagnostic err=%v", err)
	}
	inv := backendplugin.ListModelsResponse{Models: make([]backendplugin.ModelDescriptor, 3)}
	if err := inv.Validate(2); !errors.Is(err, backendplugin.ErrOversizedMessage) {
		t.Fatalf("inventory err=%v", err)
	}
}

func validFactory() backendplugin.FactoryDescriptor {
	return backendplugin.FactoryDescriptor{
		Kind:           "localstub",
		CredentialMode: backendplugin.CredentialModeNone,
		AccessScope:    backendplugin.AccessScopeLocalOnly,
		ProcessSharing: backendplugin.ProcessSharingPerInstance,
	}
}

func TestDescriptorAndInvocationValidation(t *testing.T) {
	t.Parallel()
	d := backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: 0,
		PluginID:      "com.aiproxer.localstub",
		Version:       "1.0.0",
		Factories:     []backendplugin.FactoryDescriptor{validFactory()},
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	d.Factories = append(d.Factories, validFactory())
	if err := d.Validate(); err == nil {
		t.Fatal("duplicate factory kind")
	}

	text := "hi"
	inv := sampleInvocation(text)
	if err := inv.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestDescriptorValidate_OAuthUserImpliesLocalOnly mirrors the packaged-manifest
// rule on the runtime FactoryDescriptor, so a connector cannot serve a posture its
// own manifest would be rejected for. Both fields stay mandatory declarations: the
// check only proves they agree and never infers a value.
func TestDescriptorValidate_OAuthUserImpliesLocalOnly(t *testing.T) {
	t.Parallel()
	base := func(cred backendplugin.CredentialMode, scope backendplugin.AccessScope) backendplugin.PluginDescriptor {
		return backendplugin.PluginDescriptor{
			ProtocolMajor: 1,
			PluginID:      "com.aiproxer.oauth",
			Version:       "1.0.0",
			Factories: []backendplugin.FactoryDescriptor{{
				Kind:           "user-credential",
				CredentialMode: cred,
				AccessScope:    scope,
				ProcessSharing: backendplugin.ProcessSharingPerInstance,
			}},
		}
	}

	for _, tc := range []struct {
		name  string
		cred  backendplugin.CredentialMode
		scope backendplugin.AccessScope
	}{
		{"oauth_user local_only", backendplugin.CredentialModeOAuthUser, backendplugin.AccessScopeLocalOnly},
		{"static any", backendplugin.CredentialModeStatic, backendplugin.AccessScopeAny},
		{"workload any", backendplugin.CredentialModeWorkload, backendplugin.AccessScopeAny},
		{"none any", backendplugin.CredentialModeNone, backendplugin.AccessScopeAny},
		{"none local_only", backendplugin.CredentialModeNone, backendplugin.AccessScopeLocalOnly},
		{"unknown any", backendplugin.CredentialModeUnknown, backendplugin.AccessScopeAny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := base(tc.cred, tc.scope).Validate(); err != nil {
				t.Fatalf("valid posture rejected: %v", err)
			}
		})
	}

	err := base(backendplugin.CredentialModeOAuthUser, backendplugin.AccessScopeAny).Validate()
	if err == nil {
		t.Fatal("oauth_user with access_scope any must be rejected")
	}
	if !errors.Is(err, backendplugin.ErrInconsistentSecurityPosture) {
		t.Fatalf("want %v, got %v", backendplugin.ErrInconsistentSecurityPosture, err)
	}
	if !errors.Is(err, backendplugin.ErrInvalidDescriptor) {
		t.Fatalf("inconsistent posture must stay classified as an invalid descriptor, got %v", err)
	}
	if !strings.Contains(err.Error(), "user-credential") {
		t.Fatalf("error must name the offending factory kind: %v", err)
	}
}

func sampleInvocation(text string) backendplugin.Invocation {
	return backendplugin.Invocation{
		RequestID:        "r1",
		AttemptID:        "a1",
		ALegID:           "aleg",
		BLegID:           "bleg",
		CanonicalModelID: "m1",
		Messages: []backendplugin.Message{{
			Role:  backendplugin.RoleUser,
			Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}},
		}},
		Options: backendplugin.GenerationOptions{
			ResponseSchemaJSON: backendplugin.RawJSONAbsentValue(),
		},
	}
}

func TestStream_AcceptedSequenceTerminalAndFailures(t *testing.T) {
	t.Parallel()
	var v backendplugin.StreamValidator
	if err := v.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 1,
		Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta},
	}); !errors.Is(err, backendplugin.ErrAcceptedRequired) {
		t.Fatalf("before accepted: %v", err)
	}
	if err := v.Push(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		t.Fatal(err)
	}
	if err := v.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 2,
		Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta},
	}); !errors.Is(err, backendplugin.ErrSequenceGap) {
		t.Fatalf("gap: %v", err)
	}
	if err := v.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 1,
		Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta},
	}); err != nil {
		t.Fatal(err)
	}
	if !backendplugin.OutputCommittedEvent(backendplugin.EventTextDelta) {
		t.Fatal("text delta commits output")
	}
	if backendplugin.OutputCommittedEvent(backendplugin.EventUsageDelta) {
		t.Fatal("usage does not commit")
	}
	if err := v.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameCancelOutcome,
		Sequence: 2,
		CancelOutcome: &backendplugin.CancelOutcome{
			Acknowledged: true,
			Reason:       backendplugin.CancelReasonClient,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Sequence: 3,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	}); err != nil {
		t.Fatal(err)
	}
	if err := v.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Sequence: 4,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	}); !errors.Is(err, backendplugin.ErrMultipleTerminals) {
		t.Fatalf("err=%v", err)
	}
	var v2 backendplugin.StreamValidator
	if err := v2.Push(backendplugin.ServerFrame{Kind: "nope"}); !errors.Is(err, backendplugin.ErrUnknownFrameKind) {
		t.Fatalf("err=%v", err)
	}
	var v3 backendplugin.StreamValidator
	_ = v3.Push(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
	if err := v3.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 1,
		Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventUnspecified},
	}); !errors.Is(err, backendplugin.ErrUnknownEventKind) {
		t.Fatalf("unspecified event: %v", err)
	}
	var v4 backendplugin.StreamValidator
	_ = v4.Push(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
	if err := v4.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameCancelOutcome,
		Sequence: 1,
	}); !errors.Is(err, backendplugin.ErrInvalidFrame) {
		t.Fatalf("cancel shape: %v", err)
	}
	var v5 backendplugin.StreamValidator
	_ = v5.Push(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted})
	_ = v5.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Sequence: 1,
		Terminal: &backendplugin.Terminal{Status: backendplugin.TerminalSuccess},
	})
	if err := v5.Push(backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameEvent,
		Sequence: 2,
		Event:    &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta},
	}); !errors.Is(err, backendplugin.ErrEventAfterTerminal) {
		t.Fatalf("post-terminal: %v", err)
	}
}

func TestCategoryRoundtrips_ProfileInventoryCountFinalizeErrorsCancelTerminal(t *testing.T) {
	t.Parallel()
	text := "hi"
	desc := backendplugin.PluginDescriptor{
		ProtocolMajor: 1,
		ProtocolMinor: 0,
		PluginID:      "com.aiproxer.localstub",
		Version:       "1.0.0",
		Factories:     []backendplugin.FactoryDescriptor{validFactory()},
	}
	pd, err := backendplugin.PluginDescriptorToProto(desc)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.PluginDescriptorFromProto(pd); err != nil {
		t.Fatal(err)
	}

	neg, err := backendplugin.Negotiate(
		backendplugin.ProtocolOffer{Major: 1, Minor: 1, DisableTransportRetries: true},
		backendplugin.ProtocolOffer{Major: 1, Minor: 1, DisableTransportRetries: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := backendplugin.ConfigureRequest{
		InstanceID:  "i1",
		FactoryKind: "localstub",
		ConfigYAML:  []byte("kind: localstub\n"),
		Negotiation: neg,
		RuntimePolicy: backendplugin.RuntimePolicy{
			DisableTransportRetries: true,
			MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
			MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		},
	}
	if _, err := backendplugin.ConfigureRequestFromProto(backendplugin.ConfigureRequestToProto(cfg), neg); err != nil {
		t.Fatal(err)
	}

	profile := backendplugin.ResolvedProfile{
		Capabilities:          backendplugin.CapabilitySummary{Streaming: true},
		TransportCapabilities: backendplugin.TransportCapabilitySummary{Cancellation: true},
		EvidenceSource:        "static",
		ProfileVersion:        "1",
	}
	if _, err := backendplugin.ResolvedProfileFromProto(backendplugin.ResolvedProfileToProto(profile)); err != nil {
		t.Fatal(err)
	}

	inv := sampleInvocation(text)
	ip, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.InvocationFromProto(ip); err != nil {
		t.Fatal(err)
	}

	list := backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: "m1", NativeModelID: "n1", FactoryKind: "localstub",
		}},
		InventorySource: "static",
		FetchedUnixMS:   1,
	}
	if _, err := backendplugin.ListModelsResponseFromProto(backendplugin.ListModelsResponseToProto(list)); err != nil {
		t.Fatal(err)
	}

	count := backendplugin.CountTokensResponse{
		InputTokens:     protoInt64(3),
		Presence:        backendplugin.UsagePresence{InputTokens: true},
		EvidenceQuality: "estimated",
	}
	if _, err := backendplugin.CountTokensResponseFromProto(backendplugin.CountTokensResponseToProto(count)); err != nil {
		t.Fatal(err)
	}

	finReq := backendplugin.FinalizeBillingRequest{
		InstanceID: "i1", ALegID: "a", BLegID: "b", ModelID: "m1", IdempotencyKey: "k1",
	}
	if err := finReq.Validate(); err != nil {
		t.Fatal(err)
	}
	fin := backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			TotalTokens:  protoInt64(9),
			Presence:     backendplugin.UsagePresence{TotalTokens: true},
			RawUsageJSON: backendplugin.RawJSONFromBytes([]byte(`{"n":9}`)),
		},
		EvidenceQuality: "provider",
	}
	fp, err := backendplugin.FinalizeBillingResponseToProto(fin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.FinalizeBillingResponseFromProto(fp); err != nil {
		t.Fatal(err)
	}

	pe := &backendplugin.PluginError{Code: backendplugin.ErrorCodeUnavailable, Message: "down", Retryable: true}
	pp, err := backendplugin.PluginErrorToProto(pe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.PluginErrorFromProto(pp); err != nil {
		t.Fatal(err)
	}

	co := &backendplugin.CancelOutcome{Acknowledged: true, Reason: backendplugin.CancelReasonDeadline}
	cp, err := backendplugin.CancelOutcomeToProto(co)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.CancelOutcomeFromProto(cp); err != nil {
		t.Fatal(err)
	}

	term := &backendplugin.Terminal{Status: backendplugin.TerminalCancelled, Error: pe}
	tp, err := backendplugin.TerminalToProto(term)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.TerminalFromProto(tp); err != nil {
		t.Fatal(err)
	}

	sf := backendplugin.ServerFrame{
		Kind:     backendplugin.ServerFrameTerminal,
		Sequence: 1,
		Terminal: term,
	}
	sfp, err := backendplugin.ServerFrameToProto(sf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.ServerFrameFromProto(sfp); err != nil {
		t.Fatal(err)
	}

	cf := backendplugin.ClientFrame{
		Kind:       backendplugin.ClientFrameStart,
		InstanceID: "i1",
		Invocation: &inv,
	}
	cfp, err := backendplugin.ClientFrameToProto(cf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backendplugin.ClientFrameFromProto(cfp); err != nil {
		t.Fatal(err)
	}

	h := backendplugin.HealthResponse{Serving: true, Detail: "ok"}
	if got := backendplugin.HealthResponseFromProto(backendplugin.HealthResponseToProto(h)); !got.Serving {
		t.Fatal(got)
	}
	sh := backendplugin.GracefulShutdownRequest{DrainTimeoutMS: 100}
	if got := backendplugin.GracefulShutdownRequestFromProto(backendplugin.GracefulShutdownRequestToProto(sh)); got.DrainTimeoutMS != 100 {
		t.Fatal(got)
	}
}

func TestEnumFailClosed_UnspecifiedProto(t *testing.T) {
	t.Parallel()
	_, err := backendplugin.PluginDescriptorFromProto(&backendpluginv1.PluginDescriptor{
		ProtocolMajor: 1,
		PluginId:      "com.aiproxer.x",
		Version:       "1",
		Factories: []*backendpluginv1.FactoryDescriptor{{
			Kind:           "k",
			CredentialMode: backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNSPECIFIED,
			AccessScope:    backendpluginv1.AccessScope_ACCESS_SCOPE_ANY,
			ProcessSharing: backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE,
		}},
	})
	if !errors.Is(err, backendplugin.ErrUnknownEnum) {
		t.Fatalf("err=%v", err)
	}
}

func TestAipsdkSecurityConversion(t *testing.T) {
	t.Parallel()
	cm, err := backendplugin.CredentialModeFromAipsdk(aipsdk.CredentialNone)
	if err != nil || cm != backendplugin.CredentialModeNone {
		t.Fatalf("%v %v", cm, err)
	}
	as, err := backendplugin.AccessScopeFromAipsdk(aipsdk.BackendAccessLocalOnly)
	if err != nil || as != backendplugin.AccessScopeLocalOnly {
		t.Fatalf("%v %v", as, err)
	}
}

func TestProtoGolden_NegotiateRoundTrip(t *testing.T) {
	t.Parallel()
	req := &backendpluginv1.NegotiateRequest{
		HostMajor:               1,
		HostMinor:               2,
		DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{
			{Name: "count_tokens", Required: false},
		},
	}
	bin, err := proto.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "golden", "negotiate_request.bin")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, bin, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run UPDATE_GOLDEN=1 once): %v", err)
	}
	if !proto.Equal(req, mustUnmarshalNegotiate(t, want)) {
		t.Fatal("golden mismatch")
	}
}

func TestProto_UnknownFieldsRetained(t *testing.T) {
	t.Parallel()
	msg := &backendpluginv1.PluginDescriptor{
		ProtocolMajor: 1,
		PluginId:      "com.aiproxer.x",
		Version:       "1.0.0",
	}
	bin, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	bin = append(bin, 0x98, 0x06, 0x07)
	var out backendpluginv1.PluginDescriptor
	if err := proto.Unmarshal(bin, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ProtoReflect().GetUnknown()) == 0 {
		t.Fatal("expected retained unknown fields")
	}
}

func TestPublicInterfacesCompile(t *testing.T) {
	t.Parallel()
	var _ backendplugin.Service
	var _ backendplugin.ConfiguredInstance
	var _ backendplugin.TokenCounter
	var _ backendplugin.BillingFinalizer
	var _ backendplugin.ExecuteStream
}

func protoInt64(v int64) *int64 { return new(v) }

func mustUnmarshalNegotiate(t *testing.T, b []byte) *backendpluginv1.NegotiateRequest {
	t.Helper()
	var m backendpluginv1.NegotiateRequest
	if err := proto.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return &m
}
