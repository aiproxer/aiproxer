package openaiwire

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Finish reason vocabulary shared across the bundled frontends. The values are
// the vendor lexemes canonical backends put on EventResponseFinished.
const (
	// finishReasonLength is the OpenAI Chat/Responses output-limit lexeme.
	finishReasonLength = "length"
	// finishReasonMaxTokens is the Anthropic and Bedrock output-limit lexeme.
	finishReasonMaxTokens = "max_tokens"
	// finishReasonMaxOutputTokens is the OpenAI Responses incomplete_details
	// discriminator.
	finishReasonMaxOutputTokens = "max_output_tokens"
)

// IsOutputLimitStop reports whether a canonical terminal event describes a turn
// that the upstream cut off because it hit the output-token limit.
//
// ResponseStatus is authoritative when set: an explicit "completed" is never
// rewritten to a truncation vocabulary, because a completed response may
// legitimately carry a vendor reason such as "content_filter". When the
// producer left ResponseStatus empty the terminal semantics fall back to legacy
// inference over the vendor reason, matching the OpenResponses state machine
// (which infers only the token-limit taxonomy, never a bare non-empty reason).
//
// An empty vendor reason means absent, never terminal: some relays stamp
// finish_reason "" on every chunk.
func IsOutputLimitStop(status, reason string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "incomplete":
		switch strings.ToLower(strings.TrimSpace(reason)) {
		case finishReasonLength, finishReasonMaxTokens, finishReasonMaxOutputTokens:
			return true
		}
		return false
	case "":
		switch strings.ToLower(strings.TrimSpace(reason)) {
		case finishReasonLength, finishReasonMaxTokens:
			return true
		}
		return false
	default:
		return false
	}
}

// TerminalTee records the ResponseStatus of the terminal event while the
// canonical stream is collected. aipapi.Collected carries FinishReason but not
// ResponseStatus, so a non-stream encoder that must honour the authoritative
// status wraps its stream with TeeTerminal before collecting. The wrapped
// stream is passed through unchanged.
type TerminalTee struct {
	inner  aipapi.EventStream
	status string
}

// TeeTerminal wraps es so the terminal event's ResponseStatus is observable
// after collection.
func TeeTerminal(es aipapi.EventStream) *TerminalTee {
	return &TerminalTee{inner: es}
}

// Recv implements aipapi.EventStream, recording the terminal status.
func (t *TerminalTee) Recv(ctx context.Context) (aipapi.Event, error) {
	if t == nil || t.inner == nil {
		return aipapi.Event{}, aipapi.ErrNilContext
	}
	ev, err := t.inner.Recv(ctx)
	if err == nil && ev.Kind == aipapi.EventResponseFinished {
		t.status = ev.ResponseStatus
	}
	return ev, err
}

// Close implements aipapi.EventStream.
func (t *TerminalTee) Close() error {
	if t == nil || t.inner == nil {
		return nil
	}
	return t.inner.Close()
}

// Status returns the recorded terminal ResponseStatus, or "" when no terminal
// event was observed.
func (t *TerminalTee) Status() string {
	if t == nil {
		return ""
	}
	return t.status
}

// IsOutputLimitTerminal classifies a collected stream from the ResponseStatus
// recorded through TeeTerminal and the FinishReason aipapi.Collected copied.
func (t *TerminalTee) IsOutputLimitTerminal(col aipapi.Collected) bool {
	return IsOutputLimitStop(t.Status(), col.FinishReason)
}

// ToolArgsRawJSON normalizes canonical tool arguments into a JSON value that is
// safe to embed in a structured wire field.
//
// An upstream that hits the output-token limit cuts argument JSON off midway,
// so the accumulated text is often not parseable. That must never fail the whole
// response and must never be silently dropped: the raw text is preserved
// verbatim under a "value" key so the client still sees what arrived. An empty
// argument string becomes an empty object, matching a provider that returned no
// arguments at all.
func ToolArgsRawJSON(raw string) json.RawMessage {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return json.RawMessage("{}")
	}
	if !json.Valid([]byte(raw)) {
		wrapped, err := json.Marshal(struct {
			Value string `json:"value"`
		}{Value: raw})
		if err != nil {
			// A string always marshals; fall back to the empty object rather
			// than propagating an impossible marshal failure.
			return json.RawMessage("{}")
		}
		return wrapped
	}
	return json.RawMessage(raw)
}
