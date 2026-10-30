package openairesponses_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Response-clock parity (spec large-payload-proof-and-parity 5.1-5.4):
// wire Responses writers must honor the configured executor wall clock exactly
// like canonical BuildEncodeOpts, sampling once per response, while the direct
// WireWrite* helpers stay deterministic-only and identity/carriers/framing stay
// unchanged.

const (
	clockParityFixedCreatedAt int64 = 1700000000
	clockParitySeedTimestamp  int64 = 1715620000
	clockParitySeedToken            = "clockparity-seedtest-token"
)

type clockParityExec struct {
	calls atomic.Int64
	clock func() time.Time
}

func (e *clockParityExec) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func (e *clockParityExec) CancelALeg(_ context.Context, req aipapi.ALegCancelRequest) error {
	return nil
}

func (e *clockParityExec) WallClock() func() time.Time { return e.clock }

func clockParityFixedExec() *clockParityExec {
	return &clockParityExec{clock: func() time.Time { return time.Unix(clockParityFixedCreatedAt, 0).UTC() }}
}

func clockParityRC(stream bool) frontendpipe.ResponseContext {
	delivery := aipapi.DeliveryModeNonStreaming
	if stream {
		delivery = aipapi.DeliveryModeStreaming
	}
	return frontendpipe.ResponseContext{
		State: frontendpipe.FrontendWireState{
			Seeds: frontendpipe.ResponseStateSeeds{
				DeterministicCallID:    "call_" + clockParitySeedToken,
				DeterministicTimestamp: clockParitySeedTimestamp,
				DeterministicToken:     clockParitySeedToken,
				ClientModel:            "gpt-4o-mini",
				Stream:                 stream,
			},
		},
		ResponseFacts: largebody.ResponseFacts{
			Delivery:       delivery,
			EffectiveModel: "gpt-4o-mini",
		},
	}
}

func clockParityStream() aipapi.EventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "clock-parity"},
		{Kind: aipapi.EventResponseFinished},
	})
}

// createdAts extracts every response.created_at value from Responses SSE frames.
func createdAts(t *testing.T, rec *httptest.ResponseRecorder) []int64 {
	t.Helper()
	var out []int64
	for _, fr := range testkit.ParseRecorderSSE(rec) {
		if fr.Data == "" || fr.Data == "[DONE]" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(fr.Data), &v); err != nil {
			t.Fatalf("unmarshal frame %q: %v", fr.Data, err)
		}
		resp, ok := v["response"].(map[string]any)
		if !ok {
			continue
		}
		raw, ok := resp["created_at"]
		if !ok {
			continue
		}
		f, ok := raw.(float64)
		if !ok {
			t.Fatalf("created_at is not numeric in %q", fr.Data)
		}
		out = append(out, int64(f))
	}
	return out
}

func TestResponseClockParity_WireStreamUsesFixedClock(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{Exec: clockParityFixedExec()}
	rc := clockParityRC(true)
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, rc, clockParityStream()); err != nil {
		t.Fatal(err)
	}
	got := createdAts(t, rec)
	if len(got) < 3 {
		t.Fatalf("created_at samples = %v, want response.created/in_progress/completed", got)
	}
	for _, ts := range got {
		if ts != clockParityFixedCreatedAt {
			t.Fatalf("wire stream created_at = %v, want literal %d", got, clockParityFixedCreatedAt)
		}
	}
}

func TestResponseClockParity_WireNonStreamUsesFixedClock(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{Exec: clockParityFixedExec()}
	rc := clockParityRC(false)
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteNonStream(t.Context(), rec, rc, clockParityStream()); err != nil {
		t.Fatal(err)
	}
	var v struct {
		CreatedAt int64 `json:"created_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.CreatedAt != clockParityFixedCreatedAt {
		t.Fatalf("wire non-stream created_at = %d, want literal %d", v.CreatedAt, clockParityFixedCreatedAt)
	}
}

func TestResponseClockParity_AdvancingClockSampledOnce(t *testing.T) {
	t.Parallel()
	exec := &clockParityExec{}
	base := int64(1700000000)
	exec.clock = func() time.Time {
		return time.Unix(base+exec.calls.Add(1)-1, 0).UTC()
	}
	h := &openairesponses.Handler{Exec: exec}
	rc := clockParityRC(true)
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, rc, clockParityStream()); err != nil {
		t.Fatal(err)
	}
	got := createdAts(t, rec)
	if len(got) < 3 {
		t.Fatalf("created_at samples = %v, want response.created/in_progress/completed", got)
	}
	for _, ts := range got {
		if ts != base {
			t.Fatalf("wire stream created_at = %v, want single sample %d", got, base)
		}
	}
	if n := exec.calls.Load(); n != 1 {
		t.Fatalf("clock calls = %d, want exactly 1 per response", n)
	}

	exec2 := &clockParityExec{}
	exec2.clock = func() time.Time {
		return time.Unix(base+exec2.calls.Add(1)-1, 0).UTC()
	}
	h2 := &openairesponses.Handler{Exec: exec2}
	rec2 := httptest.NewRecorder()
	if err := h2.Spec().WireWriteNonStream(t.Context(), rec2, clockParityRC(false), clockParityStream()); err != nil {
		t.Fatal(err)
	}
	var v struct {
		CreatedAt int64 `json:"created_at"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.CreatedAt != base {
		t.Fatalf("wire non-stream created_at = %d, want single sample %d", v.CreatedAt, base)
	}
	if n := exec2.calls.Load(); n != 1 {
		t.Fatalf("clock calls = %d, want exactly 1 per response", n)
	}
}

func TestResponseClockParity_NilClockKeepsDeterministic(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{Exec: &clockParityExec{}}
	rc := clockParityRC(true)
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, rc, clockParityStream()); err != nil {
		t.Fatal(err)
	}
	for _, ts := range createdAts(t, rec) {
		if ts != clockParitySeedTimestamp {
			t.Fatalf("nil-clock wire stream created_at = %d, want deterministic %d", ts, clockParitySeedTimestamp)
		}
	}
	rec2 := httptest.NewRecorder()
	if err := h.Spec().WireWriteNonStream(t.Context(), rec2, clockParityRC(false), clockParityStream()); err != nil {
		t.Fatal(err)
	}
	var v struct {
		CreatedAt int64 `json:"created_at"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.CreatedAt != clockParitySeedTimestamp {
		t.Fatalf("nil-clock wire non-stream created_at = %d, want deterministic %d", v.CreatedAt, clockParitySeedTimestamp)
	}
}

// Deterministic-only convenience writers own no executor clock: they emit the
// deterministic seeds even when the handler is configured with a fixed clock.
func TestResponseClockParity_DeterministicHelpersUnchanged(t *testing.T) {
	t.Parallel()
	rc := clockParityRC(true)
	rec := httptest.NewRecorder()
	if err := openairesponses.WireWriteStreamSSE(t.Context(), rec, rc, clockParityStream(), false); err != nil {
		t.Fatal(err)
	}
	got := createdAts(t, rec)
	if len(got) == 0 {
		t.Fatal("no created_at samples in deterministic wire stream")
	}
	for _, ts := range got {
		if ts != clockParitySeedTimestamp {
			t.Fatalf("deterministic wire stream created_at = %d, want %d", ts, clockParitySeedTimestamp)
		}
	}
	rec2 := httptest.NewRecorder()
	if err := openairesponses.WireWriteNonStreamJSON(t.Context(), rec2, clockParityRC(false), clockParityStream(), false); err != nil {
		t.Fatal(err)
	}
	var v struct {
		CreatedAt int64 `json:"created_at"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.CreatedAt != clockParitySeedTimestamp {
		t.Fatalf("deterministic wire non-stream created_at = %d, want %d", v.CreatedAt, clockParitySeedTimestamp)
	}
}

func TestResponseClockParity_IdentityCarriersFramingUnchanged(t *testing.T) {
	t.Parallel()
	h := &openairesponses.Handler{Exec: clockParityFixedExec()}
	rc := clockParityRC(true)
	rc.Session = largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess-clock-1",
		ALegID:                 "aleg-clock-1",
		ResumeToken:            largebody.NewSensitiveString("resume-clock-1"),
	}
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, rc, clockParityStream()); err != nil {
		t.Fatal(err)
	}
	wantID := rc.OpenAIResponseID()
	if !strings.HasPrefix(wantID, "resp_aip_") {
		t.Fatalf("response id %q is not carrier-bound", wantID)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"`+wantID+`"`) {
		t.Fatalf("wire stream lost carrier-bound response id %q", wantID)
	}
	if !strings.Contains(body, `"id":"`+rc.OpenAIMessageID()+`"`) {
		t.Fatalf("wire stream lost deterministic message id %q", rc.OpenAIMessageID())
	}
	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess-clock-1" {
		t.Fatalf("session header = %q, want sess-clock-1", got)
	}
	if got := rec.Header().Get(sessionwire.HeaderALegID); got != "aleg-clock-1" {
		t.Fatalf("a-leg header = %q, want aleg-clock-1", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("wire stream lost [DONE] framing: %q", body)
	}
}
