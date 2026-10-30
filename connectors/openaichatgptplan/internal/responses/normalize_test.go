package responses

// Canonical stream proofs for the Responses normalizer (spec task 5.2).
//
// The provider stream becomes canonical aipapi events in the provider's order.
// The terminal rule is the whole point of this file: `response.completed` is
// the ONLY success terminal, so a stream that merely ENDS - or that ends with
// a different terminal shape - is a failure, never a success with partial
// output. The two obligations the 5.1 seam left open are pinned here: EOF is
// not success (N2) and a mid-stream failure stays correlated with the provider
// request id (S2).
//
// Every terminal proof fails if the terminal check is replaced by an
// end-of-stream success: the tests assert the typed failure, that io.EOF was
// NOT reported as the outcome, and that no `response_finished` event was
// emitted. The no-replay proof fails if a replay is allowed after visible
// output.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// normalizedFixture builds the normalizer over one fixed SSE body.
func normalizedFixture(t *testing.T, body string) *NormalizedStream {
	t.Helper()
	raw := newStream(context.Background(), io.NopCloser(strings.NewReader(body)), "acct-one", [32]byte{})
	t.Cleanup(func() { _ = raw.Close() })
	return raw.Normalize()
}

// drainStream reads every canonical event and the terminal outcome.
func drainStream(t *testing.T, stream *NormalizedStream) ([]aipapi.Event, error) {
	t.Helper()
	var events []aipapi.Event
	for {
		event, err := stream.Recv(context.Background())
		if err != nil {
			return events, err
		}
		events = append(events, event)
	}
}

// requireEvents compares the canonical events with the exact expected list.
func requireEvents(t *testing.T, got, want []aipapi.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("the canonical events = %#v, want %#v", got, want)
	}
	for index := range want {
		if !reflect.DeepEqual(got[index], want[index]) {
			t.Fatalf("canonical event %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

// requireStreamFailure asserts the typed stream failure and its reason.
func requireStreamFailure(t *testing.T, err error, want StreamFailureReason) *StreamFailure {
	t.Helper()
	if err == nil {
		t.Fatalf("the stream ended with no outcome, want the %q failure", want)
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("the stream reported a clean end of stream (%v), want the %q failure", err, want)
	}
	if !errors.Is(err, ErrStreamFailed) {
		t.Fatalf("the stream failure = %v, want ErrStreamFailed", err)
	}
	var failure *StreamFailure
	if !errors.As(err, &failure) {
		t.Fatalf("the stream failure is %T, want a *StreamFailure: %v", err, err)
	}
	if failure.Reason != want {
		t.Fatalf("the stream failure reason = %q, want %q", failure.Reason, want)
	}
	return failure
}

// requireNoFinishedEvent proves no success terminal was emitted.
func requireNoFinishedEvent(t *testing.T, events []aipapi.Event) {
	t.Helper()
	for index, event := range events {
		if event.Kind == aipapi.EventResponseFinished {
			t.Fatalf("event %d is a success terminal %#v, want no success terminal", index, event)
		}
	}
}

// TestNormalizedStream_MapsTheDocumentedEventsInProviderOrder proves the
// documented text, tool, reasoning, usage and completion events become the
// canonical stream in the provider's order, with the provider's content.
func TestNormalizedStream_MapsTheDocumentedEventsInProviderOrder(t *testing.T) {
	t.Parallel()
	const body = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"lo"}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","text":"Hello"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"{\"city\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"\"Paris\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"Checking the forecast"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}}

`
	events, terminal := drainStream(t, normalizedFixture(t, body))
	if !errors.Is(terminal, io.EOF) {
		t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
	}
	want := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "Hel"},
		{Kind: aipapi.EventTextDelta, Delta: "lo"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"city":`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `"Paris"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventReasoningDelta, Delta: "Checking the forecast"},
		{
			Kind:            aipapi.EventUsageDelta,
			InputTokens:     11,
			OutputTokens:    7,
			TotalTokens:     18,
			CacheReadTokens: 3,
			ReasoningTokens: 2,
			UsagePresence: aipapi.UsagePresence{
				InputTokens:     true,
				OutputTokens:    true,
				TotalTokens:     true,
				CacheReadTokens: true,
				ReasoningTokens: true,
			},
		},
		{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"},
	}
	requireEvents(t, events, want)
}

// TestNormalizedStream_DoesNotDuplicateCompletedValues proves a value the
// provider sent once reaches the client once, whichever documented carrier
// carried it.
//
// The stream may deliver text and arguments as deltas and then repeat the
// completed value in `response.output_text.done`, `response.output_item.done`
// and the terminal's `response.output`. Only the first carrier may emit.
func TestNormalizedStream_DoesNotDuplicateCompletedValues(t *testing.T) {
	t.Parallel()
	const body = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","text":"Hello there"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","item_id":"fc_1","call_id":"call_1","arguments":"{\"city\":\"Paris\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Hello there"}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}]}}

`
	events, terminal := drainStream(t, normalizedFixture(t, body))
	if !errors.Is(terminal, io.EOF) {
		t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
	}
	want := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "Hello there"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"city":"Paris"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"},
	}
	requireEvents(t, events, want)
}

// TestNormalizedStream_RequiresTheCompletedTerminal proves a stream that ENDS
// without `response.completed` is a failure, not a success with partial output.
//
// A proof that accepted end of stream as success would return io.EOF here and
// would have emitted a `response_finished` event; the assertions below reject
// both.
func TestNormalizedStream_RequiresTheCompletedTerminal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want []aipapi.Event
	}{
		{
			name: "an answer that ends before any event",
			body: "",
			want: nil,
		},
		{
			name: "an answer that only starts",
			body: `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

`,
			want: []aipapi.Event{{Kind: aipapi.EventResponseStarted}},
		},
		{
			name: "an answer that streams content and then ends",
			body: `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"partial"}

`,
			want: []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "partial"},
			},
		},
		{
			name: "an answer that only reports progress",
			body: `event: response.in_progress
data: {"type":"response.in_progress","response":{"id":"resp_1"}}

`,
			want: nil,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stream := normalizedFixture(t, testCase.body)
			events, terminal := drainStream(t, stream)
			requireEvents(t, events, testCase.want)
			requireNoFinishedEvent(t, events)
			_ = requireStreamFailure(t, terminal, StreamFailureMissingTerminal)
			// The failure is terminal: the same answer cannot become a
			// success on a later read.
			if _, again := stream.Recv(context.Background()); !errors.Is(again, ErrStreamFailed) {
				t.Fatalf("the ended stream changed its outcome to %v, want the same failure", again)
			}
		})
	}
}

// TestNormalizedStream_RefusesEveryOtherTerminal proves only
// `response.completed` succeeds; every other terminal shape is a distinct
// failure, and a `response.completed` carrying a contradictory status is a
// failure too.
func TestNormalizedStream_RefusesEveryOtherTerminal(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		body       string
		wantReason StreamFailureReason
		wantCode   string
	}{
		{
			name: "incomplete",
			body: `event: response.incomplete
data: {"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}

`,
			wantReason: StreamFailureTerminalIncomplete,
			wantCode:   "max_output_tokens",
		},
		{
			name: "failed",
			body: `event: response.failed
data: {"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"server_error","message":"the provider failed"}}}

`,
			wantReason: StreamFailureTerminalFailed,
			wantCode:   "server_error",
		},
		{
			name: "cancelled",
			body: `event: response.cancelled
data: {"type":"response.cancelled","response":{"id":"resp_1","status":"cancelled"}}

`,
			wantReason: StreamFailureTerminalCancelled,
		},
		{
			name: "completed with a contradictory status",
			body: `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"incomplete"}}

`,
			wantReason: StreamFailureContradictoryEvent,
		},
		{
			name: "completed named but failed typed",
			body: `event: response.completed
data: {"type":"response.failed","response":{"id":"resp_1","status":"failed"}}

`,
			wantReason: StreamFailureContradictoryEvent,
		},
		{
			name: "a stream error",
			body: `event: error
data: {"type":"error","error":{"code":"subscription_sharing_usage_limit_exceeded","message":"the plan usage limit was reached"}}

`,
			wantReason: StreamFailureProviderError,
			wantCode:   "subscription_sharing_usage_limit_exceeded",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events, terminal := drainStream(t, normalizedFixture(t, testCase.body))
			requireNoFinishedEvent(t, events)
			failure := requireStreamFailure(t, terminal, testCase.wantReason)
			if testCase.wantCode != "" && failure.Code != testCase.wantCode {
				t.Fatalf("the failure code = %q, want %q", failure.Code, testCase.wantCode)
			}
		})
	}
}

// TestNormalizedStream_AcceptsTheCompletedTerminal is the positive control of
// the terminal rule: the same shape the failures above are compared with does
// succeed, so `RefusesEveryOtherTerminal` fails for its named reason and not
// because no terminal is ever accepted.
func TestNormalizedStream_AcceptsTheCompletedTerminal(t *testing.T) {
	t.Parallel()
	events, terminal := drainStream(t, normalizedFixture(t, `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`))
	if !errors.Is(terminal, io.EOF) {
		t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
	}
	want := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"},
	}
	requireEvents(t, events, want)
}

// TestNormalizedStream_RefusesArrivedButUnusableEvents proves an event that
// ARRIVED and cannot be used is a stream failure, never skipped as if absent.
//
// Each case places the unusable event after visible output, so a normalizer
// that silently dropped it would end the stream successfully (the terminal is
// missing) or would report only the later end of stream. The reason assertions
// prove which event was refused.
func TestNormalizedStream_RefusesArrivedButUnusableEvents(t *testing.T) {
	t.Parallel()
	const preamble = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"visible"}

`
	cases := []struct {
		name       string
		event      string
		wantReason StreamFailureReason
	}{
		{
			name:       "a payload that is not JSON",
			event:      "event: response.output_text.delta\ndata: {not json\n\n",
			wantReason: StreamFailureMalformedEvent,
		},
		{
			name:       "a payload that is not a JSON object",
			event:      "event: response.output_text.delta\ndata: [1,2,3]\n\n",
			wantReason: StreamFailureMalformedEvent,
		},
		{
			// JSON `null` unmarshals into a struct as an all-zero value, so a
			// decoder without the object guard silently skips this delta and
			// lets the following valid completion succeed.
			name:       "a null payload before a valid terminal",
			event:      "event: response.output_text.delta\ndata: null\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\"}}\n\n",
			wantReason: StreamFailureMalformedEvent,
		},
		{
			name:       "an event with no type discriminator",
			event:      "data: {\"delta\":\"x\"}\n\n",
			wantReason: StreamFailureMalformedEvent,
		},
		{
			name:       "an output item with no type",
			event:      "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"x\"}}\n\n",
			wantReason: StreamFailureMalformedEvent,
		},
		{
			name:       "an output item this connector never asked for",
			event:      "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"web_search_call\",\"id\":\"ws_1\"}}\n\n",
			wantReason: StreamFailureUnrepresentableItem,
		},
		{
			name:       "tool arguments with no identity",
			event:      "event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"delta\":\"{}\"}\n\n",
			wantReason: StreamFailureToolIdentityMissing,
		},
		{
			name:       "an event type the connector does not know",
			event:      "event: response.something_new\ndata: {\"type\":\"response.something_new\"}\n\n",
			wantReason: StreamFailureUnrecognizedEvent,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			stream := normalizedFixture(t, preamble+testCase.event)
			events, terminal := drainStream(t, stream)
			requireNoFinishedEvent(t, events)
			_ = requireStreamFailure(t, terminal, testCase.wantReason)
			// The visible output that arrived BEFORE the unusable event was
			// not withheld: nothing is silently dropped from either side.
			wantPrefix := []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "visible"},
			}
			requireEvents(t, events, wantPrefix)
		})
	}
}

// failingAfterReader returns a prepared prefix and then a prepared failure, so
// a stream can fail in the middle of an answer.
type failingAfterReader struct {
	data []byte
	err  error
}

// Read returns the prefix first, then the prepared failure.
func (r *failingAfterReader) Read(p []byte) (int, error) {
	if len(r.data) > 0 {
		n := copy(p, r.data)
		r.data = r.data[n:]
		return n, nil
	}
	return 0, r.err
}

// Close is a no-op; the stream owns the reader.
func (r *failingAfterReader) Close() error { return nil }

// TestNormalizedStream_NoReplayAfterVisibleOutput proves that once a
// client-visible event has been emitted, the managed stream carries a marker
// that forbids a transparent replay.
//
// Before visible output a retry is still permissible (core owns that policy),
// so the positive control proves the marker is not a constant refusal. After
// visible output - including after a mid-stream failure - the refusal is
// stable, so a caller cannot silently re-execute committed output.
func TestNormalizedStream_NoReplayAfterVisibleOutput(t *testing.T) {
	t.Parallel()
	prefix := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"visible\"}\n\n"
	raw := newStream(
		context.Background(),
		&failingAfterReader{data: []byte(prefix), err: ErrStreamUnreadable},
		"acct-one",
		[32]byte{},
	)
	defer func() { _ = raw.Close() }()
	stream := raw.Normalize()

	// Lifecycle frames open the canonical envelope without committing output,
	// so a retry is still permissible until the first visible event.
	var visible aipapi.Event
	for {
		event, err := stream.Recv(context.Background())
		if err != nil {
			t.Fatalf("the lifecycle event: %v", err)
		}
		if event.Kind == aipapi.EventTextDelta {
			visible = event
			break
		}
		if event.Kind != aipapi.EventResponseStarted && event.Kind != aipapi.EventMessageStarted {
			t.Fatalf("the lifecycle event = %#v, want only envelope frames", event)
		}
		if stream.OutputCommitted() {
			t.Fatalf("the lifecycle event %q committed output", event.Kind)
		}
		if err := stream.ReplayAllowed(); err != nil {
			t.Fatalf("a replay before any visible output was refused: %v", err)
		}
	}

	if visible.Delta != "visible" {
		t.Fatalf("the visible event = %#v, want the provider's text delta", visible)
	}
	if !stream.OutputCommitted() {
		t.Fatal("visible output was emitted but the stream did not mark it committed")
	}
	if err := stream.ReplayAllowed(); !errors.Is(err, ErrReplayAfterVisibleOutput) {
		t.Fatalf("a replay after visible output returned %v, want ErrReplayAfterVisibleOutput", err)
	}

	if _, err := stream.Recv(context.Background()); !errors.Is(err, ErrStreamUnreadable) {
		t.Fatalf("the mid-stream failure = %v, want ErrStreamUnreadable", err)
	}
	if !stream.OutputCommitted() {
		t.Fatal("the failure cleared the committed-output marker")
	}
	if err := stream.ReplayAllowed(); !errors.Is(err, ErrReplayAfterVisibleOutput) {
		t.Fatalf("a replay after a post-output failure returned %v, want the same refusal", err)
	}
}

// TestNormalizedStream_NoReplayAfterMissingTerminal proves the marker survives
// the other mid-stream outcome: an answer that ends after visible output
// without the completed terminal is a failure AND cannot be replayed.
func TestNormalizedStream_NoReplayAfterMissingTerminal(t *testing.T) {
	t.Parallel()
	const body = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"visible"}

`
	stream := normalizedFixture(t, body)
	events, terminal := drainStream(t, stream)
	requireNoFinishedEvent(t, events)
	_ = requireStreamFailure(t, terminal, StreamFailureMissingTerminal)
	if !stream.OutputCommitted() {
		t.Fatal("visible output was emitted but the stream did not mark it committed")
	}
	if err := stream.ReplayAllowed(); !errors.Is(err, ErrReplayAfterVisibleOutput) {
		t.Fatalf("a replay after committed output returned %v, want ErrReplayAfterVisibleOutput", err)
	}
}

// TestNormalizedStream_CapturesTheProviderRequestID proves the transport keeps
// the provider's correlation id on a SUCCESSFUL event-stream answer (the 5.1
// S2 obligation), so a mid-stream failure can be correlated with the provider.
func TestNormalizedStream_CapturesTheProviderRequestID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		header    string
		value     string
		wantValue string
	}{
		{name: "x-request-id", header: "X-Request-Id", value: "req_public_123", wantValue: "req_public_123"},
		{name: "openai-request-id", header: "Openai-Request-Id", value: "req_openai_456", wantValue: "req_openai_456"},
		{name: "absent", header: "", value: "", wantValue: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			header := make(http.Header)
			if testCase.header != "" {
				header.Set(testCase.header, testCase.value)
			}
			client := newStubClient(t, &stubTransport{
				status:      http.StatusOK,
				contentType: "text/event-stream",
				header:      header,
				body:        io.NopCloser(strings.NewReader("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")),
			})
			raw, err := client.OpenStream(context.Background(), loopbackRequest())
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			defer func() { _ = raw.Close() }()
			if got := raw.RequestID(); got != testCase.wantValue {
				t.Fatalf("the transport request id = %q, want %q", got, testCase.wantValue)
			}
			stream := raw.Normalize()
			if got := stream.RequestID(); got != testCase.wantValue {
				t.Fatalf("the normalized request id = %q, want %q", got, testCase.wantValue)
			}
			events, terminal := drainStream(t, stream)
			if !errors.Is(terminal, io.EOF) {
				t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
			}
			requireEvents(t, events, []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"},
			})
			if got := stream.RequestID(); got != testCase.wantValue {
				t.Fatalf("the request id after the terminal = %q, want %q", got, testCase.wantValue)
			}
		})
	}
}

// TestNormalizedStream_CorrelatesAMidStreamFailureWithTheRequestID proves the
// captured id travels on the typed failure, so an answer that fails after the
// status line is still correlatable.
func TestNormalizedStream_CorrelatesAMidStreamFailureWithTheRequestID(t *testing.T) {
	t.Parallel()
	header := make(http.Header)
	header.Set("X-Request-Id", "req_midstream_789")
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		header:      header,
		body: io.NopCloser(strings.NewReader(
			"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n",
		)),
	})
	raw, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = raw.Close() }()
	stream := raw.Normalize()
	_, terminal := drainStream(t, stream)
	failure := requireStreamFailure(t, terminal, StreamFailureTerminalFailed)
	if failure.RequestID != "req_midstream_789" {
		t.Fatalf("the failure request id = %q, want %q", failure.RequestID, "req_midstream_789")
	}
	if stream.RequestID() != "req_midstream_789" {
		t.Fatalf("the stream request id = %q, want %q", stream.RequestID(), "req_midstream_789")
	}
}

// TestNormalizedStream_RedactsTheRequestID proves the captured id passes the
// same redact-before-bound path as every other provider diagnostic.
func TestNormalizedStream_RedactsTheRequestID(t *testing.T) {
	t.Parallel()
	const token = "siwc-access-token-for-acct-one"
	header := make(http.Header)
	header.Set("X-Request-Id", "req_"+token+"_"+strings.Repeat("r", 4096))
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		header:      header,
		body:        io.NopCloser(strings.NewReader("data: x\n\n")),
	})
	raw, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = raw.Close() }()
	got := raw.RequestID()
	if strings.Contains(got, token) {
		t.Fatalf("bearer_leaked=true: the captured request id repeated the token: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("the echoed token was not redacted from the request id: %q", got)
	}
	if runes := len([]rune(got)); runes > maxRequestIDRunes+3 {
		t.Fatalf("the request id is %d runes, want at most %d", runes, maxRequestIDRunes+3)
	}
}

// TestNormalizedStream_RedactsSecretsBeyondTheDisplayBound proves the full
// identity (RequestIdentity) redacts a credential seated past the diagnostic
// length bound while its non-secret surroundings survive. The display form
// (RequestID) truncates that region away, so only the identity assertion can
// catch a redaction gap there.
func TestNormalizedStream_RedactsSecretsBeyondTheDisplayBound(t *testing.T) {
	t.Parallel()
	const token = "siwc-access-token-for-acct-one"
	const tail = "tailmarker"
	header := make(http.Header)
	header.Set("X-Request-Id", strings.Repeat("x", 160)+token+"_"+tail)
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		header:      header,
		body:        io.NopCloser(strings.NewReader("data: x\n\n")),
	})
	raw, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = raw.Close() }()
	identity := raw.RequestIdentity()
	if strings.Contains(identity, token) {
		t.Fatalf("bearer_leaked=true: the full identity repeated the token: %q", identity)
	}
	if !strings.Contains(identity, "[REDACTED]") {
		t.Fatalf("the echoed token was not redacted from the full identity: %q", identity)
	}
	if !strings.Contains(identity, tail) {
		t.Fatalf("redaction destroyed the non-secret suffix of the full identity: %q", identity)
	}
}

// TestNormalizedStream_KeepsTheFullRequestIdentityBeforeDisplayBounding proves
// the transport keeps TWO forms of the provider correlation id: the bounded
// diagnostic form (RequestID) that operators see, and a non-lossy identity
// (RequestIdentity) for accounting. Two headers sharing the first 160
// characters must stay distinct as identities even though their diagnostic
// forms - correctly - collapse.
func TestNormalizedStream_KeepsTheFullRequestIdentityBeforeDisplayBounding(t *testing.T) {
	t.Parallel()
	shared := strings.Repeat("x", 160)
	displays := make([]string, 0, 2)
	identities := make([]string, 0, 2)
	for _, suffix := range []string{"a", "b"} {
		want := shared + suffix
		header := make(http.Header)
		header.Set("X-Request-Id", want)
		client := newStubClient(t, &stubTransport{
			status:      http.StatusOK,
			contentType: "text/event-stream",
			header:      header,
			body:        io.NopCloser(strings.NewReader("data: x\n\n")),
		})
		raw, err := client.OpenStream(context.Background(), loopbackRequest())
		if err != nil {
			t.Fatalf("OpenStream: %v", err)
		}
		if runes := len([]rune(raw.RequestID())); runes > maxRequestIDRunes+3 {
			t.Fatalf("the diagnostic request id is %d runes, want at most %d", runes, maxRequestIDRunes+3)
		}
		if got := raw.RequestIdentity(); got != want {
			t.Fatalf("the transport request identity = %q, want the full correlation header", got)
		}
		displays = append(displays, raw.RequestID())
		identities = append(identities, raw.RequestIdentity())
		if err := raw.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if displays[0] != displays[1] {
		t.Fatalf("the diagnostic form is no longer bounded to the display prefix: %q vs %q", displays[0], displays[1])
	}
	if identities[0] == identities[1] {
		t.Fatalf("the transport identity lost the distinguishing suffix: %q", identities[0])
	}
}

// TestNormalizedStream_CollectsThroughTheCanonicalContract proves the
// normalized stream IS a canonical stream: the canonical collector aggregates
// the completed answer, and an answer without the completed terminal is an
// error, never a successful partial collection.
func TestNormalizedStream_CollectsThroughTheCanonicalContract(t *testing.T) {
	t.Parallel()
	const completed = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"Hello"}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"{}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6}}}

`
	collected, err := aipapi.Collect(context.Background(), normalizedFixture(t, completed))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !collected.FinishReceived {
		t.Fatal("the collected answer carries no terminal")
	}
	if got, want := collected.Text.String(), "Hello"; got != want {
		t.Fatalf("the collected text = %q, want %q", got, want)
	}
	calls := collected.OrderedToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "get_weather" || calls[0].Arguments != "{}" {
		t.Fatalf("the collected tool calls = %#v, want one completed get_weather call", calls)
	}
	if collected.InputTokens != 4 || collected.OutputTokens != 2 || collected.TotalTokens != 6 {
		t.Fatalf("the collected usage = (%d, %d, %d), want (4, 2, 6)",
			collected.InputTokens, collected.OutputTokens, collected.TotalTokens)
	}

	const truncated = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"partial"}

`
	collected, err = aipapi.Collect(context.Background(), normalizedFixture(t, truncated))
	if err == nil {
		t.Fatalf("an answer without the completed terminal was collected as success: %#v", collected)
	}
	if !errors.Is(err, ErrStreamFailed) {
		t.Fatalf("the collection failure = %v, want ErrStreamFailed", err)
	}
	if collected.FinishReceived {
		t.Fatal("the collected truncated answer reports a terminal")
	}
}

// TestNormalizedStream_PreservesTheDocumentedErrorIdentity proves a stream
// `error` event keeps the code and message the provider sent in its documented
// TOP-LEVEL shape, so downstream classification can read them.
//
// OpenAI's generated `response_error_event` carries nullable top-level `code`
// and `message`. The compatible nested `error` envelope and the terminal
// resource's `response.error` remain readable; the top-level fields take
// precedence, and a code that is not a string stays absent instead of becoming
// a fabricated diagnostic.
func TestNormalizedStream_PreservesTheDocumentedErrorIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name           string
		body           string
		wantCode       string
		wantDiagnostic string
	}{
		{
			name: "the documented top-level shape",
			body: `event: error
data: {"type":"error","code":"subscription_sharing_usage_limit_exceeded","message":"the plan usage limit was reached","param":null,"sequence_number":1}

`,
			wantCode:       "subscription_sharing_usage_limit_exceeded",
			wantDiagnostic: "the plan usage limit was reached",
		},
		{
			name: "top-level fields take precedence over the nested envelope",
			body: `event: error
data: {"type":"error","code":"top_level_code","message":"top level message","error":{"code":"nested_code","message":"nested message"}}

`,
			wantCode:       "top_level_code",
			wantDiagnostic: "top level message",
		},
		{
			name: "the compatible nested envelope is retained",
			body: `event: error
data: {"type":"error","error":{"code":"nested_code","message":"nested message"}}

`,
			wantCode:       "nested_code",
			wantDiagnostic: "nested message",
		},
		{
			name: "a top-level code with a nested message keeps both",
			body: `event: error
data: {"type":"error","code":"top_level_code","error":{"message":"nested message"}}

`,
			wantCode:       "top_level_code",
			wantDiagnostic: "nested message",
		},
		{
			name: "a code that is not a string stays absent",
			body: `event: error
data: {"type":"error","code":429,"message":"rate limited"}

`,
			wantDiagnostic: "rate limited",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events, terminal := drainStream(t, normalizedFixture(t, testCase.body))
			requireNoFinishedEvent(t, events)
			failure := requireStreamFailure(t, terminal, StreamFailureProviderError)
			if failure.Code != testCase.wantCode {
				t.Fatalf("the failure code = %q, want %q", failure.Code, testCase.wantCode)
			}
			if failure.Diagnostic != testCase.wantDiagnostic {
				t.Fatalf("the failure diagnostic = %q, want %q", failure.Diagnostic, testCase.wantDiagnostic)
			}
		})
	}
}

// TestNormalizedStream_BoundsTheDocumentedErrorDiagnostic proves the
// documented top-level message travels through the same bound as every other
// provider diagnostic, so one event cannot flood a failure.
func TestNormalizedStream_BoundsTheDocumentedErrorDiagnostic(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("x", maxStreamFailureTextRunes*2)
	body := `event: error
data: {"type":"error","code":"subscription_sharing_usage_limit_exceeded","message":"` + long + `"}

`
	_, terminal := drainStream(t, normalizedFixture(t, body))
	failure := requireStreamFailure(t, terminal, StreamFailureProviderError)
	if failure.Code != "subscription_sharing_usage_limit_exceeded" {
		t.Fatalf("the failure code = %q, want the provider's code", failure.Code)
	}
	if runes := len([]rune(failure.Diagnostic)); runes > maxStreamFailureTextRunes+3 {
		t.Fatalf("the failure diagnostic is %d runes, want at most %d", runes, maxStreamFailureTextRunes+3)
	}
	if !strings.HasPrefix(failure.Diagnostic, strings.Repeat("x", 32)) {
		t.Fatalf("the bounded diagnostic lost the provider's message: %q", failure.Diagnostic)
	}
}

// TestNormalizedStream_RefusesNonObjectAndUnusableTerminals proves a terminal
// event is only a successful answer when it carries a usable response
// resource.
//
// `json.Unmarshal` accepts `null` into a struct as an all-zero value, so an
// unchecked decoder would let `data: null` (or an empty payload, or an
// explicitly null resource) terminate the answer successfully. The SSE name may
// still supply a missing payload discriminator for an OBJECT payload, which the
// positive control below pins as intentional.
func TestNormalizedStream_RefusesNonObjectAndUnusableTerminals(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "a JSON null terminal",
			body: "event: response.completed\ndata: null\n\n",
		},
		{
			name: "an array terminal",
			body: "event: response.completed\ndata: []\n\n",
		},
		{
			name: "a scalar terminal",
			body: "event: response.completed\ndata: \"completed\"\n\n",
		},
		{
			name: "an empty object terminal",
			body: "event: response.completed\ndata: {}\n\n",
		},
		{
			name: "a null response resource",
			body: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":null}\n\n",
		},
		{
			name: "a non-object response resource",
			body: "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":[]}\n\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events, terminal := drainStream(t, normalizedFixture(t, testCase.body))
			// A terminal that arrived unusable is a failure, not a success
			// with no output.
			requireEvents(t, events, nil)
			requireNoFinishedEvent(t, events)
			_ = requireStreamFailure(t, terminal, StreamFailureMalformedEvent)
		})
	}

	t.Run("a name-only object terminal with a resource is accepted", func(t *testing.T) {
		t.Parallel()
		events, terminal := drainStream(t, normalizedFixture(t, `event: response.completed
data: {"response":{"id":"resp_1","status":"completed"}}

`))
		if !errors.Is(terminal, io.EOF) {
			t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
		}
		requireEvents(t, events, []aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"},
		})
	})
}

// TestNormalizedStream_FailsAtomicallyAndStaysTerminal proves a failure is one
// atomic outcome: events queued by the event that failed are discarded with it,
// and every later read reports the same failure instead of delivering content
// after it.
//
// The completed event below queues visible output before it reaches an
// unusable member. A normalizer that returned the error and then drained the
// queue would deliver a successful event after the failure was observed.
func TestNormalizedStream_FailsAtomicallyAndStaysTerminal(t *testing.T) {
	t.Parallel()
	const preamble = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"visible"}

`
	cases := []struct {
		name       string
		terminal   string
		wantReason StreamFailureReason
	}{
		{
			name: "an output item this connector cannot represent",
			terminal: `event: response.completed
data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_2","content":[{"type":"output_text","text":"late"}]},{"type":"web_search_call","id":"ws_1"}]}}

`,
			wantReason: StreamFailureUnrepresentableItem,
		},
		{
			name: "an invalid usage counter",
			terminal: `event: response.completed
data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_2","content":[{"type":"output_text","text":"late"}]}],"usage":{"input_tokens":-1}}}

`,
			wantReason: StreamFailureMalformedEvent,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			body := preamble + testCase.terminal
			stream := normalizedFixture(t, body)
			events, terminal := drainStream(t, stream)
			requireEvents(t, events, []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
				{Kind: aipapi.EventTextDelta, Delta: "visible"},
			})
			requireNoFinishedEvent(t, events)
			failure := requireStreamFailure(t, terminal, testCase.wantReason)

			// Once observed, the failure stays terminal: no queued event may
			// follow it.
			for read := range 2 {
				event, again := stream.Recv(context.Background())
				if again == nil {
					t.Fatalf("read %d after the failure returned the event %#v, want the same failure", read, event)
				}
				if !errors.Is(again, ErrStreamFailed) {
					t.Fatalf("read %d after the failure returned %v, want the same failure", read, again)
				}
				var repeated *StreamFailure
				if !errors.As(again, &repeated) || repeated.Reason != failure.Reason {
					t.Fatalf("read %d after the failure returned %v, want the %q failure", read, again, failure.Reason)
				}
			}

			// Collection stops at the failure and never invents a terminal or
			// unqueues output the caller already failed on.
			collected, err := aipapi.Collect(context.Background(), normalizedFixture(t, body))
			if err == nil || !errors.Is(err, ErrStreamFailed) {
				t.Fatalf("Collect = %v, want the stream failure", err)
			}
			if collected.FinishReceived {
				t.Fatal("the failed answer was collected with a terminal")
			}
			if got := collected.Text.String(); got != "visible" {
				t.Fatalf("the collected text = %q, want only the output committed before the failure", got)
			}
		})
	}
}

// TestNormalizedStream_EmitsPlainReasoningFromCompletedItems proves plain
// reasoning text carried by a COMPLETED reasoning item reaches the client, and
// that an item carrier does not repeat the summary a delta already streamed.
//
// This is the plain summary/content text, not the optional encrypted historical
// replay that belongs to a later task.
func TestNormalizedStream_EmitsPlainReasoningFromCompletedItems(t *testing.T) {
	t.Parallel()
	const created = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

`
	const completed = `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "an item-only summary carrier",
			body: created + `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"analysis"}]}}

` + completed,
			want: []string{"analysis"},
		},
		{
			name: "a delta and its completed carrier",
			body: created + `event: response.reasoning_summary_text.delta
data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","summary_index":0,"delta":"analysis"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"analysis"},{"type":"summary_text","text":"more"}]}}

` + completed,
			want: []string{"analysis", "more"},
		},
		{
			name: "a reasoning-text content carrier",
			body: created + `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","content":[{"type":"reasoning_text","text":"chain of thought"}]}}

` + completed,
			want: []string{"chain of thought"},
		},
		{
			name: "the terminal carries the only reasoning",
			body: created + `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"terminal analysis"}]}]}}

`,
			want: []string{"terminal analysis"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events, terminal := drainStream(t, normalizedFixture(t, testCase.body))
			if !errors.Is(terminal, io.EOF) {
				t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
			}
			want := []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
			}
			for _, text := range testCase.want {
				want = append(want, aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: text})
			}
			want = append(want, aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"})
			requireEvents(t, events, want)
		})
	}
}

// TestNormalizedStream_RefusesUnusableReasoningCarriers proves a supported
// carrier with an unusable part is refused instead of silently dropped, so
// reasoning content cannot disappear from a successful answer.
func TestNormalizedStream_RefusesUnusableReasoningCarriers(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		item       string
		wantReason StreamFailureReason
	}{
		{
			name:       "a summary part with no type",
			item:       `{"type":"reasoning","id":"rs_1","summary":[{"text":"analysis"}]}`,
			wantReason: StreamFailureMalformedEvent,
		},
		{
			name:       "a summary part this connector cannot represent",
			item:       `{"type":"reasoning","id":"rs_1","summary":[{"type":"encrypted_summary","text":"analysis"}]}`,
			wantReason: StreamFailureUnrepresentableItem,
		},
		{
			name:       "a content part this connector cannot represent",
			item:       `{"type":"reasoning","id":"rs_1","content":[{"type":"audio","text":"analysis"}]}`,
			wantReason: StreamFailureUnrepresentableItem,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			body := `event: response.output_item.done
data: {"type":"response.output_item.done","item":` + testCase.item + `}

`
			events, terminal := drainStream(t, normalizedFixture(t, body))
			requireNoFinishedEvent(t, events)
			_ = requireStreamFailure(t, terminal, testCase.wantReason)
		})
	}
}

// TestNormalizedStream_RedactsStreamedFailureDiagnostics proves the credential
// that was SENT is removed from provider text arriving inside the stream, and
// that redaction runs BEFORE the length bound.
//
// The message places the token at the truncation boundary: a normalizer that
// bounded first would keep a visible prefix of the credential and could no
// longer match it.
func TestNormalizedStream_RedactsStreamedFailureDiagnostics(t *testing.T) {
	t.Parallel()
	const token = "siwc-access-token-for-acct-one"
	straddling := strings.Repeat("a", maxStreamFailureTextRunes-8) + token + "tail"
	body := `event: error
data: {"type":"error","code":"` + token + `","message":"` + straddling + `"}

`
	client := newStubClient(t, &stubTransport{
		status:      http.StatusOK,
		contentType: "text/event-stream",
		body:        io.NopCloser(strings.NewReader(body)),
	})
	raw, err := client.OpenStream(context.Background(), loopbackRequest())
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer func() { _ = raw.Close() }()
	_, terminal := drainStream(t, raw.Normalize())
	failure := requireStreamFailure(t, terminal, StreamFailureProviderError)

	if failure.Code != "[REDACTED]" {
		t.Fatalf("bearer_leaked: the failure code = %q, want the redacted credential", failure.Code)
	}
	if strings.Contains(failure.Diagnostic, token) {
		t.Fatalf("bearer_leaked=true: the diagnostic repeated the token: %q", failure.Diagnostic)
	}
	if strings.Contains(failure.Diagnostic, token[:8]) {
		t.Fatalf("bearer_leaked=true: the diagnostic kept a token prefix across the bound: %q", failure.Diagnostic)
	}
	if runes := len([]rune(failure.Diagnostic)); runes > maxStreamFailureTextRunes+3 {
		t.Fatalf("the failure diagnostic is %d runes, want at most %d", runes, maxStreamFailureTextRunes+3)
	}
}

// TestNormalizedStream_AdoptsTheAuthoritativeCallID proves a tool call first
// seen through item-only argument frames is re-keyed to the call_id the item
// later supplies.
//
// The public argument-delta events carry `item_id` and normally NO `call_id`,
// so the provisional identity is the item id; the item's `call_id` is the join
// id the client needs for its tool result.
func TestNormalizedStream_AdoptsTheAuthoritativeCallID(t *testing.T) {
	t.Parallel()
	const body = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"city\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"\"Paris\"}"}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`
	stream := normalizedFixture(t, body)
	events, terminal := drainStream(t, stream)
	if !errors.Is(terminal, io.EOF) {
		t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
	}
	requireEvents(t, events, []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `{"city":`},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: `"Paris"}`},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
		{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"},
	})

	collected, err := aipapi.Collect(context.Background(), normalizedFixture(t, body))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	calls := collected.OrderedToolCalls()
	if len(calls) != 1 {
		t.Fatalf("the collected tool calls = %#v, want one call", calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "get_weather" || calls[0].Arguments != `{"city":"Paris"}` {
		t.Fatalf("the collected tool call = %#v, want the authoritative call_id and its arguments", calls[0])
	}
}

// TestNormalizedStream_RefusesMergingAStartedIdentity proves an existing
// authoritative call cannot absorb a provisional state that already emitted
// its own identity: the client saw fc_2 content, so relabelling it as call_1
// would rewrite committed output instead of refusing the contradiction.
//
// The argument frame carries only `item_id`, as the provider's public
// function_call_arguments events do, so the provisional identity is the item
// id until the item supplies call_id.
func TestNormalizedStream_RefusesMergingAStartedIdentity(t *testing.T) {
	t.Parallel()
	const body = `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather"}}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_2","name":"get_weather"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_2","delta":"{}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_2","call_id":"call_1","name":"get_weather","arguments":"{}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`
	events, terminal := drainStream(t, normalizedFixture(t, body))
	_ = requireStreamFailure(t, terminal, StreamFailureContradictoryEvent)
	requireNoFinishedEvent(t, events)
	// The content already emitted under fc_2 stays under fc_2; nothing was
	// replayed or relabelled as call_1.
	requireEvents(t, events, []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "fc_2", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "fc_2", Delta: "{}"},
	})
}

// TestNormalizedStream_RefusesAdoptionIntoAFinishedCall proves buffered
// arguments cannot be adopted into a call that already finished: the client
// saw finished(call_1), so a later argument under that identity would mutate
// committed output.
func TestNormalizedStream_RefusesAdoptionIntoAFinishedCall(t *testing.T) {
	t.Parallel()
	const body = `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":"{}"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_2","delta":"late"}

event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_2","call_id":"call_1","name":"get_weather"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`
	events, terminal := drainStream(t, normalizedFixture(t, body))
	_ = requireStreamFailure(t, terminal, StreamFailureContradictoryEvent)
	requireNoFinishedEvent(t, events)
	requireEvents(t, events, []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventToolCallStarted, ToolCallID: "call_1", ToolName: "get_weather"},
		{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: "call_1", Delta: "{}"},
		{Kind: aipapi.EventToolCallFinished, ToolCallID: "call_1"},
	})
}

// TestNormalizedStream_RefusesContradictoryToolIdentities proves one tool item
// cannot silently name two calls: an identity that already committed to a
// provider item id or call id is not rewritten under a later event.
func TestNormalizedStream_RefusesContradictoryToolIdentities(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{
			name: "two call ids for one item",
			body: `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"get_weather","arguments":""}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_2","name":"get_weather","arguments":"{}"}}

`,
		},
		{
			name: "a started item identity renamed by a later call id",
			body: `event: response.output_item.added
data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","name":"get_weather","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_2","name":"get_weather","arguments":"{}"}}

`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events, terminal := drainStream(t, normalizedFixture(t, testCase.body))
			requireNoFinishedEvent(t, events)
			_ = requireStreamFailure(t, terminal, StreamFailureContradictoryEvent)
		})
	}
}

// TestNormalizedStream_PreservesDistinctProviderParts proves every distinct
// provider content part reaches the client once, in order.
//
// Deduplication is keyed by the full part identity - item, carrier and the
// provider's content_index/summary_index - so completing one part never
// suppresses the next part of the same item, while a repeated carrier of the
// SAME part is still suppressed.
func TestNormalizedStream_PreservesDistinctProviderParts(t *testing.T) {
	t.Parallel()
	const created = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

`
	const completed = `event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed"}}

`
	cases := []struct {
		name string
		body string
		want []aipapi.Event
	}{
		{
			name: "two completed text parts in one item",
			body: created + `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"second"}]}}

` + completed,
			want: []aipapi.Event{
				{Kind: aipapi.EventTextDelta, Delta: "first"},
				{Kind: aipapi.EventTextDelta, Delta: "second"},
			},
		},
		{
			name: "two completed text parts by content index",
			body: created + `event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","content_index":0,"text":"first"}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","content_index":1,"text":"second"}

` + completed,
			want: []aipapi.Event{
				{Kind: aipapi.EventTextDelta, Delta: "first"},
				{Kind: aipapi.EventTextDelta, Delta: "second"},
			},
		},
		{
			name: "mixed delta and completed carriers",
			body: created + `event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"fir"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","content_index":0,"delta":"st"}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","content_index":0,"text":"first"}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","content_index":1,"text":"second"}

` + completed,
			want: []aipapi.Event{
				{Kind: aipapi.EventTextDelta, Delta: "fir"},
				{Kind: aipapi.EventTextDelta, Delta: "st"},
				{Kind: aipapi.EventTextDelta, Delta: "second"},
			},
		},
		{
			name: "a repeated carrier of the same part",
			body: created + `event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","content_index":0,"text":"first"}

event: response.output_text.done
data: {"type":"response.output_text.done","item_id":"msg_1","content_index":0,"text":"first"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"first"}]}}

` + completed,
			want: []aipapi.Event{
				{Kind: aipapi.EventTextDelta, Delta: "first"},
			},
		},
		{
			name: "the terminal repeats the item's parts",
			body: created + `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"second"}]}}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","output":[{"type":"message","id":"msg_1","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"second"}]}]}}

`,
			want: []aipapi.Event{
				{Kind: aipapi.EventTextDelta, Delta: "first"},
				{Kind: aipapi.EventTextDelta, Delta: "second"},
			},
		},
		{
			name: "two completed summary parts",
			body: created + `event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"one"},{"type":"summary_text","text":"two"}]}}

` + completed,
			want: []aipapi.Event{
				{Kind: aipapi.EventReasoningDelta, Delta: "one"},
				{Kind: aipapi.EventReasoningDelta, Delta: "two"},
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events, terminal := drainStream(t, normalizedFixture(t, testCase.body))
			if !errors.Is(terminal, io.EOF) {
				t.Fatalf("the completed stream ended with %v, want io.EOF", terminal)
			}
			want := []aipapi.Event{
				{Kind: aipapi.EventResponseStarted},
				{Kind: aipapi.EventMessageStarted},
			}
			want = append(want, testCase.want...)
			want = append(want, aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"})
			requireEvents(t, events, want)
		})
	}
}

// TestNormalizedStream_PropagatesTheRawTransportFailure proves the 5.1 failure
// classes survive normalization and never become a success.
func TestNormalizedStream_PropagatesTheRawTransportFailure(t *testing.T) {
	t.Parallel()
	raw := newStream(
		context.Background(),
		&failingAfterReader{data: []byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n"), err: ErrStreamTooLarge},
		"acct-one",
		[32]byte{},
	)
	defer func() { _ = raw.Close() }()
	stream := raw.Normalize()
	events, terminal := drainStream(t, stream)
	requireNoFinishedEvent(t, events)
	if !errors.Is(terminal, ErrStreamTooLarge) {
		t.Fatalf("the transport failure = %v, want ErrStreamTooLarge", terminal)
	}
	if errors.Is(terminal, io.EOF) {
		t.Fatalf("the transport failure was reported as a clean end of stream: %v", terminal)
	}
}
