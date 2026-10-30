package aipapi

import (
	"errors"
	"fmt"
)

// TransportMode identifies the upstream transport shape selected for one backend attempt.
type TransportMode string

const (
	TransportModeStreaming    TransportMode = "streaming"
	TransportModeNonStreaming TransportMode = "non_streaming"
)

// TransportFallbackPolicy controls how missing backend transport declarations are handled.
type TransportFallbackPolicy string

const (
	TransportFallbackCompatibility TransportFallbackPolicy = "compatibility"
	TransportFallbackExact         TransportFallbackPolicy = "exact"
)

// ErrTransportReject is returned when a backend lacks required operation+transport support.
var ErrTransportReject = errors.New("aipapi: transport capability reject")

// OperationTransportSupport declares supported transport modes for one protocol operation.
type OperationTransportSupport struct {
	Operation Operation
	Modes     []TransportMode
}

// BackendTransportCaps is the operation+transport capability surface for one backend adapter.
type BackendTransportCaps map[Operation]TransportModeSet

// TransportModeSet is a set of supported transport modes.
type TransportModeSet map[TransportMode]struct{}

// NewBackendTransportCaps builds transport caps for negotiation helpers and tests.
func NewBackendTransportCaps(entries ...OperationTransportSupport) BackendTransportCaps {
	m := make(BackendTransportCaps, len(entries))
	for _, e := range entries {
		set := make(TransportModeSet, len(e.Modes))
		for _, mode := range e.Modes {
			set[mode] = struct{}{}
		}
		m[e.Operation] = set
	}
	return m
}

// DeclaredFor reports whether transport support was explicitly declared for an operation.
func (c BackendTransportCaps) DeclaredFor(op Operation) bool {
	if len(c) == 0 {
		return false
	}
	_, ok := c[op]
	return ok
}

// Supports reports whether an operation+transport pair is explicitly declared.
func (c BackendTransportCaps) Supports(op Operation, mode TransportMode) bool {
	if len(c) == 0 {
		return false
	}
	modes, ok := c[op]
	if !ok {
		return false
	}
	_, ok = modes[mode]
	return ok
}

// PreferredTransportMode maps client delivery mode to the preferred backend transport mode.
func PreferredTransportMode(delivery DeliveryMode) TransportMode {
	switch delivery {
	case DeliveryModeStreaming:
		return TransportModeStreaming
	case DeliveryModeNonStreaming:
		return TransportModeNonStreaming
	default:
		return ""
	}
}

// TransportNegotiationResult is the outcome of operation+transport capability matching.
type TransportNegotiationResult struct {
	Kind      NegotiationKind
	Selected  TransportMode
	Operation Operation
	Mode      TransportMode
}

// Err returns a typed reject error for Kind==NegotiationReject, otherwise nil.
func (r TransportNegotiationResult) Err() error {
	if r.Kind != NegotiationReject {
		return nil
	}
	return &TransportRejectError{
		Operation: r.Operation,
		Mode:      r.Mode,
	}
}

// TransportRejectError records a hard transport capability mismatch.
type TransportRejectError struct {
	Operation Operation
	Mode      TransportMode
}

func (e *TransportRejectError) Error() string {
	return fmt.Sprintf("aipapi: transport reject: operation %q mode %q", e.Operation, e.Mode)
}

func (e *TransportRejectError) Is(target error) bool {
	return target == ErrTransportReject
}

// NegotiateTransport selects the backend transport mode for one invocation.
//
// Compatibility policy preserves legacy behavior when transport caps are omitted or the
// requested operation is undeclared. Exact policy requires explicit operation+mode support.
func NegotiateTransport(inv Invocation, caps BackendTransportCaps, policy TransportFallbackPolicy) TransportNegotiationResult {
	if inv.Operation == "" || inv.DeliveryMode == "" {
		return TransportNegotiationResult{Kind: NegotiationLossless}
	}
	preferred := PreferredTransportMode(inv.DeliveryMode)
	if preferred == "" {
		return TransportNegotiationResult{Kind: NegotiationLossless}
	}
	if policy == TransportFallbackExact {
		if !caps.DeclaredFor(inv.Operation) || !caps.Supports(inv.Operation, preferred) {
			return TransportNegotiationResult{
				Kind:      NegotiationReject,
				Operation: inv.Operation,
				Mode:      preferred,
			}
		}
		return TransportNegotiationResult{Kind: NegotiationLossless, Selected: preferred}
	}
	if caps.DeclaredFor(inv.Operation) && !caps.Supports(inv.Operation, preferred) {
		return TransportNegotiationResult{
			Kind:      NegotiationReject,
			Operation: inv.Operation,
			Mode:      preferred,
		}
	}
	return TransportNegotiationResult{Kind: NegotiationLossless, Selected: preferred}
}
