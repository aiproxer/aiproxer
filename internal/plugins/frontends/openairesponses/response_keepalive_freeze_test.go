package openairesponses_test

// Task 1.6 characterization: freeze OpenAI Responses response + keepalive
// behavior for the future large-payload lane (requirements 17, 18; design
// sections 1, 13 read-only, 16).
//
// What this proves with real existing seams only:
//   - Non-stream/stream default wire identity: ResponseID
//     "resp_"+diag.StableCallToken, MessageID "msg_"+ResponseID, CreatedAt
//     diag.StableUnix, model echo with "gpt-4o-mini" default
//     (encode.go defaultEncodeOptions, encode_nonstream.go, encode_stream.go).
//   - BuildEncodeOpts WallClock override: a configured executor clock replaces
//     StableUnix while the response ID stays deterministic.
//   - Stream/non-stream response-ID parity for the same logical body (both go
//     through BuildEncodeOpts -> responseIDForCall), per requirement 18.7.
//   - Session carriers: AuthoritativeSessionID/ResumeToken round-trip as
//     response headers; full A-leg/session/resume carriers are emitted by both
//     writers; the resume sentinel never appears in JSON/SSE bodies.
//   - Debug helpers (streamdebug LogCall/LogDecodeFailure/LogExecuteOpened/Wrap)
//     never emit prompt text or resume secrets (metadata-only when enabled,
//     no-op when disabled).
//
// Reused, not duplicated here:
//   - A-leg cancellation carrier encode/decode + cancel endpoint ownership
//     (handler_internal_test.go, session_wire_test.go, cancel_e2e_test.go).
//   - Outer/method/path/body/admission ordering (ordering_characterization_test.go).
//   - Golden usage/encoding shapes (encode_test.go).
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - ExecutionResult/ResponseFacts/SessionResponseCarrier do not exist yet;
//     no wire-native response bridge is constructed here. The frozen invariant
//     is only the current writer/header/ID contract the bridge must preserve.
//
// Test-only, no production diff.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/streamdebug"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	freezePromptSentinel = "freeze-prompt-sentinel-9f3a4c1e"
	freezeResumeSentinel = "freeze-resume-sentinel-7b2d8e0a"
)

func freezeTextStream(text string) aipapi.EventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	})
}

func freezeSessionCall(tb testing.TB, model string) *aipapi.Call {
	tb.Helper()
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("freeze hello")},
		}},
		Extensions: mustModelExt(tb, model),
		Session: aipapi.SessionRef{
			ALegID:                 "a-leg-freeze-1",
			AuthoritativeSessionID: "sess-freeze-1",
			ResumeToken:            freezeResumeSentinel,
		},
	}
}

type freezeNonStreamBody struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created_at"`
	Status  string `json:"status"`
	Model   string `json:"model"`
	Output  []struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	} `json:"output"`
}

func TestResponseFreeze_NonStreamDefaultsIDTimestampModel(t *testing.T) {
	t.Parallel()
	call := freezeSessionCall(t, "gpt-4o-mini")
	wantID := "resp_" + diag.StableCallToken(call)
	wantTS := diag.StableUnix(call)

	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, freezeTextStream("hi"), openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got freezeNonStreamBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != wantID {
		t.Fatalf("id=%q want %q", got.ID, wantID)
	}
	if got.Object != "response" || got.Status != "completed" {
		t.Fatalf("object/status=%q/%q want response/completed", got.Object, got.Status)
	}
	if got.Created != wantTS {
		t.Fatalf("created_at=%d want %d", got.Created, wantTS)
	}
	if got.Model != "gpt-4o-mini" {
		t.Fatalf("model=%q want gpt-4o-mini", got.Model)
	}
	if len(got.Output) == 0 || got.Output[0].Type != "message" {
		t.Fatalf("output[0]=%+v want leading message item", got.Output)
	}
	if want := "msg_" + wantID; got.Output[0].ID != want {
		t.Fatalf("message id=%q want %q", got.Output[0].ID, want)
	}
}

func TestResponseFreeze_NonStreamModelDefaultAndExplicitOpts(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}

	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, freezeTextStream("hi"), openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var got freezeNonStreamBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-4o-mini" {
		t.Fatalf("default model=%q want gpt-4o-mini", got.Model)
	}

	rec2 := httptest.NewRecorder()
	explicit := openairesponses.EncodeOptions{ResponseID: "resp_explicit", MessageID: "msg_explicit", CreatedAt: 1700000000}
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec2, call, freezeTextStream("hi"), explicit); err != nil {
		t.Fatal(err)
	}
	var got2 freezeNonStreamBody
	if err := json.Unmarshal(rec2.Body.Bytes(), &got2); err != nil {
		t.Fatal(err)
	}
	if got2.ID != "resp_explicit" || got2.Created != 1700000000 {
		t.Fatalf("explicit opts not preserved: %+v", got2)
	}
	if len(got2.Output) == 0 || got2.Output[0].ID != "msg_explicit" {
		t.Fatalf("explicit message id not preserved: %+v", got2.Output)
	}
}

func TestResponseFreeze_WritersEmitSessionCarriersWithoutBodySecrets(t *testing.T) {
	t.Parallel()
	call := freezeSessionCall(t, "gpt-4o-mini")

	rec := httptest.NewRecorder()
	if err := openairesponses.WriteNonStreamJSON(context.Background(), rec, call, freezeTextStream("hi"), openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFreezeCarriers(t, rec.Header())
	if strings.Contains(rec.Body.String(), freezeResumeSentinel) {
		t.Fatal("non-stream body leaks resume token")
	}

	rec2 := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(context.Background(), rec2, call, freezeTextStream("hi"), openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFreezeCarriers(t, rec2.Header())
	if ct := rec2.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Fatalf("stream content-type=%q", ct)
	}
	if strings.Contains(rec2.Body.String(), freezeResumeSentinel) {
		t.Fatal("stream body leaks resume token")
	}
	if !strings.Contains(rec2.Body.String(), "data: [DONE]\n\n") {
		t.Fatal("stream body missing terminal [DONE]")
	}
}

func assertFreezeCarriers(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get(sessionwire.HeaderALegID); got != "a-leg-freeze-1" {
		t.Fatalf("A-leg header=%q", got)
	}
	if got := h.Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess-freeze-1" {
		t.Fatalf("session header=%q", got)
	}
	if got := h.Get(sessionwire.HeaderResumeToken); got != freezeResumeSentinel {
		t.Fatalf("resume header missing")
	}
}

func sseDataPayloads(t *testing.T, body string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(body, "\n") {
		rest, ok := strings.CutPrefix(line, "data: ")
		if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "{") {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(rest), &v); err != nil {
			t.Fatalf("bad sse payload %q: %v", rest, err)
		}
		out = append(out, v)
	}
	return out
}

func TestResponseFreeze_StreamCompletedMatchesNonStreamIdentity(t *testing.T) {
	t.Parallel()
	call := freezeSessionCall(t, "gpt-4o-mini")
	wantID := "resp_" + diag.StableCallToken(call)
	wantTS := diag.StableUnix(call)

	rec := httptest.NewRecorder()
	if err := openairesponses.WriteStreamSSE(context.Background(), rec, call, freezeTextStream("hi"), openairesponses.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var created, completed map[string]any
	for _, p := range sseDataPayloads(t, rec.Body.String()) {
		switch p["type"] {
		case "response.created":
			created = p
		case "response.completed":
			completed = p
		}
	}
	if created == nil || completed == nil {
		t.Fatal("stream missing created/completed envelopes")
	}
	for _, p := range []map[string]any{created, completed} {
		resp, _ := p["response"].(map[string]any)
		if resp == nil {
			t.Fatalf("envelope without response: %v", p)
		}
		if resp["id"] != wantID {
			t.Fatalf("stream response id=%v want %v", resp["id"], wantID)
		}
		if resp["model"] != "gpt-4o-mini" {
			t.Fatalf("stream model=%v", resp["model"])
		}
		if created, ok := resp["created_at"].(float64); !ok || int64(created) != wantTS {
			t.Fatalf("stream created_at=%v want %d", resp["created_at"], wantTS)
		}
	}
}

type freezeClockExec struct {
	orderingRouteExec
	now time.Time
}

func (e *freezeClockExec) WallClock() func() time.Time {
	return func() time.Time { return e.now }
}

func TestResponseFreeze_WallClockOverridesStableTimestampEndToEnd(t *testing.T) {
	t.Parallel()
	wall := time.Unix(1800000000, 0).UTC()
	exec := &freezeClockExec{now: wall}
	h := &openairesponses.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"hi"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got freezeNonStreamBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Created != wall.Unix() {
		t.Fatalf("created_at=%d want wall clock %d", got.Created, wall.Unix())
	}
	if !strings.HasPrefix(got.ID, "resp_") || got.Model != "gpt-4o-mini" {
		t.Fatalf("id/model not preserved under wall clock: %+v", got)
	}
}

func TestResponseFreeze_StreamNonStreamIDParityEndToEnd(t *testing.T) {
	t.Parallel()
	serve := func(body string) *httptest.ResponseRecorder {
		h := &openairesponses.Handler{Exec: &orderingRouteExec{}, DefaultRouteSelector: "stub:gpt-4o-mini"}
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec
	}
	nonStream := serve(`{"model":"gpt-4o-mini","input":"hi"}`)
	var ns freezeNonStreamBody
	if err := json.Unmarshal(nonStream.Body.Bytes(), &ns); err != nil {
		t.Fatal(err)
	}
	streamRec := serve(`{"model":"gpt-4o-mini","input":"hi","stream":true}`)
	var completedID string
	for _, p := range sseDataPayloads(t, streamRec.Body.String()) {
		if p["type"] == "response.completed" {
			resp, _ := p["response"].(map[string]any)
			completedID, _ = resp["id"].(string)
		}
	}
	if completedID == "" {
		t.Fatal("stream missing response.completed")
	}
	if completedID != ns.ID {
		t.Fatalf("stream id=%q non-stream id=%q: same logical body must share deterministic response ID", completedID, ns.ID)
	}
}

func TestResponseFreeze_SessionHeadersRoundTripEndToEnd(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{Exec: &orderingRouteExec{}, DefaultRouteSelector: "stub:gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"hi"}`))
	req.Header.Set(sessionwire.HeaderAuthoritativeSessionID, "sess-freeze-1")
	req.Header.Set(sessionwire.HeaderResumeToken, freezeResumeSentinel)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess-freeze-1" {
		t.Fatalf("session header=%q", got)
	}
	if got := rec.Header().Get(sessionwire.HeaderResumeToken); got != freezeResumeSentinel {
		t.Fatalf("resume header missing")
	}
	if strings.Contains(rec.Body.String(), freezeResumeSentinel) {
		t.Fatal("response body leaks resume token")
	}
}

func TestResponseFreeze_DebugHelpersNeverEmitPromptOrResumeSecrets(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	call := &aipapi.Call{
		ID: "call-freeze-debug-1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(freezePromptSentinel)},
		}},
		Extensions: mustModelExt(t, "gpt-4o-mini"),
		Session: aipapi.SessionRef{
			ALegID:                 "a-leg-freeze-1",
			AuthoritativeSessionID: "sess-freeze-1",
			ResumeToken:            freezeResumeSentinel,
		},
	}
	ctx := context.Background()
	streamdebug.LogCall(ctx, log, "openairesponses", call, true, 2048, "stub:x")
	streamdebug.LogDecodeFailure(ctx, log, "openairesponses",
		[]byte(`{"model":"gpt-4o-mini","input":"`+freezePromptSentinel+`"}`), errors.New("freeze decode boom"))
	streamdebug.LogExecuteOpened(ctx, log, "openairesponses", call, time.Now())
	wrapped := streamdebug.Wrap(ctx, log, "openairesponses", call, freezeTextStream("hi"), time.Now())
	for {
		if _, err := wrapped.Recv(ctx); err != nil {
			break
		}
	}
	_ = wrapped.Close()
	if out := buf.String(); strings.Contains(out, freezePromptSentinel) || strings.Contains(out, freezeResumeSentinel) {
		t.Fatalf("debug output leaks secrets: %q", out)
	}
}
