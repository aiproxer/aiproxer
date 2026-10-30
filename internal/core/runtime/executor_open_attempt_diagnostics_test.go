package runtime_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestExecutor_decisionLog_backendAttemptOpened_includesInvocationMetadata(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Log = log
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIChatCompletions,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:gpt-4o-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIChatCompletions,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}

	entry := findDecisionLogEntry(t, buf.Bytes(), "backend_attempt_opened")
	assertLogField(t, entry, "operation", string(aipapi.OperationOpenAIChatCompletions))
	assertLogField(t, entry, "client_delivery_mode", string(aipapi.DeliveryModeNonStreaming))
	assertLogField(t, entry, "upstream_transport_mode", string(aipapi.TransportModeNonStreaming))
	assertLogField(t, entry, "backend", "stub")
	assertLogField(t, entry, "model", "gpt-4o-mini")
	assertLogFieldPresent(t, entry, "candidate_key")
	assertLogFieldPresent(t, entry, "open_duration_ms")
	assertLogFieldPresent(t, entry, "trace_id")
	assertLogFieldPresent(t, entry, "a_leg_id")
}

func TestExecutor_decisionLog_backendAttemptOpened_streamingDelivery(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}

	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Log = log
	ex.Rand = routing.NewSeededRng(2)
	ex.Backends = map[string]execbackend.Backend{
		"stub": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			TransportCaps: aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
				Operation: aipapi.OperationOpenAIResponses,
				Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming},
			}),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "stub:o3-mini"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIResponses,
			DeliveryMode: aipapi.DeliveryModeStreaming,
		},
	}

	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatal(err)
	}

	entry := findDecisionLogEntry(t, buf.Bytes(), "backend_attempt_opened")
	assertLogField(t, entry, "operation", string(aipapi.OperationOpenAIResponses))
	assertLogField(t, entry, "client_delivery_mode", string(aipapi.DeliveryModeStreaming))
	assertLogField(t, entry, "upstream_transport_mode", string(aipapi.TransportModeStreaming))
}

func findDecisionLogEntry(t *testing.T, raw []byte, msg string) map[string]any {
	t.Helper()
	for line := range bytes.SplitSeq(raw, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			continue
		}
		if m["msg"] == msg {
			return m
		}
	}
	t.Fatalf("no structured %q log entry: %s", msg, string(raw))
	return nil
}

func assertLogField(t *testing.T, entry map[string]any, key, want string) {
	t.Helper()
	got, ok := entry[key]
	if !ok {
		t.Fatalf("log missing %q: %#v", key, entry)
	}
	gotStr, ok := got.(string)
	if !ok {
		t.Fatalf("log field %q = %#v, want string %q", key, got, want)
	}
	if gotStr != want {
		t.Fatalf("log field %q = %q, want %q", key, gotStr, want)
	}
}

func assertLogFieldPresent(t *testing.T, entry map[string]any, key string) {
	t.Helper()
	if _, ok := entry[key]; !ok {
		t.Fatalf("log missing %q: %#v", key, entry)
	}
}
