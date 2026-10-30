package openailegacy_test

// Task 1.6 characterization: freeze OpenAI Chat response + keepalive behavior
// for the future large-payload lane (requirements 17, 18; design sections 1,
// 13 read-only, 16).
//
// What this proves with real existing seams only:
//   - Non-stream/stream default wire identity: CompletionID
//     "chatcmpl_"+diag.StableCallToken, CreatedAt diag.StableUnix, model echo
//     with "gpt-4o-mini" default (encode.go defaultEncodeOptions,
//     WriteNonStreamJSON, WriteStreamSSE).
//   - BuildEncodeOpts WallClock override end to end (clock replaces StableUnix,
//     completion ID stays deterministic).
//   - Stream/non-stream completion-ID parity for the same logical body.
//   - Session carriers (A-leg/session/resume) emitted by both writers; the
//     resume sentinel never appears in JSON/SSE bodies.
//
// Reused, not duplicated here:
//   - Outer/method/path/body/admission ordering (ordering_characterization_test.go).
//   - Golden usage/encoding shapes (encode_test.go).
//   - Shared debug-helper secrecy (streamdebug is frontend-neutral; frozen in
//     openairesponses/response_keepalive_freeze_test.go).
//
// What this explicitly does NOT claim (out of scope, needs future tasks):
//   - ExecutionResult/ResponseFacts do not exist yet; no wire-native response
//     bridge is constructed here. The frozen invariant is only the current
//     writer/header/ID contract the bridge must preserve.
//
// Test-only, no production diff.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const freezeChatResumeSentinel = "freeze-chat-resume-sentinel-4c1a9d2b"

func freezeChatTextStream(text string) aipapi.EventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	})
}

func freezeChatSessionCall(tb testing.TB) *aipapi.Call {
	tb.Helper()
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("freeze hello")},
		}},
		Extensions: mustModelExt(tb, "gpt-4o-mini"),
		Session: aipapi.SessionRef{
			ALegID:                 "a-leg-chat-freeze-1",
			AuthoritativeSessionID: "sess-chat-freeze-1",
			ResumeToken:            freezeChatResumeSentinel,
		},
	}
}

type freezeChatCompletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
}

func TestChatResponseFreeze_NonStreamDefaultsIDTimestampModel(t *testing.T) {
	t.Parallel()
	call := freezeChatSessionCall(t)
	wantID := "chatcmpl_" + diag.StableCallToken(call)
	wantTS := diag.StableUnix(call)

	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(context.Background(), rec, call, freezeChatTextStream("hi"), openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got freezeChatCompletion
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != wantID {
		t.Fatalf("id=%q want %q", got.ID, wantID)
	}
	if got.Object != "chat.completion" {
		t.Fatalf("object=%q want chat.completion", got.Object)
	}
	if got.Created != wantTS {
		t.Fatalf("created=%d want %d", got.Created, wantTS)
	}
	if got.Model != "gpt-4o-mini" {
		t.Fatalf("model=%q want gpt-4o-mini", got.Model)
	}
}

func TestChatResponseFreeze_NonStreamModelDefaultAndExplicitOpts(t *testing.T) {
	t.Parallel()
	call := &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}

	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(context.Background(), rec, call, freezeChatTextStream("hi"), openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	var got freezeChatCompletion
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-4o-mini" {
		t.Fatalf("default model=%q want gpt-4o-mini", got.Model)
	}

	rec2 := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(context.Background(), rec2, call, freezeChatTextStream("hi"), openailegacy.EncodeOptions{
		CompletionID: "chatcmpl_explicit",
		CreatedAt:    1700000000,
	}); err != nil {
		t.Fatal(err)
	}
	var got2 freezeChatCompletion
	if err := json.Unmarshal(rec2.Body.Bytes(), &got2); err != nil {
		t.Fatal(err)
	}
	if got2.ID != "chatcmpl_explicit" || got2.Created != 1700000000 {
		t.Fatalf("explicit opts not preserved: %+v", got2)
	}
}

func TestChatResponseFreeze_WritersEmitSessionCarriersWithoutBodySecrets(t *testing.T) {
	t.Parallel()
	call := freezeChatSessionCall(t)

	rec := httptest.NewRecorder()
	if err := openailegacy.WriteNonStreamJSON(context.Background(), rec, call, freezeChatTextStream("hi"), openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFreezeChatCarriers(t, rec.Header())
	if strings.Contains(rec.Body.String(), freezeChatResumeSentinel) {
		t.Fatal("non-stream body leaks resume token")
	}

	rec2 := httptest.NewRecorder()
	if err := openailegacy.WriteStreamSSE(context.Background(), rec2, call, freezeChatTextStream("hi"), openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	assertFreezeChatCarriers(t, rec2.Header())
	if ct := rec2.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Fatalf("stream content-type=%q", ct)
	}
	if strings.Contains(rec2.Body.String(), freezeChatResumeSentinel) {
		t.Fatal("stream body leaks resume token")
	}
	if !strings.Contains(rec2.Body.String(), "data: [DONE]\n\n") {
		t.Fatal("stream body missing terminal [DONE]")
	}
}

func assertFreezeChatCarriers(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get(sessionwire.HeaderALegID); got != "a-leg-chat-freeze-1" {
		t.Fatalf("A-leg header=%q", got)
	}
	if got := h.Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess-chat-freeze-1" {
		t.Fatalf("session header=%q", got)
	}
	if got := h.Get(sessionwire.HeaderResumeToken); got != freezeChatResumeSentinel {
		t.Fatalf("resume header missing")
	}
}

func freezeChatSSEChunks(t *testing.T, body string) []freezeChatCompletion {
	t.Helper()
	var out []freezeChatCompletion
	for line := range strings.SplitSeq(body, "\n") {
		rest, ok := strings.CutPrefix(line, "data: ")
		if !ok || !strings.HasPrefix(strings.TrimSpace(rest), "{") {
			continue
		}
		var v freezeChatCompletion
		if err := json.Unmarshal([]byte(rest), &v); err != nil {
			t.Fatalf("bad sse chunk %q: %v", rest, err)
		}
		out = append(out, v)
	}
	return out
}

func TestChatResponseFreeze_StreamChunksCarryStableIDTimestampModel(t *testing.T) {
	t.Parallel()
	call := freezeChatSessionCall(t)
	wantID := "chatcmpl_" + diag.StableCallToken(call)
	wantTS := diag.StableUnix(call)

	rec := httptest.NewRecorder()
	if err := openailegacy.WriteStreamSSE(context.Background(), rec, call, freezeChatTextStream("hi"), openailegacy.EncodeOptions{}); err != nil {
		t.Fatal(err)
	}
	chunks := freezeChatSSEChunks(t, rec.Body.String())
	if len(chunks) == 0 {
		t.Fatal("stream emitted no chunks")
	}
	for _, c := range chunks {
		if c.ID != wantID {
			t.Fatalf("chunk id=%q want %q", c.ID, wantID)
		}
		if c.Object != "chat.completion.chunk" {
			t.Fatalf("chunk object=%q", c.Object)
		}
		if c.Created != wantTS {
			t.Fatalf("chunk created=%d want %d", c.Created, wantTS)
		}
		if c.Model != "gpt-4o-mini" {
			t.Fatalf("chunk model=%q", c.Model)
		}
	}
}

type freezeChatClockExec struct {
	orderingRouteExec
	now time.Time
}

func (e *freezeChatClockExec) WallClock() func() time.Time {
	return func() time.Time { return e.now }
}

func TestChatResponseFreeze_WallClockOverridesStableTimestampEndToEnd(t *testing.T) {
	t.Parallel()
	wall := time.Unix(1800000000, 0).UTC()
	exec := &freezeChatClockExec{now: wall}
	h := &openailegacy.Handler{Exec: exec, DefaultRouteSelector: "stub:gpt-4o-mini"}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got freezeChatCompletion
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Created != wall.Unix() {
		t.Fatalf("created=%d want wall clock %d", got.Created, wall.Unix())
	}
	if !strings.HasPrefix(got.ID, "chatcmpl_") || got.Model != "gpt-4o-mini" {
		t.Fatalf("id/model not preserved under wall clock: %+v", got)
	}
}

func TestChatResponseFreeze_StreamNonStreamIDParityEndToEnd(t *testing.T) {
	t.Parallel()
	serve := func(body string) *httptest.ResponseRecorder {
		h := &openailegacy.Handler{Exec: &orderingRouteExec{}, DefaultRouteSelector: "stub:gpt-4o-mini"}
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec
	}
	nonStream := serve(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	var ns freezeChatCompletion
	if err := json.Unmarshal(nonStream.Body.Bytes(), &ns); err != nil {
		t.Fatal(err)
	}
	streamRec := serve(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	chunks := freezeChatSSEChunks(t, streamRec.Body.String())
	if len(chunks) == 0 {
		t.Fatal("stream emitted no chunks")
	}
	if chunks[0].ID != ns.ID {
		t.Fatalf("stream id=%q non-stream id=%q: same logical body must share deterministic completion ID", chunks[0].ID, ns.ID)
	}
}

func TestChatResponseFreeze_SessionHeadersRoundTripEndToEnd(t *testing.T) {
	t.Parallel()
	h := &openailegacy.Handler{Exec: &orderingRouteExec{}, DefaultRouteSelector: "stub:gpt-4o-mini"}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set(sessionwire.HeaderAuthoritativeSessionID, "sess-chat-freeze-1")
	req.Header.Set(sessionwire.HeaderResumeToken, freezeChatResumeSentinel)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess-chat-freeze-1" {
		t.Fatalf("session header=%q", got)
	}
	if got := rec.Header().Get(sessionwire.HeaderResumeToken); got != freezeChatResumeSentinel {
		t.Fatalf("resume header missing")
	}
	if strings.Contains(rec.Body.String(), freezeChatResumeSentinel) {
		t.Fatal("response body leaks resume token")
	}
}
