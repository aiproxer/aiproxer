package adapter

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	publichost "github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ExecuteFailureKind is the stable ExecuteSession failure classification.
type ExecuteFailureKind int

const (
	// ExecuteFailureProvider is a provider-declared error (terminal PluginError).
	ExecuteFailureProvider ExecuteFailureKind = iota + 1
	// ExecuteFailureCanceled is host/context or deadline cancellation.
	ExecuteFailureCanceled
	// ExecuteFailureTransportDeath is transport loss or process death.
	ExecuteFailureTransportDeath
	// ExecuteFailureProtocolViolation is an ABI/stream protocol violation.
	ExecuteFailureProtocolViolation
	// executeFailureHostEvidenceRefused is the host's refusal of connector
	// economic evidence the host could not admit. The kind is package-private:
	// no provider string and no remote peer can select it, and it is only
	// reachable through the host's own strict evidence policy.
	executeFailureHostEvidenceRefused
)

// ExecuteFailureError is the classified Execute boundary error.
type ExecuteFailureError struct {
	Kind            ExecuteFailureKind
	Err             error
	OutputCommitted bool
}

func (e *ExecuteFailureError) Error() string {
	if e == nil {
		return "execute failure"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return "execute failure"
}

func (e *ExecuteFailureError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// InvalidatesGeneration reports whether the failure requires generation invalidation.
func (e *ExecuteFailureError) InvalidatesGeneration() bool {
	if e == nil {
		return false
	}
	return e.Kind == ExecuteFailureTransportDeath || e.Kind == ExecuteFailureProtocolViolation
}

// ToClassifiedError maps the failure to a sanitized ClassifiedError for core.
func (e *ExecuteFailureError) ToClassifiedError() *ClassifiedError {
	if e == nil {
		return nil
	}
	code := "transport"
	retryable := !e.OutputCommitted
	switch e.Kind {
	case ExecuteFailureProvider:
		code = "provider"
		retryable = false
	case ExecuteFailureCanceled:
		code = "canceled"
		retryable = false
	case ExecuteFailureProtocolViolation:
		code = "protocol"
		if errors.Is(e.Err, execbackend.ErrPromptCacheLineageConflict) {
			// A security rejection keeps its own stable class so the core can
			// log an explicit lineage conflict without echoing producer text.
			code = "prompt_cache_lineage"
		}
	case ExecuteFailureTransportDeath:
		code = "transport"
	case executeFailureHostEvidenceRefused:
		// The recognized non-retryable terminal is reused: no new classified
		// code is introduced, and the refusal is never retryable. The existing
		// Unwrap rule bridges the classified error to the frontend reject
		// branch, so the refusal is never swallowed for another candidate.
		code = "failed_precondition"
		retryable = false
	}
	msg := ""
	if e.Err != nil {
		msg = sanitizeDiagnosticText(e.Err.Error(), 256)
	}
	return &ClassifiedError{
		Code:            code,
		Message:         msg,
		Retryable:       retryable,
		OutputCommitted: e.OutputCommitted,
	}
}

// TransportDeath wraps err as transport/process death.
func TransportDeath(err error) *ExecuteFailureError {
	return &ExecuteFailureError{Kind: ExecuteFailureTransportDeath, Err: err}
}

// HostEvidenceRefusal is the closed typed reason of a host evidence refusal.
// It carries only the host-owned closed rejection token; it never carries
// connector text, a payload, a hash or a source identity.
type HostEvidenceRefusal struct {
	// Reason is the closed rejection token of the refused claim.
	Reason string
}

func (e *HostEvidenceRefusal) Error() string {
	// The diagnostic message is a bounded constant. The reason token stays in
	// the typed field so it is never echoed into a client-visible message.
	return "host refused connector economic evidence"
}

// hostEvidenceRefused builds the typed refusal of one unadmittable connector
// claim. The kind is private, so the refusal can only be raised by the host.
func hostEvidenceRefused(reason string) *ExecuteFailureError {
	return &ExecuteFailureError{
		Kind: executeFailureHostEvidenceRefused,
		Err:  &HostEvidenceRefusal{Reason: reason},
	}
}

// ProtocolViolation wraps err as an ABI protocol violation.
func ProtocolViolation(err error) *ExecuteFailureError {
	return &ExecuteFailureError{Kind: ExecuteFailureProtocolViolation, Err: err}
}

// ClassifyExecuteError classifies an ExecuteSession error at the openStream boundary.
func ClassifyExecuteError(err error, committed bool) *ExecuteFailureError {
	if err == nil {
		return nil
	}
	if ef, ok := errors.AsType[*ExecuteFailureError](err); ok {
		out := *ef
		out.OutputCommitted = committed
		if out.Err == nil {
			out.Err = err
		}
		return &out
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &ExecuteFailureError{Kind: ExecuteFailureCanceled, Err: err, OutputCommitted: committed}
	}
	if isProtocolSentinel(err) {
		return &ExecuteFailureError{Kind: ExecuteFailureProtocolViolation, Err: err, OutputCommitted: committed}
	}
	if ce, ok := errors.AsType[*ClassifiedError](err); ok {
		switch ce.Code {
		case "frame_too_large", "protocol":
			return &ExecuteFailureError{Kind: ExecuteFailureProtocolViolation, Err: err, OutputCommitted: committed}
		case "provider":
			return &ExecuteFailureError{Kind: ExecuteFailureProvider, Err: err, OutputCommitted: committed}
		case "canceled":
			return &ExecuteFailureError{Kind: ExecuteFailureCanceled, Err: err, OutputCommitted: committed}
		}
	}
	if _, ok := errors.AsType[backendplugin.ModeError](err); ok {
		return &ExecuteFailureError{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.Canceled:
			return &ExecuteFailureError{Kind: ExecuteFailureCanceled, Err: err, OutputCommitted: committed}
		case codes.Unavailable, codes.DeadlineExceeded:
			return &ExecuteFailureError{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
		case codes.Unknown, codes.Internal, codes.Aborted:
			if errors.Is(err, io.EOF) || strings.Contains(st.Message(), "EOF") || strings.Contains(st.Message(), "transport is closing") {
				return &ExecuteFailureError{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
			}
		}
	}
	if errors.Is(err, io.EOF) {
		return &ExecuteFailureError{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
	}
	return &ExecuteFailureError{Kind: ExecuteFailureTransportDeath, Err: err, OutputCommitted: committed}
}

func isProtocolSentinel(err error) bool {
	if _, ok := errors.AsType[*publichost.ProtocolViolationError](err); ok {
		return true
	}
	return errors.Is(err, backendplugin.ErrMultipleTerminals) ||
		errors.Is(err, backendplugin.ErrEventAfterTerminal) ||
		errors.Is(err, backendplugin.ErrSequenceGap) ||
		errors.Is(err, backendplugin.ErrAcceptedRequired) ||
		errors.Is(err, backendplugin.ErrUnknownFrameKind) ||
		errors.Is(err, backendplugin.ErrInvalidFrame) ||
		errors.Is(err, backendplugin.ErrOversizedMessage) ||
		errors.Is(err, backendplugin.ErrUnknownEventKind) ||
		errors.Is(err, backendplugin.ErrUnknownEnum) ||
		errors.Is(err, backendplugin.ErrInvalidInvocation)
}
