package localturn

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

// Bounds: consistent with steering's 64KiB cap.
const (
	MaxReasonCodeBytes = 64
	MaxReplyTextBytes  = 64 * 1024
	MaxHandlerIDBytes  = 128
)

// Re-export hooks failure mode for convenience; handlers must not import internal.
type FailureMode = hooks.FailureMode

const (
	FailureModeUnspecified = hooks.FailureModeUnspecified
	FailOpen               = hooks.FailOpen
	FailClosed             = hooks.FailClosed
)

// ReasonCode is a bounded non-secret identifier for diagnostics.
type ReasonCode string

// Validate reports whether the reason code is a bounded ascii identifier.
func (r ReasonCode) Validate() error {
	s := string(r)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("localturn: reason code is required")
	}
	if len(s) > MaxReasonCodeBytes {
		return fmt.Errorf("localturn: reason code exceeds %d bytes", MaxReasonCodeBytes)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("localturn: reason code must be ascii")
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("localturn: invalid character %q in reason code", ch)
		}
	}
	return nil
}

// ValidateHandlerID reports whether id is a bounded ascii identifier for a handler.
// Exported for feature-bundle validation; keeps vet/lint from flagging unused helper.
func ValidateHandlerID(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("localturn: handler id is required")
	}
	if len(trimmed) > MaxHandlerIDBytes {
		return fmt.Errorf("localturn: handler id exceeds %d bytes", MaxHandlerIDBytes)
	}
	for _, ch := range trimmed {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("localturn: handler id must be ascii")
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("localturn: invalid character %q in handler id", ch)
		}
	}
	return nil
}

// Meta carries authoritative per-request context for Match.
// MessageCount is the number of complete normalized messages in the ingress call
// available for claiming. Handlers must not claim indexes outside [0, MessageCount).
type Meta struct {
	TraceID      string
	MessageCount int
}

// MatchResult claims zero or more complete normalized source messages for local handling.
// When Claimed is false, the handler passes and Indexes/Reasons must be empty.
// When Claimed is true, Indexes identifies the complete message indexes in the
// normalized ingress call and Reason is the bounded identifier for source tagging.
type MatchResult struct {
	Claimed bool
	Indexes []int
	Reason  ReasonCode
}

// Validate reports whether the match result is well-formed for the given Meta.
// It enforces: claimed => non-empty indexes with bounded reason and each index in range,
// all indexes are distinct, non-claimed => empty indexes and empty reason.
func (m MatchResult) Validate(meta Meta) error {
	if !m.Claimed {
		if len(m.Indexes) != 0 {
			return fmt.Errorf("localturn: unclaimed result must have no indexes")
		}
		if strings.TrimSpace(string(m.Reason)) != "" {
			return fmt.Errorf("localturn: unclaimed result must have no reason")
		}
		return nil
	}
	if len(m.Indexes) == 0 {
		return fmt.Errorf("localturn: claimed result must have at least one index")
	}
	if err := m.Reason.Validate(); err != nil {
		return err
	}
	if meta.MessageCount < 0 {
		return fmt.Errorf("localturn: message count must be >= 0")
	}
	seen := make(map[int]struct{}, len(m.Indexes))
	for _, idx := range m.Indexes {
		if idx < 0 || idx >= meta.MessageCount {
			return fmt.Errorf("localturn: index %d out of range [0,%d)", idx, meta.MessageCount)
		}
		if _, dup := seen[idx]; dup {
			return fmt.Errorf("localturn: duplicate index %d", idx)
		}
		seen[idx] = struct{}{}
	}
	return nil
}

// HandleInput is the input to Handler.Handle after successful source tagging.
type HandleInput struct {
	Call  aipapi.Call
	Meta  Meta
	Match MatchResult
}

// Reply is the bounded assistant text returned by a local-turn handler.
// Core constructs the canonical assistant message, tags it, and builds the
// local finite EventStream from this content. Role is always assistant.
type Reply struct {
	Text string
}

// Validate reports whether the reply is well-formed.
func (r Reply) Validate() error {
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("localturn: reply text is required")
	}
	if len(r.Text) > MaxReplyTextBytes {
		return fmt.Errorf("localturn: reply text exceeds %d bytes", MaxReplyTextBytes)
	}
	if !utf8.ValidString(r.Text) {
		return fmt.Errorf("localturn: reply text must be valid utf8")
	}
	for _, ch := range r.Text {
		if ch == 0 {
			return fmt.Errorf("localturn: reply text must not contain NUL")
		}
	}
	return nil
}

// Handler is the generic local-turn extension seam contributed via FeatureBundle.
type Handler interface {
	ID() string
	Order() int
	FailureMode() hooks.FailureMode
	Match(ctx context.Context, call aipapi.Call, meta Meta) (MatchResult, error)
	Handle(ctx context.Context, input HandleInput) (Reply, error)
}
