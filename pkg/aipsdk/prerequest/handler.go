package prerequest

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/state"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// ErrRejected is returned when a pre-request handler denies the canonical call.
var ErrRejected = errors.New("aipsdk/prerequest: pre-request rejected")

// RejectError carries a deterministic client-safe rejection message.
type RejectError struct {
	HandlerID string
	Message   string
}

func (e *RejectError) Error() string {
	if e == nil {
		return ErrRejected.Error()
	}
	if e.HandlerID != "" && e.Message != "" {
		return fmt.Sprintf("pre-request handler %q rejected: %s", e.HandlerID, e.Message)
	}
	if e.Message != "" {
		return "pre-request rejected: " + e.Message
	}
	if e.HandlerID != "" {
		return fmt.Sprintf("pre-request handler %q rejected", e.HandlerID)
	}
	return ErrRejected.Error()
}

func (e *RejectError) Unwrap() error { return ErrRejected }

// NewRejectError returns a stable rejection error for handler decisions.
func NewRejectError(handlerID, message string) error {
	return &RejectError{HandlerID: handlerID, Message: message}
}

// IsRejected reports whether err is or wraps a pre-request rejection.
func IsRejected(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrRejected) {
		return true
	}
	var re *RejectError
	return errors.As(err, &re)
}

// Decision is the outcome of one pre-request handler.
type Decision struct {
	Deny        bool
	DenyMessage string
	Annotations map[string]string
}

// Allow returns an allow decision.
func Allow() Decision { return Decision{} }

// Deny returns a deny decision with a client-safe message.
func Deny(message string) Decision {
	return Decision{Deny: true, DenyMessage: message}
}

// Allowed reports whether the decision allows the call to continue.
func (d Decision) Allowed() bool { return !d.Deny }

// Meta is read-only request context for pre-request admission handlers.
// Scope is the authoritative principal/scope attribution for the accepted request,
// carried separately from the legacy Principal projection (requirement 2.6). A zero
// Scope preserves local/anonymous/unknown attribution semantics: SubjectKind is the
// empty string (the zero SubjectKind) and presence-aware Values remain unknown.
type Meta struct {
	TraceID        string
	Annotations    map[string]string
	Principal      execview.PrincipalView
	Scope          scope.PrincipalScopeView
	Session        session.SessionView
	Workspace      workspace.WorkspaceView
	AuxiliaryDepth int
}

// Services exposes narrow capabilities for pre-request handlers.
type Services struct {
	State state.Store
	Aux   auxiliary.Client
}

// Handler runs before route planning for the primary model.
type Handler interface {
	ID() string
	Order() int
	FailureMode() sdkhooks.FailureMode
	Handle(ctx context.Context, call *aipapi.Call, meta Meta, svc Services) (Decision, error)
}
