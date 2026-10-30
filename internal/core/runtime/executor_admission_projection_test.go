package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_projectionOnlyRejectBeforeOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"legacy": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return nil, errors.New("should not open")
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "legacy:g"},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindItemReference, ID: "ref-1", Status: aipapi.ItemStatusCompleted,
			Reference: &aipapi.ItemReference{ID: "msg-prev"},
		}},
	}
	_, err = ex.Execute(context.Background(), call)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&opens) != 0 {
		t.Fatalf("backend opened despite admission reject, opens=%d", opens)
	}
}

func TestExecutor_nativeOrderedAdmissionBeforeOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var opens int32
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"ordered": {
			Caps: aipapi.NewBackendCaps(
				aipapi.CapabilityStreaming,
				aipapi.CapabilityOrderedItems,
				aipapi.CapabilityItemReferences,
			),
			DialectSupport: aipapi.DialectSupport{
				ItemDialects: []aipapi.DialectRequirement{{Kind: "item", Dialect: "item_reference"}},
			},
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				atomic.AddInt32(&opens, 1)
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "ordered:g"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindItemReference, ID: "ref-1", Status: aipapi.ItemStatusCompleted,
			Reference: &aipapi.ItemReference{ID: "msg-prev"},
		}},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	if atomic.LoadInt32(&opens) != 1 {
		t.Fatalf("expected one backend open for native ordered candidate, opens=%d", opens)
	}
}

func TestExecutor_legacyProjectionDeliveredAtOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotCall aipapi.Call
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"ordered": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityOrderedItems, aipapi.CapabilityTools),
			DialectSupport: aipapi.DialectSupport{
				ItemDialects: []aipapi.DialectRequirement{{Kind: "item", Dialect: "message"}},
			},
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotCall = call
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "ordered:g"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind: aipapi.PartJSON, ToolCallID: "call_1", ToolName: "weather", Content: []byte(`{"city":"SF"}`),
			}}},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	if len(gotCall.Messages) != 0 {
		t.Fatalf("expected ordered view at Open, still has messages=%#v", gotCall.Messages)
	}
	if len(gotCall.Items) != 2 {
		t.Fatalf("projected items=%#v", gotCall.Items)
	}
	if gotCall.Items[1].Kind != aipapi.ItemKindToolCall || gotCall.Items[1].ToolCall.CallID != "call_1" {
		t.Fatalf("projected items=%#v", gotCall.Items)
	}
}

func TestExecutor_itemAuthorityLegacyProjectionDeliveredAtOpen_NoNetwork(t *testing.T) {
	t.Parallel()

	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var gotCall aipapi.Call
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"legacy": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(_ context.Context, call aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				gotCall = call
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "legacy:g"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Items: []aipapi.Item{
			{
				Kind: aipapi.ItemKindMessage, ID: "m1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
				Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hello"}},
			},
			{
				Kind: aipapi.ItemKindToolCall, ID: "tc1", Status: aipapi.ItemStatusCompleted,
				ToolCall: &aipapi.ToolCallItem{CallID: "c1", Name: "weather", Arguments: []byte(`{"city":"SF"}`)},
			},
		},
	}
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if stream != nil {
		_, _ = aipapi.Collect(context.Background(), stream)
	}
	if len(gotCall.Items) != 0 {
		t.Fatalf("expected legacy messages at Open, still has items=%#v", gotCall.Items)
	}
	if len(gotCall.Messages) != 2 {
		t.Fatalf("projected messages=%#v", gotCall.Messages)
	}
	if gotCall.Messages[1].Parts[0].ToolCallID != "c1" {
		t.Fatalf("projected tool call=%#v", gotCall.Messages[1].Parts[0])
	}
}
