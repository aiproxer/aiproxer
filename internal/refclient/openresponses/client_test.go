package openresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func newTestClient(t *testing.T, baseURL string) *Client {
	t.Helper()
	return New(Config{BaseURL: baseURL, APIKey: "sk-test", Clock: NewClock(time.Time{})})
}

// clientScenarioCases registers HTTP JSON/SSE/compact client scenario cases.
func clientScenarioCases() []scenarioCase {
	return []scenarioCase{
		{
			id:          "scenario-client-json-create",
			kind:        ScenarioJSONText,
			fixture:     "response_text.json",
			description: "HTTP JSON create returns the pinned response resource with required presence.",
			run: func(t *testing.T) {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				data := mustReadScenario(t, "response_text.json")
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
						http.NotFound(w, r)
						return
					}
					if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
						t.Errorf("authorization: %q", got)
					}
					body, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(body), `"model":"gpt-openresponses-1"`) && !strings.Contains(string(body), `"model": "gpt-openresponses-1"`) {
						t.Errorf("request missing model: %s", body)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(data)
				})
				cli := newTestClient(t, srv.URL)
				res, err := cli.Create(context.Background(), CreateParams{
					Model: "gpt-openresponses-1",
					Input: Input{Text: "what's the weather?"},
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.Status != "completed" || res.ID != "resp_5a3e04d550c84a63a1d4fc4e3e206abb" {
					t.Fatalf("response: %+v", res)
				}
				if cli.RequestCount() != 1 {
					t.Fatalf("request count: %d", cli.RequestCount())
				}
				if !cli.LastRequest().Redacted {
					t.Fatal("last request must redact authorization")
				}
			},
		},
		{
			id:          "scenario-client-sse-create",
			kind:        ScenarioSSEText,
			fixture:     "stream_text.sse",
			description: "HTTP SSE create streams events and returns the terminal response.",
			run: func(t *testing.T) {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				data := mustReadScenario(t, "stream_text.sse")
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					if !strings.HasSuffix(r.URL.Path, "/responses") {
						http.NotFound(w, r)
						return
					}
					body, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(body), `"stream":true`) {
						t.Errorf("stream must be true: %s", body)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(data)
				})
				cli := newTestClient(t, srv.URL)
				var types []string
				terminal, err := cli.CreateStream(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}, func(evt Event) error {
					types = append(types, evt.Type)
					return nil
				})
				if err != nil {
					t.Fatalf("CreateStream: %v", err)
				}
				if len(types) != 9 {
					t.Fatalf("event count: %d", len(types))
				}
				if types[len(types)-1] != "response.completed" {
					t.Fatalf("terminal event: %q", types[len(types)-1])
				}
				if terminal == nil || terminal.ID != "resp_stream_1" {
					t.Fatalf("terminal response: %+v", terminal)
				}
			},
		},
		{
			id:          "scenario-client-compact",
			kind:        ScenarioCompaction,
			fixture:     "compact_resource.json",
			description: "Standalone compact returns the response.compaction resource.",
			run: func(t *testing.T) {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				data := mustReadScenario(t, "compact_resource.json")
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					if !strings.HasSuffix(r.URL.Path, "/responses/compact") {
						http.NotFound(w, r)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(data)
				})
				cli := newTestClient(t, srv.URL)
				res, err := cli.Compact(context.Background(), CompactParams{Model: "m", Input: Input{Items: []Item{NewMessageItem("user", "input_text", "compact me")}}})
				if err != nil {
					t.Fatalf("Compact: %v", err)
				}
				if !res.IsCompact() || res.ID != "resp_compact_1" {
					t.Fatalf("compact: %+v", res)
				}
			},
		},
		{
			id:          "scenario-client-tools",
			kind:        ScenarioTools,
			fixture:     "response_tools.json",
			description: "Tool declaration and function_call/function_call_output round-trip through HTTP JSON create.",
			run: func(t *testing.T) {
				t.Helper()
				data := mustReadScenario(t, "response_tools.json")
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(body), `"name":"get_weather"`) || !strings.Contains(string(body), `"parameters"`) {
						t.Errorf("tools missing from request: %s", body)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(data)
				})
				cli := newTestClient(t, srv.URL)
				res, err := cli.Create(context.Background(), CreateParams{
					Model: "gpt-openresponses-1",
					Input: Input{Items: []Item{NewMessageItem("user", "input_text", "weather?")}},
					Tools: []Tool{{Type: "function", Name: "get_weather", Description: "get weather", Parameters: json.RawMessage(`{"type":"object"}`)}},
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.Output[0].Type != string(ItemFunctionCall) || res.Output[0].Name != "get_weather" {
					t.Fatalf("tool output: %+v", res.Output)
				}
			},
		},
		{
			id:          "scenario-client-multimodal",
			kind:        ScenarioMultimodal,
			fixture:     "request_multimodal.json",
			description: "Multimodal input (image and file) marshals into the create request.",
			run: func(t *testing.T) {
				t.Helper()
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					if !strings.Contains(string(body), "input_image") || !strings.Contains(string(body), "input_file") {
						t.Errorf("multimodal parts missing: %s", body)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(mustReadScenario(t, "response_text.json"))
				})
				cli := newTestClient(t, srv.URL)
				params := CreateParams{Model: "gpt-openresponses-1", Input: Input{Items: []Item{
					{
						Type: "message",
						Role: "user",
						Content: []ContentPart{
							{Type: "input_text", Text: "see this"},
							{Type: "input_image", ImageURL: json.RawMessage(`{"data":"aGVsbG8=","media_type":"image/png"}`)},
							{Type: "input_file", FileURL: json.RawMessage(`{"file_id":"f1"}`)},
						},
					},
				}}}
				if _, err := cli.Create(context.Background(), params); err != nil {
					t.Fatalf("Create: %v", err)
				}
			},
		},
		{
			id:          "scenario-client-reasoning",
			kind:        ScenarioReasoning,
			fixture:     "response_reasoning.json",
			description: "Reasoning item lifecycle parses through the HTTP JSON client.",
			run: func(t *testing.T) {
				t.Helper()
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(mustReadScenario(t, "response_reasoning.json"))
				})
				cli := newTestClient(t, srv.URL)
				res, err := cli.Create(context.Background(), CreateParams{Model: "m", Input: Input{Text: "code word?"}})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.Output[0].Type != "reasoning" || res.Output[0].Reasoning == nil {
					t.Fatalf("reasoning: %+v", res.Output[0])
				}
				if res.Usage.ReasoningTokens != 6 {
					t.Fatalf("reasoning tokens: %d", res.Usage.ReasoningTokens)
				}
			},
		},
		{
			id:          "scenario-client-phase",
			kind:        ScenarioPhase,
			fixture:     "response_phase.json",
			description: "Assistant phase labels survive HTTP JSON create.",
			run: func(t *testing.T) {
				t.Helper()
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(mustReadScenario(t, "response_phase.json"))
				})
				cli := newTestClient(t, srv.URL)
				res, err := cli.Create(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.Output[0].Phase != "commentary" || res.Output[1].Phase != "final_answer" {
					t.Fatalf("phase: %+v", res.Output)
				}
			},
		},
		{
			id:          "scenario-client-extensions",
			kind:        ScenarioExtensions,
			fixture:     "response_extensions.json",
			description: "Prefixed extension items parse opaquely through the HTTP JSON client.",
			run: func(t *testing.T) {
				t.Helper()
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(mustReadScenario(t, "response_extensions.json"))
				})
				cli := newTestClient(t, srv.URL)
				res, err := cli.Create(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.Output[0].Type != "acme:telemetry_chunk" || !res.Output[0].IsExtension() {
					t.Fatalf("extension: %+v", res.Output[0])
				}
			},
		},
		{
			id:          "scenario-client-error",
			kind:        ScenarioNegative,
			fixture:     "response_error.json",
			description: "Non-2xx HTTP responses map to structured client errors.",
			run: func(t *testing.T) {
				t.Helper()
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"type":"invalid_request","code":"model_not_found","message":"bad model","param":"model"}}`))
				})
				cli := newTestClient(t, srv.URL)
				_, err := cli.Create(context.Background(), CreateParams{Model: "fake-model", Input: Input{Text: "hi"}})
				if err == nil {
					t.Fatal("expected HTTP error")
				}
				httpErr, ok := err.(*HTTPError)
				if !ok {
					t.Fatalf("expected HTTPError, got %T: %v", err, err)
				}
				if httpErr.StatusCode != http.StatusBadRequest || httpErr.ErrorObject == nil || httpErr.ErrorObject.Code != "model_not_found" {
					t.Fatalf("http error: %+v", httpErr)
				}
			},
		},
		{
			id:          "scenario-client-cancellation",
			kind:        ScenarioNegative,
			fixture:     "response_text.json",
			description: "Cancelled context aborts the create before the server completes.",
			run: func(t *testing.T) {
				t.Helper()
				block := make(chan struct{})
				defer close(block)
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					<-r.Context().Done()
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(mustReadScenario(t, "response_text.json"))
				})
				cli := newTestClient(t, srv.URL)
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if _, err := cli.Create(ctx, CreateParams{Model: "m", Input: Input{Text: "hi"}}); err == nil {
					t.Fatal("expected cancellation error")
				}
			},
		},
	}
}

// compactScenarioCases registers the compact scenario cases.
func compactScenarioCases() []scenarioCase {
	return []scenarioCase{
		{
			id:          "scenario-compact-missing-model",
			kind:        ScenarioNegative,
			fixture:     "compact_resource.json",
			description: "Compact without model is still issued; server may reject with 4xx.",
			run: func(t *testing.T) {
				t.Helper()
				srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					var m map[string]json.RawMessage
					_ = json.Unmarshal(body, &m)
					if _, ok := m["model"]; ok {
						t.Errorf("model should be omitted: %s", body)
					}
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"error":{"type":"invalid_request","code":"model_required","message":"model is required","param":"model"}}`))
				})
				cli := newTestClient(t, srv.URL)
				_, err := cli.Compact(context.Background(), CompactParams{Input: Input{Items: []Item{NewMessageItem("user", "input_text", "compact me")}}})
				if err == nil {
					t.Fatal("expected 422 for missing model")
				}
				httpErr, ok := err.(*HTTPError)
				if !ok || httpErr.StatusCode != http.StatusUnprocessableEntity {
					t.Fatalf("expected 422 HTTPError, got %v", err)
				}
			},
		},
	}
}

// TestClient_SlowConsumer verifies the slow-consumer mode keeps events in order.
func TestClient_SlowConsumer(t *testing.T) {
	t.Parallel()
	data := mustReadScenario(t, "stream_text.sse")
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(data)
	})
	cli := New(Config{
		BaseURL:           srv.URL,
		APIKey:            "sk-test",
		SlowConsumerDelay: 2 * time.Millisecond,
	})
	start := time.Now()
	var count atomic.Int32
	_, err := cli.CreateStream(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}, func(evt Event) error {
		count.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("CreateStream: %v", err)
	}
	if count.Load() != 9 {
		t.Fatalf("events: %d", count.Load())
	}
	if time.Since(start) < 9*2*time.Millisecond {
		t.Fatalf("slow-consumer delay not honored")
	}
}

// TestClient_SlowConsumerCancelled verifies a slow consumer can be cancelled mid-stream.
func TestClient_SlowConsumerCancelled(t *testing.T) {
	t.Parallel()
	data := mustReadScenario(t, "stream_text.sse")
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(data)
	})
	cli := New(Config{BaseURL: srv.URL, APIKey: "sk-test", SlowConsumerDelay: 10 * time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err := cli.CreateStream(ctx, CreateParams{Model: "m", Input: Input{Text: "hi"}}, func(evt Event) error { return nil })
	if err == nil {
		t.Fatal("expected cancellation/context error")
	}
}

// TestClient_HandlerErrorPropagates verifies handler errors abort streaming.
func TestClient_HandlerErrorPropagates(t *testing.T) {
	t.Parallel()
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(mustReadScenario(t, "stream_text.sse"))
	})
	cli := newTestClient(t, srv.URL)
	sentinel := fmt.Errorf("handler boom")
	_, err := cli.CreateStream(context.Background(), CreateParams{Model: "m", Input: Input{Text: "hi"}}, func(evt Event) error {
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("expected handler error, got %v", err)
	}
}

// TestClient_BaseURLValidation verifies config rejects unsafe base URLs.
func TestClient_BaseURLValidation(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"", "ftp://host", "https://", "https://user:pass@host/x", "not a url://"} {
		if err := validBaseURL(bad); err == nil {
			t.Errorf("expected error for base URL %q", bad)
		}
	}
	for _, good := range []string{"http://127.0.0.1:8080", "https://example.com"} {
		if err := validBaseURL(good); err != nil {
			t.Errorf("unexpected error for %q: %v", good, err)
		}
	}
}

// TestClient_OnRequestObservation verifies raw request capture with redaction.
func TestClient_OnRequestObservation(t *testing.T) {
	t.Parallel()
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(mustReadScenario(t, "response_text.json"))
	})
	var obs RequestObservation
	cli := New(Config{BaseURL: srv.URL, APIKey: "sk-secret", OnRequest: func(o RequestObservation) { obs = o }})
	if _, err := cli.Create(context.Background(), CreateParams{Model: "m", Input: Input{Text: "x"}}); err != nil {
		t.Fatal(err)
	}
	if obs.Method != http.MethodPost || !strings.HasSuffix(obs.URLPath, "/responses") {
		t.Fatalf("observation: %+v", obs)
	}
	if obs.Headers.Get("Authorization") != "[REDACTED]" {
		t.Fatalf("authorization not redacted: %+v", obs.Headers)
	}
	if !obs.Redacted || len(obs.Body) == 0 {
		t.Fatalf("observation redacted/body: %+v", obs)
	}
}

// TestFixtureDigestConstants assert the pinned digest constants used by fixtures_test.
func TestFixtureDigestConstants(t *testing.T) {
	t.Parallel()
	if OfficialParamFixtureDigest == "" || OfficialResourceFixtureDigest == "" {
		t.Fatal("digest constants must not be empty")
	}
	if !strings.HasPrefix(OfficialParamFixtureDigest, "sha256:") {
		t.Fatalf("param digest prefix: %q", OfficialParamFixtureDigest)
	}
}

// ensure unused import guard: os is used by mustReadScenario in response_test.go.
var _ = os.Getenv
