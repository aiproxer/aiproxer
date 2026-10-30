package openresponses

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	refclient "github.com/aiproxer/aiproxer/internal/refclient/openresponses"
)

// readRefClientFixture reads immutable official/scenario fixture bytes from the
// independent reference client's testdata tree (data sharing only, no code).
func readRefClientFixture(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../refclient/openresponses/testdata/" + rel)
	if err != nil {
		t.Fatalf("read refclient fixture %s: %v", rel, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		t.Fatalf("refclient fixture %s must not be empty", rel)
	}
	return b
}

func directClient(t *testing.T, baseURL string) *refclient.Client {
	t.Helper()
	return refclient.New(refclient.Config{BaseURL: baseURL, APIKey: "sk-test", Clock: refclient.NewClock(time.Time{})})
}

// directWireCase pins a declarative direct-wire scenario ID to a live test.
type directWireCase struct {
	id          string
	description string
	run         func(t *testing.T)
}

func directWireCases() []directWireCase {
	return []directWireCase{
		{
			id: "scenario-official-json", description: "refclient parses the pinned official response resource served verbatim",
			run: func(t *testing.T) {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				fixture := readRefClientFixture(t, "official_examples/ResponseResource.json")
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-official-json", Description: "official json", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "gpt-4o-2024-06-13", ContentType: "application/json"},
					RawBody:  fixture,
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Create(context.Background(), refclient.CreateParams{
					Model: "gpt-4o-2024-06-13",
					Input: refclient.Input{Text: "describe"},
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.ID != "resp_5a3e04d550c84a63a1d4fc4e3e206abb" || res.Status != "completed" {
					t.Fatalf("resource: %+v", res)
				}
				if res.Usage.TotalTokens != 67 {
					t.Fatalf("usage: %+v", res.Usage)
				}
				if got := res.OutputText(); !strings.Contains(got, "response object") {
					t.Fatalf("output text: %q", got)
				}
			},
		},
		{
			id: "scenario-official-sse", description: "refclient streams the pinned official SSE fixture verbatim",
			run: func(t *testing.T) {
				t.Helper()
				t.Helper()
				t.Helper()
				t.Helper()
				fixture := readRefClientFixture(t, "scenarios/stream_text.sse")
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-official-sse", Description: "official sse", Mode: ModeSSE,
					Expected: ExpectedRequest{Model: "gpt-openresponses-1"},
					RawBody:  fixture,
				})
				cli := directClient(t, ts.URL)
				var types []string
				terminal, err := cli.CreateStream(context.Background(), refclient.CreateParams{
					Model: "gpt-openresponses-1",
					Input: refclient.Input{Text: "hi"},
				}, func(evt refclient.Event) error {
					types = append(types, evt.Type)
					return nil
				})
				if err != nil {
					t.Fatalf("CreateStream: %v", err)
				}
				if len(types) != 9 {
					t.Fatalf("events: %d", len(types))
				}
				if terminal == nil || terminal.ID != "resp_stream_1" || terminal.Status != "completed" {
					t.Fatalf("terminal: %+v", terminal)
				}
			},
		},
		{
			id: "scenario-official-compact", description: "refclient parses the pinned official compaction resource",
			run: func(t *testing.T) {
				t.Helper()
				fixture := readRefClientFixture(t, "scenarios/compact_resource.json")
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-official-compact", Description: "official compact", Mode: ModeCompact,
					Expected: ExpectedRequest{Model: "gpt-openresponses-1", MinInputItems: 1},
					RawBody:  fixture,
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Compact(context.Background(), refclient.CompactParams{
					Model: "gpt-openresponses-1",
					Input: refclient.Input{Items: []refclient.Item{refclient.NewMessageItem("user", "input_text", "compress")}},
				})
				if err != nil {
					t.Fatalf("Compact: %v", err)
				}
				if !res.IsCompact() || res.ID != "resp_compact_1" || res.Usage.TotalTokens != 25 {
					t.Fatalf("compact: %+v", res)
				}
			},
		},
		{
			id: "scenario-json-text", description: "refclient parses a refbackend-built text resource with required presence",
			run: func(t *testing.T) {
				srv, ts := startServer(t, Options{}, &Script{
					ID: "scenario-json-text", Description: "built json text", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "gpt-openresponses-1", MinInputItems: 1},
					Resource: NewResource("resp_direct_json", "gpt-openresponses-1", 1719900000, []Item{
						NewMessageItem("assistant", "output_text", "Hello from the emulator"),
					}).WithUsage(Usage{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}),
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Create(context.Background(), refclient.CreateParams{
					Model: "gpt-openresponses-1",
					Input: refclient.Input{Items: []refclient.Item{refclient.NewMessageItem("user", "input_text", "hi")}},
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.ID != "resp_direct_json" || res.Status != "completed" {
					t.Fatalf("resource: %+v", res)
				}
				if got := res.OutputText(); got != "Hello from the emulator" {
					t.Fatalf("output text: %q", got)
				}
				if res.Usage.TotalTokens != 5 || res.Usage.InputTokens != 3 {
					t.Fatalf("usage: %+v", res.Usage)
				}
				if srv.Capture().Total() != 1 || srv.MismatchCount() != 0 {
					t.Fatalf("capture: total=%d mismatch=%d", srv.Capture().Total(), srv.MismatchCount())
				}
				obs, ok := srv.Capture().Last()
				if !ok || !obs.Redacted || obs.Headers.Get("Authorization") != RedactedAuthorization {
					t.Fatalf("authorization must be redacted: %+v", obs)
				}
			},
		},
		{
			id: "scenario-sse-text", description: "refclient consumes a refbackend-built lifecycle stream",
			run: func(t *testing.T) {
				srv, ts := startServer(t, Options{}, &Script{
					ID: "scenario-sse-text", Description: "built sse text", Mode: ModeSSE,
					Expected: ExpectedRequest{Model: "gpt-openresponses-1", MinInputItems: 1},
					Resource: NewResource("resp_direct_sse", "gpt-openresponses-1", 1719900600, []Item{
						NewMessagePartsItem("assistant", "", NewTextPart("streaming from emulator")),
					}),
				})
				cli := directClient(t, ts.URL)
				var types []string
				terminal, err := cli.CreateStream(context.Background(), refclient.CreateParams{
					Model: "gpt-openresponses-1",
					Input: refclient.Input{Items: []refclient.Item{refclient.NewMessageItem("user", "input_text", "hi")}},
				}, func(evt refclient.Event) error {
					types = append(types, evt.Type)
					return nil
				})
				if err != nil {
					t.Fatalf("CreateStream: %v", err)
				}
				if len(types) != 8 {
					t.Fatalf("events: %d", len(types))
				}
				if terminal == nil || terminal.ID != "resp_direct_sse" || terminal.Status != "completed" {
					t.Fatalf("terminal: %+v", terminal)
				}
				if got := terminal.OutputText(); got != "streaming from emulator" {
					t.Fatalf("output text: %q", got)
				}
				if srv.MismatchCount() != 0 {
					t.Fatalf("mismatch: %d", srv.MismatchCount())
				}
			},
		},
		{
			id: "scenario-compact", description: "refclient compacts against a refbackend-built compaction resource",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-compact", Description: "built compact", Mode: ModeCompact,
					Expected: ExpectedRequest{Model: "gpt-openresponses-1", MinInputItems: 1},
					CompactResource: NewCompactResource("resp_compact_direct", "gpt-openresponses-1", 1719900400, []Item{
						NewCompactionItem("cmp_direct_1", ""),
					}),
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Compact(context.Background(), refclient.CompactParams{
					Model: "gpt-openresponses-1",
					Input: refclient.Input{Items: []refclient.Item{refclient.NewMessageItem("user", "input_text", "compress")}},
				})
				if err != nil {
					t.Fatalf("Compact: %v", err)
				}
				if !res.IsCompact() || res.ID != "resp_compact_direct" || len(res.Output) != 1 || res.Output[0].Type != "compaction" {
					t.Fatalf("compact: %+v", res)
				}
			},
		},
		{
			id: "scenario-ws-basic", description: "refclient runs a direct WebSocket turn",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-ws-basic", Description: "direct ws basic", Mode: ModeWebSocket,
					Expected: ExpectedRequest{Model: "gpt-openresponses-1"},
					Resource: NewResource("resp_ws_direct", "gpt-openresponses-1", 1719901000, []Item{
						NewMessagePartsItem("assistant", "", NewTextPart("ws hello")),
					}),
				})
				sess, err := refclient.Dial(context.Background(), refclient.WSDialOptions{BaseURL: ts.URL, APIKey: "sk-test"})
				if err != nil {
					t.Fatalf("Dial: %v", err)
				}
				defer func() { _ = sess.Close() }()
				turn, err := sess.Turn(context.Background(), refclient.CreateParams{Model: "gpt-openresponses-1", Input: refclient.Input{Text: "hi"}})
				if err != nil {
					t.Fatalf("Turn: %v", err)
				}
				if turn.Response == nil || turn.Response.ID != "resp_ws_direct" {
					t.Fatalf("turn response: %+v", turn.Response)
				}
				if turn.Error != nil {
					t.Fatalf("unexpected error: %+v", turn.Error)
				}
				if len(turn.Events) == 0 || turn.Events[len(turn.Events)-1].Type != "response.completed" {
					t.Fatalf("events: %+v", turn.Events)
				}
			},
		},
		{
			id: "scenario-ws-sequential", description: "refclient runs sequential direct WebSocket turns",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-ws-sequential", Description: "direct ws sequential", Mode: ModeWebSocket,
					Expected: ExpectedRequest{Model: "m"},
					Resource: NewResource("resp_ws_seq", "m", 1719901000, []Item{NewMessagePartsItem("assistant", "", NewTextPart("turn"))}),
				})
				sess, err := refclient.Dial(context.Background(), refclient.WSDialOptions{BaseURL: ts.URL, APIKey: "sk-test"})
				if err != nil {
					t.Fatalf("Dial: %v", err)
				}
				defer func() { _ = sess.Close() }()
				for i := range 2 {
					turn, err := sess.Turn(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "go"}})
					if err != nil {
						t.Fatalf("Turn %d: %v", i, err)
					}
					if turn.Response == nil || turn.Response.ID != "resp_ws_seq" {
						t.Fatalf("turn %d response: %+v", i, turn.Response)
					}
				}
			},
		},
		{
			id: "scenario-ws-continuation", description: "refclient continuation reaches the emulator with previous_response_id",
			run: func(t *testing.T) {
				srv, ts := startServer(t, Options{}, &Script{
					ID: "scenario-ws-continuation", Description: "direct ws continuation", Mode: ModeWebSocket,
					Expected: ExpectedRequest{Model: "m", Contains: []string{`"previous_response_id":"resp_prev"`}},
					Resource: NewResource("resp_ws_cont", "m", 1719901000, []Item{NewMessagePartsItem("assistant", "", NewTextPart("cont"))}),
				})
				sess, err := refclient.Dial(context.Background(), refclient.WSDialOptions{BaseURL: ts.URL, APIKey: "sk-test"})
				if err != nil {
					t.Fatalf("Dial: %v", err)
				}
				defer func() { _ = sess.Close() }()
				prev := "resp_prev"
				turn, err := sess.Turn(context.Background(), refclient.CreateParams{
					Model: "m", Input: refclient.Input{Text: "next"},
					Store:              new(false),
					PreviousResponseID: &prev,
				})
				if err != nil {
					t.Fatalf("Turn: %v", err)
				}
				if turn.Response == nil || turn.Response.ID != "resp_ws_cont" {
					t.Fatalf("turn response: %+v", turn.Response)
				}
				if srv.MismatchCount() != 0 {
					t.Fatalf("mismatch: %d", srv.MismatchCount())
				}
			},
		},
		{
			id: "scenario-tools", description: "refclient round-trips function_call and function_call_output items",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-tools", Description: "direct tools", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "m", MinInputItems: 1, RequireTools: 1},
					Resource: NewResource("resp_tools_direct", "m", 1, []Item{
						NewFunctionCallItem("fc_1", "call_1", "get_weather", `{"city":"paris"}`),
						NewFunctionCallOutputItem("call_1", "22c"),
					}).WithUsage(Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}),
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Create(context.Background(), refclient.CreateParams{
					Model: "m",
					Input: refclient.Input{Items: []refclient.Item{refclient.NewMessageItem("user", "input_text", "weather?")}},
					Tools: []refclient.Tool{{Type: "function", Name: "get_weather", Description: "weather"}},
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if len(res.Output) != 2 || res.Output[0].Type != "function_call" || res.Output[0].Name != "get_weather" {
					t.Fatalf("output: %+v", res.Output)
				}
				if res.Output[1].Type != "function_call_output" || res.Output[1].CallID != "call_1" {
					t.Fatalf("call output: %+v", res.Output[1])
				}
				if !strings.Contains(string(res.Output[1].Output), "22c") {
					t.Fatalf("call output bytes: %s", res.Output[1].Output)
				}
			},
		},
		{
			id: "scenario-reasoning", description: "refclient parses reasoning items and reasoning token usage",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-reasoning", Description: "direct reasoning", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "m"},
					Resource: NewResource("resp_reason_direct", "m", 1, []Item{
						NewReasoningItem("rs_1", []ContentPart{{Type: "summary_text", Text: "reasoned"}}, []ContentPart{{Type: "output_text", Text: "trace"}}),
						NewMessagePartsItem("assistant", "", NewTextPart("answer")),
					}).WithUsage(Usage{InputTokens: 5, OutputTokens: 6, ReasoningTokens: 6, TotalTokens: 11}),
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "think"}})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.Output[0].Type != "reasoning" || res.Output[0].Reasoning == nil {
					t.Fatalf("reasoning: %+v", res.Output[0])
				}
				if len(res.Output[0].Reasoning.Summary) != 1 || res.Output[0].Reasoning.Summary[0].Text != "reasoned" {
					t.Fatalf("reasoning summary: %+v", res.Output[0].Reasoning.Summary)
				}
				if res.Usage.ReasoningTokens != 6 {
					t.Fatalf("reasoning tokens: %d", res.Usage.ReasoningTokens)
				}
			},
		},
		{
			id: "scenario-phase", description: "refclient preserves assistant phase labels",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-phase", Description: "direct phase", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "m"},
					Resource: NewResource("resp_phase_direct", "m", 1, []Item{
						NewMessagePartsItem("assistant", "commentary", NewTextPart("first")),
						NewMessagePartsItem("assistant", "final_answer", NewTextPart("second")),
					}),
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if res.Output[0].Phase != "commentary" || res.Output[1].Phase != "final_answer" {
					t.Fatalf("phases: %+v", res.Output)
				}
			},
		},
		{
			id: "scenario-extensions", description: "refclient preserves prefixed extension items and top-level extensions opaquely",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-extensions", Description: "direct extensions", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "m"},
					Resource: func() *Resource {
						r := NewResource("resp_ext_direct", "m", 1, []Item{
							NewExtensionItem("acme:telemetry_chunk", `{"id":"tc_1","status":"completed","latency_ms":72,"cache_hit":true}`),
							NewMessagePartsItem("assistant", "", NewTextPart("done")),
						})
						r.Extensions["acme:meta"] = json.RawMessage(`{"region":"eu"}`)
						return r
					}(),
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				ext := res.Output[0]
				if ext.Type != "acme:telemetry_chunk" || !ext.IsExtension() || len(ext.Opaque) == 0 {
					t.Fatalf("extension: %+v", ext)
				}
				if !strings.Contains(string(ext.Opaque), "latency_ms") {
					t.Fatalf("opaque bytes: %s", ext.Opaque)
				}
				if res.Extensions["acme:meta"] == nil {
					t.Fatalf("top-level extension missing: %+v", res.Extensions)
				}
			},
		},
		{
			id: "scenario-required-presence", description: "every null-permitted required field is present on the refbackend wire",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-required-presence", Description: "required presence", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "m"},
					Resource: NewResource("resp_presence_direct", "m", 1, nil),
				})
				cli := directClient(t, ts.URL)
				res, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				if err != nil {
					t.Fatalf("strict required-presence parse failed: %v", err)
				}
				if res.Status != "completed" || res.Object != "response" {
					t.Fatalf("resource: %+v", res)
				}
			},
		},
		{
			id: "scenario-multimodal-input", description: "refclient multimodal input is captured exactly by the emulator",
			run: func(t *testing.T) {
				srv, ts := startServer(t, Options{}, &Script{
					ID: "scenario-multimodal-input", Description: "multimodal input", Mode: ModeJSON,
					Expected: ExpectedRequest{Model: "m", MinInputItems: 1, Contains: []string{"input_image", "input_file"}},
					Resource: NewResource("r", "m", 1, nil),
				})
				cli := directClient(t, ts.URL)
				_, err := cli.Create(context.Background(), refclient.CreateParams{
					Model: "m",
					Input: refclient.Input{Items: []refclient.Item{{
						Type: "message", Role: "user",
						Content: []refclient.ContentPart{
							{Type: "input_text", Text: "see"},
							{Type: "input_image", ImageURL: json.RawMessage(`{"data":"aGVsbG8=","media_type":"image/png"}`)},
							{Type: "input_file", FileURL: json.RawMessage(`{"file_id":"f1"}`)},
						},
					}}},
				})
				if err != nil {
					t.Fatalf("Create: %v", err)
				}
				if srv.MismatchCount() != 0 {
					t.Fatalf("mismatch: %d", srv.MismatchCount())
				}
				obs, ok := srv.Capture().Last()
				if !ok || !strings.Contains(string(obs.Body), "input_image") {
					t.Fatalf("multimodal body not captured: %+v", obs)
				}
			},
		},
		{
			id: "scenario-auth-missing", description: "missing credentials yield a structured 401 to the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{AllowMissingBearer: false}, &Script{
					ID: "scenario-auth-missing", Description: "auth missing", Mode: ModeJSON,
					Resource: NewResource("r", "m", 1, nil),
				})
				cli := refclient.New(refclient.Config{BaseURL: ts.URL, APIKey: ""})
				_, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				httpErr, ok := err.(*refclient.HTTPError)
				if !ok || httpErr.StatusCode != 401 {
					t.Fatalf("expected 401 HTTPError, got %v", err)
				}
				if httpErr.ErrorObject == nil || httpErr.ErrorObject.Code != "invalid_api_key" {
					t.Fatalf("error object: %+v", httpErr.ErrorObject)
				}
			},
		},
		{
			id: "scenario-auth-wrong", description: "wrong bearer token yields a structured 401",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{RequiredBearer: "sk-good"}, &Script{
					ID: "scenario-auth-wrong", Description: "auth wrong", Mode: ModeJSON,
					Resource: NewResource("r", "m", 1, nil),
				})
				cli := refclient.New(refclient.Config{BaseURL: ts.URL, APIKey: "sk-wrong"})
				_, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				if err == nil {
					t.Fatal("expected 401")
				}
				if _, ok := err.(*refclient.HTTPError); !ok {
					t.Fatalf("expected HTTPError: %v", err)
				}
			},
		},
		{
			id: "scenario-rate-limit", description: "rate-limit 429 with Retry-After is surfaced to the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-rate-limit", Description: "rate limit", Mode: ModeJSON,
					Resource: NewResource("r", "m", 1, nil),
					Error:    &ErrorStep{Status: 429, Type: "requests", Code: "rate_limit_exceeded", Message: "slow down", RetryAfter: "42"},
				})
				cli := directClient(t, ts.URL)
				_, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				httpErr, ok := err.(*refclient.HTTPError)
				if !ok || httpErr.StatusCode != 429 {
					t.Fatalf("expected 429, got %v", err)
				}
				if httpErr.ErrorObject == nil || httpErr.ErrorObject.Code != "rate_limit_exceeded" {
					t.Fatalf("error object: %+v", httpErr.ErrorObject)
				}
			},
		},
		{
			id: "scenario-4xx", description: "4xx error steps surface to the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-4xx", Description: "4xx", Mode: ModeJSON,
					Resource: NewResource("r", "m", 1, nil),
					Error:    &ErrorStep{Status: 400, Type: "invalid_request", Code: "bad_request", Message: "bad request"},
				})
				cli := directClient(t, ts.URL)
				_, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				httpErr, ok := err.(*refclient.HTTPError)
				if !ok || httpErr.StatusCode != 400 || httpErr.ErrorObject.Code != "bad_request" {
					t.Fatalf("expected 400, got %v", err)
				}
			},
		},
		{
			id: "scenario-5xx", description: "5xx error steps surface to the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-5xx", Description: "5xx", Mode: ModeJSON,
					Resource: NewResource("r", "m", 1, nil),
					Error:    &ErrorStep{Status: 500, Type: "server_error", Code: "internal_error", Message: "boom"},
				})
				cli := directClient(t, ts.URL)
				_, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}})
				httpErr, ok := err.(*refclient.HTTPError)
				if !ok || httpErr.StatusCode != 500 {
					t.Fatalf("expected 500, got %v", err)
				}
			},
		},
		{
			id: "scenario-malformed-event", description: "malformed SSE event framing is rejected by the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-malformed-event", Description: "malformed event", Mode: ModeSSE,
					Expected:  ExpectedRequest{Model: "m", MinInputItems: 1},
					Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
					Malformed: MalformedEventNoHeader,
				})
				cli := directClient(t, ts.URL)
				if _, err := cli.CreateStream(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}}, nil); err == nil {
					t.Fatal("expected malformed SSE rejection")
				}
			},
		},
		{
			id: "scenario-malformed-resource", description: "malformed resource (missing required field) is rejected by the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-malformed-resource", Description: "malformed resource", Mode: ModeJSON,
					Expected:  ExpectedRequest{Model: "m", MinInputItems: 1},
					Resource:  NewResource("r", "m", 1, nil),
					Malformed: MalformedResourceMissingField,
				})
				cli := directClient(t, ts.URL)
				if _, err := cli.Create(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}}); err == nil {
					t.Fatal("expected required-presence rejection")
				}
			},
		},
		{
			id: "scenario-malformed-content-type", description: "wrong streaming content-type is rejected by the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-malformed-content-type", Description: "malformed content type", Mode: ModeSSE,
					Expected:  ExpectedRequest{Model: "m", MinInputItems: 1},
					Resource:  NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
					Malformed: MalformedContentType,
				})
				cli := directClient(t, ts.URL)
				if _, err := cli.CreateStream(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}}, nil); err == nil {
					t.Fatal("expected content-type rejection")
				}
			},
		},
		{
			id: "scenario-disconnect", description: "mid-stream disconnect is observed by the refclient",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-disconnect", Description: "disconnect", Mode: ModeSSE,
					Expected:        ExpectedRequest{Model: "m", MinInputItems: 1},
					Resource:        NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("x"))}),
					DisconnectAfter: 2,
				})
				cli := directClient(t, ts.URL)
				if _, err := cli.CreateStream(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}}, nil); err == nil {
					t.Fatal("expected disconnect error")
				}
			},
		},
		{
			id: "scenario-cancel", description: "client cancellation is observed by the emulator",
			run: func(t *testing.T) {
				// A large per-event payload forces the server's next write to hit
				// the closed connection once the client aborts mid-stream.
				big := strings.Repeat("x", 256<<10)
				srv, ts := startServer(t, Options{}, &Script{
					ID: "scenario-cancel", Description: "cancel", Mode: ModeSSE,
					Expected: ExpectedRequest{Model: "m"},
					Delay:    DelayPlan{BetweenEvents: 20 * time.Millisecond},
					Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart(big))}),
				})
				cli := directClient(t, ts.URL)
				// The handler aborts the stream after the first event; the client
				// closes the response body, and the emulator must observe the abort.
				_, _ = cli.CreateStream(context.Background(), refclient.CreateParams{
					Model: "m",
					Input: refclient.Input{Text: "hi"},
				}, func(evt refclient.Event) error {
					return errAbortStream
				})
				if !eventually(t, 3*time.Second, func() bool {
					return srv.CancelCount() >= 1 || srv.WriteErrorCount() >= 1
				}) {
					t.Fatalf("server did not observe client abort (cancel=%d writeErr=%d)", srv.CancelCount(), srv.WriteErrorCount())
				}
			},
		},
		{
			id: "scenario-slow-write", description: "slow-write server keeps every event in order",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-slow-write", Description: "slow write", Mode: ModeSSE,
					Expected: ExpectedRequest{Model: "m"},
					Delay:    DelayPlan{SlowWrite: 5 * time.Millisecond},
					Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("slow but ordered"))}),
				})
				cli := directClient(t, ts.URL)
				var types []string
				terminal, err := cli.CreateStream(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}}, func(evt refclient.Event) error {
					types = append(types, evt.Type)
					return nil
				})
				if err != nil {
					t.Fatalf("CreateStream: %v", err)
				}
				if len(types) != 8 || terminal == nil || terminal.OutputText() != "slow but ordered" {
					t.Fatalf("ordered stream lost: events=%d terminal=%+v", len(types), terminal)
				}
			},
		},
		{
			id: "scenario-backpressure", description: "slow consumer plus slow writer preserves every event",
			run: func(t *testing.T) {
				_, ts := startServer(t, Options{}, &Script{
					ID: "scenario-backpressure", Description: "backpressure", Mode: ModeSSE,
					Expected: ExpectedRequest{Model: "m"},
					Delay:    DelayPlan{SlowWrite: 4 * time.Millisecond},
					Resource: NewResource("r", "m", 1, []Item{NewMessagePartsItem("assistant", "", NewTextPart("backpressure ok"))}),
				})
				cli := refclient.New(refclient.Config{
					BaseURL: ts.URL, APIKey: "sk-test",
					SlowConsumerDelay: 6 * time.Millisecond,
				})
				var count int
				terminal, err := cli.CreateStream(context.Background(), refclient.CreateParams{Model: "m", Input: refclient.Input{Text: "hi"}}, func(evt refclient.Event) error {
					count++
					return nil
				})
				if err != nil {
					t.Fatalf("CreateStream: %v", err)
				}
				if count != 8 || terminal == nil || terminal.OutputText() != "backpressure ok" {
					t.Fatalf("backpressure lost events: count=%d terminal=%+v", count, terminal)
				}
			},
		},
		{
			id: "scenario-zero-upstream", description: "no request means a zero request counter (pre-network rejection proof)",
			run: func(t *testing.T) {
				srv, _ := startServer(t, Options{}, &Script{
					ID: "scenario-zero-upstream", Description: "zero upstream", Mode: ModeJSON,
					Resource: NewResource("r", "m", 1, nil),
				})
				if srv.Capture().Total() != 0 || srv.Capture().Count("/responses") != 0 {
					t.Fatalf("zero-upstream counters must be 0")
				}
			},
		},
	}
}

// TestDirectWire_All is the direct refclient ↔ refbackend interoperability suite.
// Every directWireCase ID is registered in the coverage registry and executed live.
func TestDirectWire_All(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range directWireCases() {
		t.Run(tc.id, func(t *testing.T) {
			seen[tc.id] = true
			if strings.TrimSpace(tc.description) == "" {
				t.Fatal("direct wire case must have a description")
			}
			tc.run(t)
		})
	}
	if len(seen) != len(directWireCases()) {
		t.Fatalf("subtest execution mismatch: %d/%d", len(seen), len(directWireCases()))
	}
}

//go:fix inline
func boolRef(b bool) *bool { return new(b) }

// errAbortStream lets a client handler abort a direct-wire stream deterministically.
var errAbortStream = &abortError{}

type abortError struct{}

func (*abortError) Error() string { return "refbackend/openresponses: abort stream" }
