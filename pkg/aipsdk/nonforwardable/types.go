package nonforwardable

import (
	"context"
	"fmt"
	"strings"
	"unicode"
)

// Bounds: mirror internal/core/conversationview limits.
const (
	MaxReasonCodeBytes = 64
	MaxALegIDBytes     = 256
	MaxIdentityBytes   = 512
)

// ReasonCode is a bounded non-secret identifier for diagnostics.
type ReasonCode string

// Validate reports whether the reason code is a bounded ascii identifier.
func (r ReasonCode) Validate() error {
	s := string(r)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("nonforwardable: reason code is required")
	}
	if len(s) > MaxReasonCodeBytes {
		return fmt.Errorf("nonforwardable: reason code exceeds %d bytes", MaxReasonCodeBytes)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("nonforwardable: reason code must be ascii")
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("nonforwardable: invalid character %q in reason code", ch)
		}
	}
	return nil
}

// ALegRef is a trusted reference to an authoritative A-leg.
type ALegRef struct {
	ID string
}

// Validate reports whether the A-leg reference is well-formed.
func (a ALegRef) Validate() error {
	trimmed := strings.TrimSpace(a.ID)
	if trimmed == "" {
		return fmt.Errorf("nonforwardable: a-leg id is required")
	}
	if len(trimmed) > MaxALegIDBytes {
		return fmt.Errorf("nonforwardable: a-leg id exceeds %d bytes", MaxALegIDBytes)
	}
	return nil
}

// MessageRef carries the replay-stable semantic identity of a complete message.
// Identity is a versioned digest (e.g. v1:sha256:...) precomputed by the caller
// via the conversationview identity service. Index is the normalized position
// in the ingress call and is informational only; the identity is authoritative.
type MessageRef struct {
	Identity string
}

// Validate reports whether the message reference is well-formed.
func (m MessageRef) Validate() error {
	trimmed := strings.TrimSpace(m.Identity)
	if trimmed == "" {
		return fmt.Errorf("nonforwardable: message identity is required")
	}
	if len(trimmed) > MaxIdentityBytes {
		return fmt.Errorf("nonforwardable: message identity exceeds %d bytes", MaxIdentityBytes)
	}
	return nil
}

// Registrar is the trusted narrow port for tagging A-leg-visible messages as
// never_backend. Implementations are explicitly constructed with an
// authoritative A-leg scope and must not be exposed to client frontends or
// retrieved via a global registry.
type Registrar interface {
	TagMessages(ctx context.Context, aLeg ALegRef, msgs []MessageRef, reason ReasonCode) error
}
