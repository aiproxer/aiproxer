package steering

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Bounds mirror internal/core/conversationview limits.
const (
	MaxOverlayIDBytes    = 128
	MaxReasonCodeBytes   = 64
	MaxSteeringTextBytes = 64 * 1024
)

// OverlayID is a bounded stable identifier for a steering overlay.
type OverlayID string

// Validate reports whether the overlay ID is a bounded ascii identifier.
func (id OverlayID) Validate() error {
	s := string(id)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("steering: overlay id is required")
	}
	if len(s) > MaxOverlayIDBytes {
		return fmt.Errorf("steering: overlay id exceeds %d bytes", MaxOverlayIDBytes)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("steering: overlay id must be ascii")
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("steering: invalid character %q in overlay id", ch)
		}
	}
	return nil
}

// ReasonCode is a bounded non-secret identifier for diagnostics.
type ReasonCode string

// Validate reports whether the reason code is a bounded ascii identifier.
func (r ReasonCode) Validate() error {
	s := string(r)
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("steering: reason code is required")
	}
	if len(s) > MaxReasonCodeBytes {
		return fmt.Errorf("steering: reason code exceeds %d bytes", MaxReasonCodeBytes)
	}
	for _, ch := range s {
		if ch > unicode.MaxASCII {
			return fmt.Errorf("steering: reason code must be ascii")
		}
		if ch != '_' && ch != '-' && ch != '.' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return fmt.Errorf("steering: invalid character %q in reason code", ch)
		}
	}
	return nil
}

// PlacementKind enumerates producer-facing steering placement.
type PlacementKind string

const (
	StablePrefix     PlacementKind = "stable_prefix"
	AfterIngressTail PlacementKind = "after_ingress_tail"
)

// Validate reports whether the placement kind is known.
func (k PlacementKind) Validate() error {
	switch k {
	case StablePrefix, AfterIngressTail:
		return nil
	default:
		return fmt.Errorf("steering: unknown placement %q", k)
	}
}

// AnchorMissingPolicy controls behavior when a fixed anchor disappears.
type AnchorMissingPolicy string

const (
	StablePrefixFallback AnchorMissingPolicy = "stable_prefix_fallback"
	FailClosed           AnchorMissingPolicy = "fail_closed"
)

// Validate reports whether the policy is known.
func (p AnchorMissingPolicy) Validate() error {
	switch p {
	case StablePrefixFallback, FailClosed:
		return nil
	default:
		return fmt.Errorf("steering: unknown anchor missing policy %q", p)
	}
}

// Message is the bounded model-visible payload for a steering overlay.
// It deliberately mirrors internal/core/conversationview#StoredMessageV1 shape
// publicly without importing internal packages. Only role and text are
// persisted; proxy-only metadata, trace IDs, and transport wrappers are never
// part of the payload.
type Message struct {
	Role aipapi.Role
	Text string
}

// Validate reports whether the steering message is well-formed.
func (m Message) Validate() error {
	role := aipapi.Role(strings.TrimSpace(string(m.Role)))
	if role == "" {
		return fmt.Errorf("steering: message role is required")
	}
	switch role {
	case aipapi.RoleSystem, aipapi.RoleDeveloper, aipapi.RoleUser, aipapi.RoleAssistant, aipapi.RoleTool:
	default:
		return fmt.Errorf("steering: invalid message role %q", role)
	}
	if strings.TrimSpace(m.Text) == "" {
		return fmt.Errorf("steering: message text is required")
	}
	if len(m.Text) > MaxSteeringTextBytes {
		return fmt.Errorf("steering: message text exceeds %d bytes", MaxSteeringTextBytes)
	}
	if !utf8.ValidString(m.Text) {
		return fmt.Errorf("steering: message text must be valid utf8")
	}
	for _, ch := range m.Text {
		if ch == 0 {
			return fmt.Errorf("steering: message text must not contain NUL")
		}
	}
	return nil
}

// PutRequest is the trusted mutation for a steering overlay.
type PutRequest struct {
	OverlayID           OverlayID
	Message             Message
	Placement           PlacementKind
	AnchorMissingPolicy AnchorMissingPolicy
	Reason              ReasonCode
}

// Validate reports whether the put request is well-formed.
// It rejects empty/oversized IDs, unknown placement/policy values, and invalid payloads.
func (r PutRequest) Validate() error {
	if err := r.OverlayID.Validate(); err != nil {
		return err
	}
	if err := r.Message.Validate(); err != nil {
		return err
	}
	if err := r.Placement.Validate(); err != nil {
		return err
	}
	if err := r.AnchorMissingPolicy.Validate(); err != nil {
		return err
	}
	if err := r.Reason.Validate(); err != nil {
		return err
	}
	return nil
}

// State is the post-mutation steering summary returned by Writer.
type State struct {
	OverlayID   OverlayID
	Revision    uint64
	SlotOrdinal uint64
	Active      bool
}

// Writer is the trusted narrow port for mutating persistent backend-only steering.
// Implementations are explicitly constructed with an authoritative A-leg scope
// and must not be exposed to client frontends or retrieved via a global registry.
type Writer interface {
	Put(ctx context.Context, req PutRequest) (State, error)
	Deactivate(ctx context.Context, id OverlayID) (State, error)
}
