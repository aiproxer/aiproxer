package openresponses_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// unsupportedFrontendControls mirror the protocol-level table: non-null values
// must fail in create frontend admission before any executor/upstream work, on
// both HTTP and WebSocket transports. instructions is deliberately absent here:
// the protocol decoder maps it into a leading canonical system item, so create
// forwards it (see TestFrontendHTTP_InstructionsForwardedToExecutor).
// stream_options is deliberately absent: it is an HTTP-only transport control
// that WebSocket rejects as field_not_allowed, and HTTP create rejects it via
// TestFrontendHTTP_StreamOptionsRejectedBeforeExecutor. Metadata is excluded.
var unsupportedFrontendControls = []struct {
	field string
	value string
}{
	{"include", `["reasoning.encrypted_content"]`},
	{"presence_penalty", `0.5`},
	{"frequency_penalty", `0.5`},
	{"top_logprobs", `1`},
	{"text", `{"format":"json_object"}`},
	{"truncation", `"auto"`},
	{"service_tier", `"auto"`},
	{"safety_identifier", `"safety_001"`},
	{"prompt_cache_key", `"k1"`},
	{"prompt_cache_retention", `"recent"`},
	{"max_tool_calls", `5`},
}

// createHTTPOnlyUnsupportedControls are pinned standard controls rejected on
// the HTTP create transport before network. stream_options is HTTP-only: the
// WebSocket turn path rejects it earlier as field_not_allowed.
var createHTTPOnlyUnsupportedControls = []struct {
	field string
	value string
}{
	{"stream_options", `{"include_obfuscation":true}`},
}

// compactSchemaPermittedFrontendControls are permitted by the pinned compact
// schema (compactResponseMethodPublicBodySchema): instructions forwards as a
// leading canonical system item and prompt_cache_key is carried on the
// canonical call, so neither is silently dropped.
var compactSchemaPermittedFrontendControls = []struct {
	field string
	value string
}{
	{"instructions", `"Be brief"`},
	{"prompt_cache_key", `"openresponses-compact-test"`},
}

// compactUnsupportedFrontendControls are absent from the pinned compact schema;
// a non-null value must fail compact admission instead of being silently dropped.
var compactUnsupportedFrontendControls = []struct {
	field string
	value string
}{
	{"tools", `[{"type":"function","name":"f"}]`},
	{"tool_choice", `"auto"`},
	{"parallel_tool_calls", `true`},
	{"temperature", `0.5`},
	{"top_p", `0.9`},
	{"max_output_tokens", `100`},
	{"metadata", `{"tenant":"acme"}`},
	{"include", `["reasoning.encrypted_content"]`},
	{"presence_penalty", `0.5`},
	{"frequency_penalty", `0.5`},
	{"stream_options", `{"include_obfuscation":true}`},
	{"top_logprobs", `1`},
	{"text", `{"format":"json_object"}`},
	{"truncation", `"auto"`},
	{"service_tier", `"auto"`},
	{"safety_identifier", `"safety_001"`},
	{"prompt_cache_retention", `"recent"`},
	{"max_tool_calls", `5`},
}

func TestFrontendHTTP_UnsupportedControlsRejectBeforeExecutor(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedFrontendControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec,
			})
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("non-null %s=%s: status=%d, want 400 (body=%s)", tc.field, tc.value, rec.Code, rec.Body.String())
			}
			if exec.executeCalls != 0 {
				t.Fatalf("non-null %s reached the executor: calls=%d", tc.field, exec.executeCalls)
			}
		})
	}
}

func TestFrontendHTTP_UnsupportedControlsNullAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range unsupportedFrontendControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":null}`)
			decoded, err := openresponses.AuthenticateAndDecodeCreate(context.Background(), body, openresponses.DecodeCreateOptions{
				Auth: &mockAuthorizer{authenticated: true},
			})
			if err != nil {
				t.Fatalf("null %s must be accepted, got error: %v", tc.field, err)
			}
			if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
				t.Fatalf("null %s produced an invalid canonical call: %+v", tc.field, decoded.Call)
			}
		})
	}
}

func TestFrontendHTTP_UnpinnedContentFieldsRejectBeforeExecutor(t *testing.T) {
	t.Parallel()
	// The pinned 2026-04-24 profile does not define input_file.file_id or
	// input_video.video_data. A non-null value must fail create admission before
	// any executor/upstream work instead of being silently dropped during
	// canonical construction.
	cases := []struct {
		name string
		body string
	}{
		{
			name: "input_file_file_id",
			body: `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-abc","file_url":"https://x/report.pdf"}]}]}`,
		},
		{
			name: "input_video_video_data",
			body: `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_video","video_data":"aGVsbG8=","video_url":"https://x/v.mp4"}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec,
			})
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader([]byte(tc.body)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
			if exec.executeCalls != 0 {
				t.Fatalf("unpinned content field reached the executor: calls=%d", exec.executeCalls)
			}
		})
	}
}

func TestFrontendHTTP_StreamOptionsRejectedBeforeExecutor(t *testing.T) {
	t.Parallel()
	for _, tc := range createHTTPOnlyUnsupportedControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec,
			})
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("non-null %s=%s: status=%d, want 400 (body=%s)", tc.field, tc.value, rec.Code, rec.Body.String())
			}
			if exec.executeCalls != 0 {
				t.Fatalf("non-null %s reached the executor: calls=%d", tc.field, exec.executeCalls)
			}
		})
	}
}

func TestFrontendHTTP_SupportedControlsPreserved(t *testing.T) {
	t.Parallel()
	exec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: exec,
	})
	body := []byte(`{
		"model":"gpt-4o",
		"input":"hello",
		"temperature":0.7,
		"top_p":0.9,
		"max_output_tokens":256,
		"parallel_tool_calls":true,
		"metadata":{"tenant":"acme"}
	}`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if exec.executeCalls != 1 {
		t.Fatalf("supported controls must reach the executor: calls=%d (status=%d)", exec.executeCalls, rec.Code)
	}
	call := exec.lastCall
	if call == nil {
		t.Fatal("no canonical call captured")
	}
	if call.Options.Temperature == nil || *call.Options.Temperature != 0.7 {
		t.Errorf("temperature=%v, want 0.7", call.Options.Temperature)
	}
	if call.Options.TopP == nil || *call.Options.TopP != 0.9 {
		t.Errorf("top_p=%v, want 0.9", call.Options.TopP)
	}
	if call.Options.MaxOutputTokens == nil || *call.Options.MaxOutputTokens != 256 {
		t.Errorf("max_output_tokens=%v, want 256", call.Options.MaxOutputTokens)
	}
	if call.Options.ParallelToolCalls == nil || !*call.Options.ParallelToolCalls {
		t.Errorf("parallel_tool_calls=%v, want true", call.Options.ParallelToolCalls)
	}
	if got := call.Session.Metadata["tenant"]; got != "acme" {
		t.Errorf("metadata=%v, want tenant=acme", call.Session.Metadata)
	}
}

func TestFrontendHTTP_ReasoningEffortAllPinnedValuesReachExecutor(t *testing.T) {
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{AllowUnauthenticated: true, Executor: exec})
			body := []byte(`{"model":"gpt-4o","input":"hello","reasoning":{"effort":"` + effort + `"}}`)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if exec.executeCalls != 1 || exec.lastCall == nil {
				t.Fatalf("effort %q did not reach executor: status=%d calls=%d body=%s", effort, rec.Code, exec.executeCalls, rec.Body.String())
			}
			if got := exec.lastCall.Options.ReasoningEffort; got != effort {
				t.Fatalf("canonical effort=%q, want %q", got, effort)
			}
		})
	}
}

func TestFrontendHTTP_ReasoningEmptyFormsAccepted(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"effort":null}`} {
		t.Run(raw, func(t *testing.T) {
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{AllowUnauthenticated: true, Executor: exec})
			body := []byte(`{"model":"gpt-4o","input":"hello","reasoning":` + raw + `}`)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if exec.executeCalls != 1 {
				t.Fatalf("empty reasoning %s rejected or skipped: status=%d calls=%d", raw, rec.Code, exec.executeCalls)
			}
			if exec.lastCall.Options.ReasoningEffort != "" {
				t.Fatalf("canonical effort=%q, want empty", exec.lastCall.Options.ReasoningEffort)
			}
		})
	}
}

func TestFrontendHTTP_ReasoningInvalidValuesRejectBeforeExecutor(t *testing.T) {
	for _, raw := range []string{`{"effort":"minimal"}`, `{"effort":1}`, `{"unknown":"low"}`} {
		t.Run(raw, func(t *testing.T) {
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{AllowUnauthenticated: true, Executor: exec})
			body := []byte(`{"model":"gpt-4o","input":"hello","reasoning":` + raw + `}`)
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || exec.executeCalls != 0 {
				t.Fatalf("invalid reasoning %s was not rejected before executor: status=%d calls=%d", raw, rec.Code, exec.executeCalls)
			}
		})
	}
}

func TestFrontendHTTP_CompactSchemaPermittedControlsAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range compactSchemaPermittedFrontendControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`)

			decoded, err := openresponses.AuthenticateAndDecodeCompact(context.Background(), body, openresponses.DecodeCompactOptions{
				Auth: &mockAuthorizer{authenticated: true},
			})
			if err != nil {
				t.Fatalf("compact decode must accept schema-permitted %s=%s: %v", tc.field, tc.value, err)
			}
			if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
				t.Fatalf("schema-permitted %s produced an invalid compact call", tc.field)
			}
			switch tc.field {
			case "instructions":
				if len(decoded.Call.Items) == 0 || decoded.Call.Items[0].Role != aipapi.RoleSystem {
					t.Fatalf("compact instructions must map into a leading system item, got %+v", decoded.Call.Items)
				}
			case "prompt_cache_key":
				if decoded.Call.PromptCacheKey != "openresponses-compact-test" {
					t.Fatalf("compact prompt_cache_key must be carried on the canonical call, got %q", decoded.Call.PromptCacheKey)
				}
			}

			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec,
			})
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if exec.executeCalls != 1 {
				t.Fatalf("schema-permitted %s=%s compact request must reach the executor: calls=%d (status=%d)", tc.field, tc.value, exec.executeCalls, rec.Code)
			}
		})
	}
}

func TestFrontendHTTP_InstructionsForwardedToExecutor(t *testing.T) {
	t.Parallel()
	exec := &mockExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true, Executor: exec,
	})
	body := []byte(`{"model":"gpt-4o","input":"hello","instructions":"Be brief"}`)
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if exec.executeCalls != 1 {
		t.Fatalf("create instructions must reach the executor: calls=%d (status=%d body=%s)", exec.executeCalls, rec.Code, rec.Body.String())
	}
	call := exec.lastCall
	if call == nil {
		t.Fatal("no canonical call captured")
	}
	if len(call.Items) != 2 || call.Items[0].Role != aipapi.RoleSystem {
		t.Fatalf("create instructions must map into a leading system item, got %+v", call.Items)
	}
	if call.Items[0].Content[0].Text != "Be brief" {
		t.Fatalf("leading system item must preserve the exact instruction text, got %+v", call.Items[0].Content)
	}
}

func TestFrontendHTTP_CompactUnsupportedControlsRejectBeforeExecutor(t *testing.T) {
	t.Parallel()
	for _, tc := range compactUnsupportedFrontendControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			exec := &mockExecutor{}
			handler := openresponses.NewHandler(openresponses.HandlerConfig{
				AllowUnauthenticated: true, Executor: exec,
			})
			req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses/compact",
				bytes.NewReader([]byte(`{"model":"gpt-4o","input":"hello","`+tc.field+`":`+tc.value+`}`)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("compact non-null %s=%s: status=%d, want 400 (body=%s)", tc.field, tc.value, rec.Code, rec.Body.String())
			}
			if exec.executeCalls != 0 {
				t.Fatalf("compact non-null %s reached the executor: calls=%d", tc.field, exec.executeCalls)
			}
		})
	}
}

func TestFrontendHTTP_CompactUnsupportedControlsNullAccepted(t *testing.T) {
	t.Parallel()
	for _, tc := range compactUnsupportedFrontendControls {
		t.Run(tc.field, func(t *testing.T) {
			t.Parallel()
			decoded, err := openresponses.AuthenticateAndDecodeCompact(context.Background(),
				[]byte(`{"model":"gpt-4o","input":"hello","`+tc.field+`":null}`),
				openresponses.DecodeCompactOptions{Auth: &mockAuthorizer{authenticated: true}})
			if err != nil {
				t.Fatalf("compact null %s must be accepted, got error: %v", tc.field, err)
			}
			if decoded.Call == nil || !decoded.Call.HasItemAuthority() {
				t.Fatalf("compact null %s produced an invalid canonical call", tc.field)
			}
		})
	}
}

func TestWebSocketTurn_UnsupportedControlsRejectBeforeExecutor(t *testing.T) {
	for _, tc := range unsupportedFrontendControls {
		t.Run(tc.field, func(t *testing.T) {
			exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
				aipapi.Event{Kind: aipapi.EventResponseStarted},
				aipapi.Event{Kind: aipapi.EventMessageStarted},
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "alive"},
				aipapi.Event{Kind: aipapi.EventResponseFinished},
			)}}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_unsup", now: time.Unix(1_700_002_000, 0)}, nil)
			conn := wsDial(t, srv, nil)

			msg := `{"type":"response.create","model":"gpt-4o","input":"hello","` + tc.field + `":` + tc.value + `}`
			wsText(t, conn, msg)
			f := wsReadTextFrame(t, conn, 3*time.Second)
			code, param := wsErrorEnvelope(t, f)
			if code != "invalid_request" {
				t.Fatalf("error code=%q, want invalid_request (frame: %s)", code, f.raw)
			}
			if param != "" {
				t.Fatalf("expected no param for %s rejection, got %q (frame: %s)", tc.field, param, f.raw)
			}
			if exec.count() != 0 {
				t.Fatalf("non-null %s reached the executor: calls=%d", tc.field, exec.count())
			}

			// The connection must survive the classified rejection.
			wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"next"}`)
			frames := wsReadUntilTerminal(t, conn, 3*time.Second)
			if !containsDelta(frames, "alive") {
				t.Fatalf("connection did not survive the %s rejection: %v", tc.field, frameTypes(frames))
			}
			_ = conn.Close()
			if exec.count() != 1 {
				t.Fatalf("executor calls=%d after follow-up, want 1", exec.count())
			}
		})
	}
}

func TestWebSocketTurn_UnsupportedControlsNullAccepted(t *testing.T) {
	for _, tc := range unsupportedFrontendControls {
		t.Run(tc.field, func(t *testing.T) {
			exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
				aipapi.Event{Kind: aipapi.EventResponseStarted},
				aipapi.Event{Kind: aipapi.EventMessageStarted},
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"},
				aipapi.Event{Kind: aipapi.EventResponseFinished},
			)}}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_null", now: time.Unix(1_700_002_100, 0)}, nil)
			conn := wsDial(t, srv, nil)

			msg := `{"type":"response.create","model":"gpt-4o","input":"hello","` + tc.field + `":null}`
			wsText(t, conn, msg)
			frames := wsReadUntilTerminal(t, conn, 3*time.Second)
			if !containsDelta(frames, "ok") {
				t.Fatalf("null %s was rejected: %v", tc.field, frameTypes(frames))
			}
			_ = conn.Close()
			if exec.count() != 1 {
				t.Fatalf("executor calls=%d, want 1", exec.count())
			}
		})
	}
}

func TestWebSocketTurn_ReasoningEffortAllPinnedValuesReachExecutor(t *testing.T) {
	for _, effort := range []string{"none", "low", "medium", "high", "xhigh"} {
		t.Run(effort, func(t *testing.T) {
			exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
				aipapi.Event{Kind: aipapi.EventResponseStarted}, aipapi.Event{Kind: aipapi.EventMessageStarted},
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"}, aipapi.Event{Kind: aipapi.EventResponseFinished},
			)}}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_effort", now: time.Unix(1_700_002_200, 0)}, nil)
			conn := wsDial(t, srv, nil)
			wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hello","reasoning":{"effort":"`+effort+`"}}`)
			frames := wsReadUntilTerminal(t, conn, 3*time.Second)
			if !containsDelta(frames, "ok") || exec.count() != 1 {
				t.Fatalf("effort %q did not execute: frames=%v calls=%d", effort, frameTypes(frames), exec.count())
			}
			if got := exec.callAt(0).Options.ReasoningEffort; got != effort {
				t.Fatalf("canonical effort=%q, want %q", got, effort)
			}
			_ = conn.Close()
		})
	}
}

func TestWebSocketTurn_ReasoningInvalidValuesRejectBeforeExecutor(t *testing.T) {
	for _, raw := range []string{`{"effort":"minimal"}`, `{"effort":1}`, `{"unknown":"low"}`} {
		t.Run(raw, func(t *testing.T) {
			exec := &wsTurnExecutor{}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_bad_effort", now: time.Unix(1_700_002_300, 0)}, nil)
			conn := wsDial(t, srv, nil)
			wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hello","reasoning":`+raw+`}`)
			frame := wsReadTextFrame(t, conn, 3*time.Second)
			code, _ := wsErrorEnvelope(t, frame)
			if code != "invalid_request" || exec.count() != 0 {
				t.Fatalf("invalid reasoning %s was not rejected before executor: code=%q calls=%d", raw, code, exec.count())
			}
			_ = conn.Close()
		})
	}
}

func TestWebSocketTurn_ReasoningEmptyFormsReachExecutor(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"effort":null}`} {
		t.Run(raw, func(t *testing.T) {
			exec := &wsTurnExecutor{streams: []aipapi.EventStream{fixedStream(
				aipapi.Event{Kind: aipapi.EventResponseStarted}, aipapi.Event{Kind: aipapi.EventMessageStarted},
				aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"}, aipapi.Event{Kind: aipapi.EventResponseFinished},
			)}}
			srv, _ := newWSTurnServer(t, exec, deterministicResponseMetadata{id: "resp_empty_effort", now: time.Unix(1_700_002_400, 0)}, nil)
			conn := wsDial(t, srv, nil)
			wsText(t, conn, `{"type":"response.create","model":"gpt-4o","input":"hello","reasoning":`+raw+`}`)
			frames := wsReadUntilTerminal(t, conn, 3*time.Second)
			if !containsDelta(frames, "ok") || exec.count() != 1 {
				t.Fatalf("empty reasoning %s rejected or skipped: frames=%v calls=%d", raw, frameTypes(frames), exec.count())
			}
			if got := exec.callAt(0).Options.ReasoningEffort; got != "" {
				t.Fatalf("canonical effort=%q, want empty", got)
			}
			_ = conn.Close()
		})
	}
}
