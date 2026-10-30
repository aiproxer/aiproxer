package aipapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
)

// CollectLimits bounds memory growth while aggregating streaming events into Collected.
// A zero value disables all limits (CollectUnbounded). Individual fields use zero to mean
// “no limit” for that dimension only when using CollectWithLimits with a partially filled struct.
type CollectLimits struct {
	MaxTextBytes             int
	MaxReasoningBytes        int
	MaxToolArgsTotalBytes    int
	MaxWarnings              int
	MaxAssistantMediaParts   int // assistant_image_ref / assistant_file_ref events aggregated into Collected.AssistantMedia; 0 = unlimited
	MaxAggregatePayloadBytes int // aggregate retained bytes across Text + Reasoning (delta + exact ReasoningPart payload) + ToolArgs; 0 = unlimited (backward-compatible). Overflow-safe.
}

// DefaultCollectLimits returns conservative defaults for Collect (non-streaming aggregation).
func DefaultCollectLimits() CollectLimits {
	return CollectLimits{
		MaxTextBytes:           64 << 20,
		MaxReasoningBytes:      64 << 20,
		MaxToolArgsTotalBytes:  128 << 20,
		MaxWarnings:            100_000,
		MaxAssistantMediaParts: 1024,
	}
}

// EventKind identifies canonical stream events.
type EventKind string

const (
	// EventDecisionResult carries a complete typed decision and commits output.
	EventDecisionResult  EventKind = "decision_result"
	EventResponseStarted EventKind = "response_started"
	EventMessageStarted  EventKind = "message_started"
	EventTextDelta       EventKind = "text_delta"
	EventReasoningDelta  EventKind = "reasoning_delta"
	// EventReasoningSignatureDelta carries a provider-specific thinking-block
	// signature (Anthropic extended-thinking integrity metadata) as a canonical
	// carrier distinct from reasoning text. The value is provider-specific and is
	// ignored by non-Anthropic frontends via their default switch case; the carrier
	// is canonical. Mirrors the EventReasoningDelta precedent of carrying provider
	// reasoning text through the canonical stream.
	EventReasoningSignatureDelta EventKind = "reasoning_signature_delta"
	// EventReasoningOpaqueDelta carries a provider opaque reasoning payload
	// (Anthropic redacted_thinking envelope bytes). Non-Anthropic frontends ignore
	// it via their default switch case; the carrier is canonical.
	EventReasoningOpaqueDelta EventKind = "reasoning_opaque_delta"
	// EventReasoningPart carries one complete dialect-tagged historical reasoning
	// part (see Event.Reasoning). Distinct from EventReasoningOpaqueDelta (Anthropic
	// redacted_thinking byte stream) and from progressive EventReasoningDelta text.
	// Dialect must already be normalized; provider envelope schema is adapter-owned.
	EventReasoningPart     EventKind = "reasoning_part"
	EventToolCallStarted   EventKind = "tool_call_started"
	EventToolCallArgsDelta EventKind = "tool_call_args_delta"
	EventToolCallFinished  EventKind = "tool_call_finished"
	EventUsageDelta        EventKind = "usage_delta"
	EventWarning           EventKind = "warning"
	EventError             EventKind = "error"
	EventResponseFinished  EventKind = "response_finished"
	// EventItem carries one complete validated canonical Item (for example a
	// provider compaction item in a compacted ordered window). It is a
	// standalone item carrier: it must not be treated as content-class output
	// and is not synthesized by legacy normalizers. The OR state machine maps it
	// into the canonical output trajectory; other frontends ignore it.
	EventItem EventKind = "item"

	// Assistant-side multimodal references (streaming). Adapters emit these instead of
	// overloading text_delta when the vendor returns image/file output items.
	EventAssistantImageRef EventKind = "assistant_image_ref"
	EventAssistantFileRef  EventKind = "assistant_file_ref"
)

// isContentClassKind reports kinds that must appear after both response_started
// and message_started in the canonical stream. Keep this in sync with the
// content-class group in the sequence validators below.
func isContentClassKind(k EventKind) bool {
	switch k {
	case EventTextDelta, EventReasoningDelta, EventReasoningSignatureDelta, EventReasoningOpaqueDelta,
		EventReasoningPart,
		EventToolCallStarted, EventToolCallArgsDelta, EventToolCallFinished,
		EventAssistantImageRef, EventAssistantFileRef:
		return true
	}
	return false
}

// Event is one canonical streaming item.
type Event struct {
	Kind EventKind
	// Decision carries the validated result on EventDecisionResult only.
	Decision *DecisionResult `json:",omitempty"`

	MessageIndex int
	Delta        string
	// Signature carries the Anthropic thinking-block signature on
	// EventReasoningSignatureDelta; it is empty for other kinds.
	Signature string
	// Opaque carries provider opaque reasoning bytes on EventReasoningOpaqueDelta
	// (Anthropic redacted_thinking JSON envelope). Empty for other kinds.
	Opaque []byte
	// Reasoning carries one complete dialect-tagged reasoning part on
	// EventReasoningPart. Nil for other kinds. Callers must not mutate Opaque
	// bytes after the event is handed to streams/collectors; receivers deep-copy.
	Reasoning *ReasoningPart
	// Item carries one complete canonical Item on EventItem (for example a
	// compaction item preserved in a compacted ordered window). Nil for other
	// kinds; EventItem events must not carry content-class fields. Receivers
	// deep-copy before retaining.
	Item       *Item
	ToolCallID string
	ToolName   string

	// Usage fields apply to EventUsageDelta. InputTokens and OutputTokens are
	// retained as the compatibility totals used by existing frontends.
	InputTokens  int
	OutputTokens int
	// CacheReadTokens are submitted/input tokens served from a provider cache.
	CacheReadTokens int
	// CacheWriteTokens are submitted/input tokens written to a provider cache.
	CacheWriteTokens int
	// ReasoningTokens are response/output tokens used for hidden reasoning or
	// thinking when the provider reports them separately.
	ReasoningTokens int
	// TotalTokens is the provider-reported total when available.
	TotalTokens int
	// UsagePresence distinguishes explicitly reported zero counters from
	// counters omitted by a partial provider response.
	UsagePresence UsagePresence
	// CostNanoUnits is a high-precision per-response cost amount in Currency.
	// One whole currency unit is 1e9 nano-units.
	CostNanoUnits int64
	Currency      string
	CostSource    string
	// CostPresent distinguishes an explicitly reported monetary value (including
	// authoritative zero) from a usage event that omits cost. CostSource alone
	// is provenance metadata and must not imply a present monetary value.
	CostPresent bool
	// RawUsageJSON stores bounded provider usage metadata for audit/backfill.
	RawUsageJSON string
	// Accounting annotates the compatibility usage totals on EventUsageDelta.
	Accounting UsageAccountingMetadata
	// UsageScopes carries per-plane usage entries for EventUsageDelta.
	UsageScopes []ScopedUsageDelta

	WarningCode    string
	WarningMessage string

	ErrorCode    string
	ErrorMessage string

	// FinishReason is optional metadata on EventResponseFinished (vendor stop/finish taxonomy).
	FinishReason string

	// ResponseStatus is explicit terminal status on EventResponseFinished
	// ("completed" or "incomplete"). It is authoritative when set: producers that
	// know the upstream response status (for example an OpenResponses backend
	// mapping an upstream resource whose status is "incomplete") set it so
	// downstream state machines never have to infer incompleteness solely from
	// FinishReason, which is ambiguous (a completed response may legitimately
	// carry finish_reason "content_filter"). Empty means the producer left the
	// terminal semantics to the frontend's legacy inference.
	ResponseStatus string

	// AssistantRef / AssistantMIME / AssistantName apply to EventAssistantImageRef and
	// EventAssistantFileRef (same meaning as Part.ImageRef / Part.FileRef fields).
	AssistantRef  string
	AssistantMIME string
	AssistantName string
}

// Structural storage sizes for PayloadBytes memory accounting, computed once
// so the hot path stays reflection-free.
var (
	scopedUsageDeltaSize = int(reflect.TypeFor[ScopedUsageDelta]().Size())
	decisionAnswerSize   = int(reflect.TypeFor[DecisionAnswer]().Size())
	decisionResultSize   = int(reflect.TypeFor[DecisionResult]().Size())
	reasoningPartSize    = int(reflect.TypeFor[ReasoningPart]().Size())
	itemSize             = int(reflect.TypeFor[Item]().Size())
)

// PayloadBytes returns the retained payload bytes of one event for memory
// bounding: every retained string, byte-slice, and slice field, including
// nested Decision, Reasoning (with dialect), Item, Accounting, and UsageScopes
// entries. The estimate includes element backing storage (slice length times
// element size) and pointed-to struct storage for each non-nil pointer. It is
// a cheap field walk without JSON marshaling and without reflection on the hot
// path (element sizes are computed once above); a new memory-bearing Event
// field must be added here (the coverage test in event_payload_bytes_test.go
// fails until it is).
func (e Event) PayloadBytes() int {
	n := len(e.Kind) + len(e.Delta) + len(e.Signature) + len(e.Opaque) +
		len(e.ToolCallID) + len(e.ToolName) + len(e.Currency) + len(e.CostSource) +
		len(e.RawUsageJSON) + len(e.WarningCode) + len(e.WarningMessage) +
		len(e.ErrorCode) + len(e.ErrorMessage) + len(e.FinishReason) +
		len(e.ResponseStatus) + len(e.AssistantRef) + len(e.AssistantMIME) +
		len(e.AssistantName)
	n += decisionPayloadBytes(e.Decision)
	if e.Reasoning != nil {
		n += reasoningPartSize + len(e.Reasoning.Dialect) + ReasoningPayloadBytes(e.Reasoning)
	}
	if e.Item != nil {
		n += itemSize + e.Item.PayloadBytes()
	}
	n += accountingPayloadBytes(e.Accounting)
	n += len(e.UsageScopes) * scopedUsageDeltaSize
	for i := range e.UsageScopes {
		n += accountingPayloadBytes(e.UsageScopes[i].Accounting)
	}
	return n
}

// decisionPayloadBytes is the retained string and slice bytes of a decision
// result: the pointed-to struct, model, per-answer IDs, kinds, selections, and
// probability backing arrays (8 bytes per float64) plus a present confidence
// value, on top of per-answer element storage.
func decisionPayloadBytes(d *DecisionResult) int {
	if d == nil {
		return 0
	}
	n := decisionResultSize + len(d.Model) + len(d.Answers)*decisionAnswerSize
	for i := range d.Answers {
		a := &d.Answers[i]
		n += len(a.QuestionID) + len(a.Kind) + len(a.Selected) + len(a.Probabilities)*8
		if a.Confidence != nil {
			n += 8
		}
	}
	return n
}

// accountingPayloadBytes is the retained string bytes of usage accounting
// metadata: plane, source, authority, tokenizer reference, provider
// correlation keys, service context, and dedupe key.
func accountingPayloadBytes(m UsageAccountingMetadata) int {
	return len(m.Plane) + len(m.Source) + len(m.Authority) +
		len(m.Tokenizer.Type) + len(m.Tokenizer.ID) + len(m.Tokenizer.Version) +
		len(m.Tokenizer.Source) + len(m.Tokenizer.ModelUsed) +
		len(m.ProviderAccountKey) + len(m.ProviderRequestID) + len(m.ProviderChargeID) +
		len(m.ServiceContext) + len(m.DedupeKey)
}

// ValidateEventEnvelope applies maximum sizes and kind-specific structural checks to
// canonical event fields (codec output, backend mapping, or hook mutations) so one
// stream chunk cannot force unbounded allocations. It does not mutate ev.
func ValidateEventEnvelope(ev *Event) error {
	if ev == nil {
		return &ValidationError{Field: "Event", Message: "nil event"}
	}
	if len(ev.Delta) > MaxEventDeltaBytes {
		return &ValidationError{Field: "Delta", Message: fmt.Sprintf("exceeds %d bytes", MaxEventDeltaBytes)}
	}
	if len(ev.ToolCallID) > MaxRefStringBytes {
		return &ValidationError{Field: "ToolCallID", Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	if len(ev.ToolName) > MaxRefStringBytes {
		return &ValidationError{Field: "ToolName", Message: fmt.Sprintf("exceeds %d bytes", MaxRefStringBytes)}
	}
	if len(ev.WarningCode) > MaxEventCodeFieldBytes {
		return &ValidationError{Field: "WarningCode", Message: fmt.Sprintf("exceeds %d bytes", MaxEventCodeFieldBytes)}
	}
	if len(ev.WarningMessage) > MaxEventDiagMessageBytes {
		return &ValidationError{Field: "WarningMessage", Message: fmt.Sprintf("exceeds %d bytes", MaxEventDiagMessageBytes)}
	}
	if len(ev.ErrorCode) > MaxEventCodeFieldBytes {
		return &ValidationError{Field: "ErrorCode", Message: fmt.Sprintf("exceeds %d bytes", MaxEventCodeFieldBytes)}
	}
	if len(ev.ErrorMessage) > MaxEventDiagMessageBytes {
		return &ValidationError{Field: "ErrorMessage", Message: fmt.Sprintf("exceeds %d bytes", MaxEventDiagMessageBytes)}
	}
	if err := validateStringField("FinishReason", ev.FinishReason, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("ResponseStatus", ev.ResponseStatus, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("AssistantRef", ev.AssistantRef, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("AssistantMIME", ev.AssistantMIME, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("AssistantName", ev.AssistantName, MaxRefStringBytes); err != nil {
		return err
	}
	if err := validateStringField("Signature", ev.Signature, MaxRefStringBytes); err != nil {
		return err
	}
	if len(ev.Opaque) > MaxReasoningOpaqueBytes {
		return &ValidationError{Field: "Opaque", Message: fmt.Sprintf("exceeds %d bytes", MaxReasoningOpaqueBytes)}
	}
	if err := ev.Accounting.validate("Accounting"); err != nil {
		return err
	}
	if err := validateScopedUsage(ev.UsageScopes); err != nil {
		return err
	}
	if ev.Kind != EventReasoningPart && ev.Reasoning != nil {
		return &ValidationError{Field: "Reasoning", Message: "only allowed on reasoning_part"}
	}
	if ev.Kind != EventItem && ev.Item != nil {
		return &ValidationError{Field: "Item", Message: "only allowed on item events"}
	}
	if ev.Kind != EventDecisionResult && ev.Decision != nil {
		return &ValidationError{Field: "Decision", Message: "only allowed on decision_result"}
	}
	switch ev.Kind {
	case EventDecisionResult:
		if ev.Decision == nil {
			return &ValidationError{Field: "Decision", Message: "required for decision_result"}
		}
	case EventReasoningPart:
		if ev.Reasoning == nil {
			return &ValidationError{Field: "Reasoning", Message: "required for reasoning_part"}
		}
		if err := validateReasoningPart(ev.Reasoning); err != nil {
			return &ValidationError{Field: "Reasoning", Message: err.Error()}
		}
		if err := validateReasoningPartExclusiveFields(ev); err != nil {
			return err
		}
	case EventItem:
		if ev.Item == nil {
			return &ValidationError{Field: "Item", Message: "required for item events"}
		}
		if err := ev.Item.validate(eventItemField()); err != nil {
			return &ValidationError{Field: "Item", Message: err.Error()}
		}
	case EventAssistantImageRef, EventAssistantFileRef:
		if strings.TrimSpace(ev.AssistantRef) == "" {
			return &ValidationError{Field: "AssistantRef", Message: "required for assistant media ref events"}
		}
	case EventResponseFinished:
		// FinishReason optional
	default:
		// Other kinds ignore assistant/finish fields at envelope validation time.
	}
	return nil
}

// validateReasoningPartExclusiveFields rejects event-level carriers that belong to
// other EventKinds. Exact reasoning payload lives only on Event.Reasoning.
func validateReasoningPartExclusiveFields(ev *Event) error {
	if ev == nil {
		return nil
	}
	switch {
	case ev.Delta != "":
		return &ValidationError{Field: "Delta", Message: "not allowed on reasoning_part"}
	case ev.Signature != "":
		return &ValidationError{Field: "Signature", Message: "not allowed on reasoning_part"}
	case len(ev.Opaque) > 0:
		return &ValidationError{Field: "Opaque", Message: "not allowed on reasoning_part"}
	case ev.ToolCallID != "":
		return &ValidationError{Field: "ToolCallID", Message: "not allowed on reasoning_part"}
	case ev.ToolName != "":
		return &ValidationError{Field: "ToolName", Message: "not allowed on reasoning_part"}
	case strings.TrimSpace(ev.AssistantRef) != "":
		return &ValidationError{Field: "AssistantRef", Message: "not allowed on reasoning_part"}
	case strings.TrimSpace(ev.AssistantMIME) != "":
		return &ValidationError{Field: "AssistantMIME", Message: "not allowed on reasoning_part"}
	case strings.TrimSpace(ev.AssistantName) != "":
		return &ValidationError{Field: "AssistantName", Message: "not allowed on reasoning_part"}
	}
	return nil
}

// EventStream is the primary execution result from backends and the executor.
// Implementations assume a single goroutine calls Recv until completion or error
// (no concurrent Recv on the same stream). Close may run concurrently with a
// blocked Recv only when the implementation documents that guarantee locally.
// See each concrete stream type for its Close/Recv concurrency contract.
//
// Cancellation: Recv should respect ctx when the underlying source supports it
// (for example blocking on a channel that also selects on ctx.Done). Some vendor
// SDKs block without consulting ctx; in those cases Recv may remain blocked until
// Close unblocks the SDK. Callers that cancel ctx must still call Close (typically
// via defer right after obtaining the stream) so blocked reads and background work
// tear down promptly.
//
// Recv requires a non-nil ctx (Go context contract). Passing nil returns [ErrNilContext]
// from core and reference implementations in this module; other implementations should
// follow the same rule or document divergent behavior.
type EventStream interface {
	Recv(ctx context.Context) (Event, error) // io.EOF means normal completion after terminal event
	Close() error
}

// FixedEventStream is a finite stream for tests and in-memory adapters.
// It is not safe for concurrent Recv; use one consumer at a time.
// Recv on a nil *FixedEventStream returns [ErrNilFixedEventStream]. Close on nil returns nil.
type FixedEventStream struct {
	events []Event
	pos    int
}

// NewFixedEventStream returns a copied finite stream for tests and in-memory adapters.
func NewFixedEventStream(events []Event) *FixedEventStream {
	s := make([]Event, len(events))
	for i, ev := range events {
		s[i] = cloneEvent(ev)
	}
	return &FixedEventStream{events: s}
}

func (f *FixedEventStream) Recv(ctx context.Context) (Event, error) {
	if f == nil {
		return Event{}, ErrNilFixedEventStream
	}
	if ctx == nil {
		return Event{}, ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if f.pos >= len(f.events) {
		return Event{}, io.EOF
	}
	e := cloneEvent(f.events[f.pos])
	f.pos++
	return e, nil
}

func (f *FixedEventStream) Close() error { return nil }

func (f *FixedEventStream) Cancel(context.Context, CancelCause) CancelResult {
	return CancelResult{Mode: CancelModeCloseOnly}
}

// Collected aggregates a canonical stream for non-streaming responses.
type Collected struct {
	Text      strings.Builder
	Reasoning strings.Builder
	ToolArgs  map[string]*strings.Builder // keyed by tool_call_id
	// ToolNames maps tool_call_id to the function name from EventToolCallStarted.
	ToolNames map[string]string
	// ToolCallOrder is the order tool_call_ids first appear (started or first args delta).
	ToolCallOrder    []string
	Warnings         []string
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	ReasoningTokens  int
	TotalTokens      int
	CostNanoUnits    int64
	Currency         string
	CostSource       string
	TerminalError    *Event
	FinishReceived   bool

	// FinishReason is copied from the terminal EventResponseFinished when set.
	FinishReason string
	// AssistantMedia collects EventAssistantImageRef / EventAssistantFileRef in order.
	AssistantMedia []Part
	// ReasoningParts collects EventReasoningPart payloads in stream emission order.
	// It does not encode placement among text/tool parts; feature observers own
	// cross-part positioning for restore. Distinct from Reasoning, which aggregates
	// progressive EventReasoningDelta text only.
	ReasoningParts []ReasoningPart
	// cacheReadExcludedFromInput tracks reads already excluded by their usage
	// source, without changing the public compatibility counters.
	cacheReadExcludedFromInput int
	// cacheWriteExcludedFromInput tracks writes already excluded by their
	// usage source, without changing the public compatibility counters.
	cacheWriteExcludedFromInput int
	// clientVisibleUsageSelected records that the turn selected the explicit
	// client-visible usage perspective. Until then the collector retains the
	// legacy unscoped fallback accumulation.
	clientVisibleUsageSelected bool
}

// ToolCallSummary is one completed tool invocation aggregated from the stream.
type ToolCallSummary struct {
	ID        string
	Name      string
	Arguments string
}

// TotalOrDerived returns TotalTokens when present, otherwise derives a protocol-safe total
// from input and output token counts.
func (c Collected) TotalOrDerived() int {
	if c.TotalTokens != 0 {
		return c.TotalTokens
	}
	return c.InputTokens + c.OutputTokens
}

// UncachedInputTokens returns input tokens outside provider cache reads.
// AccumulateUsage honors each selected usage entry's cache-read inclusion
// metadata. Directly populated counters retain the legacy inclusive assumption.
func (c Collected) UncachedInputTokens() int {
	uncached := c.InputTokens - (c.CacheReadTokens - c.cacheReadExcludedFromInput)
	if uncached < 0 {
		return 0
	}
	return uncached
}

// InclusiveInputTokens returns the input quantity including the provider cache
// reads and cache writes that a producer declared outside its input.
//
// Each declaration is honored on its own: only the excluded reads of an entry
// that declares the cache-read exclusion and the excluded writes of an entry
// that declares the cache-write exclusion are added back. A producer whose
// input counter already contains them is returned unchanged.
func (c Collected) InclusiveInputTokens() int {
	return c.InputTokens + c.cacheReadExcludedFromInput + c.cacheWriteExcludedFromInput
}

// OrderedToolCalls returns tool calls in first-seen order for stable encoding.
func (c Collected) OrderedToolCalls() []ToolCallSummary {
	if len(c.ToolCallOrder) == 0 {
		return nil
	}
	out := make([]ToolCallSummary, 0, len(c.ToolCallOrder))
	for _, id := range c.ToolCallOrder {
		name := ""
		if c.ToolNames != nil {
			name = c.ToolNames[id]
		}
		args := ""
		if b := c.ToolArgs[id]; b != nil {
			args = b.String()
		}
		out = append(out, ToolCallSummary{ID: id, Name: name, Arguments: args})
	}
	return out
}

// Collect drains a stream until a terminal event or an error using DefaultCollectLimits.
// Terminal success is EventResponseFinished. Terminal failure is EventError followed by optional EOF.
// ctx must be non-nil; nil returns [ErrNilContext].
func Collect(ctx context.Context, s EventStream) (Collected, error) {
	return CollectWithLimits(ctx, s, DefaultCollectLimits())
}

// CollectUnbounded aggregates without CollectLimits checks (legacy / testing only).
// ctx must be non-nil; nil returns [ErrNilContext].
func CollectUnbounded(ctx context.Context, s EventStream) (Collected, error) {
	return CollectWithLimits(ctx, s, CollectLimits{})
}

// CollectWithLimits drains a stream until a terminal event or an error.
// Terminal success is EventResponseFinished. Terminal failure is EventError followed by optional EOF.
// ctx must be non-nil; nil returns [ErrNilContext].
func CollectWithLimits(ctx context.Context, s EventStream, limits CollectLimits) (out Collected, err error) {
	if ctx == nil {
		return Collected{}, ErrNilContext
	}
	if s == nil {
		return Collected{}, ErrNilEventStream
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			closeErr := fmt.Errorf("aipapi: close event stream: %w", cerr)
			if err != nil {
				err = errors.Join(err, closeErr)
			} else {
				err = closeErr
			}
		}
	}()

	out = Collected{}
	out.ToolArgs = make(map[string]*strings.Builder)
	out.ToolNames = make(map[string]string)
	seenTool := make(map[string]struct{})
	var toolArgBytes int
	var exactReasoningBytes int
	var aggregateBytes int

	var sawResponseStarted bool
	var sawMessage bool

	var ev Event
	for {
		ev, err = s.Recv(ctx)
		if errors.Is(err, io.EOF) {
			if out.FinishReceived {
				return out, nil
			}
			if out.TerminalError != nil {
				return out, terminalError(*out.TerminalError)
			}
			return out, fmt.Errorf("stream ended without terminal event")
		}
		if err != nil {
			return out, err
		}

		switch ev.Kind {
		case EventResponseStarted:
			if sawResponseStarted {
				return out, fmt.Errorf("duplicate %s", EventResponseStarted)
			}
			sawResponseStarted = true
		case EventMessageStarted:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", EventMessageStarted, EventResponseStarted)
			}
			sawMessage = true
		case EventUsageDelta, EventWarning, EventItem:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			// Usage and warnings may appear without a message frame on some adapters.
			// Standalone item carriers (EventItem) are preserved by OR-specific
			// consumers; the generic aggregate simply acknowledges them.
		case EventError:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", EventError, EventResponseStarted)
			}
			cp := ev
			out.TerminalError = &cp
			return out, terminalError(ev)
		case EventResponseFinished:
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", EventResponseFinished, EventResponseStarted)
			}
			out.FinishReceived = true
			out.FinishReason = ev.FinishReason
			return out, nil
		default:
			if !isContentClassKind(ev.Kind) {
				return out, fmt.Errorf("unknown event kind %q", ev.Kind)
			}
			if !sawResponseStarted {
				return out, fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			if !sawMessage {
				return out, fmt.Errorf("%s before %s", ev.Kind, EventMessageStarted)
			}
		}

		switch ev.Kind {
		case EventTextDelta:
			add := len(ev.Delta)
			if aggregateWouldExceed(aggregateBytes, add, limits.MaxAggregatePayloadBytes) {
				return out, fmt.Errorf("%w: aggregate payload would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxAggregatePayloadBytes)
			}
			if limits.MaxTextBytes > 0 && out.Text.Len()+add > limits.MaxTextBytes {
				return out, fmt.Errorf("%w: text aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxTextBytes)
			}
			out.Text.WriteString(ev.Delta)
			if aggregateBytes > math.MaxInt-add {
				aggregateBytes = math.MaxInt
			} else {
				aggregateBytes += add
			}
		case EventReasoningDelta:
			add := len(ev.Delta)
			if aggregateWouldExceed(aggregateBytes, add, limits.MaxAggregatePayloadBytes) {
				return out, fmt.Errorf("%w: aggregate payload would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxAggregatePayloadBytes)
			}
			if reasoningAggregateWouldExceed(out.Reasoning.Len(), exactReasoningBytes, add, limits.MaxReasoningBytes) {
				return out, fmt.Errorf("%w: reasoning aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxReasoningBytes)
			}
			out.Reasoning.WriteString(ev.Delta)
			if aggregateBytes > math.MaxInt-add {
				aggregateBytes = math.MaxInt
			} else {
				aggregateBytes += add
			}
		case EventReasoningPart:
			if ev.Reasoning == nil {
				return out, fmt.Errorf("%s without reasoning payload", EventReasoningPart)
			}
			// Compute payload bytes before cloning to enforce aggregate limit before allocation.
			preAdd := ReasoningPayloadBytes(ev.Reasoning)
			if aggregateWouldExceed(aggregateBytes, preAdd, limits.MaxAggregatePayloadBytes) {
				return out, fmt.Errorf("%w: aggregate payload would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxAggregatePayloadBytes)
			}
			if reasoningAggregateWouldExceed(out.Reasoning.Len(), exactReasoningBytes, preAdd, limits.MaxReasoningBytes) {
				return out, fmt.Errorf("%w: reasoning aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxReasoningBytes)
			}
			cloned := cloneReasoningPart(ev.Reasoning)
			if err := validateReasoningPart(cloned); err != nil {
				return out, fmt.Errorf("%s: %w", EventReasoningPart, err)
			}
			add := ReasoningPayloadBytes(cloned)
			// If cloned size differs from pre-check (defensive), re-validate aggregate/reasoning limits.
			if add != preAdd {
				if aggregateWouldExceed(aggregateBytes, add, limits.MaxAggregatePayloadBytes) {
					return out, fmt.Errorf("%w: aggregate payload would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxAggregatePayloadBytes)
				}
				if reasoningAggregateWouldExceed(out.Reasoning.Len(), exactReasoningBytes, add, limits.MaxReasoningBytes) {
					return out, fmt.Errorf("%w: reasoning aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxReasoningBytes)
				}
			}
			if exactReasoningBytes > math.MaxInt-add {
				return out, fmt.Errorf("%w: reasoning aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxReasoningBytes)
			}
			exactReasoningBytes += add
			if aggregateBytes > math.MaxInt-add {
				aggregateBytes = math.MaxInt
			} else {
				aggregateBytes += add
			}
			out.ReasoningParts = append(out.ReasoningParts, *cloned)
		case EventToolCallStarted:
			if strings.TrimSpace(ev.ToolCallID) == "" {
				return out, fmt.Errorf("%s without tool_call_id", EventToolCallStarted)
			}
			id := ev.ToolCallID
			if _, ok := seenTool[id]; !ok {
				seenTool[id] = struct{}{}
				out.ToolCallOrder = append(out.ToolCallOrder, id)
			}
			if strings.TrimSpace(ev.ToolName) != "" {
				out.ToolNames[id] = ev.ToolName
			}
		case EventToolCallArgsDelta:
			if strings.TrimSpace(ev.ToolCallID) == "" {
				return out, fmt.Errorf("%s without tool_call_id", EventToolCallArgsDelta)
			}
			add := len(ev.Delta)
			if aggregateWouldExceed(aggregateBytes, add, limits.MaxAggregatePayloadBytes) {
				return out, fmt.Errorf("%w: aggregate payload would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxAggregatePayloadBytes)
			}
			if limits.MaxToolArgsTotalBytes > 0 && toolArgBytes+add > limits.MaxToolArgsTotalBytes {
				return out, fmt.Errorf("%w: tool arguments aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxToolArgsTotalBytes)
			}
			id := ev.ToolCallID
			if _, ok := seenTool[id]; !ok {
				seenTool[id] = struct{}{}
				out.ToolCallOrder = append(out.ToolCallOrder, id)
			}
			b := out.ToolArgs[id]
			if b == nil {
				nb := new(strings.Builder)
				out.ToolArgs[id] = nb
				b = nb
			}
			if toolArgBytes > math.MaxInt-add {
				return out, fmt.Errorf("%w: tool arguments aggregate would exceed %d bytes", ErrCollectLimitExceeded, limits.MaxToolArgsTotalBytes)
			}
			toolArgBytes += add
			if aggregateBytes > math.MaxInt-add {
				aggregateBytes = math.MaxInt
			} else {
				aggregateBytes += add
			}
			b.WriteString(ev.Delta)
		case EventToolCallFinished:
			if strings.TrimSpace(ev.ToolCallID) == "" {
				return out, fmt.Errorf("%s without tool_call_id", EventToolCallFinished)
			}
			id := ev.ToolCallID
			if _, ok := seenTool[id]; !ok {
				seenTool[id] = struct{}{}
				out.ToolCallOrder = append(out.ToolCallOrder, id)
			}
		case EventWarning:
			if limits.MaxWarnings > 0 && len(out.Warnings) >= limits.MaxWarnings {
				return out, fmt.Errorf("%w: warning count would exceed %d", ErrCollectLimitExceeded, limits.MaxWarnings)
			}
			out.Warnings = append(out.Warnings, ev.WarningMessage)
		case EventUsageDelta:
			out.AccumulateUsage(ev)
		case EventAssistantImageRef:
			if limits.MaxAssistantMediaParts > 0 && len(out.AssistantMedia) >= limits.MaxAssistantMediaParts {
				return out, fmt.Errorf("%w: assistant media parts would exceed %d", ErrCollectLimitExceeded, limits.MaxAssistantMediaParts)
			}
			p := Part{Kind: PartImageRef, ImageRef: ev.AssistantRef, ImageMIME: ev.AssistantMIME}
			if err := p.validate(); err != nil {
				return out, fmt.Errorf("assistant_image_ref: %w", err)
			}
			out.AssistantMedia = append(out.AssistantMedia, p)
		case EventAssistantFileRef:
			if limits.MaxAssistantMediaParts > 0 && len(out.AssistantMedia) >= limits.MaxAssistantMediaParts {
				return out, fmt.Errorf("%w: assistant media parts would exceed %d", ErrCollectLimitExceeded, limits.MaxAssistantMediaParts)
			}
			p := Part{Kind: PartFileRef, FileRef: ev.AssistantRef, FileMIME: ev.AssistantMIME, FileName: ev.AssistantName}
			if err := p.validate(); err != nil {
				return out, fmt.Errorf("assistant_file_ref: %w", err)
			}
			out.AssistantMedia = append(out.AssistantMedia, p)
		}
	}
}

// AccumulateUsage folds one usage delta into the collected turn.
//
// The turn reports one client usage perspective. A client-visible scope, or a
// top-level client-visible event without scopes, selects the explicit
// client-visible perspective, including an explicit zero. The first selection
// replaces the usage counters, exclusion corrections and usage cost metadata
// accumulated from the fallback perspective; text, tools, warnings and terminal
// state are preserved. Later fallback quantities and costs are ignored, and
// explicit client-visible deltas accumulate. The incoming event is never
// mutated, so provider evidence stays available to other consumers.
func (c *Collected) AccumulateUsage(ev Event) {
	entries, explicit := clientVisibleUsageEntries(ev)
	if !explicit {
		if c.clientVisibleUsageSelected {
			// The selected perspective owns the turn; this delta belongs to
			// another accounting perspective.
			return
		}
		c.accumulateUsageEntry(shellUsageEntry(ev))
		c.accumulateUsageCost(ev)
		return
	}
	if !c.clientVisibleUsageSelected {
		c.resetUsagePerspective()
		c.clientVisibleUsageSelected = true
	}
	for _, entry := range entries {
		c.accumulateUsageEntry(entry)
	}
	c.accumulateUsageCost(ev)
}

// clientVisibleUsageEntries returns the client-visible usage entries carried by
// ev and reports whether ev selects the explicit client-visible perspective.
//
// A payload that carries a client-visible scope selects explicit usage, and a
// scope-less event whose own accounting plane is client-visible selects it as
// well. Scoped payloads take precedence over the event shell, and every
// client-visible entry of the payload contributes.
func clientVisibleUsageEntries(ev Event) ([]ScopedUsageDelta, bool) {
	if len(ev.UsageScopes) > 0 {
		entries := make([]ScopedUsageDelta, 0, len(ev.UsageScopes))
		for _, scope := range ev.UsageScopes {
			if scope.Accounting.Plane == UsagePlaneClientVisible {
				entries = append(entries, scope)
			}
		}
		return entries, len(entries) > 0
	}
	if ev.Accounting.Plane == UsagePlaneClientVisible {
		return []ScopedUsageDelta{shellUsageEntry(ev)}, true
	}
	return nil, false
}

// shellUsageEntry returns the event's own legacy totals as one usage entry.
func shellUsageEntry(ev Event) ScopedUsageDelta {
	return ScopedUsageDelta{
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		UsagePresence:    ev.UsagePresence,
		Accounting:       ev.Accounting,
	}
}

// accumulateUsageEntry adds one usage entry to the collected counters,
// honoring the entry's own cache-read and cache-write inclusion metadata.
// The two declarations are independent: an entry may exclude reads, writes or
// both from its input quantity.
func (c *Collected) accumulateUsageEntry(entry ScopedUsageDelta) {
	if entry.Accounting.InputTokensExcludeCacheRead {
		c.cacheReadExcludedFromInput += entry.CacheReadTokens
	}
	if entry.Accounting.InputTokensExcludeCacheWrite {
		c.cacheWriteExcludedFromInput += entry.CacheWriteTokens
	}
	c.InputTokens += entry.InputTokens
	c.OutputTokens += entry.OutputTokens
	c.CacheReadTokens += entry.CacheReadTokens
	c.CacheWriteTokens += entry.CacheWriteTokens
	c.ReasoningTokens += entry.ReasoningTokens
	c.TotalTokens = mergeTotalTokens(c.TotalTokens, entry.TotalTokens)
}

// accumulateUsageCost adds the event's usage cost metadata to the cost of the
// perspective the event contributes to.
func (c *Collected) accumulateUsageCost(ev Event) {
	c.CostNanoUnits += ev.CostNanoUnits
	if ev.Currency != "" {
		c.Currency = ev.Currency
	}
	if ev.CostSource != "" {
		c.CostSource = ev.CostSource
	}
}

// resetUsagePerspective clears the usage counters, exclusion corrections and
// usage cost metadata accumulated from the fallback perspective. Text, tools,
// warnings, terminal fields and other non-usage state are preserved.
func (c *Collected) resetUsagePerspective() {
	c.InputTokens = 0
	c.OutputTokens = 0
	c.CacheReadTokens = 0
	c.CacheWriteTokens = 0
	c.ReasoningTokens = 0
	c.TotalTokens = 0
	c.cacheReadExcludedFromInput = 0
	c.cacheWriteExcludedFromInput = 0
	c.CostNanoUnits = 0
	c.Currency = ""
	c.CostSource = ""
}

// ClientVisibleUsage returns the usage scope safe to expose on frontend protocols.
// When no scoped client-visible usage is present it falls back to legacy Event totals.
//
// It stays an event-local accessor: the turn-wide precedence between the
// fallback and the client-visible perspective is collector state.
func ClientVisibleUsage(ev Event) ScopedUsageDelta {
	for _, scope := range ev.UsageScopes {
		if scope.Accounting.Plane == UsagePlaneClientVisible {
			return scope
		}
	}
	return ScopedUsageDelta{
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		ReasoningTokens:  ev.ReasoningTokens,
		TotalTokens:      ev.TotalTokens,
		UsagePresence:    ev.UsagePresence,
		Accounting:       ev.Accounting,
	}
}

func mergeTotalTokens(cur, next int) int {
	if next == 0 {
		return cur
	}
	return next
}

func terminalError(ev Event) error {
	return NewStreamError(ev.ErrorCode, ev.ErrorMessage)
}

// aggregateWouldExceed reports whether adding add bytes to current would exceed maxLen.
// maxLen<=0 means unlimited. Overflow-safe.
func aggregateWouldExceed(current, add, maxLen int) bool {
	if maxLen <= 0 {
		return false
	}
	if current < 0 || add < 0 {
		return true
	}
	if current > maxLen {
		return true
	}
	if add > maxLen-current {
		return true
	}
	return false
}

// reasoningAggregateWouldExceed reports whether adding add bytes to text+exact
// reasoning aggregates would exceed maxLen. maxLen<=0 means unlimited. Overflow-safe.
func reasoningAggregateWouldExceed(textLen, exactLen, add, maxLen int) bool {
	if maxLen <= 0 {
		return false
	}
	if textLen < 0 || exactLen < 0 || add < 0 {
		return true
	}
	used := textLen + exactLen
	if used < textLen {
		return true
	}
	if add > maxLen-used {
		return true
	}
	return false
}

// ValidateEventSequence checks ordering rules for a replayed event slice.
// A well-formed sequence ends with EventResponseFinished or EventError after EventResponseStarted.
func ValidateEventSequence(events []Event) error {
	var sawResponseStarted bool
	var sawMessage bool
	var sawDecision bool

	for _, ev := range events {
		switch ev.Kind {
		case EventResponseStarted:
			if sawResponseStarted {
				return fmt.Errorf("duplicate %s", EventResponseStarted)
			}
			sawResponseStarted = true
		case EventMessageStarted:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", EventMessageStarted, EventResponseStarted)
			}
			sawMessage = true
		case EventUsageDelta, EventWarning, EventItem:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			// Usage, warnings, and standalone item carriers may appear without a
			// message frame on some adapters.
		case EventDecisionResult:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			if sawDecision {
				return fmt.Errorf("duplicate %s", EventDecisionResult)
			}
			sawDecision = true
		case EventError:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", EventError, EventResponseStarted)
			}
			return nil
		case EventResponseFinished:
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", EventResponseFinished, EventResponseStarted)
			}
			return nil
		default:
			if !isContentClassKind(ev.Kind) {
				return fmt.Errorf("unknown event kind %q", ev.Kind)
			}
			if !sawResponseStarted {
				return fmt.Errorf("%s before %s", ev.Kind, EventResponseStarted)
			}
			if !sawMessage {
				return fmt.Errorf("%s before %s", ev.Kind, EventMessageStarted)
			}
		}
	}
	return fmt.Errorf("missing terminal event")
}
