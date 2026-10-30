package largebody

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// WireSupportReason classifies the compatibility or decline reason for a backend
// wire resolution (design section 9, Requirement 8).
type WireSupportReason uint8

const (
	WireSupportReasonNone WireSupportReason = iota
	WireSupportReasonUnsupported
	WireSupportReasonProfileUnsupported
	WireSupportReasonOperationUnsupported
	WireSupportReasonDeliveryUnsupported
	WireSupportReasonBodyModeUnsupported
	WireSupportReasonModelUnsupported
	WireSupportReasonRewriteUnsupported
	WireSupportReasonClampUnsupported
	WireSupportReasonCapabilityUnsupported
)

func (r WireSupportReason) String() string {
	switch r {
	case WireSupportReasonNone:
		return "none"
	case WireSupportReasonUnsupported:
		return "unsupported"
	case WireSupportReasonProfileUnsupported:
		return "profile_unsupported"
	case WireSupportReasonOperationUnsupported:
		return "operation_unsupported"
	case WireSupportReasonDeliveryUnsupported:
		return "delivery_unsupported"
	case WireSupportReasonBodyModeUnsupported:
		return "body_mode_unsupported"
	case WireSupportReasonModelUnsupported:
		return "model_unsupported"
	case WireSupportReasonRewriteUnsupported:
		return "rewrite_unsupported"
	case WireSupportReasonClampUnsupported:
		return "clamp_unsupported"
	case WireSupportReasonCapabilityUnsupported:
		return "capability_unsupported"
	default:
		return "unknown"
	}
}

// WireRequestSupport represents the pure exact wire compatibility outcome
// for one candidate (design section 9, Requirements 8, 9).
type WireRequestSupport struct {
	Compatible        bool
	NeedsModelRewrite bool
	Reason            WireSupportReason
}

// Validate checks internal consistency of the support outcome.
func (s WireRequestSupport) Validate() error {
	if s.Reason.String() == "unknown" {
		return fmt.Errorf("largebody: unknown wire support reason %d", uint8(s.Reason))
	}
	if s.Compatible && s.Reason != WireSupportReasonNone {
		return fmt.Errorf("largebody: compatible wire request support must have reason none")
	}
	if !s.Compatible && s.Reason == WireSupportReasonNone {
		return fmt.Errorf("largebody: incompatible wire request support must have a non-none reason")
	}
	return nil
}

// WireDomainSupport represents the pure late-route domain wire compatibility
// outcome (design section 9, Requirement 7).
type WireDomainSupport struct {
	Compatible       bool
	AnyAcceptedModel bool
	Reason           WireSupportReason
}

// Validate checks internal consistency of the domain support outcome.
func (s WireDomainSupport) Validate() error {
	if s.Reason.String() == "unknown" {
		return fmt.Errorf("largebody: unknown wire support reason %d", uint8(s.Reason))
	}
	if s.Compatible && s.Reason != WireSupportReasonNone {
		return fmt.Errorf("largebody: compatible wire domain support must have reason none")
	}
	if !s.Compatible && s.Reason == WireSupportReasonNone {
		return fmt.Errorf("largebody: incompatible wire domain support must have a non-none reason")
	}
	return nil
}

// WireBackend is the optional internal interface implemented by backends
// capable of evaluating direct wire compatibility (Task 8.1, Requirements 7, 8, 9).
type WireBackend interface {
	ResolveWireRequest(ctx context.Context, facts WireRequestFacts, cand routing.AttemptCandidate) WireRequestSupport
	ResolveWireDomain(ctx context.Context, facts WireDomainFacts) WireDomainSupport
}

// WireBackendProvider is an optional interface for types that adapt into a WireBackend.
type WireBackendProvider interface {
	AsWireBackend() WireBackend
}

// AsWireBackend probes a value for the internal WireBackend capability.
func AsWireBackend(v any) (WireBackend, bool) {
	if v == nil {
		return nil, false
	}
	if wb, ok := v.(WireBackend); ok && wb != nil {
		return wb, true
	}
	if p, ok := v.(WireBackendProvider); ok && p != nil {
		wb := p.AsWireBackend()
		if wb != nil {
			return wb, true
		}
	}
	return nil, false
}

// WireOpenRequest carries bounded facts and a fresh offset-zero source reader
// required by a backend to open an attempt without constructing a aipapi.Call (Requirement 8, 9, 10, 12).
//
// Replay is an optional exact-bytes factory owned by the largebody wire
// boundary. When valid, the adapter may open one fresh reader per
// adapter-local credential attempt and rotate across keys pre-output through
// the shared credential coordinator. When invalid (zero value), the adapter
// must make a single provider attempt: update future credential state from
// the outcome and return the classified error without another HTTP attempt.
type WireOpenRequest struct {
	Candidate     routing.AttemptCandidate
	Body          io.ReadCloser
	ContentLength int64
	WireRequest   WireRequestFacts
	TraceID       string
	ALegID        string
	BLegID        string
	Header        http.Header
	Replay        WireReplayFactory
}

// Validate checks internal consistency of the wire open request.
func (r WireOpenRequest) Validate() error {
	if r.Candidate.Primary.Backend == "" {
		return fmt.Errorf("largebody: candidate primary backend must not be empty")
	}
	if r.Body == nil {
		return fmt.Errorf("largebody: wire open body must not be nil")
	}
	if r.ContentLength < 0 {
		return fmt.Errorf("largebody: wire open content length must be >= 0, got %d", r.ContentLength)
	}
	if strings.TrimSpace(r.TraceID) == "" {
		return fmt.Errorf("largebody: wire open trace id must not be empty")
	}
	if strings.TrimSpace(r.ALegID) == "" {
		return fmt.Errorf("largebody: wire open a-leg id must not be empty")
	}
	if strings.TrimSpace(r.BLegID) == "" {
		return fmt.Errorf("largebody: wire open b-leg id must not be empty")
	}
	return nil
}

// WireOpener is an optional internal interface implemented by backends
// capable of opening a wire attempt without constructing a aipapi.Call (Requirement 8, 12).
type WireOpener interface {
	OpenWire(ctx context.Context, req WireOpenRequest) (aipapi.ManagedEventStream, error)
}

// WireOpenerProvider is an optional interface for types that adapt into a WireOpener.
type WireOpenerProvider interface {
	AsWireOpener() WireOpener
}

// AsWireOpener probes a value for the internal WireOpener capability.
func AsWireOpener(v any) (WireOpener, bool) {
	if v == nil {
		return nil, false
	}
	if wo, ok := v.(WireOpener); ok && wo != nil {
		return wo, true
	}
	if p, ok := v.(WireOpenerProvider); ok && p != nil {
		wo := p.AsWireOpener()
		if wo != nil {
			return wo, true
		}
	}
	return nil, false
}
