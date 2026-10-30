package openailegacy_test

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
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Response-clock parity (spec large-payload-proof-and-parity 5.1-5.4):
// wire Chat writers must honor the configured executor wall clock exactly like
// canonical BuildEncodeOpts, sampling once per response, while the direct
// WireWrite* helpers stay deterministic-only and identity/carriers/framing stay
// unchanged.

const (
	chatClockFixedCreatedAt int64 = 1700000000
	chatClockSeedTimestamp  int64 = 1715620000
	chatClockSeedToken            = "clockparity-seedtest-token"
)

type chatClockExec struct {
	calls atomic.Int64
	clock func() time.Time
}

func (e *chatClockExec) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseFinished}}), nil
}

func (e *chatClockExec) CancelALeg(_ context.Context, req aipapi.ALegCancelRequest) error { return nil }

func (e *chatClockExec) WallClock() func() time.Time { return e.clock }

func chatClockFixedExec() *chatClockExec {
	return &chatClockExec{clock: func() time.Time { return time.Unix(chatClockFixedCreatedAt, 0).UTC() }}
}

func chatClockRC(stream bool) frontendpipe.ResponseContext {
	delivery := aipapi.DeliveryModeNonStreaming
	if stream {
		delivery = aipapi.DeliveryModeStreaming
	}
	return frontendpipe.ResponseContext{
		State: frontendpipe.FrontendWireState{
			Seeds: frontendpipe.ResponseStateSeeds{
				DeterministicCallID:    "call_" + chatClockSeedToken,
				DeterministicTimestamp: chatClockSeedTimestamp,
				DeterministicToken:     chatClockSeedToken,
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

func chatClockStream() aipapi.EventStream {
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "clock-parity"},
		{Kind: aipapi.EventResponseFinished},
	})
}

// createdValues extracts every chat chunk/completion "created" value.
func createdValues(t *testing.T, rec *httptest.ResponseRecorder) []int64 {
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
		raw, ok := v["created"]
		if !ok {
			continue
		}
		f, ok := raw.(float64)
		if !ok {
			t.Fatalf("created is not numeric in %q", fr.Data)
		}
		out = append(out, int64(f))
	}
	return out
}

func TestResponseClockParity_WireStreamUsesFixedClock(t *testing.T) {
	t.Parallel()
	h := &openailegacy.Handler{Exec: chatClockFixedExec()}
	rc := chatClockRC(true)
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, rc, chatClockStream()); err != nil {
		t.Fatal(err)
	}
	got := createdValues(t, rec)
	if len(got) < 2 {
		t.Fatalf("created samples = %v, want opening and terminal chunks", got)
	}
	for _, ts := range got {
		if ts != chatClockFixedCreatedAt {
			t.Fatalf("wire stream created = %v, want literal %d", got, chatClockFixedCreatedAt)
		}
	}
}

func TestResponseClockParity_WireNonStreamUsesFixedClock(t *testing.T) {
	t.Parallel()
	h := &openailegacy.Handler{Exec: chatClockFixedExec()}
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteNonStream(t.Context(), rec, chatClockRC(false), chatClockStream()); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Created != chatClockFixedCreatedAt {
		t.Fatalf("wire non-stream created = %d, want literal %d", v.Created, chatClockFixedCreatedAt)
	}
}

func TestResponseClockParity_AdvancingClockSampledOnce(t *testing.T) {
	t.Parallel()
	exec := &chatClockExec{}
	base := int64(1700000000)
	exec.clock = func() time.Time {
		return time.Unix(base+exec.calls.Add(1)-1, 0).UTC()
	}
	h := &openailegacy.Handler{Exec: exec}
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, chatClockRC(true), chatClockStream()); err != nil {
		t.Fatal(err)
	}
	got := createdValues(t, rec)
	if len(got) < 2 {
		t.Fatalf("created samples = %v, want opening and terminal chunks", got)
	}
	for _, ts := range got {
		if ts != base {
			t.Fatalf("wire stream created = %v, want single sample %d", got, base)
		}
	}
	if n := exec.calls.Load(); n != 1 {
		t.Fatalf("clock calls = %d, want exactly 1 per response", n)
	}

	exec2 := &chatClockExec{}
	exec2.clock = func() time.Time {
		return time.Unix(base+exec2.calls.Add(1)-1, 0).UTC()
	}
	h2 := &openailegacy.Handler{Exec: exec2}
	rec2 := httptest.NewRecorder()
	if err := h2.Spec().WireWriteNonStream(t.Context(), rec2, chatClockRC(false), chatClockStream()); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Created != base {
		t.Fatalf("wire non-stream created = %d, want single sample %d", v.Created, base)
	}
	if n := exec2.calls.Load(); n != 1 {
		t.Fatalf("clock calls = %d, want exactly 1 per response", n)
	}
}

func TestResponseClockParity_NilClockKeepsDeterministic(t *testing.T) {
	t.Parallel()
	h := &openailegacy.Handler{Exec: &chatClockExec{}}
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, chatClockRC(true), chatClockStream()); err != nil {
		t.Fatal(err)
	}
	for _, ts := range createdValues(t, rec) {
		if ts != chatClockSeedTimestamp {
			t.Fatalf("nil-clock wire stream created = %d, want deterministic %d", ts, chatClockSeedTimestamp)
		}
	}
	rec2 := httptest.NewRecorder()
	if err := h.Spec().WireWriteNonStream(t.Context(), rec2, chatClockRC(false), chatClockStream()); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Created != chatClockSeedTimestamp {
		t.Fatalf("nil-clock wire non-stream created = %d, want deterministic %d", v.Created, chatClockSeedTimestamp)
	}
}

// Deterministic-only convenience writers own no executor clock: they emit the
// deterministic seeds even when the handler is configured with a fixed clock.
func TestResponseClockParity_DeterministicHelpersUnchanged(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	if err := openailegacy.WireWriteStreamSSE(t.Context(), rec, chatClockRC(true), chatClockStream(), false); err != nil {
		t.Fatal(err)
	}
	got := createdValues(t, rec)
	if len(got) == 0 {
		t.Fatal("no created samples in deterministic wire stream")
	}
	for _, ts := range got {
		if ts != chatClockSeedTimestamp {
			t.Fatalf("deterministic wire stream created = %d, want %d", ts, chatClockSeedTimestamp)
		}
	}
	rec2 := httptest.NewRecorder()
	if err := openailegacy.WireWriteNonStreamJSON(t.Context(), rec2, chatClockRC(false), chatClockStream(), false); err != nil {
		t.Fatal(err)
	}
	var v struct {
		Created int64 `json:"created"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Created != chatClockSeedTimestamp {
		t.Fatalf("deterministic wire non-stream created = %d, want %d", v.Created, chatClockSeedTimestamp)
	}
}

func TestResponseClockParity_IdentityCarriersFramingUnchanged(t *testing.T) {
	t.Parallel()
	h := &openailegacy.Handler{Exec: chatClockFixedExec()}
	rc := chatClockRC(true)
	rc.Session = largebody.SessionResponseCarrier{
		AuthoritativeSessionID: "sess-chat-clock-1",
		ALegID:                 "aleg-chat-clock-1",
		ResumeToken:            largebody.NewSensitiveString("resume-chat-clock-1"),
	}
	rec := httptest.NewRecorder()
	if err := h.Spec().WireWriteStream(t.Context(), rec, rc, chatClockStream()); err != nil {
		t.Fatal(err)
	}
	wantID := rc.OpenAIChatCompletionID()
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"`+wantID+`"`) {
		t.Fatalf("wire stream lost deterministic completion id %q", wantID)
	}
	if got := rec.Header().Get(sessionwire.HeaderAuthoritativeSessionID); got != "sess-chat-clock-1" {
		t.Fatalf("session header = %q, want sess-chat-clock-1", got)
	}
	if got := rec.Header().Get(sessionwire.HeaderALegID); got != "aleg-chat-clock-1" {
		t.Fatalf("a-leg header = %q, want aleg-chat-clock-1", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream; charset=utf-8" {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("wire stream lost [DONE] framing: %q", body)
	}
}
