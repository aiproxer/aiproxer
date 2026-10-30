package aipapi

import (
	"context"
	"strings"
)

// CancelKind classifies why a proxy-owned A-leg or B-leg is being cancelled.
type CancelKind string

const (
	CancelExplicit    CancelKind = "explicit"
	CancelClientGone  CancelKind = "client_gone"
	CancelContextDone CancelKind = "context_done"
	CancelRaceLoser   CancelKind = "race_loser"
)

// CancelCause carries low-cardinality cancellation reason metadata across core and adapters.
type CancelCause struct {
	Kind   CancelKind
	Detail string
}

// CancelMode reports how a backend stream attempted to stop remote token generation.
type CancelMode string

const (
	CancelModeNone      CancelMode = "none"
	CancelModeProvider  CancelMode = "provider"
	CancelModeTransport CancelMode = "transport"
	CancelModeCloseOnly CancelMode = "close_only"
)

// CancelResult records adapter-level cancellation behavior for audit and billing reconciliation.
type CancelResult struct {
	Mode CancelMode
	Err  error
}

// ManagedEventStream is the canonical backend stream lifecycle contract.
// Close must promptly return and unblock any in-flight Cancel.
type ManagedEventStream interface {
	EventStream
	Cancel(ctx context.Context, cause CancelCause) CancelResult
}

// UsageEvidenceSource is an internal accounting seam. Evidence is drained by
// the host and never returned from Recv, so frontends and hooks cannot observe
// provider-only charges.
type UsageEvidenceSource interface {
	DrainUsageEvidence() []Event
}

// CloseOnlyManagedStream adapts streams with no provider-native cancel API into [ManagedEventStream].
type CloseOnlyManagedStream struct {
	Stream EventStream
}

func (s CloseOnlyManagedStream) Recv(ctx context.Context) (Event, error) {
	if s.Stream == nil {
		return Event{}, ErrNilFixedEventStream
	}
	return s.Stream.Recv(ctx)
}

func (s CloseOnlyManagedStream) Close() error {
	if s.Stream == nil {
		return nil
	}
	return s.Stream.Close()
}

func (s CloseOnlyManagedStream) Cancel(context.Context, CancelCause) CancelResult {
	return CancelResult{Mode: CancelModeCloseOnly}
}

// ALegCancelRequest is the frontend-to-core request for explicit A-leg cancellation.
type ALegCancelRequest struct {
	ALegID      string
	SessionID   string
	ResumeToken string
	FrontendID  string
	Reason      string
}

func (r ALegCancelRequest) Trimmed() ALegCancelRequest {
	r.ALegID = strings.TrimSpace(r.ALegID)
	r.SessionID = strings.TrimSpace(r.SessionID)
	r.ResumeToken = strings.TrimSpace(r.ResumeToken)
	r.FrontendID = strings.TrimSpace(r.FrontendID)
	r.Reason = strings.TrimSpace(r.Reason)
	return r
}
