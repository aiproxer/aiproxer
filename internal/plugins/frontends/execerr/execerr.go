// Package execerr classifies errors returned from aipsdk.ExecutorView.Execute for HTTP frontends.
package execerr

import (
	"errors"
	"net/http"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/sessionwire"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
)

// InternalWireMessage is the stable response body for 5xx executor failures (no upstream/raw detail).
const InternalWireMessage = "internal error"

// UnknownExecuteErrorMessage is the wire-safe message when ClassifyExecute is called with a nil error.
// Callers should pass only non-nil errors from ExecutorView.Execute; the nil case exists for defensive completeness.
const UnknownExecuteErrorMessage = "unknown error"

// ContextLimitExceededWireMessage is returned for [aipapi.ErrAllCandidatesContextLimitExceeded] (HTTP 413).
const ContextLimitExceededWireMessage = "request exceeds context limits for all configured model routes"

// PolicyDeniedWireMessage is the stable client-safe wire message for policy denials when the
// decision carries no client message (requirement 5.4).
const PolicyDeniedWireMessage = "request denied by policy"

// PolicyFailureWireMessage is the stable client-safe wire message for policy decision provider
// failures handled through fail-closed behavior (requirement 6.1).
const PolicyFailureWireMessage = "policy decision unavailable"

// PolicyMalformedWireMessage is the stable client-safe wire message for malformed policy
// decisions (requirements 1.5, 6.6).
const PolicyMalformedWireMessage = "policy decision was malformed"

// ClientRejectWireMessage is the stable client-safe wire message for capability rejects
// whose normalized reason is empty (control-only or whitespace input).
const ClientRejectWireMessage = "request rejected"

// InsufficientCreditWireMessage is the stable client-safe wire message for
// spendable/account billing denials (HTTP 429). Credit and journal types stay
// out of aipapi; classification uses billing sentinels only.
const InsufficientCreditWireMessage = "insufficient credit"

// Mandatory-buffering wire messages. They are the stable client-safe messages for a
// completed tool call the tool-call assembler refused closed because a finalizer
// published a mandatory completeness requirement the assembler could not honor
// (requirements.md 4.5, 4.6, 8.3).
//
// The refusal is a decision about the client's OWN tool call, so it is a client reject
// rather than an internal fault. Before this mapping it fell through to
// [InternalWireMessage], which told a client nothing: "the proxy will not run this tool
// call for you" and "the proxy broke" arrived as the same 5xx string.
//
// Every message here is a compile-time literal and every one is selected by a switch over
// the assembler's own bounded refusal code. The refusal's Reason field is an exported
// string on an exported struct, so it is never rendered - see
// [mandatoryBufferingWireMessage].
const (
	// MandatoryBufferingOverflowWireMessage is the message for a completed call whose
	// assembled arguments exceed a bound a finalizer declared with
	// [toolcall.OverflowReject].
	MandatoryBufferingOverflowWireMessage = "tool call arguments exceed the declared completion bound"

	// MandatoryBufferingDeclarationInvalidWireMessage is the message for a call refused
	// because a finalizer published a present-but-unusable mandatory declaration. That
	// is an operator configuration fault rather than a request fault, and it is still
	// reported as a reject of this call because refusing closed is what the declaration
	// asked for.
	MandatoryBufferingDeclarationInvalidWireMessage = "tool call completion requirement is unusable"

	// MandatoryBufferingIncompleteWireMessage is the message for a call refused because
	// an unrelated finalizer failed before a declared mandatory requirement could be
	// decided on the complete arguments.
	MandatoryBufferingIncompleteWireMessage = "tool call could not be completed"

	// MandatoryBufferingWireMessage is the bounded fallback for a refusal whose code is
	// outside the set above. It exists so an unknown or future code renders a stable
	// constant rather than the code itself, and rather than an empty message a client
	// would have to guess about.
	MandatoryBufferingWireMessage = "tool call rejected"
)

// BillingUnavailableWireMessage is the stable client-safe wire message when the
// authorization store cannot complete admission (HTTP 503).
const BillingUnavailableWireMessage = "billing unavailable"

// BackendUnavailableWireMessage is the stable client-safe wire message when no
// candidate can serve the request (a hard capability rejection or a backend
// open that returned no stream, neither a client-shape reject, HTTP 502). It
// carries no capability names, reasons, or wrapped upstream detail: those vary
// per request and deployment and stay in server-side logs via Outcome.Err.
const BackendUnavailableWireMessage = "backend unavailable"

type Kind int

const (
	KindUnspecified Kind = iota
	KindClientReject
	KindInternalError
	// KindSessionDenial is a pre-backend secure-session denial with a stable public code and HTTP status.
	KindSessionDenial
	// KindPolicyDenied is a policy denial before backend output or an active-stream policy denial
	// after output (requirement 5.1). Distinct from capability, session, backend, and internal errors.
	KindPolicyDenied
	// KindPolicyFailure is a fail-closed policy decision provider failure (requirement 6.1).
	KindPolicyFailure
	// KindPolicyMalformed is a malformed policy decision: unknown stage, outcome, effect, or illegal
	// outcome/effect pair (requirements 1.5, 6.6).
	KindPolicyMalformed
	// KindBillingDenied is a pre-Open spendable or account-not-ready denial.
	KindBillingDenied
	// KindBillingUnavailable is an authorization-store failure during admission.
	KindBillingUnavailable
	// KindBackendUnavailable is no candidate could serve the request: a hard
	// capability rejection or a backend open that returned no stream. Unlike
	// KindClientReject it is not a verdict about the request shape (the IsReject
	// branch above stays 400); the client could not have fixed the request, so
	// it maps to 502 like the openresponses backend_unavailable mapping for
	// the same predicate.
	KindBackendUnavailable
)

type Outcome struct {
	Kind    Kind
	Status  int
	Message string // safe to return to clients on the wire
	// Err is the original error for server-side logging. It is set for KindClientReject and KindInternalError
	// when err != nil; nil only when ClassifyExecute was called with a nil error (see [UnknownExecuteErrorMessage]).
	Err error
	// SessionPublicCode is the aipapi session denial code string when Kind == KindSessionDenial; otherwise empty.
	SessionPublicCode string
}

// ClassifyExecute maps an executor error to HTTP-facing outcome metadata.
// err must be non-nil in normal use (the value returned from Execute). If err is nil, the result is
// InternalError with Message [UnknownExecuteErrorMessage] and Err set to nil—treat that as a programming mistake,
// not a signal that the upstream call succeeded.
func ClassifyExecute(err error) Outcome {
	if err == nil {
		return Outcome{Kind: KindInternalError, Status: http.StatusInternalServerError, Message: UnknownExecuteErrorMessage, Err: nil}
	}
	if aipapi.IsSessionDenial(err) {
		code := aipapi.SessionDenialPublicCode(err)
		var sd *aipapi.SessionDenialError
		msg := "session denied"
		if errors.As(err, &sd) && sd != nil {
			msg = sd.Error()
		}
		var c aipapi.SessionDenialCode
		if code != "" {
			c = aipapi.SessionDenialCode(code)
		}
		return Outcome{
			Kind:              KindSessionDenial,
			Status:            sessionwire.HTTPStatusForSessionDenial(c),
			Message:           msg,
			Err:               err,
			SessionPublicCode: code,
		}
	}
	if aipapi.IsPolicyDenied(err) {
		return Outcome{
			Kind:    KindPolicyDenied,
			Status:  http.StatusForbidden,
			Message: clientSafePolicyMessage(err, PolicyDeniedWireMessage),
			Err:     err,
		}
	}
	if aipapi.IsPolicyFailure(err) {
		return Outcome{
			Kind:    KindPolicyFailure,
			Status:  http.StatusServiceUnavailable,
			Message: clientSafePolicyMessage(err, PolicyFailureWireMessage),
			Err:     err,
		}
	}
	if aipapi.IsPolicyMalformed(err) {
		return Outcome{
			Kind:    KindPolicyMalformed,
			Status:  http.StatusInternalServerError,
			Message: clientSafePolicyMessage(err, PolicyMalformedWireMessage),
			Err:     err,
		}
	}
	if errors.Is(err, routing.ErrUnsafeExecutionComposition) {
		msg := "unsafe routing execution composition"
		var u *routing.UnsafeExecutionCompositionError
		if errors.As(err, &u) && u != nil {
			msg = u.Error()
		}
		return Outcome{Kind: KindClientReject, Status: http.StatusBadRequest, Message: msg, Err: err}
	}
	// A mandatory-buffering refusal is checked BEFORE the generic capability-reject
	// branch because it is the more specific answer: it is the assembler's own typed
	// refusal for one tool call, and a chain carrying both would otherwise be described
	// by whichever was wrapped outermost. It is classified here rather than inside
	// internal/core/runtime because the wire shape is frontend policy; core publishes the
	// typed refusal and the predicate, and nothing else.
	if runtime.IsMandatoryBufferingError(err) {
		return mandatoryBufferingOutcome(err)
	}
	if aipapi.IsReject(err) {
		msg := ClientRejectWireMessage
		var rej *aipapi.RejectError
		if errors.As(err, &rej) && rej != nil {
			// Bound the reject's own reason, never the wrapped chain text: err.Error()
			// would leak wrapping layers (upstream hosts, tokens, executor internals)
			// to the wire. Same normalizer as the prerequest.IsRejected branch below.
			if bounded := aipapi.NormalizeClientMessage(rej.Error()); bounded != "" {
				msg = bounded
			}
		}
		return Outcome{Kind: KindClientReject, Status: http.StatusBadRequest, Message: msg, Err: err}
	}
	// A generic capability reject is checked AFTER the RejectError branch above
	// because that branch is the more specific answer: a *RejectError is a
	// verdict about the request shape and stays 400. Any other error matching
	// ErrCapabilityReject (exact requirement mismatches, multi-root backend
	// refusals, raw sentinel wraps) names a request no candidate can serve, so
	// it maps to 502 with the fixed backend-unavailable message, matching the
	// openresponses backend_unavailable mapping for the same predicate.
	if errors.Is(err, aipapi.ErrCapabilityReject) {
		return Outcome{Kind: KindBackendUnavailable, Status: http.StatusBadGateway, Message: BackendUnavailableWireMessage, Err: err}
	}
	// A nil-stream open names the same wire condition through the runtime's
	// own predicate: a backend that opened no stream is a backend fault, not
	// a request-shape verdict, so it maps to 502 with the fixed
	// backend-unavailable message. The predicate keeps the failure's shape in
	// the runtime; the frontend only keys on it, and the message carries no
	// upstream detail.
	if runtime.IsNilStreamOpenError(err) {
		return Outcome{Kind: KindBackendUnavailable, Status: http.StatusBadGateway, Message: BackendUnavailableWireMessage, Err: err}
	}
	if prerequest.IsRejected(err) {
		msg := "request denied"
		var re *prerequest.RejectError
		if errors.As(err, &re) && re != nil && re.Message != "" {
			// Defense in depth: a bare RejectError not wrapped in a PolicyDecisionError
			// still reaches the wire here. Bound plugin-controlled text with the same
			// canonical normalizer used at PolicyDecisionError construction so the wire
			// path can never emit an unbounded or control-laden deny message.
			if bounded := aipapi.NormalizeClientMessage(re.Message); bounded != "" {
				msg = bounded
			}
		}
		return Outcome{Kind: KindClientReject, Status: http.StatusForbidden, Message: msg, Err: err}
	}
	if aipapi.IsAllCandidatesContextLimitExceeded(err) {
		return Outcome{
			Kind:    KindClientReject,
			Status:  http.StatusRequestEntityTooLarge,
			Message: ContextLimitExceededWireMessage,
			Err:     err,
		}
	}
	if errors.Is(err, billingport.ErrInsufficientSpendable) || errors.Is(err, billingport.ErrAccountNotReady) {
		return Outcome{
			Kind:    KindBillingDenied,
			Status:  http.StatusTooManyRequests,
			Message: InsufficientCreditWireMessage,
			Err:     err,
		}
	}
	if errors.Is(err, billingport.ErrBillingStoreUnavailable) {
		return Outcome{
			Kind:    KindBillingUnavailable,
			Status:  http.StatusServiceUnavailable,
			Message: BillingUnavailableWireMessage,
			Err:     err,
		}
	}
	return Outcome{Kind: KindInternalError, Status: http.StatusInternalServerError, Message: InternalWireMessage, Err: err}
}

// ShouldLogExecuteError reports whether frontend pipelines should emit a
// server-side diagnostic for out.
//
// Internal errors always log. Nil-stream backend faults also log: they name a
// backend fault that would otherwise leave no error-level diagnostic once
// classified as backend-unavailable. Ordinary capability rejections stay
// quiet, so a request no candidate can serve does not become an error-level
// event.
func ShouldLogExecuteError(out Outcome) bool {
	if out.Err == nil {
		return false
	}
	if out.Kind == KindInternalError {
		return true
	}
	return out.Kind == KindBackendUnavailable && runtime.IsNilStreamOpenError(out.Err)
}

// OpenAIWireErrorType maps HTTP status to OpenAI-compatible error.type strings for frontend adapters.
func OpenAIWireErrorType(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusServiceUnavailable, http.StatusInternalServerError:
		return "api_error"
	default:
		return "invalid_request_error"
	}
}

// mandatoryBufferingOutcome maps the tool-call assembler's typed refusal onto a client
// reject carrying one compile-time message.
//
// It is the whole wire mapping for [runtime.MandatoryBufferingError], and it is bounded in
// both directions: the status is the one this package already uses for a client reject of
// a request, and the message is selected from a fixed set of constants. Nothing from the
// refusal reaches the client. FinalizerID and ToolCallID are classification-only fields
// and are absent from the typed error's own Error text for the same reason; MaxArgsBytes
// is a declared bound rather than request content, but publishing it would still vary per
// deployment, so it stays out too.
//
// The status is uniform across all three codes on purpose. Only the overflow code is a
// size condition, so a per-code status would have to publish 413 for one of them and
// something else for the other two, and the other two are not size problems: one is a
// publisher's unusable declaration and one is an unrelated finalizer's failure. A client
// reads one code from the message rather than inferring the condition from the status.
func mandatoryBufferingOutcome(err error) Outcome {
	msg := MandatoryBufferingWireMessage
	var refusal *runtime.MandatoryBufferingError
	if errors.As(err, &refusal) && refusal != nil {
		msg = mandatoryBufferingWireMessage(refusal.Reason)
	}
	return Outcome{Kind: KindClientReject, Status: http.StatusBadRequest, Message: msg, Err: err}
}

// mandatoryBufferingWireMessage selects one compile-time message from the assembler's
// bounded refusal code.
//
// The code is SELECTED rather than rendered, which is the whole content-freedom argument:
// [runtime.MandatoryBufferingError.Reason] is an exported string field on an exported
// struct, so a value assembled by a caller - or by a future build that adds a code without
// updating this switch - would otherwise reach a client verbatim. Every branch returns a
// literal, so the function is total over any string it is handed.
func mandatoryBufferingWireMessage(reason string) string {
	switch reason {
	case runtime.ReasonMandatoryBufferingOverflow:
		return MandatoryBufferingOverflowWireMessage
	case runtime.ReasonMandatoryBufferingDeclarationInvalid:
		return MandatoryBufferingDeclarationInvalidWireMessage
	case runtime.ReasonMandatoryBufferingIncomplete:
		return MandatoryBufferingIncompleteWireMessage
	default:
		return MandatoryBufferingWireMessage
	}
}

// clientSafePolicyMessage returns the client-safe message from a policy decision error, falling
// back to defaultMsg when the error carries no safe message. It never renders Cause, provider IDs,
// stage, raw prompts, or secrets (requirement 5.4); PolicyDecisionError.ClientMessage is the only
// policy field intended for wire rendering.
func clientSafePolicyMessage(err error, defaultMsg string) string {
	if pde := aipapi.PolicyDecisionErrorFrom(err); pde != nil {
		if msg := strings.TrimSpace(pde.ClientMessage); msg != "" {
			return msg
		}
	}
	return defaultMsg
}
