package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// toolHistoryCall is the two-turn assistant-call/tool-result trajectory that a
// coding harness replays on its second turn.
//
// The two carriers under proof are independent canonical forms:
//   - the assistant's JSON tool call keeps its call id and tool name, and
//   - the tool result keeps its literal text alongside its structured content.
func toolHistoryCall() aipapi.Call {
	call := testCall()
	call.Messages = []aipapi.Message{
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("find the widget")}},
		{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
			Kind:       aipapi.PartJSON,
			ToolCallID: "call_widget_1",
			ToolName:   "widget_lookup",
			Content:    json.RawMessage(`{"id":"w1"}`),
		}}},
		{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
			Kind:       aipapi.PartToolResult,
			ToolCallID: "call_widget_1",
			ToolName:   "widget_lookup",
			Text:       "the widget is available",
			Content:    json.RawMessage(`{"ok":true}`),
		}}},
		{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("summarize it")}},
	}
	return call
}

// TestToolHistoryBridge_PreservesCallIdentityAndResultText proves the shared
// host/SDK bridge delivers both canonical tool-history carriers unchanged.
//
// The assertion runs on the reconstructed canonical call, so a mapper that
// acknowledges the request while dropping either carrier cannot pass.
func TestToolHistoryBridge_PreservesCallIdentityAndResultText(t *testing.T) {
	t.Parallel()
	inv, err := adapter.InvocationFromCall(toolHistoryCall(), testCand(), backendplugin.Negotiation{})
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
	if len(out.Messages) != 4 {
		t.Fatalf("round-trip messages=%d, want 4", len(out.Messages))
	}
	callPart := out.Messages[1].Parts[0]
	if callPart.ToolCallID != "call_widget_1" || callPart.ToolName != "widget_lookup" {
		t.Errorf("tool-call identity lost: id=%q name=%q", callPart.ToolCallID, callPart.ToolName)
	}
	if string(callPart.Content) != `{"id":"w1"}` {
		t.Errorf("tool-call arguments changed: %s", callPart.Content)
	}
	resultPart := out.Messages[2].Parts[0]
	if resultPart.ToolCallID != "call_widget_1" {
		t.Errorf("tool-result correlation lost: id=%q", resultPart.ToolCallID)
	}
	if resultPart.Text != "the widget is available" {
		t.Errorf("tool-result text lost: %q", resultPart.Text)
	}
	if string(resultPart.Content) != `{"ok":true}` {
		t.Errorf("tool-result content changed: %s", resultPart.Content)
	}
}

// TestToolHistoryBridge_PreservesCanonicalPartMatrix is the #1393 acceptance
// matrix. Each case asserts the complete reconstructed message list, so a
// carrier cannot disappear and a position or identity cannot drift while the
// targeted single-fixture proof still passes.
func TestToolHistoryBridge_PreservesCanonicalPartMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		messages []aipapi.Message
	}{
		{
			name: "text-only tool result",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("find the widget")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
					Kind: aipapi.PartJSON, ToolCallID: "call_text", ToolName: "lookup",
					Content: json.RawMessage(`{"q":"widget"}`),
				}}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
					Kind: aipapi.PartToolResult, ToolCallID: "call_text", ToolName: "lookup",
					Text: "the widget is available",
				}}},
			},
		},
		{
			name: "content-only tool result",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("find the widget")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
					Kind: aipapi.PartJSON, ToolCallID: "call_json", ToolName: "lookup",
					Content: json.RawMessage(`{"q":"widget"}`),
				}}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
					Kind: aipapi.PartToolResult, ToolCallID: "call_json", ToolName: "lookup",
					Content: json.RawMessage(`{"ok":true}`),
				}}},
			},
		},
		{
			name: "both tool-result carriers",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("find the widget")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
					Kind: aipapi.PartJSON, ToolCallID: "call_both", ToolName: "lookup",
					Content: json.RawMessage(`{"q":"widget"}`),
				}}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
					Kind: aipapi.PartToolResult, ToolCallID: "call_both", ToolName: "lookup",
					Text: "the widget is available", Content: json.RawMessage(`{"ok":true}`),
				}}},
			},
		},
		{
			name: "ordinary JSON without call identity",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("what is this")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
					Kind: aipapi.PartJSON, Content: json.RawMessage(`{"note":"not a tool call"}`),
				}}},
			},
		},
		{
			name: "multiple calls with reverse-order interleaved results",
			messages: []aipapi.Message{
				{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("inspect both widgets")}},
				{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{
					{Kind: aipapi.PartJSON, ToolCallID: "call_a", ToolName: "lookup", Content: json.RawMessage(`{"q":"a"}`)},
					{Kind: aipapi.PartJSON, ToolCallID: "call_b", ToolName: "lookup", Content: json.RawMessage(`{"q":"b"}`)},
				}},
				{Role: aipapi.RoleTool, Parts: []aipapi.Part{
					{Kind: aipapi.PartToolResult, ToolCallID: "call_b", ToolName: "lookup", Text: "result b"},
					{Kind: aipapi.PartToolResult, ToolCallID: "call_a", ToolName: "lookup", Content: json.RawMessage(`{"result":"a"}`)},
				}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			call := toolHistoryCall()
			call.Messages = tc.messages
			if err := call.Validate(); err != nil {
				t.Fatalf("fixture is not canonical-valid: %v", err)
			}
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
			if !reflect.DeepEqual(out.Messages, tc.messages) {
				t.Fatalf("reconstructed messages drifted:\n got: %#v\nwant: %#v", out.Messages, tc.messages)
			}
		})
	}
}

// TestToolHistoryBridge_ReachesARealGRPCSession drives the same trajectory
// through a real gRPC plugin session and inspects the call the plugin
// reconstructed, so the proof covers the wire encoding too.
func TestToolHistoryBridge_ReachesARealGRPCSession(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: 0, DisableTransportRetries: true,
	}, fake))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})
	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sess, profile, err := adapter.DialConfiguredSession(
		ctx, conn, "tool-history", "fake", []byte("k: v\n"),
		backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true},
	)
	if err != nil {
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = sess.Close(closeCtx)
	})
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "tool-history"})
	stream, err := br.Backend.Open(ctx, toolHistoryCall(), testCand())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	for {
		_, err := stream.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if fake.LastStartCall == nil {
		t.Fatal("the plugin reconstructed no call")
	}
	got := fake.LastStartCall
	if len(got.Messages) != 4 {
		t.Fatalf("delivered messages=%d, want 4", len(got.Messages))
	}
	callPart := got.Messages[1].Parts[0]
	if callPart.ToolCallID != "call_widget_1" || callPart.ToolName != "widget_lookup" {
		t.Errorf("delivered tool-call identity lost: id=%q name=%q", callPart.ToolCallID, callPart.ToolName)
	}
	if string(callPart.Content) != `{"id":"w1"}` {
		t.Errorf("delivered tool-call arguments changed: %s", callPart.Content)
	}
	resultPart := got.Messages[2].Parts[0]
	if resultPart.ToolCallID != "call_widget_1" || resultPart.ToolName != "widget_lookup" {
		t.Errorf("delivered tool-result identity lost: id=%q name=%q", resultPart.ToolCallID, resultPart.ToolName)
	}
	if resultPart.Text != "the widget is available" {
		t.Errorf("delivered tool-result text lost: %q", resultPart.Text)
	}
	if string(resultPart.Content) != `{"ok":true}` {
		t.Errorf("delivered tool-result content changed: %s", resultPart.Content)
	}
}

// TestToolHistoryBridge_OversizedToolResultRejectedBeforeExecute proves the
// preserved text is checked against the executable transport bound before the
// plugin sees an Execute call. Canonical admission and the ABI ceiling are
// distinct, so this rejection must be deterministic and upstream-free.
func TestToolHistoryBridge_OversizedToolResultRejectedBeforeExecute(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	inst, err := fake.Configure(context.Background(), backendplugin.ConfigureRequest{
		InstanceID: "tool-history-oversized", FactoryKind: "fake",
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
	br := adapter.Build(inst, profile, adapter.Options{InstanceID: "tool-history-oversized"})
	t.Cleanup(func() { _ = br.Cleanup() })
	call := testCall()
	call.Messages = append(call.Messages, aipapi.Message{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
		Kind:       aipapi.PartToolResult,
		ToolCallID: "call_oversized",
		Text:       strings.Repeat("x", int(backendplugin.DefaultMaxMessageBytes)+1),
	}}})
	stream, err := br.Backend.Open(context.Background(), call, testCand())
	if err == nil {
		_ = stream.Close()
		t.Fatal("an over-ceiling tool-result text must be refused before Execute")
	}
	if fake.ExecuteCount.Load() != 0 {
		t.Fatalf("ExecuteCount=%d, want zero upstream visibility", fake.ExecuteCount.Load())
	}
}
