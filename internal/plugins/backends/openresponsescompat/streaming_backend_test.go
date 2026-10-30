package openresponsescompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func streamingCreateCall() aipapi.Call {
	c := itemAuthorityCreateCall()
	c.Invocation.DeliveryMode = aipapi.DeliveryModeStreaming
	c.Invocation.TransportMode = aipapi.TransportModeStreaming
	return c
}

func streamingCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}}
}

func writeSSE(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl, _ := w.(http.Flusher)
	for _, c := range chunks {
		_, _ = io.WriteString(w, c)
		if fl != nil {
			fl.Flush()
		}
	}
}

func TestObserver_StreamingRequestExactWireShape(t *testing.T) {
	t.Setenv("MY_OR_KEY", "sk-stream-secret")
	be, obs := newObserverBackend(t, "api_key_env_var_root: MY_OR_KEY\n", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, textSSEBody())
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	events := drainManagedEvents(t, es)
	if !hasTextDelta(events, "Hello") || !hasTextDelta(events, " world") {
		t.Fatalf("missing text deltas in %+v", events)
	}

	if obs.count() != 1 {
		t.Fatalf("observer request count = %d, want 1", obs.count())
	}
	req := obs.last(t)
	if req.Method != http.MethodPost || req.Path != "/responses" {
		t.Fatalf("method/path = %s %s", req.Method, req.Path)
	}
	if accept := req.Header.Get("Accept"); !strings.Contains(strings.ToLower(accept), "text/event-stream") {
		t.Fatalf("accept = %q, want text/event-stream", accept)
	}
	if auth := req.Header.Get("Authorization"); auth != "Bearer sk-stream-secret" {
		t.Fatalf("authorization = %q", auth)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if string(payload["stream"]) != "true" {
		t.Fatalf("stream = %s, want true", string(payload["stream"]))
	}
	if string(payload["model"]) != `"model-x"` {
		t.Fatalf("model = %s", string(payload["model"]))
	}
}

func TestObserver_StreamingLifecyclePreserved(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, textSSEBody())
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	events := drainManagedEvents(t, es)
	want := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventTextDelta,
		aipapi.EventUsageDelta,
		aipapi.EventResponseFinished,
	}
	assertKinds(t, kindsOf(events), want)
	if !hasEventKind(events, aipapi.EventUsageDelta) {
		t.Fatalf("missing usage in %+v", events)
	}
}

func TestObserver_StreamingHTTPStatusClassification(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		wantRecover bool
	}{
		{name: "rate_limit_429", status: http.StatusTooManyRequests, wantRecover: true},
		{name: "server_500", status: http.StatusInternalServerError, wantRecover: true},
		{name: "bad_gateway_502", status: http.StatusBadGateway, wantRecover: true},
		{name: "unauthorized_401", status: http.StatusUnauthorized, wantRecover: false},
		{name: "validation_422", status: http.StatusUnprocessableEntity, wantRecover: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "boom", tc.status)
			})
			_, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
			if err == nil {
				t.Fatal("expected error")
			}
			if got := aipapi.IsRecoverablePreOutput(err); got != tc.wantRecover {
				t.Fatalf("recoverable = %v, want %v (err=%v)", got, tc.wantRecover, err)
			}
		})
	}
}

func TestObserver_StreamingContentTypeRejected(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "{}")
	})
	_, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err == nil {
		t.Fatal("expected content-type rejection")
	}
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output content-type failure must be retryable, got %v", err)
	}
}

func TestObserver_StreamingFirstProviderErrorIsRetryable(t *testing.T) {
	// A provider may terminate immediately with an error-shaped response.
	// It produced no usable canonical output, so the adapter must classify this
	// as a recoverable pre-output failure rather than committing the attempt.
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEvent("response.failed", `{"type":"response.failed","sequence_number":0,"response":{"id":"r","status":"failed","model":"m","output":[],"error":{"type":"server_error","code":"provider_broken","message":"upstream failed"}}}`))
	})
	_, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err == nil {
		t.Fatal("expected first provider error to reject stream opening")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("first provider error must be retryable, got %v", err)
	}
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
	var streamErr *aipapi.StreamError
	if !errors.As(err, &streamErr) {
		t.Fatalf("error = %v, want structured StreamError", err)
	}
	if streamErr.Code != "provider_broken" || streamErr.Message != "upstream reported an error" {
		t.Fatalf("stream error details = %#v, want provider_broken/stable sanitized message", streamErr)
	}
}

func TestObserver_StreamingPreOutputProtocolErrorRetryable(t *testing.T) {
	// The server streams a protocol violation as the first record: the SSE
	// event field disagrees with the body type. No canonical output was
	// committed, so core must be able to fail over.
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEvent("response.created", `{"type":"response.completed"}`)+"data: [DONE]\n\n")
	})
	_, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err == nil {
		t.Fatal("expected pre-output protocol rejection")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output protocol error must be retryable, got %v", err)
	}
	if !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
}

func TestObserver_StreamingPreOutputTransportDisconnectRetryable(t *testing.T) {
	// Server sends headers + flushes, then aborts the connection before any
	// event body. The peek fails on the wire: still pre-output and retryable.
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		panic(http.ErrAbortHandler)
	})
	_, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err == nil {
		t.Fatal("expected pre-output transport failure")
	}
	if !aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("pre-output transport failure must be retryable, got %v", err)
	}
}

func TestObserver_StreamingPostOutputDisconnectIsStreamError(t *testing.T) {
	// The peek commits on response.created; a later provider disconnect must
	// surface as a stream error the caller cannot fail over on.
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEvent("response.created", `{"type":"response.created","sequence_number":0}`))
		panic(http.ErrAbortHandler)
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()

	ev, err := es.Recv(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first event = %+v", ev)
	}
	_, err = es.Recv(context.Background())
	if err == nil {
		t.Fatal("expected post-output stream error")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-output disconnect must not be retryable: %v", err)
	}
}

func TestObserver_StreamingNoEchoOfBodyInError(t *testing.T) {
	secret := "sk-stream-body-secret"
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "auth failed "+secret, http.StatusUnauthorized)
	})
	_, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed body secret: %v", err)
	}
}

func TestObserver_StreamingSlowConsumerBackpressure(t *testing.T) {
	release := make(chan struct{})
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEvent("response.created", `{"type":"response.created","sequence_number":0}`))
		<-release
		writeSSE(w, sseEvent("response.completed", `{"type":"response.completed","sequence_number":1,"response":{"id":"r","status":"completed","model":"m","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)+"data: [DONE]\n\n")
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()

	// The committed stream's first Recv returns the peeked event; the next
	// Recv must wait for the server write, proving the adapter does not read
	// ahead of consumption.
	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("peeked event = %+v, %v", ev, err)
	}
	got := make(chan struct{})
	var secondEv aipapi.Event
	go func() {
		secondEv, _ = es.Recv(context.Background())
		close(got)
	}()
	select {
	case <-got:
		t.Fatal("second Recv returned before the server released the next record")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("second Recv did not return after the server released the record")
	}
	// The completed record maps to usage_delta then response_finished; the
	// goroutine consumed the first of those.
	if secondEv.Kind != aipapi.EventUsageDelta {
		t.Fatalf("second event = %+v, want usage_delta", secondEv)
	}
	rest, err := drainStream(es)
	if err != nil {
		t.Fatal(err)
	}
	assertKinds(t, kindsOf(rest), []aipapi.EventKind{aipapi.EventResponseFinished})
}

func TestObserver_StreamingCancellationClosesBody(t *testing.T) {
	release := make(chan struct{})
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEvent("response.created", `{"type":"response.created","sequence_number":0}`))
		<-release
		writeSSE(w, sseEvent("response.completed", `{"type":"response.completed","sequence_number":1}`)+"data: [DONE]\n\n")
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()

	// Consume the peeked first event; the next Recv then blocks on the wire.
	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("peeked event = %+v, %v", ev, err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = es.Recv(context.Background())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("Recv returned before the server released the next record")
	case <-time.After(50 * time.Millisecond):
	}
	res := es.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelClientGone})
	if res.Mode != aipapi.CancelModeTransport {
		t.Fatalf("cancel mode = %q", res.Mode)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Recv did not unblock after Cancel")
	}
	if err := es.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
}

func TestObserver_StreamingMalformedEventTypeRejected(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(
			w,
			sseEvent("response.created", `{"type":"response.created","sequence_number":0}`),
			sseEvent("response.bogus", `{"type":"response.bogus","sequence_number":1}`),
			"data: [DONE]\n\n",
		)
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()
	if ev, err := es.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventResponseStarted {
		t.Fatalf("first recv = %+v, %v", ev, err)
	}
	if _, err := es.Recv(context.Background()); err == nil || !errors.Is(err, ErrMalformedResponse) {
		t.Fatalf("error = %v, want ErrMalformedResponse", err)
	}
}

func TestObserver_StreamingNoAuthSendsNoAuthHeader(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, textSSEBody())
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	_ = drainManagedEvents(t, es)
	req := obs.last(t)
	if auth := req.Header.Get("Authorization"); auth != "" {
		t.Fatalf("no-auth mode must not send Authorization header, got %q", auth)
	}
}

func TestClassifyStreamOpenError_CommitmentBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		err         error
		wantRecover bool
	}{
		{name: "protocol_error", err: errors.New("my-or: " + ErrMalformedResponse.Error() + ": bad event"), wantRecover: true},
		{name: "plain_error", err: errors.New("boom"), wantRecover: true},
		{name: "already_recoverable", err: aipapi.RecoverablePreOutputError(errors.New("transport")), wantRecover: true},
		{name: "context_canceled", err: context.Canceled, wantRecover: false},
		{name: "deadline_exceeded", err: context.DeadlineExceeded, wantRecover: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := aipapi.IsRecoverablePreOutput(classifyStreamOpenError(tc.err)); got != tc.wantRecover {
				t.Fatalf("recoverable = %v, want %v", got, tc.wantRecover)
			}
		})
	}
}

func TestObserver_StreamingUnsupportedTransportRejectedBeforeNetwork(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, textSSEBody())
	})
	call := itemAuthorityCreateCall()
	call.Invocation.TransportMode = aipapi.TransportMode("snail_mail")
	_, err := be.Open(context.Background(), call, streamingCandidate())
	if err == nil {
		t.Fatal("expected unsupported transport rejection")
	}
	if obs.count() != 0 {
		t.Fatalf("unsupported transport caused %d round trips, want 0", obs.count())
	}
}

func TestObserver_StreamingMalformedEventCountsOneRequest(t *testing.T) {
	be, obs := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, sseEvent("response.created", `{"type":"response.completed"}`))
	})
	_, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err == nil {
		t.Fatal("expected pre-output rejection")
	}
	if obs.count() != 1 {
		t.Fatalf("request count = %d, want exactly 1", obs.count())
	}
}

func TestObserver_StreamingExtendedEventPreserved(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(
			w,
			sseEvent("response.created", `{"type":"response.created","sequence_number":0}`),
			sseEvent("acme:telemetry", `{"type":"acme:telemetry","sequence_number":1,"latency_ms":4}`),
			sseEvent("response.completed", `{"type":"response.completed","sequence_number":2,"response":{"id":"r","status":"completed","model":"m","output":[]}}`),
			"data: [DONE]\n\n",
		)
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	events := drainManagedEvents(t, es)
	assertKinds(t, kindsOf(events), []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventResponseFinished,
	})
}

func TestObserver_StreamingToolAndReasoningDeltas(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(
			w,
			sseEvent("response.created", `{"type":"response.created","sequence_number":0}`),
			sseEvent("response.output_item.added", `{"type":"response.output_item.added","sequence_number":1,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather"}}`),
			sseEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":2,"call_id":"call_1","delta":"{\"loc"}`),
			sseEvent("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","sequence_number":3,"call_id":"call_1","delta":"ation\":\"Paris\"}"}`),
			sseEvent("response.output_item.done", `{"type":"response.output_item.done","sequence_number":4,"item":{"id":"fc_1","type":"function_call","call_id":"call_1","name":"get_weather"}}`),
			sseEvent("response.output_item.added", `{"type":"response.output_item.added","sequence_number":5,"item":{"id":"rs_1","type":"reasoning","status":"in_progress"}}`),
			sseEvent("response.reasoning_text.delta", `{"type":"response.reasoning_text.delta","sequence_number":6,"item_id":"rs_1","delta":"check weather"}`),
			sseEvent("response.output_item.done", `{"type":"response.output_item.done","sequence_number":7,"item":{"id":"rs_1","type":"reasoning","status":"completed"}}`),
			sseEvent("response.completed", `{"type":"response.completed","sequence_number":8,"response":{"id":"r","status":"completed","model":"m","output":[]}}`),
			"data: [DONE]\n\n",
		)
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	events := drainManagedEvents(t, es)
	var toolArgs, reasoning bool
	var toolFinished bool
	for _, ev := range events {
		switch ev.Kind {
		case aipapi.EventToolCallArgsDelta:
			if ev.ToolCallID == "call_1" && ev.Delta == `{"loc` {
				toolArgs = true
			}
		case aipapi.EventToolCallFinished:
			if ev.ToolCallID == "call_1" {
				toolFinished = true
			}
		case aipapi.EventReasoningDelta:
			if ev.Delta == "check weather" {
				reasoning = true
			}
		}
	}
	for name, ok := range map[string]bool{"toolArgs": toolArgs, "toolFinished": toolFinished, "reasoning": reasoning} {
		if !ok {
			t.Fatalf("missing %s in %+v", name, events)
		}
	}
}

func TestObserver_StreamingProviderErrorSanitized(t *testing.T) {
	be, _ := newObserverBackend(t, "", func(w http.ResponseWriter, r *http.Request) {
		writeSSE(
			w,
			sseEvent("response.created", `{"type":"response.created","sequence_number":0}`),
			sseEvent("error", `{"type":"error","code":"provider_broken","message":"boom with secret sk-abc"}`),
			sseEvent("response.failed", `{"type":"response.failed","sequence_number":2,"response":{"id":"r","status":"failed","model":"m","output":[]}}`),
			"data: [DONE]\n\n",
		)
	})
	es, err := be.Open(context.Background(), streamingCreateCall(), streamingCandidate())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = es.Close() }()
	var last aipapi.Event
	for {
		ev, err := es.Recv(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		last = ev
	}
	if last.Kind != aipapi.EventError || last.ErrorCode != "provider_broken" {
		t.Fatalf("last = %+v", last)
	}
	if strings.Contains(last.ErrorMessage, "sk-abc") {
		t.Fatalf("error message echoed secret: %q", last.ErrorMessage)
	}
}
