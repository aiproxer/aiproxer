package adapter_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func testCall() aipapi.Call {
	return aipapi.Call{
		ID:         "req-1",
		Session:    aipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIResponses},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func testCand() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
		Key:     "fake:fake-model",
	}
}

func TestCapabilityInventoryCountFinalize(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "i1", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "i1", RoutePrefixes: []string{"fake"}, EnforcesMaxOutput: true,
	})
	be := br.Backend
	if _, ok := be.Caps[aipapi.CapabilityStreaming]; !ok {
		t.Fatal("missing streaming cap")
	}
	if be.ResolveCaps == nil || be.ResolveTransportCaps == nil {
		t.Fatal("expected resolve seams")
	}
	caps := be.ResolveTransportCaps(context.Background(), testCall(), testCand())
	if !caps.Supports(aipapi.OperationOpenAIResponses, aipapi.TransportModeStreaming) {
		t.Fatal("transport caps must derive from profile+call operation")
	}
	if be.TransportCaps != nil {
		t.Fatal("static transport caps must not invent operations")
	}
	if be.ModelInventory == nil {
		t.Fatal("expected inventory")
	}
	if be.ProviderCounter == nil {
		t.Fatal("expected counter")
	}
	if be.FinalizeBilling == nil {
		t.Fatal("expected billing")
	}
	count, err := be.ProviderCounter.CountCall(context.Background(), accountingapp.CountCallInput{
		Backend: "fake", Model: "fake-model", CallID: "req-1", Call: testCall(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if count.InputTokens != 1 {
		t.Fatalf("InputTokens=%d want 1: the connector must count the caller's message", count.InputTokens)
	}
	if count.TotalTokensPresent {
		t.Fatal("must not invent TotalTokens from InputTokens")
	}
	_ = be.Open
	_ = br.Cleanup()
}

// countPayloadCall is a canonical call whose payload is large enough that an
// empty invocation cannot be mistaken for it: instructions, three messages and
// a tool definition.
func countPayloadCall() aipapi.Call {
	call := testCall()
	call.Instructions = []aipapi.Message{{
		Role:  aipapi.RoleSystem,
		Parts: []aipapi.Part{aipapi.TextPart("be brief")},
	}}
	call.Messages = append(call.Messages,
		aipapi.Message{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
		aipapi.Message{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("and now?")}},
	)
	call.Tools = []aipapi.ToolDef{{Name: "weather", Parameters: []byte(`{"type":"object"}`)}}
	return call
}

// TestProviderCounterCountCall_CarriesCallerPayload is the regression for a
// count bridge that dropped its input and asked the connector about an empty
// invocation. Oracle: the testkit fake counts Invocation.Messages, and the
// capturing session proves instructions/tools/model reach the connector too.
func TestProviderCounterCountCall_CarriesCallerPayload(t *testing.T) {
	t.Parallel()
	call := countPayloadCall()

	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "count-payload", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "count-payload", Negotiation: backendplugin.Negotiation{Compatible: true},
	})
	t.Cleanup(func() { _ = br.Cleanup() })

	res, err := br.Backend.ProviderCounter.CountCall(context.Background(), accountingapp.CountCallInput{
		Backend: "fake", Model: "fake-model", CallID: call.ID, Call: call,
	})
	if err != nil {
		t.Fatalf("CountCall: %v", err)
	}
	if res.InputTokens != 3 {
		t.Fatalf("InputTokens=%d want 3: the connector must see all three messages", res.InputTokens)
	}
	if res.OutputTokens != 0 || res.TotalTokensPresent {
		t.Fatalf("provider count must not invent output/total evidence: %+v", res)
	}

	capture := &capturingCounterSession{}
	captured := adapter.Build(capture, backendplugin.ResolvedProfile{
		Capabilities:        backendplugin.CapabilitySummary{Streaming: true},
		SupportsCountTokens: true,
	}, adapter.Options{InstanceID: "count-shape"})
	t.Cleanup(func() { _ = captured.Cleanup() })

	if _, err := captured.Backend.ProviderCounter.CountCall(context.Background(), accountingapp.CountCallInput{
		Backend: "fake", Model: "fake-model", CallID: call.ID, Call: call,
	}); err != nil {
		t.Fatalf("CountCall: %v", err)
	}
	got := capture.request
	if got.InstanceID != "count-shape" {
		t.Fatalf("InstanceID=%q", got.InstanceID)
	}
	if got.ModelID != "fake-model" {
		t.Fatalf("ModelID=%q", got.ModelID)
	}
	inv := got.Invocation
	if inv.RequestID != "req-1" || inv.ALegID != "aleg-1" {
		t.Fatalf("invocation identity lost: %+v", inv)
	}
	if inv.CanonicalModelID != "fake-model" || inv.NativeModelID != "fake-model" {
		t.Fatalf("invocation model lost: %+v", inv)
	}
	if len(inv.Instructions) != 1 || inv.Instructions[0].Parts[0].Text == nil || *inv.Instructions[0].Parts[0].Text != "be brief" {
		t.Fatalf("instructions lost: %+v", inv.Instructions)
	}
	if len(inv.Messages) != 3 {
		t.Fatalf("Messages=%d want 3", len(inv.Messages))
	}
	if len(inv.Tools) != 1 || inv.Tools[0].Name != "weather" {
		t.Fatalf("tools lost: %+v", inv.Tools)
	}
}

// TestProviderCounter_NonCallKindsAreExplicitlyUnsupported pins the honest
// boundary: the backend-plugin count ABI counts an invocation's input tokens,
// so text/output counting must fall back to the local tokenizer instead of
// returning provider input-token evidence for non-input content.
func TestProviderCounter_NonCallKindsAreExplicitlyUnsupported(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "count-kinds", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "count-kinds"})
	t.Cleanup(func() { _ = br.Cleanup() })
	counter := br.Backend.ProviderCounter

	for _, kind := range []accountingapp.CountKind{accountingapp.CountKindText, accountingapp.CountKindOutput} {
		support := counter.SupportsCount(context.Background(), accountingapp.ProviderCountInput{
			Backend: "fake", Model: "fake-model", Kind: kind,
		})
		if support.Status != accountingapp.SupportStatusUnsupported {
			t.Fatalf("kind %q support=%+v want unsupported", kind, support)
		}
		if support.Message == "" {
			t.Fatalf("kind %q must explain why it is unsupported", kind)
		}
	}
	if got := counter.SupportsCount(context.Background(), accountingapp.ProviderCountInput{
		Backend: "fake", Model: "fake-model", Kind: accountingapp.CountKindCall,
	}); got.Status != accountingapp.SupportStatusSupported {
		t.Fatalf("call support=%+v want supported", got)
	}

	text, err := counter.CountText(context.Background(), accountingapp.CountTextInput{Model: "fake-model", Text: "hello"})
	if !errors.Is(err, accountingapp.ErrProviderUnsupported) {
		t.Fatalf("CountText err=%v want ErrProviderUnsupported (result %+v)", err, text)
	}
	output, err := counter.CountOutput(context.Background(), accountingapp.CountOutputInput{Model: "fake-model", Text: "hello"})
	if !errors.Is(err, accountingapp.ErrProviderUnsupported) {
		t.Fatalf("CountOutput err=%v want ErrProviderUnsupported (result %+v)", err, output)
	}
}

// TestProviderCounterCountCall_EmptyCallFailsExplicitly keeps empty input from
// becoming fabricated authoritative evidence: with no countable content the
// connector is never asked, and the caller gets an error to fall back on.
func TestProviderCounterCountCall_EmptyCallFailsExplicitly(t *testing.T) {
	t.Parallel()
	capture := &capturingCounterSession{}
	br := adapter.Build(capture, backendplugin.ResolvedProfile{
		Capabilities:        backendplugin.CapabilitySummary{Streaming: true},
		SupportsCountTokens: true,
	}, adapter.Options{InstanceID: "count-empty"})
	t.Cleanup(func() { _ = br.Cleanup() })

	res, err := br.Backend.ProviderCounter.CountCall(context.Background(), accountingapp.CountCallInput{
		Backend: "fake", Model: "fake-model", CallID: "req-empty",
		Call: aipapi.Call{ID: "req-empty", Session: aipapi.SessionRef{ALegID: "aleg-1"}},
	})
	if err == nil {
		t.Fatalf("empty call must not yield provider evidence: %+v", res)
	}
	if !errors.Is(err, backendplugin.ErrInvalidInvocation) {
		t.Fatalf("err=%v want ErrInvalidInvocation", err)
	}
	if res.InputTokens != 0 || res.OutputTokens != 0 || res.TotalTokensPresent {
		t.Fatalf("failed count must return zero evidence: %+v", res)
	}
	if capture.calls != 0 {
		t.Fatalf("connector calls=%d want 0", capture.calls)
	}
}

func TestStream_AcceptedDoesNotCommit_PrePostOutput(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "i2", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "i2"})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var sawText bool
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			sawText = true
			if !aipapi.OutputCommitted(ev) {
				t.Fatal("text should commit")
			}
		}
		if ev.Kind == aipapi.EventUsageDelta && aipapi.OutputCommitted(ev) {
			t.Fatal("usage must not commit")
		}
	}
	if !sawText {
		t.Fatal("expected text delta from fake")
	}
	ms, ok := stream.(interface {
		OutputCommitted() bool
		Attempts() int64
	})
	if !ok {
		t.Fatal("stream missing instrumentation")
	}
	if !ms.OutputCommitted() {
		t.Fatal("expected committed")
	}
	if ms.Attempts() != 1 {
		t.Fatalf("attempts=%d", ms.Attempts())
	}
}

func TestPostOutput_NoReplay(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeCommitThenFail}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "i3", FactoryKind: "fake",
		Negotiation:   backendplugin.Negotiation{Compatible: true},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, _ := inst.Resolve(context.Background(), nil)
	var invalidated bool
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID:           "i3",
		InvalidateGeneration: func() { invalidated = true },
	})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventTextDelta || !aipapi.OutputCommitted(ev) {
		t.Fatalf("expected committed text, got %+v", ev)
	}
	_, err = stream.Recv(context.Background())
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatal("expected post-output classified failure")
	}
	var ce *adapter.ClassifiedError
	if !errors.As(err, &ce) {
		t.Fatalf("%T %v", err, err)
	}
	if !ce.OutputCommitted || ce.Retryable {
		t.Fatalf("%+v", ce)
	}
	ms, ok := stream.(interface{ Attempts() int64 })
	if !ok {
		t.Fatalf("stream %T missing Attempts", stream)
	}
	if ms.Attempts() != 1 {
		t.Fatalf("host must not restart: attempts=%d", ms.Attempts())
	}
	if !invalidated {
		t.Fatal("expected generation invalidation callback")
	}
}

func TestUnadvertisedOptionalNil(t *testing.T) {
	t.Parallel()
	sess := &minimalSession{}
	profile := backendplugin.ResolvedProfile{
		Capabilities: backendplugin.CapabilitySummary{Streaming: true},
	}
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "m"})
	if br.Backend.ModelInventory != nil || br.Backend.ProviderCounter != nil || br.Backend.FinalizeBilling != nil {
		t.Fatal("unadvertised seams must stay nil")
	}
	if br.Backend.ResolveReplaySupport != nil {
		t.Fatal("replay seam only when advertised")
	}
}

func TestBuild_PooledLifecycleCallbacksRemainNil(t *testing.T) {
	t.Parallel()
	br := adapter.Build(minimalSession{}, backendplugin.ResolvedProfile{}, adapter.Options{InstanceID: "pool-compatible"})
	t.Cleanup(func() { _ = br.Cleanup() })

	tests := []struct {
		name  string
		isSet func(execbackend.Backend) bool
	}{
		{name: "Close", isSet: func(be execbackend.Backend) bool { return be.Close != nil }},
		{name: "Start", isSet: func(be execbackend.Backend) bool { return be.Start != nil }},
		{name: "Stop", isSet: func(be execbackend.Backend) bool { return be.Stop != nil }},
		{name: "CleanupIdleTransports", isSet: func(be execbackend.Backend) bool {
			return be.CleanupIdleTransports != nil
		}},
		{name: "PreflightCapability", isSet: func(be execbackend.Backend) bool {
			return be.PreflightCapability != nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.isSet(br.Backend) {
				t.Fatalf("adapter.Build returned %s callback; standard executable adapters must remain pool-compatible", tt.name)
			}
		})
	}
}

func TestInvocationFromCall_Validates(t *testing.T) {
	t.Parallel()
	inv, err := adapter.InvocationFromCall(testCall(), testCand(), backendplugin.Negotiation{})
	if err != nil {
		t.Fatal(err)
	}
	if inv.CanonicalModelID != "fake-model" || inv.BLegID != "fake:fake-model" {
		t.Fatalf("%+v", inv)
	}
	if len(inv.Messages) != 1 || inv.Messages[0].Parts[0].Text == nil {
		t.Fatalf("%+v", inv)
	}
}

func TestOrderedItemAuthorityCapabilityNoNetwork_rejectsOldMinorBeforeExecute(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		ID:         "req-ordered",
		Session:    aipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate},
		Items: []aipapi.Item{{
			Kind:    aipapi.ItemKindMessage,
			ID:      "msg-1",
			Status:  aipapi.ItemStatusCompleted,
			Role:    aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
		}},
	}
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "ord-old", FactoryKind: "fake",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
			EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
		},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "ord-old",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: 0,
		},
	})
	_, err = br.Backend.Open(context.Background(), call, testCand())
	if err == nil {
		t.Fatal("expected ABI rejection before Execute")
	}
	if fake.ExecuteCount.Load() != 0 {
		t.Fatalf("ExecuteCount=%d want 0", fake.ExecuteCount.Load())
	}
	_ = br.Cleanup()
}

func TestOrderedItemAuthorityCapabilityNoNetwork_executesViaAdapterSession(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		ID:         "req-ordered",
		Session:    aipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate},
		Items: []aipapi.Item{
			{Kind: aipapi.ItemKindItemReference, ID: "ref-1", Status: aipapi.ItemStatusCompleted, Reference: &aipapi.ItemReference{ID: "prev"}},
			{Kind: aipapi.ItemKindReasoning, ID: "rs-1", Status: aipapi.ItemStatusCompleted, Reasoning: &aipapi.ReasoningItem{Reasoning: &aipapi.ReasoningPart{
				Dialect: aipapi.ReasoningDialectOpenAIChatTextV1, Text: "chain",
			}}},
			{
				Kind: aipapi.ItemKindMessage, ID: "msg-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{
					{Kind: aipapi.ContentPartJSON, Text: `{"k":1}`},
					{Kind: aipapi.ContentPartToolResult, Text: "72F"},
				},
			},
		},
	}
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	neg := backendplugin.Negotiation{
		Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		EnabledFeatures: []string{backendplugin.FeatureOrderedItems},
	}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "ord-new", FactoryKind: "fake",
		Negotiation:   neg,
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "ord-new", Negotiation: neg})
	stream, err := br.Backend.Open(context.Background(), call, testCand())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	var sawText bool
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			sawText = true
		}
	}
	if fake.ExecuteCount.Load() != 1 {
		t.Fatalf("ExecuteCount=%d", fake.ExecuteCount.Load())
	}
	if !sawText {
		t.Fatal("expected text delta from fake Execute path")
	}
	if fake.LastStartInvocation == nil || !fake.LastStartInvocation.ItemAuthority {
		t.Fatal("expected ordered item invocation at Execute")
	}
	if fake.LastStartCall == nil || len(fake.LastStartCall.Items) != 3 {
		t.Fatalf("reconstructed call=%#v", fake.LastStartCall)
	}
	_ = br.Cleanup()
}

func TestOrderedItemAuthorityCapabilityNoNetwork_rejectsMinor2WithoutFeature(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{
		ID:         "req-ordered",
		Session:    aipapi.SessionRef{ALegID: "aleg-1"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, ID: "msg-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
		}},
	}
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "ord-m2", FactoryKind: "fake",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
			EnabledFeatures: nil,
		},
		RuntimePolicy: backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := inst.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(inst, profile, adapter.Options{
		InstanceID: "ord-m2",
		Negotiation: backendplugin.Negotiation{
			Compatible: true, NegotiatedMinor: backendplugin.ProtocolMinorOrderedItems,
		},
	})
	_, err = br.Backend.Open(context.Background(), call, testCand())
	if err == nil {
		t.Fatal("expected ABI rejection before Execute")
	}
	if fake.ExecuteCount.Load() != 0 {
		t.Fatalf("ExecuteCount=%d want 0", fake.ExecuteCount.Load())
	}
	_ = br.Cleanup()
}

type minimalSession struct{}

func (minimalSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (minimalSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}
func (minimalSession) Execute(backendplugin.ExecuteStream) error { return nil }
func (minimalSession) Close(context.Context) error               { return nil }

// capturingCounterSession records the exact count request the bridge sends to
// the connector boundary, so a payload that never leaves the adapter is visible.
type capturingCounterSession struct {
	minimalSession
	request backendplugin.CountTokensRequest
	calls   int
}

func (s *capturingCounterSession) CountTokens(_ context.Context, req backendplugin.CountTokensRequest) (backendplugin.CountTokensResponse, error) {
	s.request = req
	s.calls++
	n := int64(len(req.Invocation.Messages))
	return backendplugin.CountTokensResponse{
		InputTokens: &n,
		Presence:    backendplugin.UsagePresence{InputTokens: true},
	}, nil
}
