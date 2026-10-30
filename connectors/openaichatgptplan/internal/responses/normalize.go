package responses

// The canonical Responses stream.
//
// The raw seam (stream.go) delivers the provider's events, byte for byte. This
// file turns them into canonical aipapi events, in the provider's order, and it
// owns the one decision that separates an answer from an interruption: only
// `response.completed` is a success terminal.
//
// # The terminal rule
//
// A provider answer is finished when the provider SAYS it is finished. The
// completed event is checked against the required shape - an OBJECT payload,
// the event type (the payload's discriminator, or the SSE event name when the
// payload carries none), a response resource that actually arrived, and a
// status that does not contradict completion - instead of being assumed from
// the end of the stream. A stream that ends without that event is a failure
// (StreamFailureMissingTerminal), even when content already arrived: the
// partial output is not a completed answer, and reporting it as one would
// fabricate a terminal the provider never sent. `response.incomplete`,
// `response.failed`, `response.cancelled` and a stream `error` event are
// failures with their own reasons.
//
// A failure is ATOMIC: the events queued by the event that failed were never
// returned to the caller, so they are discarded with it and the answer cannot
// deliver content after the error was observed.
//
// EOF is therefore NOT the canonical end of this stream: Recv reports io.EOF
// only after the completed terminal has been delivered. A truncated answer
// reports a *StreamFailure instead, so a caller cannot mistake an interruption
// for a successful end.
//
// # Arrived is not absent
//
// A malformed event, an unrepresentable output item and a tool call without an
// identity are stream failures. They are never skipped as if the provider had
// not sent them, because a skipped event could carry output the client never
// sees. The only frames that carry no event at all are comments and
// keep-alives, which the raw seam already drops; documented lifecycle and
// structural frames (response.in_progress, content/reasoning part boundaries)
// are recognized because they carry no output of their own.
//
// # No hidden degradation
//
// Nothing here rewrites or reorders provider content. A value the provider sent
// once reaches the client once: the completed-value carriers
// (`response.output_text.done`, `response.reasoning_summary_text.done`,
// `response.function_call_arguments.done`, `response.output_item.done` and the
// terminal's `response.output`) are used only when the streaming carrier did
// not already deliver that value. Deduplication is keyed by the provider's FULL
// part identity - the item, the carrier kind and the provider's own
// content_index / summary_index - so completing one part of an item never
// suppresses the next part of the same item. No terminal is invented and no
// delta is synthesized.
//
// # Committed output is not replayable
//
// Once a client-visible canonical event has been emitted, the stream records it
// and ReplayAllowed refuses a transparent replay. Core owns retry and failover
// policy; this adapter only supplies the marker that makes a transparent replay
// of committed output impossible to perform unknowingly.
//
// # Correlation and secrets
//
// The transport-owned request id is captured on the successful event-stream
// path (spec task 5.1 follow-up S2) and carried by the normalized stream, by
// every *StreamFailure and by a raw read failure's *StreamReadFailure, so a
// mid-stream failure can be correlated with the provider. Provider-controlled
// text placed in a failure is redacted against the credential that was sent,
// stripped of control characters and bounded before it can reach an error
// string; a raw read error is never retained at all, only its constant
// sentinel.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

const (
	// maxStreamFailureEventRunes bounds the provider event name a failure
	// carries.
	maxStreamFailureEventRunes = 128
	// maxStreamFailureCodeRunes bounds a provider code or discriminator.
	maxStreamFailureCodeRunes = 128
	// maxStreamFailureTextRunes bounds a provider diagnostic.
	maxStreamFailureTextRunes = 512
)

// ErrStreamFailed is the class of every canonical stream failure.
//
// A caller distinguishes the outcomes through StreamFailure.Reason and the raw
// transport sentinels the normalizer propagates unchanged, so spec task 6.1 can
// classify them without re-parsing provider documents.
var ErrStreamFailed = errors.New("responses: the provider stream did not complete")

// ErrReplayAfterVisibleOutput reports a transparent replay of an attempt whose
// client-visible output has already been emitted.
//
// It is the adapter-side marker for the no-retry-after-output rule: core owns
// the retry policy, and this refusal is what a replay must consult.
var ErrReplayAfterVisibleOutput = errors.New("responses: the attempt's visible output is already committed and must not be replayed")

// StreamReadFailure is one raw provider-stream read failure with the safe
// provenance of the answer it interrupted.
//
// Err is one of the raw seam's constant sentinels (ErrStreamUnreadable, the two
// size bounds, or ErrStreamClosed): the untrusted read error is never retained,
// so this carrier adds correlation without restoring provider-controlled text.
// RequestID is the transport-owned provider correlation id after
// redact-before-bound, and ProfileID is the local, non-secret registration id;
// neither is rendered by Error().
type StreamReadFailure struct {
	// Err is the value-free raw-seam sentinel.
	Err error
	// RequestID is the provider request id of the interrupted answer.
	RequestID string
	// ProfileID is the local registration id of the interrupted answer.
	ProfileID string
}

// Error renders the constant sentinel, never the untrusted read error.
func (e *StreamReadFailure) Error() string {
	if e == nil || e.Err == nil {
		return ErrStreamUnreadable.Error()
	}
	return e.Err.Error()
}

// Unwrap reports the raw-seam sentinel, so errors.Is keeps working.
func (e *StreamReadFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StreamFailureReason identifies a canonical stream failure class.
type StreamFailureReason string

const (
	// StreamFailureMissingTerminal reports an answer that ended without the
	// completed terminal. The end of the stream is not a success signal.
	StreamFailureMissingTerminal StreamFailureReason = "missing_terminal"
	// StreamFailureTerminalIncomplete reports a `response.incomplete` terminal.
	StreamFailureTerminalIncomplete StreamFailureReason = "terminal_incomplete"
	// StreamFailureTerminalFailed reports a `response.failed` terminal.
	StreamFailureTerminalFailed StreamFailureReason = "terminal_failed"
	// StreamFailureTerminalCancelled reports a `response.cancelled` terminal.
	StreamFailureTerminalCancelled StreamFailureReason = "terminal_cancelled"
	// StreamFailureProviderError reports a stream `error` event.
	StreamFailureProviderError StreamFailureReason = "provider_error"
	// StreamFailureMalformedEvent reports an event whose payload arrived and
	// cannot be read: invalid JSON, a non-object payload, a missing type
	// discriminator or a field of the wrong type.
	StreamFailureMalformedEvent StreamFailureReason = "malformed_event"
	// StreamFailureContradictoryEvent reports an event whose own members
	// disagree: a name and type that differ, a completed terminal carrying a
	// non-completed status, or a second response lifecycle start.
	StreamFailureContradictoryEvent StreamFailureReason = "contradictory_event"
	// StreamFailureUnrepresentableItem reports a documented output item this
	// connector cannot represent. It arrived, so it is not absent.
	StreamFailureUnrepresentableItem StreamFailureReason = "unrepresentable_output_item"
	// StreamFailureToolIdentityMissing reports tool content that arrived
	// without any call identity the canonical stream could key it by.
	StreamFailureToolIdentityMissing StreamFailureReason = "tool_identity_missing"
	// StreamFailureUnrecognizedEvent reports an event type this build does not
	// know. It cannot be proven content-free, so it is refused instead of
	// silently dropped.
	StreamFailureUnrecognizedEvent StreamFailureReason = "unrecognized_event"
)

// StreamFailure is one canonical stream failure.
//
// Reason is the stable identity of the failure. Origin reports who produced
// it. Event, Code and Diagnostic are the provider's own, non-secret context
// after redaction, sanitisation and bounding; RequestID is the
// transport-owned correlation id of the answer.
type StreamFailure struct {
	// Reason is the failure class.
	Reason StreamFailureReason
	// Origin reports whether the provider or this connector produced the
	// failure. For a local origin, Code is a LOCAL discriminator this
	// connector derived and never a provider recovery code.
	Origin FailureOrigin
	// Event is the provider event type that failed, when one was read.
	Event string
	// Code is the provider's machine-readable code when Origin is the
	// provider, or a local discriminator when Origin is local.
	Code string
	// Diagnostic is the bounded, redacted provider text, when it arrived.
	Diagnostic string
	// RequestID is the provider request id of the answer, when it arrived.
	RequestID string
	// ProfileID is the non-secret local id of the registration that
	// authorized the answer, when it is known.
	ProfileID string
}

// FailureOrigin names who produced a stream failure.
type FailureOrigin string

const (
	// FailureOriginLocal is a judgement or refusal this connector produced.
	// The zero value of the type is the empty string, not this constant,
	// and the classifier reads every value other than
	// FailureOriginProvider as local, so an unset origin is never read as
	// provider data.
	FailureOriginLocal FailureOrigin = "local"
	// FailureOriginProvider is a failure the provider itself reported.
	FailureOriginProvider FailureOrigin = "provider"
)

// Error renders the failure class and the bounded provider context.
func (f *StreamFailure) Error() string {
	if f == nil {
		return ErrStreamFailed.Error()
	}
	message := ErrStreamFailed.Error() + ": " + string(f.Reason)
	if f.Event != "" {
		message += " (" + f.Event + ")"
	}
	if f.Code != "" {
		message += " (code " + f.Code + ")"
	}
	if f.RequestID != "" {
		message += " (request id " + f.RequestID + ")"
	}
	if f.ProfileID != "" {
		message += " (profile " + f.ProfileID + ")"
	}
	if f.Diagnostic != "" {
		message += ": " + f.Diagnostic
	}
	return message
}

// Unwrap reports the failure class.
func (f *StreamFailure) Unwrap() error { return ErrStreamFailed }

// reasonReportedByProvider reports whether a stream-failure reason is a
// provider report rather than a local judgement.
//
// The four provider reasons carry the provider's own terminal or error
// document. Every other reason is the connector refusing what it read, even
// when the provider's text travelled in the failure's fields.
func reasonReportedByProvider(reason StreamFailureReason) bool {
	switch reason {
	case StreamFailureProviderError, StreamFailureTerminalIncomplete,
		StreamFailureTerminalFailed, StreamFailureTerminalCancelled:
		return true
	default:
		return false
	}
}

// streamEventPayload is the decoded provider event.
//
// Unknown members are tolerated because the provider adds fields over time;
// a field this build knows with the wrong type is a malformed event. Usage
// counters are pointers so an omitted counter stays omitted: reporting an
// absent counter as an explicit zero would fabricate usage.
//
// Code and Message are the documented TOP-LEVEL identity of an `error` event
// (OpenAI's generated `response_error_event`); Error is the compatible nested
// envelope. errorIdentity specifies which one wins.
type streamEventPayload struct {
	Type         string              `json:"type"`
	Response     *streamResponseBody `json:"response"`
	Item         *streamOutputItem   `json:"item"`
	Delta        string              `json:"delta"`
	Text         string              `json:"text"`
	Refusal      string              `json:"refusal"`
	ItemID       string              `json:"item_id"`
	CallID       string              `json:"call_id"`
	Arguments    string              `json:"arguments"`
	ContentIndex *int                `json:"content_index"`
	SummaryIndex *int                `json:"summary_index"`
	Error        *streamErrorObject  `json:"error"`
	Code         json.RawMessage     `json:"code"`
	Message      string              `json:"message"`
}

// streamResponseBody is the response resource the terminal events carry.
type streamResponseBody struct {
	ID                string             `json:"id"`
	Status            string             `json:"status"`
	Error             *streamErrorObject `json:"error"`
	IncompleteDetails *streamIncomplete  `json:"incomplete_details"`
	Usage             *streamUsage       `json:"usage"`
	Output            []streamOutputItem `json:"output"`
}

// streamIncomplete names why a response stopped early.
type streamIncomplete struct {
	Reason string `json:"reason"`
}

// streamErrorObject is a provider error envelope.
type streamErrorObject struct {
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Type    string          `json:"type"`
}

// streamUsage is the provider's usage record. Every counter is optional.
type streamUsage struct {
	InputTokens         *int                     `json:"input_tokens"`
	OutputTokens        *int                     `json:"output_tokens"`
	TotalTokens         *int                     `json:"total_tokens"`
	InputTokensDetails  *streamInputTokenDetails `json:"input_tokens_details"`
	OutputTokensDetails *streamOutputTokenDetail `json:"output_tokens_details"`
}

// streamInputTokenDetails carries the cached-input counter.
type streamInputTokenDetails struct {
	CachedTokens *int `json:"cached_tokens"`
}

// streamOutputTokenDetail carries the reasoning counter.
type streamOutputTokenDetail struct {
	ReasoningTokens *int `json:"reasoning_tokens"`
}

// streamOutputItem is one output item of the answer.
//
// Content is a message's content array; Summary is a reasoning item's summary
// array and content its reasoning-text array. Their positions ARE the
// provider's content_index / summary_index, so completion is reconciled per
// part rather than per item.
type streamOutputItem struct {
	Type      string              `json:"type"`
	ID        string              `json:"id"`
	CallID    string              `json:"call_id"`
	Name      string              `json:"name"`
	Arguments string              `json:"arguments"`
	Content   []streamContentPart `json:"content"`
	Summary   []streamContentPart `json:"summary"`
}

// streamContentPart is one content part of a message item or one summary part
// of a reasoning item.
type streamContentPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

// streamToolCall is the canonical state of one function call.
//
// Nothing is emitted from a provisional state before its identity is learned:
// argument deltas are buffered, so one call never fragments into two canonical
// calls when the provider sends arguments before the item. A provisional state
// is keyed by the provider's item id; the provider's call_id is the
// authoritative join identity and is adopted as soon as it arrives, before the
// call starts.
type streamToolCall struct {
	id       string
	name     string
	started  bool
	argsSent bool
	finished bool
}

// unknownPartIndex is the index of a part whose provider event carried no
// content_index / summary_index.
const unknownPartIndex = -1

// The provider part carriers this connector maps onto canonical content.
const (
	partKindOutputText    = "output_text"
	partKindRefusal       = "refusal"
	partKindSummary       = "summary_text"
	partKindReasoningText = "reasoning_text"
	partKindReasoning     = "reasoning"
)

// partIdentity is the provider's own identity of one content part.
//
// A provider item may carry several parts and each of them is distinct
// content: a message's content_index names one part of its content array and a
// reasoning item's summary_index names one part of its summary array. The
// index is part of the identity, so completing one part never suppresses the
// next one.
type partIdentity struct {
	// item is the provider item id that carries the part.
	item string
	// kind is the provider part carrier.
	kind string
	// index is the provider's content_index or summary_index, or
	// unknownPartIndex when the event carried none.
	index int
}

// itemPart names one carrier of one item, for the unidentified-part fallback.
type itemPart struct {
	item string
	kind string
}

// NormalizedStream is the canonical stream of one provider answer.
//
// Recv must not be called concurrently with itself. Close, ReplayAllowed and
// OutputCommitted may be called from any goroutine.
type NormalizedStream struct {
	raw      *Stream
	sanitize func(string, int) string

	mu       sync.Mutex
	pending  []aipapi.Event
	terminal error
	done     bool
	closed   bool

	committed atomic.Bool

	// The remaining state is owned by the Recv goroutine.
	responseStarted bool
	messageStarted  bool
	emittedParts    map[partIdentity]struct{}
	itemParts       map[itemPart]struct{}
	partKinds       map[string]struct{}
	tools           map[string]*streamToolCall
	toolOrder       []*streamToolCall
	itemTool        map[string]string
	pendingArgs     map[string][]string
}

// The normalized stream is a canonical event stream: the canonical collector
// can drain it directly, and a caller cannot observe provider framing through
// it.
var _ aipapi.EventStream = (*NormalizedStream)(nil)

// Normalize returns the canonical stream of this raw provider answer.
func (s *Stream) Normalize() *NormalizedStream {
	stream := &NormalizedStream{
		raw:          s,
		emittedParts: make(map[partIdentity]struct{}),
		itemParts:    make(map[itemPart]struct{}),
		partKinds:    make(map[string]struct{}),
		tools:        make(map[string]*streamToolCall),
		itemTool:     make(map[string]string),
		pendingArgs:  make(map[string][]string),
	}
	if s != nil {
		stream.sanitize = s.sanitizeText
	}
	return stream
}

// Recv returns the next canonical event.
//
// io.EOF means the completed terminal was already delivered. Every failure -
// a different terminal, a malformed event, an ended stream, a raw read failure
// - is reported as an error and stays terminal for the rest of the answer.
// Events queued by the event that failed were never returned to the caller, so
// they are discarded with it: a failure is one atomic outcome and no event
// follows it.
func (s *NormalizedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if ctx == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	if s == nil {
		return aipapi.Event{}, ErrStreamClosed
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return aipapi.Event{}, ErrStreamClosed
		}
		if len(s.pending) > 0 {
			event := s.pending[0]
			s.pending = s.pending[1:]
			s.mu.Unlock()
			if aipapi.OutputCommitted(event) {
				s.committed.Store(true)
			}
			return event, nil
		}
		if s.terminal != nil {
			terminal := s.terminal
			s.mu.Unlock()
			return aipapi.Event{}, terminal
		}
		if s.done {
			s.mu.Unlock()
			return aipapi.Event{}, io.EOF
		}
		s.mu.Unlock()

		if err := ctx.Err(); err != nil {
			return aipapi.Event{}, err
		}
		raw, err := s.raw.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// CHECK, do not assume: the end of the body is not the end of
				// the answer. Only the completed terminal may report success.
				return aipapi.Event{}, s.fail(s.failure(StreamFailureMissingTerminal, "", "", "the provider stream ended before response.completed"))
			}
			// The raw seam's failures keep their identity: cancellation is the
			// context's error, the size bounds and the unreadable body are its
			// constant sentinels. They also keep the answer envelope's safe
			// provenance, so a caller can correlate the interruption with the
			// provider without ever seeing the untrusted read error.
			return aipapi.Event{}, s.fail(s.readFailure(err))
		}
		if err := s.handle(raw); err != nil {
			return aipapi.Event{}, s.fail(err)
		}
	}
}

// Close releases the underlying provider answer.
func (s *NormalizedStream) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	if s.raw == nil {
		return nil
	}
	return s.raw.Close()
}

// OutputCommitted reports whether a client-visible canonical event has already
// been emitted by this stream.
func (s *NormalizedStream) OutputCommitted() bool {
	return s != nil && s.committed.Load()
}

// ReplayAllowed reports whether a transparent retry or failover may replay this
// attempt.
//
// It returns nil only while no client-visible output has been emitted; core
// owns the retry policy and decides what to do with that answer. Once visible
// output is committed the replay is refused with ErrReplayAfterVisibleOutput,
// including after a mid-stream failure, so committed output cannot be
// re-executed transparently.
func (s *NormalizedStream) ReplayAllowed() error {
	if s != nil && s.committed.Load() {
		return ErrReplayAfterVisibleOutput
	}
	return nil
}

// RequestID is the provider request id of this answer, or empty when the
// provider sent none.
func (s *NormalizedStream) RequestID() string {
	if s == nil || s.raw == nil {
		return ""
	}
	return s.raw.RequestID()
}

// ProfileID is the non-secret local id of the registration that authorized
// this answer, or empty when the answer was not opened for one.
func (s *NormalizedStream) ProfileID() string {
	if s == nil || s.raw == nil {
		return ""
	}
	return s.raw.ProfileID()
}

// fail records the terminal outcome of the answer.
//
// The first failure also discards the pending queue: those events belong to the
// event that failed and were never returned, so delivering one after the error
// would make a failed answer look partly successful. Once recorded, the failure
// is what every later Recv reports.
func (s *NormalizedStream) fail(err error) error {
	s.mu.Lock()
	if s.terminal == nil {
		s.terminal = err
		s.pending = nil
	}
	terminal := s.terminal
	s.mu.Unlock()
	return terminal
}

// push appends one canonical event to the pending queue.
func (s *NormalizedStream) push(event aipapi.Event) {
	s.pending = append(s.pending, event)
}

// readFailure attaches the interrupted answer's safe provenance to one raw
// stream failure.
//
// Only the constant sentinel and the sanitized request/profile ids travel: the
// untrusted read error is never wrapped, so a malformed chunk trailer that
// echoes the credential cannot reach a log through this carrier. A
// cancellation stays the context's own error, because the caller's withdrawal
// is not a connector failure and is not this answer's outcome.
func (s *NormalizedStream) readFailure(err error) error {
	if err == nil || s == nil || s.raw == nil {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	requestID := s.RequestID()
	profileID := safeProviderText(s.ProfileID(), maxProfileIDRunes)
	if requestID == "" && profileID == "" {
		return err
	}
	return &StreamReadFailure{Err: err, RequestID: requestID, ProfileID: profileID}
}

// failure builds one typed, redacted, bounded stream failure.
func (s *NormalizedStream) failure(reason StreamFailureReason, event, code, diagnostic string) *StreamFailure {
	sanitize := func(text string, limit int) string {
		if text == "" {
			return ""
		}
		if s != nil && s.sanitize != nil {
			return s.sanitize(text, limit)
		}
		return safeProviderText(text, limit)
	}
	// CHECK, do not assume: the origin is read from the failure REASON, so a
	// provider-controlled string in Code can never make this connector's own
	// judgement look like a provider report.
	origin := FailureOriginLocal
	if reasonReportedByProvider(reason) {
		origin = FailureOriginProvider
	}
	return &StreamFailure{
		Reason:     reason,
		Origin:     origin,
		Event:      sanitize(event, maxStreamFailureEventRunes),
		Code:       sanitize(code, maxStreamFailureCodeRunes),
		Diagnostic: sanitize(diagnostic, maxStreamFailureTextRunes),
		RequestID:  s.RequestID(),
		ProfileID:  safeProviderText(s.ProfileID(), maxProfileIDRunes),
	}
}

// handle maps one raw provider event onto canonical events.
func (s *NormalizedStream) handle(raw Event) error {
	payload, err := s.decode(raw)
	if err != nil {
		return err
	}
	switch payload.Type {
	case "response.created":
		if s.responseStarted {
			return s.failure(StreamFailureContradictoryEvent, payload.Type, "", "a second response lifecycle start")
		}
		s.responseStarted = true
		s.push(aipapi.Event{Kind: aipapi.EventResponseStarted})
		return nil
	case "response.in_progress":
		// A lifecycle frame with no output of its own.
		return nil
	case "response.output_item.added":
		return s.outputItemAdded(payload.Item)
	case "response.output_item.done":
		return s.outputItemDone(payload.Item)
	case "response.content_part.added", "response.content_part.done",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		// Structural frames: the content of a part arrives in its own events.
		return nil
	case "response.output_text.delta":
		s.emitText(providerPart(payload.ItemID, partKindOutputText, payload.ContentIndex), payload.Delta)
		return nil
	case "response.output_text.done":
		s.completedText(providerPart(payload.ItemID, partKindOutputText, payload.ContentIndex), payload.Text)
		return nil
	case "response.refusal.delta":
		// A refusal is assistant-visible content. The canonical vocabulary has
		// no refusal kind, so it travels as text; the refusal annotation has no
		// canonical carrier and is not invented here.
		s.emitText(providerPart(payload.ItemID, partKindRefusal, payload.ContentIndex), payload.Delta)
		return nil
	case "response.refusal.done":
		s.completedText(providerPart(payload.ItemID, partKindRefusal, payload.ContentIndex), payload.Refusal)
		return nil
	case "response.reasoning_summary_text.delta":
		s.emitReasoning(providerPart(payload.ItemID, partKindSummary, payload.SummaryIndex), payload.Delta)
		return nil
	case "response.reasoning_summary_text.done":
		s.completedReasoning(providerPart(payload.ItemID, partKindSummary, payload.SummaryIndex), payload.Text)
		return nil
	case "response.reasoning_text.delta":
		s.emitReasoning(providerPart(payload.ItemID, partKindReasoningText, payload.ContentIndex), payload.Delta)
		return nil
	case "response.reasoning_text.done":
		s.completedReasoning(providerPart(payload.ItemID, partKindReasoningText, payload.ContentIndex), payload.Text)
		return nil
	case "response.reasoning.delta":
		s.emitReasoning(providerPart(payload.ItemID, partKindReasoning, nil), payload.Delta)
		return nil
	case "response.reasoning.done":
		s.completedReasoning(providerPart(payload.ItemID, partKindReasoning, nil), payload.Text)
		return nil
	case "response.function_call_arguments.delta":
		return s.argumentsDelta(payload)
	case "response.function_call_arguments.done":
		return s.argumentsDone(payload)
	case "response.completed":
		return s.completed(payload)
	case "response.incomplete":
		return s.terminalFailure(payload, StreamFailureTerminalIncomplete)
	case "response.failed":
		return s.terminalFailure(payload, StreamFailureTerminalFailed)
	case "response.cancelled":
		return s.terminalFailure(payload, StreamFailureTerminalCancelled)
	case "error":
		code, diagnostic := payload.errorIdentity()
		return s.failure(StreamFailureProviderError, payload.Type, code, diagnostic)
	default:
		// The event arrived and this build cannot prove it carries no output,
		// so it is refused instead of silently dropped.
		return s.failure(StreamFailureUnrecognizedEvent, payload.Type, "", "")
	}
}

// decode reads one raw event and resolves its authoritative type.
//
// A payload must be a JSON OBJECT before anything is read from it: the decoder
// accepts `null` into a Go struct as an all-zero value, so a non-object would
// otherwise become an empty event whose type is invented from the SSE name. An
// object payload that carries no type discriminator still uses the SSE event
// name - the provider names every event on the stream - and when both are
// present they must agree. This is a name FALLBACK, not name/payload agreement
// checking: a name-only object is intentionally accepted.
func (s *NormalizedStream) decode(raw Event) (streamEventPayload, error) {
	var payload streamEventPayload
	if !isJSONObject(raw.Data) {
		return payload, s.failure(StreamFailureMalformedEvent, raw.Name, "", "the event payload is not a JSON object")
	}
	if err := json.Unmarshal(raw.Data, &payload); err != nil {
		// The decoder's text is not retained: it is provider-controlled input.
		return payload, s.failure(StreamFailureMalformedEvent, raw.Name, "", "the event payload is not readable JSON")
	}
	declared := payload.Type
	if declared == "" {
		if raw.Name != "" && raw.Name != defaultSSEEventName {
			payload.Type = raw.Name
			return payload, nil
		}
		return payload, s.failure(StreamFailureMalformedEvent, raw.Name, "", "the event carries no type discriminator")
	}
	if raw.Name != "" && raw.Name != defaultSSEEventName && raw.Name != declared {
		// The stream's own name and the payload's discriminator disagree; one
		// of them is wrong and the connector must not pick a favourite.
		return payload, s.failure(StreamFailureContradictoryEvent, declared, "", "the event name and the payload type disagree")
	}
	return payload, nil
}

// ensureResponseStarted opens the canonical response envelope.
//
// The envelope is canonical framing, not provider content: a provider that
// sends a content event without response.created still gets a stream the
// canonical contract can consume, and no content is invented.
func (s *NormalizedStream) ensureResponseStarted() {
	if s.responseStarted {
		return
	}
	s.responseStarted = true
	s.push(aipapi.Event{Kind: aipapi.EventResponseStarted})
}

// ensureMessageStarted opens the canonical message envelope.
func (s *NormalizedStream) ensureMessageStarted() {
	s.ensureResponseStarted()
	if s.messageStarted {
		return
	}
	s.messageStarted = true
	s.push(aipapi.Event{Kind: aipapi.EventMessageStarted})
}

// providerPart builds the identity of one provider content part.
func providerPart(itemID, kind string, index *int) partIdentity {
	position := unknownPartIndex
	if index != nil {
		position = *index
	}
	return partIdentity{item: itemID, kind: kind, index: position}
}

// partEmitted reports whether this provider part already reached the client.
//
// An unidentified part is suppressed once ANY part of the same item and
// carrier was emitted, and it in turn suppresses every identified part of that
// item and carrier: without an index the connector cannot prove that a value is
// new, so it keeps the no-duplication rule instead of repeating content. A part
// with no item id consults the carrier aggregate for the same reason.
func (s *NormalizedStream) partEmitted(part partIdentity) bool {
	if _, ok := s.emittedParts[part]; ok {
		return true
	}
	if _, ok := s.emittedParts[partIdentity{item: part.item, kind: part.kind, index: unknownPartIndex}]; ok {
		return true
	}
	if part.index == unknownPartIndex {
		if _, any := s.itemParts[itemPart{item: part.item, kind: part.kind}]; any {
			return true
		}
	}
	if part.item == "" {
		_, any := s.partKinds[part.kind]
		return any
	}
	return false
}

// markPart records one emitted provider part.
func (s *NormalizedStream) markPart(part partIdentity) {
	s.emittedParts[part] = struct{}{}
	s.itemParts[itemPart{item: part.item, kind: part.kind}] = struct{}{}
	s.partKinds[part.kind] = struct{}{}
}

// emitText pushes one text delta and records the part it belongs to.
//
// Deltas are fragments of the SAME part, so every one of them is emitted; only
// the completed carriers consult the emitted-part record.
func (s *NormalizedStream) emitText(part partIdentity, text string) {
	if text == "" {
		return
	}
	s.ensureMessageStarted()
	s.markPart(part)
	s.push(aipapi.Event{Kind: aipapi.EventTextDelta, Delta: text})
}

// completedText emits the completed value of a part only when that part never
// streamed, so a completed carrier never repeats streamed content.
func (s *NormalizedStream) completedText(part partIdentity, text string) {
	if text == "" || s.partEmitted(part) {
		return
	}
	s.emitText(part, text)
}

// emitReasoning pushes one reasoning delta and records its part.
func (s *NormalizedStream) emitReasoning(part partIdentity, text string) {
	if text == "" {
		return
	}
	s.ensureMessageStarted()
	s.markPart(part)
	s.push(aipapi.Event{Kind: aipapi.EventReasoningDelta, Delta: text})
}

// completedReasoning is the reasoning counterpart of completedText.
func (s *NormalizedStream) completedReasoning(part partIdentity, text string) {
	if text == "" || s.partEmitted(part) {
		return
	}
	s.emitReasoning(part, text)
}

// outputItemAdded maps the first event of one output item.
func (s *NormalizedStream) outputItemAdded(item *streamOutputItem) error {
	if item == nil {
		return s.failure(StreamFailureMalformedEvent, "response.output_item.added", "", "the event carries no item")
	}
	switch item.Type {
	case "message":
		s.ensureMessageStarted()
		return nil
	case "reasoning":
		// The added form announces the item; its plain summary/content text
		// arrives in the completed item or in its own part events.
		return nil
	case "function_call":
		state, err := s.toolState(item)
		if err != nil {
			return err
		}
		return s.startTool(state, item.Name)
	case "":
		return s.failure(StreamFailureMalformedEvent, "response.output_item.added", "", "the output item carries no type")
	default:
		return s.unrepresentableItem("response.output_item.added", item.Type)
	}
}

// outputItemDone maps the completed form of one output item.
func (s *NormalizedStream) outputItemDone(item *streamOutputItem) error {
	if item == nil {
		return s.failure(StreamFailureMalformedEvent, "response.output_item.done", "", "the event carries no item")
	}
	return s.completedItem(*item)
}

// completedItem emits the content of a completed output item once.
//
// It is used for `response.output_item.done` and for the terminal's
// `response.output`, so a provider that sends the item only in the terminal is
// still represented, and a provider that sent it both times is not duplicated.
// Deduplication is per provider part, so two distinct parts of one item both
// reach the client.
func (s *NormalizedStream) completedItem(item streamOutputItem) error {
	switch item.Type {
	case "message":
		s.ensureMessageStarted()
		return s.completedMessageItem(item)
	case "reasoning":
		return s.completedReasoningItem(item)
	case "function_call":
		state, err := s.toolState(&item)
		if err != nil {
			return err
		}
		if err := s.startTool(state, item.Name); err != nil {
			return err
		}
		if !state.argsSent && item.Arguments != "" {
			state.argsSent = true
			s.push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: state.id, Delta: item.Arguments})
		}
		return s.finishTool(state)
	case "":
		return s.failure(StreamFailureMalformedEvent, "response.output_item.done", "", "the output item carries no type")
	default:
		return s.unrepresentableItem("response.output_item.done", item.Type)
	}
}

// completedMessageItem emits every completed content part of one message item
// once, in provider order.
func (s *NormalizedStream) completedMessageItem(item streamOutputItem) error {
	for index, part := range item.Content {
		position := index
		switch part.Type {
		case partKindOutputText:
			s.completedText(providerPart(item.ID, partKindOutputText, &position), part.Text)
		case partKindRefusal:
			s.completedText(providerPart(item.ID, partKindRefusal, &position), part.Refusal)
		case "":
			return s.failure(StreamFailureMalformedEvent, "response.output_item.done", "", "the content part carries no type")
		default:
			return s.failure(StreamFailureUnrepresentableItem, "response.output_item.done", part.Type, "the connector cannot represent this content part")
		}
	}
	return nil
}

// completedReasoningItem emits the plain reasoning text of one completed
// reasoning item: its summary parts first, then its reasoning-text content
// parts, each one once and in provider order.
//
// This is plain summary/content text. Optional encrypted historical replay is a
// separate capability and is neither decoded nor invented here.
func (s *NormalizedStream) completedReasoningItem(item streamOutputItem) error {
	for index, part := range item.Summary {
		position := index
		switch part.Type {
		case partKindSummary:
			s.completedReasoning(providerPart(item.ID, partKindSummary, &position), part.Text)
		case "":
			return s.failure(StreamFailureMalformedEvent, "response.output_item.done", "", "the reasoning summary part carries no type")
		default:
			return s.failure(StreamFailureUnrepresentableItem, "response.output_item.done", part.Type, "the connector cannot represent this reasoning summary part")
		}
	}
	for index, part := range item.Content {
		position := index
		switch part.Type {
		case partKindReasoningText:
			s.completedReasoning(providerPart(item.ID, partKindReasoningText, &position), part.Text)
		case "":
			return s.failure(StreamFailureMalformedEvent, "response.output_item.done", "", "the reasoning content part carries no type")
		default:
			return s.failure(StreamFailureUnrepresentableItem, "response.output_item.done", part.Type, "the connector cannot represent this reasoning content part")
		}
	}
	return nil
}

// unrepresentableItem refuses a documented output item this connector cannot
// carry.
func (s *NormalizedStream) unrepresentableItem(event, itemType string) error {
	return s.failure(StreamFailureUnrepresentableItem, event, itemType, "the connector cannot represent this output item")
}

// toolState resolves the canonical state of one tool call.
//
// The provider has TWO identities: the item id that argument-delta events carry,
// and the call id that the output item supplies and the client needs for its
// tool result. An explicit call_id is authoritative and is adopted as soon as
// it arrives, re-keying an unstarted provisional state created from item-only
// argument frames and migrating its buffered arguments. An identity that
// already emitted events is never rewritten: a later, different call_id is a
// contradiction rather than a silent rename. Without a call id the item id is
// the identity the call has.
func (s *NormalizedStream) toolState(item *streamOutputItem) (*streamToolCall, error) {
	itemID := strings.TrimSpace(item.ID)
	callID := strings.TrimSpace(item.CallID)
	switch {
	case callID != "":
		return s.authoritativeTool(itemID, callID)
	case itemID != "":
		if known := s.itemTool[itemID]; known != "" {
			return s.tools[known], nil
		}
		return s.provisionalTool(itemID), nil
	default:
		return nil, s.failure(StreamFailureToolIdentityMissing, "", "", "the tool call carries no identity")
	}
}

// authoritativeTool resolves a call the provider identified by its call id.
func (s *NormalizedStream) authoritativeTool(itemID, callID string) (*streamToolCall, error) {
	if itemID != "" {
		if known := s.itemTool[itemID]; known != "" && known != callID {
			return nil, s.failure(StreamFailureContradictoryEvent, "", callID, "one tool item names two different calls")
		}
	}
	if state := s.tools[callID]; state != nil {
		// A provisional state created by item-only argument frames may exist
		// beside the authoritative one; its buffered arguments belong to this
		// call and are never a second call.
		if itemID != "" && itemID != callID {
			if provisional := s.tools[itemID]; provisional != nil && provisional != state {
				// Only an UNSTARTED provisional state may migrate: none of
				// its content reached the client, so re-keying it is repair.
				// A started provisional already emitted its own identity, and
				// a finished target can no longer accept arguments.
				if provisional.started {
					return nil, s.failure(StreamFailureContradictoryEvent, "", callID, "a started tool call cannot adopt a different call id")
				}
				if provisional.finished || state.finished {
					return nil, s.failure(StreamFailureContradictoryEvent, "", callID, "arguments arrived after the call finished")
				}
				s.adoptToolArguments(state, provisional)
				delete(s.tools, itemID)
			}
		}
		if itemID != "" {
			s.itemTool[itemID] = callID
		}
		return state, nil
	}
	if itemID != "" && itemID != callID {
		if provisional := s.tools[itemID]; provisional != nil {
			if provisional.started {
				// The canonical identity was already emitted under the item
				// id; renaming it now would relabel committed output.
				return nil, s.failure(StreamFailureContradictoryEvent, "", callID, "a started tool call cannot adopt a different call id")
			}
			delete(s.tools, itemID)
			provisional.id = callID
			s.tools[callID] = provisional
			if buffered, ok := s.pendingArgs[itemID]; ok {
				s.pendingArgs[callID] = append(s.pendingArgs[callID], buffered...)
				delete(s.pendingArgs, itemID)
			}
			s.itemTool[itemID] = callID
			return provisional, nil
		}
	}
	state := &streamToolCall{id: callID}
	s.tools[callID] = state
	if itemID != "" {
		s.itemTool[itemID] = callID
	}
	return state, nil
}

// provisionalTool returns the unstarted state of a call known only by its item
// id.
func (s *NormalizedStream) provisionalTool(itemID string) *streamToolCall {
	if state := s.tools[itemID]; state != nil {
		return state
	}
	state := &streamToolCall{id: itemID}
	s.tools[itemID] = state
	return state
}

// adoptToolArguments moves a provisional state's buffered arguments onto the
// authoritative call, emitting them in order when that call already started.
func (s *NormalizedStream) adoptToolArguments(state, provisional *streamToolCall) {
	buffered := s.pendingArgs[provisional.id]
	delete(s.pendingArgs, provisional.id)
	if len(buffered) == 0 {
		return
	}
	if !state.started {
		s.pendingArgs[state.id] = append(s.pendingArgs[state.id], buffered...)
		return
	}
	for _, delta := range buffered {
		if delta == "" {
			continue
		}
		state.argsSent = true
		s.push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: state.id, Delta: delta})
	}
}

// startTool emits the started event once and flushes buffered arguments.
func (s *NormalizedStream) startTool(state *streamToolCall, name string) error {
	if name != "" {
		if state.name == "" {
			state.name = name
		} else if state.name != name {
			return s.failure(StreamFailureContradictoryEvent, "", "", "one tool call names two different tools")
		}
	}
	if state.started {
		return nil
	}
	s.ensureMessageStarted()
	state.started = true
	s.toolOrder = append(s.toolOrder, state)
	s.push(aipapi.Event{Kind: aipapi.EventToolCallStarted, ToolCallID: state.id, ToolName: state.name})
	s.flushToolArgs(state)
	return nil
}

// flushToolArgs emits the arguments buffered before the call was identified.
func (s *NormalizedStream) flushToolArgs(state *streamToolCall) {
	buffered := s.pendingArgs[state.id]
	delete(s.pendingArgs, state.id)
	for _, delta := range buffered {
		if delta == "" {
			continue
		}
		state.argsSent = true
		s.push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: state.id, Delta: delta})
	}
}

// finishTool emits the finished event once.
func (s *NormalizedStream) finishTool(state *streamToolCall) error {
	if state.finished {
		return nil
	}
	if !state.started {
		return s.failure(StreamFailureToolIdentityMissing, "", "", "a tool call finished before its identity was learned")
	}
	state.finished = true
	s.push(aipapi.Event{Kind: aipapi.EventToolCallFinished, ToolCallID: state.id})
	return nil
}

// argumentsDelta maps one streamed argument fragment.
func (s *NormalizedStream) argumentsDelta(payload streamEventPayload) error {
	if payload.Delta == "" {
		return nil
	}
	state, err := s.toolState(&streamOutputItem{ID: payload.ItemID, CallID: payload.CallID})
	if err != nil {
		return err
	}
	if state.finished {
		return s.failure(StreamFailureContradictoryEvent, payload.Type, "", "arguments arrived after the call finished")
	}
	if !state.started {
		s.pendingArgs[state.id] = append(s.pendingArgs[state.id], payload.Delta)
		return nil
	}
	state.argsSent = true
	s.push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: state.id, Delta: payload.Delta})
	return nil
}

// argumentsDone maps the completed argument value, once.
func (s *NormalizedStream) argumentsDone(payload streamEventPayload) error {
	if payload.Arguments == "" {
		return nil
	}
	state, err := s.toolState(&streamOutputItem{ID: payload.ItemID, CallID: payload.CallID})
	if err != nil {
		return err
	}
	if state.finished {
		return s.failure(StreamFailureContradictoryEvent, payload.Type, "", "arguments arrived after the call finished")
	}
	if state.argsSent {
		return nil
	}
	if !state.started {
		// Only buffer the completed value when no fragment already buffered
		// the same arguments; otherwise the flush would duplicate them.
		if len(s.pendingArgs[state.id]) == 0 {
			s.pendingArgs[state.id] = append(s.pendingArgs[state.id], payload.Arguments)
		}
		return nil
	}
	state.argsSent = true
	s.push(aipapi.Event{Kind: aipapi.EventToolCallArgsDelta, ToolCallID: state.id, Delta: payload.Arguments})
	return nil
}

// completed maps the only success terminal.
//
// The event type is the success discriminator; the resource's status is only a
// contradiction check, so an absent status (or an explicit null, which decodes
// to the same absence) contradicts nothing while a different non-empty status
// does. The response resource itself must have arrived: a completed event
// without one is malformed, never a successful empty answer.
func (s *NormalizedStream) completed(payload streamEventPayload) error {
	response := payload.Response
	if response == nil {
		// `null` and an absent member both collapse to the same absence in the
		// decoder; neither is a response resource, so neither can be read as a
		// successful answer.
		return s.failure(StreamFailureMalformedEvent, payload.Type, "", "the completed terminal carries no response resource")
	}
	if status := strings.TrimSpace(response.Status); status != "" && status != "completed" {
		return s.failure(StreamFailureContradictoryEvent, payload.Type, status, "the completed terminal reports a non-completed status")
	}
	s.ensureResponseStarted()
	for _, item := range response.Output {
		if err := s.completedItem(item); err != nil {
			return err
		}
	}
	for _, state := range s.toolOrder {
		if err := s.finishTool(state); err != nil {
			return err
		}
	}
	if len(s.pendingArgs) > 0 {
		return s.failure(StreamFailureToolIdentityMissing, payload.Type, "", "tool arguments never resolved to a call")
	}
	if err := s.emitUsage(response.Usage); err != nil {
		return err
	}
	s.mu.Lock()
	s.done = true
	s.mu.Unlock()
	s.push(aipapi.Event{Kind: aipapi.EventResponseFinished, ResponseStatus: "completed"})
	return nil
}

// emitUsage maps the terminal usage record once, with explicit presence.
func (s *NormalizedStream) emitUsage(usage *streamUsage) error {
	if usage == nil {
		return nil
	}
	event := aipapi.Event{Kind: aipapi.EventUsageDelta}
	counters := []struct {
		value    *int
		target   *int
		presence *bool
		field    string
	}{
		{usage.InputTokens, &event.InputTokens, &event.UsagePresence.InputTokens, "input_tokens"},
		{usage.OutputTokens, &event.OutputTokens, &event.UsagePresence.OutputTokens, "output_tokens"},
		{usage.TotalTokens, &event.TotalTokens, &event.UsagePresence.TotalTokens, "total_tokens"},
	}
	for _, counter := range counters {
		if counter.value == nil {
			continue
		}
		if *counter.value < 0 {
			return s.failure(StreamFailureMalformedEvent, "response.completed", counter.field, "the usage counter is negative")
		}
		*counter.target = *counter.value
		*counter.presence = true
	}
	if details := usage.InputTokensDetails; details != nil && details.CachedTokens != nil {
		if *details.CachedTokens < 0 {
			return s.failure(StreamFailureMalformedEvent, "response.completed", "cached_tokens", "the usage counter is negative")
		}
		event.CacheReadTokens = *details.CachedTokens
		event.UsagePresence.CacheReadTokens = true
	}
	if details := usage.OutputTokensDetails; details != nil && details.ReasoningTokens != nil {
		if *details.ReasoningTokens < 0 {
			return s.failure(StreamFailureMalformedEvent, "response.completed", "reasoning_tokens", "the usage counter is negative")
		}
		event.ReasoningTokens = *details.ReasoningTokens
		event.UsagePresence.ReasoningTokens = true
	}
	if !event.UsagePresence.Any() {
		// An empty usage record reports nothing; emitting an all-zero event
		// with no presence would look like a measured zero.
		return nil
	}
	s.push(event)
	return nil
}

// terminalFailure maps a non-completed terminal event.
func (s *NormalizedStream) terminalFailure(payload streamEventPayload, reason StreamFailureReason) error {
	code, diagnostic := payload.errorIdentity()
	if reason == StreamFailureTerminalIncomplete && payload.Response != nil && payload.Response.IncompleteDetails != nil {
		code = payload.Response.IncompleteDetails.Reason
	}
	return s.failure(reason, payload.Type, code, diagnostic)
}

// errorIdentity reads the provider's error code and message.
//
// The documented stream `error` event carries TOP-LEVEL `code` and `message`
// (OpenAI's generated `response_error_event`), so those fields win; the
// compatible nested `error` envelope is read next, then the terminal resource's
// `response.error`. Each field resolves independently, so a top-level code with
// a nested message keeps both. A code that is not a string stays absent instead
// of becoming a fabricated diagnostic.
func (p streamEventPayload) errorIdentity() (code, diagnostic string) {
	code = rawString(p.Code)
	diagnostic = p.Message
	if nested := p.Error; nested != nil {
		if code == "" {
			code = rawString(nested.Code)
		}
		if diagnostic == "" {
			diagnostic = nested.Message
		}
	}
	if p.Response != nil {
		if nested := p.Response.Error; nested != nil {
			if code == "" {
				code = rawString(nested.Code)
			}
			if diagnostic == "" {
				diagnostic = nested.Message
			}
		}
	}
	return code, diagnostic
}
