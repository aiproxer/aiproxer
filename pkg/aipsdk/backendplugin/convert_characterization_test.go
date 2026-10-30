package backendplugin_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestConvertCharacterization_NegotiationRoundTrip(t *testing.T) {
	t.Parallel()
	offer := backendplugin.ProtocolOffer{
		Major:                   1,
		Minor:                   2,
		DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: "count_tokens", Required: false},
			{Name: "finalize_billing", Required: true},
		},
	}
	req, err := backendplugin.ProtocolOfferToNegotiateRequest(offer)
	if err != nil {
		t.Fatal(err)
	}
	gotOffer, err := backendplugin.ProtocolOfferFromNegotiateRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(offer, gotOffer) {
		t.Fatalf("offer round-trip: want=%#v got=%#v", offer, gotOffer)
	}

	neg := backendplugin.Negotiation{
		Compatible:       true,
		NegotiatedMinor:  2,
		EnabledFeatures:  []string{"alpha", "zeta"},
		RejectReason:     "",
		TransportPolicy:  backendplugin.DefaultTransportPolicy(),
		PluginMajor:      1,
		PluginMinor:      2,
		PluginFeatures:   offer.Features,
		NegotiationToken: "tok-1",
	}
	resp, err := backendplugin.NegotiationToNegotiateResponse(neg)
	if err != nil {
		t.Fatal(err)
	}
	gotNeg, err := backendplugin.NegotiationFromNegotiateResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(neg, gotNeg) {
		t.Fatalf("negotiation round-trip: want=%#v got=%#v", neg, gotNeg)
	}
}

func TestConvertCharacterization_EnumValuesRoundTrip(t *testing.T) {
	t.Parallel()

	credentialModes := []backendplugin.CredentialMode{
		backendplugin.CredentialModeStatic,
		backendplugin.CredentialModeWorkload,
		backendplugin.CredentialModeOAuthUser,
		backendplugin.CredentialModeNone,
		backendplugin.CredentialModeUnknown,
	}
	accessScopes := []backendplugin.AccessScope{
		backendplugin.AccessScopeAny,
		backendplugin.AccessScopeLocalOnly,
	}
	processSharings := []backendplugin.ProcessSharing{
		backendplugin.ProcessSharingPerInstance,
		backendplugin.ProcessSharingSharedArtifact,
	}
	roles := []backendplugin.Role{
		backendplugin.RoleSystem,
		backendplugin.RoleUser,
		backendplugin.RoleAssistant,
		backendplugin.RoleTool,
	}
	partKinds := []backendplugin.PartKind{
		backendplugin.PartKindText,
		backendplugin.PartKindImageRef,
		backendplugin.PartKindFileRef,
		backendplugin.PartKindReasoning,
		backendplugin.PartKindToolResult,
		backendplugin.PartKindJSON,
	}
	errorCodes := []backendplugin.ErrorCode{
		backendplugin.ErrorCodeInvalidArgument,
		backendplugin.ErrorCodeUnauthenticated,
		backendplugin.ErrorCodePermissionDenied,
		backendplugin.ErrorCodeNotFound,
		backendplugin.ErrorCodeResourceExhausted,
		backendplugin.ErrorCodeFailedPrecondition,
		backendplugin.ErrorCodeAborted,
		backendplugin.ErrorCodeUnavailable,
		backendplugin.ErrorCodeInternal,
		backendplugin.ErrorCodeProviderTransient,
		backendplugin.ErrorCodeProviderTerminal,
		backendplugin.ErrorCodeProtocolViolation,
		backendplugin.ErrorCodeCancelled,
	}
	cancelReasons := []backendplugin.CancelReason{
		backendplugin.CancelReasonClient,
		backendplugin.CancelReasonHost,
		backendplugin.CancelReasonDeadline,
		backendplugin.CancelReasonShutdown,
	}
	clientFrameKinds := []backendplugin.ClientFrameKind{
		backendplugin.ClientFrameStart,
		backendplugin.ClientFrameCancel,
		backendplugin.ClientFrameCloseInput,
	}
	serverFrameKinds := []backendplugin.ServerFrameKind{
		backendplugin.ServerFrameAccepted,
		backendplugin.ServerFrameEvent,
		backendplugin.ServerFrameDiagnostic,
		backendplugin.ServerFrameCancelOutcome,
		backendplugin.ServerFrameTerminal,
	}
	eventKinds := []backendplugin.EventKind{
		backendplugin.EventResponseStarted,
		backendplugin.EventMessageStarted,
		backendplugin.EventTextDelta,
		backendplugin.EventReasoningDelta,
		backendplugin.EventReasoningSignatureDelta,
		backendplugin.EventReasoningOpaqueDelta,
		backendplugin.EventToolCallStarted,
		backendplugin.EventToolCallArgsDelta,
		backendplugin.EventToolCallFinished,
		backendplugin.EventUsageDelta,
		backendplugin.EventWarning,
		backendplugin.EventError,
		backendplugin.EventResponseFinished,
		backendplugin.EventAssistantImageRef,
		backendplugin.EventAssistantFileRef,
	}
	terminalStatuses := []backendplugin.TerminalStatus{
		backendplugin.TerminalSuccess,
		backendplugin.TerminalFailure,
		backendplugin.TerminalCancelled,
	}

	for _, cm := range credentialModes {
		factoryRoundTrip(t, "credential_"+string(cm), cm, backendplugin.AccessScopeLocalOnly, backendplugin.ProcessSharingPerInstance)
	}
	for _, as := range accessScopes {
		factoryRoundTrip(t, "access_"+string(as), backendplugin.CredentialModeNone, as, backendplugin.ProcessSharingPerInstance)
	}
	for _, ps := range processSharings {
		factoryRoundTrip(t, "process_"+string(ps), backendplugin.CredentialModeNone, backendplugin.AccessScopeLocalOnly, ps)
	}
	for _, role := range roles {
		messageRoundTrip(t, "role_"+string(role), role, backendplugin.PartKindText)
	}
	for _, kind := range partKinds {
		role := backendplugin.RoleUser
		if kind == backendplugin.PartKindReasoning {
			role = backendplugin.RoleAssistant
		}
		messageRoundTrip(t, "part_"+string(kind), role, kind)
	}
	for _, code := range errorCodes {
		pluginErrorRoundTrip(t, "error_"+string(code), code)
	}
	for _, reason := range cancelReasons {
		cancelOutcomeRoundTrip(t, "cancel_"+string(reason), reason)
	}
	for _, kind := range clientFrameKinds {
		clientFrameRoundTrip(t, "client_"+string(kind), kind)
	}
	for _, kind := range serverFrameKinds {
		serverFrameRoundTrip(t, "server_"+string(kind), kind)
	}
	for _, kind := range eventKinds {
		canonicalEventRoundTrip(t, "event_"+string(kind), kind)
	}
	for _, status := range terminalStatuses {
		terminalRoundTrip(t, "terminal_"+string(status), status)
	}
}

func factoryRoundTrip(t *testing.T, name string, cm backendplugin.CredentialMode, as backendplugin.AccessScope, ps backendplugin.ProcessSharing) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		f := backendplugin.FactoryDescriptor{
			Kind:                      "stub",
			DisplayName:               "Stub",
			Description:               "desc",
			CredentialMode:            cm,
			AccessScope:               as,
			ProcessSharing:            ps,
			RoutePrefixes:             []string{"stub/"},
			SupportsCountTokens:       true,
			SupportsFinalizeBilling:   true,
			SupportsDynamicInventory:  true,
			SupportsModelAwareProfile: true,
			StaticCapabilities:        backendplugin.CapabilitySummary{Streaming: true, Tools: true},
			TransportCapabilities:     backendplugin.TransportCapabilitySummary{Cancellation: true},
		}
		wire, err := backendplugin.PluginDescriptorToProto(backendplugin.PluginDescriptor{
			ProtocolMajor: 1,
			ProtocolMinor: 0,
			PluginID:      "com.aiproxer.test",
			Version:       "1.0.0",
			Factories:     []backendplugin.FactoryDescriptor{f},
		})
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.PluginDescriptorFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if len(back.Factories) != 1 {
			t.Fatalf("factories=%d", len(back.Factories))
		}
		got := back.Factories[0]
		if got.CredentialMode != cm || got.AccessScope != as || got.ProcessSharing != ps {
			t.Fatalf("enum mismatch: want cm=%q as=%q ps=%q got cm=%q as=%q ps=%q", cm, as, ps, got.CredentialMode, got.AccessScope, got.ProcessSharing)
		}
	})
}

func messageRoundTrip(t *testing.T, name string, role backendplugin.Role, kind backendplugin.PartKind) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		part := backendplugin.Part{Kind: kind}
		switch kind {
		case backendplugin.PartKindText:
			part.Text = new("hello")
		case backendplugin.PartKindImageRef:
			part.ImageRef = new("img://1")
		case backendplugin.PartKindFileRef:
			part.FileRef = new("file://1")
		case backendplugin.PartKindReasoning:
			d := "openai.chat.text.v1"
			part.ReasoningText = new("think")
			part.ReasoningDialect = &d
		case backendplugin.PartKindToolCall:
			part.ToolCallID = new("tc1")
			part.ToolName = new("tool")
			part.ToolArgsJSON = backendplugin.RawJSONFromBytes([]byte(`{"a":1}`))
		case backendplugin.PartKindToolResult:
			part.ToolCallID = new("tc1")
			part.ToolName = new("tool")
			part.ToolArgsJSON = backendplugin.RawJSONFromBytes([]byte(`{"ok":true}`))
		case backendplugin.PartKindJSON:
			part.ToolArgsJSON = backendplugin.RawJSONFromBytes([]byte(`{"k":"v"}`))
		}
		inv := backendplugin.Invocation{
			RequestID:        "r1",
			AttemptID:        "a1",
			ALegID:           "aleg",
			BLegID:           "bleg",
			CanonicalModelID: "model",
			Messages:         []backendplugin.Message{{Role: role, Parts: []backendplugin.Part{part}}},
			Options:          backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
		}
		wire, err := backendplugin.InvocationToProto(inv)
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.InvocationFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if len(back.Messages) != 1 || back.Messages[0].Role != role || len(back.Messages[0].Parts) != 1 || back.Messages[0].Parts[0].Kind != kind {
			t.Fatalf("message round-trip: role=%q kind=%q got=%+v", role, kind, back.Messages)
		}
	})
}

func pluginErrorRoundTrip(t *testing.T, name string, code backendplugin.ErrorCode) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		pe := &backendplugin.PluginError{Code: code, Message: "msg", Retryable: true, OutputCommitted: true}
		wire, err := backendplugin.PluginErrorToProto(pe)
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.PluginErrorFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if back.Code != code {
			t.Fatalf("code=%q want=%q", back.Code, code)
		}
	})
}

func cancelOutcomeRoundTrip(t *testing.T, name string, reason backendplugin.CancelReason) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		co := &backendplugin.CancelOutcome{Acknowledged: true, Detail: "ok", Reason: reason}
		wire, err := backendplugin.CancelOutcomeToProto(co)
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.CancelOutcomeFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if back.Reason != reason {
			t.Fatalf("reason=%q want=%q", back.Reason, reason)
		}
	})
}

func clientFrameRoundTrip(t *testing.T, name string, kind backendplugin.ClientFrameKind) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		text := "hi"
		inv := backendplugin.Invocation{
			RequestID: "r1", AttemptID: "a1", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
			Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
			Options:  backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
		}
		frame := backendplugin.ClientFrame{
			Kind:       kind,
			Sequence:   1,
			InstanceID: "inst",
		}
		switch kind {
		case backendplugin.ClientFrameStart:
			frame.Invocation = &inv
		case backendplugin.ClientFrameCancel:
			frame.CancelReason = backendplugin.CancelReasonClient
		}
		wire, err := backendplugin.ClientFrameToProto(frame)
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.ClientFrameFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if back.Kind != kind {
			t.Fatalf("kind=%q want=%q", back.Kind, kind)
		}
	})
}

func serverFrameRoundTrip(t *testing.T, name string, kind backendplugin.ServerFrameKind) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		frame := backendplugin.ServerFrame{Kind: kind, Sequence: 1}
		switch kind {
		case backendplugin.ServerFrameEvent:
			frame.Event = &backendplugin.CanonicalEvent{Kind: backendplugin.EventTextDelta, Delta: new("x")}
		case backendplugin.ServerFrameDiagnostic:
			frame.Diagnostic = "diag"
		case backendplugin.ServerFrameCancelOutcome:
			frame.CancelOutcome = &backendplugin.CancelOutcome{Acknowledged: true, Reason: backendplugin.CancelReasonHost}
		case backendplugin.ServerFrameTerminal:
			frame.Terminal = &backendplugin.Terminal{Status: backendplugin.TerminalSuccess}
		}
		wire, err := backendplugin.ServerFrameToProto(frame)
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.ServerFrameFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if back.Kind != kind {
			t.Fatalf("kind=%q want=%q", back.Kind, kind)
		}
	})
}

func canonicalEventRoundTrip(t *testing.T, name string, kind backendplugin.EventKind) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		inTok, outTok, total := int64(1), int64(2), int64(3)
		ev := &backendplugin.CanonicalEvent{
			Kind:         kind,
			MessageIndex: new(int32(2)),
			Delta:        new("delta"),
			Signature:    new("sig"),
			Opaque:       []byte(`{"type":"redacted_thinking"}`),
			ToolCallID:   new("tc1"),
			ToolName:     new("fn"),
			Usage: &backendplugin.UsageEvidence{
				InputTokens:  &inTok,
				OutputTokens: &outTok,
				TotalTokens:  &total,
				Presence: backendplugin.UsagePresence{
					InputTokens: true, OutputTokens: true, TotalTokens: true,
				},
				RawUsageJSON: backendplugin.RawJSONFromBytes([]byte(`{"n":3}`)),
			},
			Warning:  new("warn"),
			Error:    &backendplugin.PluginError{Code: backendplugin.ErrorCodeInternal, Message: "e"},
			ImageRef: new("img://1"),
			FileRef:  new("file://1"),
		}
		wire, err := backendplugin.CanonicalEventToProto(ev)
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.CanonicalEventFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if back.Kind != kind {
			t.Fatalf("kind=%q want=%q", back.Kind, kind)
		}
	})
}

func terminalRoundTrip(t *testing.T, name string, status backendplugin.TerminalStatus) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		t.Parallel()
		term := &backendplugin.Terminal{
			Status: status,
			Error:  &backendplugin.PluginError{Code: backendplugin.ErrorCodeUnavailable, Message: "down"},
		}
		wire, err := backendplugin.TerminalToProto(term)
		if err != nil {
			t.Fatal(err)
		}
		back, err := backendplugin.TerminalFromProto(wire)
		if err != nil {
			t.Fatal(err)
		}
		if back.Status != status {
			t.Fatalf("status=%q want=%q", back.Status, status)
		}
	})
}

func TestConvertCharacterization_UsagePresenceRoundTrip(t *testing.T) {
	t.Parallel()
	presence := backendplugin.UsagePresence{
		InputTokens: true, OutputTokens: true, CacheReadTokens: true,
		CacheWriteTokens: true, ReasoningTokens: true, TotalTokens: true,
	}
	wire := backendplugin.UsagePresenceToProto(presence)
	got := backendplugin.UsagePresenceFromProto(wire)
	if !reflect.DeepEqual(presence, got) {
		t.Fatalf("presence round-trip: want=%#v got=%#v", presence, got)
	}
}

func TestConvertCharacterization_FullyPopulatedInvocation(t *testing.T) {
	t.Parallel()
	text := "hello"
	schema := backendplugin.RawJSONFromBytes([]byte(`{"type":"object"}`))
	toolChoice := "auto"
	inv := backendplugin.Invocation{
		RequestID:        "req-1",
		AttemptID:        "att-1",
		ALegID:           "aleg-1",
		BLegID:           "bleg-1",
		CanonicalModelID: "canonical/model",
		NativeModelID:    "native/model",
		Instructions: []backendplugin.Message{{
			Role: backendplugin.RoleSystem,
			Parts: []backendplugin.Part{{
				Kind: backendplugin.PartKindText, Text: &text,
			}},
		}},
		Messages: []backendplugin.Message{{
			Role: backendplugin.RoleUser,
			Parts: []backendplugin.Part{
				{Kind: backendplugin.PartKindText, Text: &text},
				{Kind: backendplugin.PartKindImageRef, ImageRef: new("img://x")},
				{Kind: backendplugin.PartKindFileRef, FileRef: new("file://x")},
				{Kind: backendplugin.PartKindJSON, ToolArgsJSON: backendplugin.RawJSONFromBytes([]byte(`{"x":1}`))},
			},
		}, {
			Role: backendplugin.RoleAssistant,
			Parts: []backendplugin.Part{
				{Kind: backendplugin.PartKindReasoning, ReasoningText: new("chain"), ReasoningDialect: new("openai.chat.text.v1")},
			},
		}},
		Tools: []backendplugin.ToolDef{{
			Name: "fn", Description: "d",
			ParametersJSON: backendplugin.RawJSONFromBytes([]byte(`{"type":"object","properties":{}}`)),
		}},
		ToolChoice: &toolChoice,
		Options: backendplugin.GenerationOptions{
			MaxOutputTokens:    new(uint32(128)),
			TemperatureMillis:  new(int32(700)),
			ReasoningEffort:    new("high"),
			ParallelToolCalls:  new(true),
			ResponseMIMEType:   new("application/json"),
			ResponseSchemaJSON: schema,
		},
		SafeMetadata: map[string]string{"op": "chat"},
	}
	wire, err := backendplugin.InvocationToProto(inv)
	if err != nil {
		t.Fatal(err)
	}
	back, err := backendplugin.InvocationFromProto(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inv, back) {
		t.Fatalf("invocation round-trip mismatch:\nwant=%#v\ngot=%#v", inv, back)
	}
}

func TestConvertCharacterization_RemainingDTOConverters(t *testing.T) {
	t.Parallel()

	runtime := backendplugin.RuntimePolicy{
		MaxRequestBytes:         backendplugin.DefaultMaxMessageBytes,
		MaxStreamFrameBytes:     backendplugin.DefaultMaxStreamFrameBytes,
		MaxPendingEvents:        3,
		RequestTimeoutMS:        4,
		CancelDeadlineMS:        5,
		DiagnosticsVerbosity:    "info",
		MaxConcurrentExecutions: 6,
		LocalOnly:               true,
		AllowedEnvNames:         []string{"A", "B"},
		DisableTransportRetries: true,
		QuotaSwitchRetryAfterMS: 30 * 60 * 1000,
	}
	if !reflect.DeepEqual(runtime, backendplugin.RuntimePolicyFromProto(backendplugin.RuntimePolicyToProto(runtime))) {
		t.Fatal("runtime policy round-trip")
	}

	neg, err := backendplugin.Negotiate(
		backendplugin.ProtocolOffer{Major: 1, Minor: 1, DisableTransportRetries: true},
		backendplugin.ProtocolOffer{Major: 1, Minor: 1, DisableTransportRetries: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg := backendplugin.ConfigureRequest{
		InstanceID: "i1", FactoryKind: "stub", ConfigYAML: []byte("k: v"),
		Secrets:       backendplugin.SecretBundle{Values: map[string][]byte{"k": []byte("v")}},
		RuntimePolicy: runtime, Negotiation: neg, NegotiationToken: "tok",
	}
	cfgBack, err := backendplugin.ConfigureRequestFromProto(backendplugin.ConfigureRequestToProto(cfg), neg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg, cfgBack) {
		t.Fatalf("configure round-trip: want=%#v got=%#v", cfg, cfgBack)
	}

	profile := backendplugin.ResolvedProfile{
		Capabilities:             backendplugin.CapabilitySummary{Streaming: true, ReasoningReplay: true},
		TransportCapabilities:    backendplugin.TransportCapabilitySummary{Keepalive: true},
		ReasoningReplaySupported: true,
		RoutePrefixes:            []string{"p/"},
		EnforceMaxOutput:         true,
		MaxOutputTokens:          new(uint32(99)),
		SupportsCountTokens:      true,
		SupportsFinalizeBilling:  true,
		SupportsDynamicInventory: true,
		EvidenceSource:           "static",
		ProfileVersion:           "v1",
	}
	profBack, err := backendplugin.ResolvedProfileFromProto(backendplugin.ResolvedProfileToProto(profile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profile, profBack) {
		t.Fatalf("profile round-trip")
	}

	list := backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: "c", NativeModelID: "n", DisplayName: "d",
			RoutePrefix: "p/", FactoryKind: "stub",
			// The designated default model survives the wire: the host derives
			// the instance's default route from that designation.
			IsDefault:    true,
			Capabilities: backendplugin.CapabilitySummary{Vision: true},
		}},
		InventorySource:    "static",
		FetchedUnixMS:      100,
		RefreshAfterUnixMS: new(int64(200)),
		ErrorCode:          "",
	}
	listBack, err := backendplugin.ListModelsResponseFromProto(backendplugin.ListModelsResponseToProto(list))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(list, listBack) {
		t.Fatalf("list models round-trip")
	}

	countReq := backendplugin.CountTokensRequest{InstanceID: "i", ModelID: "m", Invocation: sampleInv(t)}
	countWire, err := backendplugin.CountTokensRequestToProto(countReq)
	if err != nil {
		t.Fatal(err)
	}
	countReqBack, err := backendplugin.CountTokensRequestFromProto(countWire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(countReq, countReqBack) {
		t.Fatalf("count tokens request round-trip")
	}

	inTok := int64(7)
	countResp := backendplugin.CountTokensResponse{
		InputTokens: &inTok, Presence: backendplugin.UsagePresence{InputTokens: true}, EvidenceQuality: "est",
	}
	countRespBack, err := backendplugin.CountTokensResponseFromProto(backendplugin.CountTokensResponseToProto(countResp))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(countResp, countRespBack) {
		t.Fatalf("count tokens response round-trip")
	}

	finReq := backendplugin.FinalizeBillingRequest{
		InstanceID: "i", ALegID: "a", BLegID: "b", ModelID: "m", Reason: "done", IdempotencyKey: "k",
	}
	finReqBack, err := backendplugin.FinalizeBillingRequestFromProto(backendplugin.FinalizeBillingRequestToProto(finReq))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(finReq, finReqBack) {
		t.Fatalf("finalize billing request round-trip")
	}

	total := int64(9)
	finResp := backendplugin.FinalizeBillingResponse{
		Usage: backendplugin.UsageEvidence{
			TotalTokens: &total, Presence: backendplugin.UsagePresence{TotalTokens: true},
			RawUsageJSON: backendplugin.RawJSONFromBytes([]byte(`{"total":9}`)),
		},
		EvidenceQuality: "provider",
	}
	finWire, err := backendplugin.FinalizeBillingResponseToProto(finResp)
	if err != nil {
		t.Fatal(err)
	}
	finRespBack, err := backendplugin.FinalizeBillingResponseFromProto(finWire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(finResp, finRespBack) {
		t.Fatalf("finalize billing response round-trip")
	}

	if got := backendplugin.HealthResponseFromProto(backendplugin.HealthResponseToProto(backendplugin.HealthResponse{Serving: true, Detail: "ok"})); !got.Serving {
		t.Fatal("health round-trip")
	}
	if got := backendplugin.GracefulShutdownRequestFromProto(backendplugin.GracefulShutdownRequestToProto(backendplugin.GracefulShutdownRequest{DrainTimeoutMS: 50})); got.DrainTimeoutMS != 50 {
		t.Fatal("shutdown request round-trip")
	}
	if got := backendplugin.GracefulShutdownResponseFromProto(backendplugin.GracefulShutdownResponseToProto(backendplugin.GracefulShutdownResponse{Accepted: true})); !got.Accepted {
		t.Fatal("shutdown response round-trip")
	}
}

func sampleInv(t *testing.T) backendplugin.Invocation {
	t.Helper()
	text := "hi"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "aleg", BLegID: "bleg", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
		Options:  backendplugin.GenerationOptions{ResponseSchemaJSON: backendplugin.RawJSONAbsentValue()},
	}
	if err := inv.Validate(); err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestConvertCharacterization_UnknownEnumFailClosed(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(t *testing.T) error
	}{
		{
			name: "credential_mode_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.PluginDescriptorFromProto(&backendpluginv1.PluginDescriptor{
					ProtocolMajor: 1, PluginId: "com.aiproxer.x", Version: "1",
					Factories: []*backendpluginv1.FactoryDescriptor{{
						Kind:           "k",
						CredentialMode: backendpluginv1.CredentialMode_CREDENTIAL_MODE_UNSPECIFIED,
						AccessScope:    backendpluginv1.AccessScope_ACCESS_SCOPE_ANY,
						ProcessSharing: backendpluginv1.ProcessSharing_PROCESS_SHARING_PER_INSTANCE,
					}},
				})
				return err
			},
		},
		{
			name: "role_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.InvocationFromProto(&backendpluginv1.Invocation{
					RequestId: "r", AttemptId: "a", ALegId: "aleg", BLegId: "bleg", CanonicalModelId: "m",
					Messages: []*backendpluginv1.Message{{
						Role: backendpluginv1.Role_ROLE_UNSPECIFIED,
						Parts: []*backendpluginv1.Part{{
							Kind: backendpluginv1.PartKind_PART_KIND_TEXT, Text: new("x"),
						}},
					}},
				})
				return err
			},
		},
		{
			name: "part_kind_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.InvocationFromProto(&backendpluginv1.Invocation{
					RequestId: "r", AttemptId: "a", ALegId: "aleg", BLegId: "bleg", CanonicalModelId: "m",
					Messages: []*backendpluginv1.Message{{
						Role: backendpluginv1.Role_ROLE_USER,
						Parts: []*backendpluginv1.Part{{
							Kind: backendpluginv1.PartKind_PART_KIND_UNSPECIFIED, Text: new("x"),
						}},
					}},
				})
				return err
			},
		},
		{
			name: "event_kind_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.CanonicalEventFromProto(&backendpluginv1.CanonicalEvent{
					Kind: backendpluginv1.EventKind_EVENT_KIND_UNSPECIFIED,
				})
				return err
			},
		},
		{
			name: "error_code_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.PluginErrorFromProto(&backendpluginv1.PluginError{
					Code: backendpluginv1.ErrorCode_ERROR_CODE_UNSPECIFIED,
				})
				return err
			},
		},
		{
			name: "client_frame_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.ClientFrameFromProto(&backendpluginv1.ExecuteClientFrame{
					Kind: backendpluginv1.ClientFrameKind_CLIENT_FRAME_KIND_UNSPECIFIED,
				})
				return err
			},
		},
		{
			name: "server_frame_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.ServerFrameFromProto(&backendpluginv1.ExecuteServerFrame{
					Kind: backendpluginv1.ServerFrameKind_SERVER_FRAME_KIND_UNSPECIFIED,
				})
				return err
			},
		},
		{
			name: "terminal_status_unspecified",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.TerminalFromProto(&backendpluginv1.Terminal{
					Status: backendpluginv1.TerminalStatus_TERMINAL_STATUS_UNSPECIFIED,
				})
				return err
			},
		},
		{
			name: "cancel_reason_unknown_dto",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.CancelOutcomeToProto(&backendplugin.CancelOutcome{
					Acknowledged: true,
					Reason:       backendplugin.CancelReason("evil"),
				})
				return err
			},
		},
		{
			name: "dto_unknown_event_kind",
			run: func(t *testing.T) error {
				t.Helper()
				_, err := backendplugin.CanonicalEventToProto(&backendplugin.CanonicalEvent{Kind: backendplugin.EventKind("not_real")})
				return err
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.run(t)
			if !errors.Is(err, backendplugin.ErrUnknownEnum) {
				t.Fatalf("err=%v want ErrUnknownEnum", err)
			}
		})
	}
}

func TestConvertCharacterization_OutputCommittedEventMatchesAipapi(t *testing.T) {
	t.Parallel()
	for _, kind := range []backendplugin.EventKind{
		backendplugin.EventResponseStarted,
		backendplugin.EventTextDelta,
		backendplugin.EventReasoningDelta,
		backendplugin.EventReasoningSignatureDelta,
		backendplugin.EventReasoningOpaqueDelta,
		backendplugin.EventToolCallStarted,
		backendplugin.EventToolCallArgsDelta,
		backendplugin.EventUsageDelta,
		backendplugin.EventAssistantImageRef,
		backendplugin.EventAssistantFileRef,
	} {
		got := backendplugin.OutputCommittedEvent(kind)
		want := aipapi.OutputCommitted(aipapi.Event{Kind: aipapi.EventKind(kind)})
		if got != want {
			t.Fatalf("kind=%q got=%v want=%v", kind, got, want)
		}
	}
}
