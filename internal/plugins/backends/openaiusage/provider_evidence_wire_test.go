package openaiusage_test

// Shared wire/evidence helpers and the intact-observation vector for the
// OpenAI usage suites — open-source half.
//
// The real-rater monetary proofs that the original file hosted (schema-free
// additive 123/0; frozen full-tariff typed overlap; child-only complete 74/0
// over four non-zero child lines) are preserved by task 3.5 in the Enterprise
// tree: enterprise/internal/preservation/openai_wire_test.go. The open-source
// half keeps the package-wide wire/evidence helpers and the intact
// aggregate+child observation assertions.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func TestOpenAIUsageEvidence_ChatAndResponsesWireAudioDirections(t *testing.T) {
	server := wireAudioServer(t)
	defer server.Close()

	chatObservations := openWireObservations(t, server, "chat", wireSyntheticIdentity("chat"))
	responsesObservations := openWireObservations(t, server, "responses", wireSyntheticIdentity("responses"))

	want := map[string]string{
		"input/audio_token":  "3",
		"output/audio_token": "4",
	}
	assertDirectionalAudio(t, chatObservations, want)
	assertDirectionalAudio(t, responsesObservations, want)
	assertWireEvidence(t, chatObservations, map[string]string{
		"$.usage.prompt_tokens_details.audio_tokens":     "3",
		"$.usage.completion_tokens_details.audio_tokens": "4",
	})
	assertWireEvidence(t, responsesObservations, map[string]string{
		"$.usage.input_tokens_details.audio_tokens":  "3",
		"$.usage.output_tokens_details.audio_tokens": "4",
	})
}

// TestOpenAIUsageEvidence_IntactNativeDetailsInclusionProof certifies that the
// intact provider observation (aggregate ordinary tokens AND directional native
// text/audio children) is the observation reaching the rater, never a
// test-rebuilt filtered measure slice.
//
// OpenAI's prompt_tokens/completion_tokens totals already include the
// prompt_tokens_details/completion_tokens_details (and Responses
// input_tokens_details/output_tokens_details) children. The rating response to
// an overlapping additive tariff (a disjoint native partition, or an explicit
// fail-closed overlap) is Enterprise-owned by task 3.5:
// enterprise/internal/preservation/openai_wire_test.go.
func TestOpenAIUsageEvidence_IntactNativeDetailsInclusionProof(t *testing.T) {
	server := wireAudioServer(t)
	defer server.Close()

	cases := []struct {
		label string
		path  string
	}{
		{label: "chat", path: "chat"},
		{label: "responses", path: "responses"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			billingCallID, err := usageevidence.NewBillingCallID()
			if err != nil {
				t.Fatalf("NewBillingCallID: %v", err)
			}
			callID := billingCallID.String()
			identity := coremetering.ObservationIdentity{
				StoreID: "store", RequestID: tc.label + "-request",
				CallID: callID, BillingCallID: callID,
				ALegID: "a-leg", BLegID: "b-leg", AttemptID: "b-leg",
			}
			observations := openWireObservations(t, server, tc.path, identity)
			if len(observations) != 1 {
				t.Fatalf("%s observations = %d, want one intact observation", tc.label, len(observations))
			}
			assertIntactAggregateAndChildren(t, tc.label, observations[0])
		})
	}
}

func wireAudioServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			_, _ = io.WriteString(w, `{"id":"chat-audio","object":"chat.completion","created":1715620000,"model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":9,"total_tokens":20,"prompt_tokens_details":{"text_tokens":8,"audio_tokens":3,"cached_tokens":0},"completion_tokens_details":{"text_tokens":5,"audio_tokens":4,"reasoning_tokens":0}}}`)
		case "/responses":
			_, _ = io.WriteString(w, `{"id":"resp-audio","object":"response","created_at":1715620000,"status":"completed","model":"gpt-test","output":[{"type":"message","id":"msg","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":11,"output_tokens":9,"total_tokens":20,"input_tokens_details":{"text_tokens":8,"audio_tokens":3,"images":0},"output_tokens_details":{"text_tokens":5,"audio_tokens":4,"images":0}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func openWireObservations(t *testing.T, server *httptest.Server, path string, identity coremetering.ObservationIdentity) []sdkmetering.Observation {
	t.Helper()
	call := aipapi.Call{
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}}}},
		Invocation: aipapi.Invocation{TransportMode: aipapi.TransportModeNonStreaming},
	}
	candidate := routing.AttemptCandidate{Primary: routing.Primary{Model: "gpt-test"}}
	client := openaicred.NewClient(server.URL, "sk-test", server.Client(), new(int))
	var (
		stream aipapi.ManagedEventStream
		err    error
	)
	switch path {
	case "chat":
		stream, err = openaicompat.OpenChat(context.Background(), client, openaicompat.InvokeRequest{
			ProviderID: "openai-chat-test", Call: call, Candidate: candidate,
		})
	case "responses":
		stream, err = openaicompat.OpenResponses(context.Background(), client, openaicompat.InvokeRequest{
			ProviderID: "openai-responses-test", Call: call, Candidate: candidate,
		})
	default:
		t.Fatalf("unknown wire path %q", path)
	}
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return drainBoundObservationsWithIdentity(t, stream, path, identity)
}

func wireSyntheticIdentity(label string) coremetering.ObservationIdentity {
	return coremetering.ObservationIdentity{
		StoreID: "store", RequestID: label + "-request", CallID: label + "-call",
		BillingCallID: label + "-billing", ALegID: label + "-a-leg", BLegID: label + "-b-leg", AttemptID: label + "-attempt",
	}
}

// assertIntactAggregateAndChildren proves the observation reaching the rater is
// the intact wire mapping: both the ordinary aggregate token measures and the
// directional native text/audio children are present. This is the property a
// filtered measure slice would mask.
func assertIntactAggregateAndChildren(t *testing.T, label string, observation sdkmetering.Observation) {
	t.Helper()
	seen := make(map[string]string)
	for _, measure := range observation.Measures {
		key := string(measure.Key.Direction) + "/" + measure.Key.Component
		if measure.Value == nil {
			t.Fatalf("%s measure %s has no value", label, key)
		}
		seen[key] = measure.Value.Coefficient
		if measure.Key.Component == sdkmetering.ComponentTextToken ||
			measure.Key.Component == sdkmetering.ComponentAudioToken {
			if measure.Key.SchemaID != "openai.usage.v2" {
				t.Fatalf("%s native child %s schema = %q, want openai.usage.v2", label, key, measure.Key.SchemaID)
			}
		}
	}
	want := map[string]string{
		"input/input_token":   "11",
		"output/output_token": "9",
		"input/text_token":    "8",
		"output/text_token":   "5",
		"input/audio_token":   "3",
		"output/audio_token":  "4",
	}
	for key, expected := range want {
		if seen[key] != expected {
			t.Fatalf("%s intact measure %s = %q, want %q (all=%v)", label, key, seen[key], expected, seen)
		}
	}
}

func wireComponentKey(direction sdkmetering.FlowDirection, component string) sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{Direction: direction, Component: component, Unit: sdkmetering.UnitToken, SchemaID: "openai.usage.v2"}
}

func wireDefaultKey(direction sdkmetering.FlowDirection, component string) sdkmetering.ComponentKey {
	return sdkmetering.ComponentKey{Direction: direction, Component: component, Unit: sdkmetering.UnitToken, SchemaID: sdkmetering.DefaultInclusionSchemaID}
}

func drainBoundObservationsWithIdentity(t *testing.T, stream aipapi.ManagedEventStream, label string, identity coremetering.ObservationIdentity) []sdkmetering.Observation {
	t.Helper()
	defer func() { _ = stream.Close() }()
	var events []aipapi.Event
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("%s Recv: %v", label, err)
		}
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatalf("%s returned no canonical events", label)
	}
	var usageEvents int
	for _, event := range events {
		if event.Kind == aipapi.EventUsageDelta {
			usageEvents++
		}
	}
	if usageEvents == 0 {
		t.Fatalf("%s returned no usage event: %+v", label, events)
	}
	binder, ok := stream.(coremetering.ProviderEvidenceBinder)
	if !ok {
		t.Fatalf("%s stream %T does not expose provider evidence binding", label, stream)
	}
	binder.BindEconomicEvidence(identity)
	source, ok := stream.(interface {
		DrainEconomicObservations() []sdkmetering.Observation
	})
	if !ok {
		t.Fatalf("%s stream %T does not expose provider evidence drain", label, stream)
	}
	observations := source.DrainEconomicObservations()
	if len(observations) != 1 {
		t.Fatalf("%s observations = %d, want one: %+v", label, len(observations), observations)
	}
	return observations
}

func assertDirectionalAudio(t *testing.T, observations []sdkmetering.Observation, want map[string]string) {
	t.Helper()
	got := make(map[string]string)
	for _, observation := range observations {
		for _, measure := range observation.Measures {
			if measure.Key.Component != sdkmetering.ComponentAudioToken {
				continue
			}
			key := string(measure.Key.Direction) + "/" + measure.Key.Component
			if _, exists := got[key]; exists {
				t.Fatalf("duplicate directional audio measure %q: %+v", key, observation.Measures)
			}
			if measure.Value == nil {
				t.Fatalf("directional audio measure %q has no value", key)
			}
			got[key] = measure.Value.Coefficient
		}
	}
	if len(got) != len(want) {
		t.Fatalf("directional audio measures = %v, want %v", got, want)
	}
	for key, expected := range want {
		if got[key] != expected {
			t.Errorf("directional audio measure %q = %q, want %q", key, got[key], expected)
		}
	}
}

func assertWireEvidence(t *testing.T, observations []sdkmetering.Observation, want map[string]string) {
	t.Helper()
	got := make(map[string]string, len(want))
	for _, observation := range observations {
		for _, field := range observation.Evidence {
			if field.Path == "$.usage.input_audio_tokens" || field.Path == "$.usage.output_audio_tokens" {
				t.Fatalf("wire nested audio evidence used synthetic canonical path %q", field.Path)
			}
			if _, wanted := want[field.Path]; wanted {
				if _, duplicate := got[field.Path]; duplicate {
					t.Fatalf("duplicate wire evidence path %q", field.Path)
				}
				got[field.Path] = field.Lexeme
			}
		}
	}
	if len(got) != len(want) {
		t.Fatalf("wire evidence paths = %v, want %v", got, want)
	}
	for path, expected := range want {
		if got[path] != expected {
			t.Errorf("wire evidence %q = %q, want %q", path, got[path], expected)
		}
	}
}
