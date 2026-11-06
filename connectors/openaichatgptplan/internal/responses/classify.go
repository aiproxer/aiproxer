package responses

// The connector's failure taxonomy.
//
// Every failure this connector can report belongs to one recovery class, and a
// caller classifies it without parsing a message, a provider code or a raw
// body. The class says WHAT the operator or the host can do next; the phase
// says WHERE the attempt stopped; and the source says WHO reported the failure
// - the provider itself, or this connector.
//
// # Why the source is structural
//
// A provider's machine-readable code is recovery information: it names a
// documented condition and the action it requires. A LOCAL discriminator is
// not that. The stream normalizer derives discriminators such as
// `contradictory_event` from its own judgement of the provider's bytes, and
// the provider's status text, item type or call id travels in the same field.
// A classifier that matched a code string would let a provider-controlled
// string decide a recovery class, so the distinction is carried by typed
// fields - StreamFailure.Origin and ClassifiedError.Source - never by the text
// itself. A StreamFailure whose Origin is local never contributes its Code to
// the provider code field.
//
// # No new leak path
//
// Classification adds no text of its own: status, code, request id and
// diagnostic are copied from errors whose provider text already passed
// redact-before-bound, and the local profile id is sanitized and bounded here.
// The classified error never renders its cause, so a cause that carries
// provider text cannot reach a log through it. A cancellation is not a
// connector failure and is deliberately left unclassified - but only when no
// typed outcome was already decided: a nested context failure does not erase an
// authoritative refresh or local class (see [Classify]).
//
// # Provider-local
//
// This taxonomy is not a canonical or core contract. It names the recovery
// families of THIS connector's public ChatGPT-plan responses route and stays
// inside the connector module.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/siwc"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

// ErrorClass is the closed recovery taxonomy of one connector failure.
type ErrorClass string

const (
	// ErrorClassUnsupportedCapability reports a request or an answer this
	// connector cannot represent. Repeating the same body cannot succeed:
	// the input, the tool, the option or the output shape has to change.
	ErrorClassUnsupportedCapability ErrorClass = "unsupported_capability"

	// ErrorClassRoute reports that the request did not reach, or was not
	// served by, the documented Responses route: an unknown endpoint or
	// method, a redirect, or the provider's route-not-supported code.
	ErrorClassRoute ErrorClass = "route"

	// ErrorClassInvalidUserContext reports that the subscriber context or
	// the request context could not be used: a refused or ineligible
	// subscriber, a missing or unusable local registration, or a request
	// this connector refuses as malformed. The Reason field names the exact
	// local decision, so a local selection fault is never mistaken for the
	// provider's invalid-user code.
	ErrorClassInvalidUserContext ErrorClass = "invalid_user_context"

	// ErrorClassTemporaryUnavailable reports a condition that may resolve
	// without changing the request: a network failure, a 5xx answer, or a
	// documented temporary provider condition. Retryable is true.
	ErrorClassTemporaryUnavailable ErrorClass = "temporary_unavailable"

	// ErrorClassUsageExhausted reports the documented plan usage limit.
	// New requests must pause; the provider's code does not name a reset
	// time, and the attempt must not move to another registration.
	ErrorClassUsageExhausted ErrorClass = "usage_exhausted"

	// ErrorClassRefreshTerminal reports the terminal credential outcome of
	// the serialized refresh: the refresh token is unusable and the
	// registration needs a new authorization. The Reason field carries
	// siwc.RefreshClassTerminal, so the 2.5 vocabulary is reused rather
	// than re-derived.
	ErrorClassRefreshTerminal ErrorClass = "refresh_terminal"

	// ErrorClassRefreshTransient reports the transient credential outcome
	// of the serialized refresh: the saved credentials are unchanged and
	// the refresh may succeed later. The Reason field carries
	// siwc.RefreshClassTransient, so the 2.5 vocabulary is reused rather
	// than re-derived.
	ErrorClassRefreshTransient ErrorClass = "refresh_transient"

	// ErrorClassProtocolViolation reports a breach of a contract this
	// connector enforces: a success answer that is not an event stream, a
	// malformed or contradictory stream event, a missing terminal, an
	// over-bound answer, or a replay of committed output. The connector
	// refused; the provider did not report a recovery code.
	ErrorClassProtocolViolation ErrorClass = "protocol_violation"

	// ErrorClassProviderFailure reports a provider-reported failure whose
	// code is outside the documented recovery vocabulary. The provider
	// said the attempt failed, and this connector knows no specific
	// recovery for it; it is never retried silently.
	ErrorClassProviderFailure ErrorClass = "provider_failure"
)

// ErrorPhase names where the attempt stopped.
type ErrorPhase string

const (
	// ErrorPhaseLocal is a refusal before any provider I/O.
	ErrorPhaseLocal ErrorPhase = "local"
	// ErrorPhaseCredential is a local credential acquisition or refresh
	// outcome.
	ErrorPhaseCredential ErrorPhase = "credential"
	// ErrorPhaseAdmission is a provider refusal before a stream opened.
	ErrorPhaseAdmission ErrorPhase = "admission"
	// ErrorPhaseStream is a provider stream that failed after it opened.
	ErrorPhaseStream ErrorPhase = "stream"
	// ErrorPhaseTransport is a request that never produced a provider
	// answer.
	ErrorPhaseTransport ErrorPhase = "transport"
)

// ErrorSource names who reported the failure.
type ErrorSource string

const (
	// ErrorSourceProvider means the provider itself reported the failure:
	// its status, code and request id are provider data.
	ErrorSourceProvider ErrorSource = "provider"
	// ErrorSourceLocal means this connector produced the failure: a local
	// refusal, a local judgement of the provider's bytes, or an observed
	// transport failure. Its Code is never a provider code.
	ErrorSourceLocal ErrorSource = "local"
)

// maxProfileIDRunes bounds the local profile id a classified error carries.
const maxProfileIDRunes = 128

// providerCodeClasses is the documented recovery table of the public
// ChatGPT-plan route.
//
// The table is closed to the codes the provider documentation names for this
// route. A code outside it is not guessed into a recovery class: the provider
// reported a failure this connector does not know how to recover from, and
// the answer is ErrorClassProviderFailure (or the status fallback when the
// answer arrived as an HTTP error).
var providerCodeClasses = map[string]ErrorClass{
	"subscription_sharing_unsupported_capability": ErrorClassUnsupportedCapability,
	"subscription_sharing_route_not_supported":    ErrorClassRoute,
	"subscription_sharing_invalid_user":           ErrorClassInvalidUserContext,
	"subscription_sharing_user_not_eligible":      ErrorClassInvalidUserContext,
	"chatpass_v2_scope_not_authorized":            ErrorClassInvalidUserContext,
	"chatpass_v2_invalid_authorization_context":   ErrorClassInvalidUserContext,
	"subscription_sharing_usage_limit_exceeded":   ErrorClassUsageExhausted,
	"subscription_sharing_usage_unavailable":      ErrorClassTemporaryUnavailable,
	"subscription_sharing_user_unavailable":       ErrorClassTemporaryUnavailable,
}

// ClassifiedError is the typed, non-secret classification of one connector
// failure.
//
// Class, Phase, Source, Retryable and Reason are this connector's own
// vocabulary. Status, Code and RequestID are the provider's identities, kept
// only when the provider answered; Code is set only for a provider-reported
// code, never for a local discriminator. Diagnostic is the bounded, redacted
// provider text. ProfileID is the non-secret local registration key.
type ClassifiedError struct {
	// Class is the recovery class.
	Class ErrorClass
	// Phase is where the attempt stopped.
	Phase ErrorPhase
	// Source reports whether the provider or this connector reported the
	// failure.
	Source ErrorSource
	// Retryable reports whether the same request may be retried unchanged.
	Retryable bool
	// Status is the provider HTTP status, when the provider answered.
	Status int
	// Code is the provider's machine-readable code, and only that: a local
	// discriminator is never copied here.
	Code string
	// RequestID is the provider request id, when it arrived.
	RequestID string
	// Diagnostic is the bounded, redacted provider text.
	Diagnostic string
	// ProfileID is the non-secret local id of the selected registration.
	ProfileID string
	// Reason is this connector's own discriminator: a policy reason, a
	// stream-failure reason, a refresh outcome or a local sentinel name. It
	// is never provider text.
	Reason string
	// cause is the classified failure, kept for errors.Is/errors.As and
	// never rendered.
	cause error
}

// Error renders the classification and the already-sanitized provider
// identity.
func (e *ClassifiedError) Error() string {
	if e == nil {
		return "responses: unclassified failure"
	}
	message := "responses: " + string(e.Class) + " (" + string(e.Phase) + "/" + string(e.Source) + ")"
	if e.Status != 0 {
		message += " (status " + strconv.Itoa(e.Status) + ")"
	}
	if e.Code != "" {
		message += " (code " + strconv.Quote(e.Code) + ")"
	}
	if e.RequestID != "" {
		message += " (request id " + strconv.Quote(e.RequestID) + ")"
	}
	if e.ProfileID != "" {
		message += " (profile " + strconv.Quote(e.ProfileID) + ")"
	}
	if e.Reason != "" {
		message += " (reason " + e.Reason + ")"
	}
	if e.Retryable {
		message += " (retryable)"
	}
	if e.Diagnostic != "" {
		message += ": " + e.Diagnostic
	}
	return message
}

// TerminalPluginError maps the classified failure to the host-visible
// terminal contract. The connector's taxonomy stays local, but the recovery
// class and its safe retryability are no longer hidden behind the generic
// internal terminal. Messages are constants: provider text, status, request id
// and the local profile id never enter the terminal.
//
// ForwardExecute owns post-output safety: when client-visible output was
// already committed it forces Retryable=false and OutputCommitted=true, so
// this method never decides to retry after output.
func (e *ClassifiedError) TerminalPluginError() *backendplugin.PluginError {
	if e == nil {
		return nil
	}
	code := backendplugin.ErrorCodeInternal
	message := "connector failure"
	retryable := false
	switch e.Class {
	case ErrorClassUnsupportedCapability:
		code, message = backendplugin.ErrorCodeFailedPrecondition, "unsupported capability"
	case ErrorClassRoute:
		code, message = backendplugin.ErrorCodeFailedPrecondition, "provider route not supported"
	case ErrorClassInvalidUserContext:
		code, message = backendplugin.ErrorCodeFailedPrecondition, "registration or context is not usable"
		switch e.Status {
		case 401:
			code = backendplugin.ErrorCodeUnauthenticated
		case 403:
			code = backendplugin.ErrorCodePermissionDenied
		}
	case ErrorClassTemporaryUnavailable:
		code, message, retryable = backendplugin.ErrorCodeProviderTransient, "provider temporarily unavailable", true
	case ErrorClassUsageExhausted:
		code, message = backendplugin.ErrorCodeResourceExhausted, "plan usage exhausted"
	case ErrorClassRefreshTerminal:
		code, message = backendplugin.ErrorCodeUnauthenticated, "registration requires reauthorization"
	case ErrorClassRefreshTransient:
		code, message, retryable = backendplugin.ErrorCodeProviderTransient, "credential refresh temporarily unavailable", true
	case ErrorClassProtocolViolation:
		code, message = backendplugin.ErrorCodeProtocolViolation, "connector protocol violation"
	case ErrorClassProviderFailure:
		code, message = backendplugin.ErrorCodeProviderTerminal, "provider failure"
	}
	return &backendplugin.PluginError{Code: code, Message: message, Retryable: retryable && e.Retryable}
}

// Unwrap returns the classified cause.
func (e *ClassifiedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// Format keeps every formatting verb away from the cause.
//
// The guarantee holds when fmt dispatches to this method. The routes where it
// cannot are the documented Formatter limits: "%T" is safe, while "%w" and
// "%p" bypass every method on the type.
func (e *ClassifiedError) Format(state fmt.State, verb rune) {
	safe := e.Error()
	if verb == 'q' {
		_, _ = io.WriteString(state, strconv.Quote(safe))
		return
	}
	_, _ = io.WriteString(state, safe)
}

// Classify maps one connector failure onto the taxonomy.
//
// The answer is deterministic: the class is read from the error's typed
// fields, its provider status or its documented provider code. A nil error and
// an error outside this connector's failure surface are reported as
// unclassified, never guessed into a recovery class.
//
// Precedence is part of the contract. A typed outcome this connector (or the
// 2.5 refresh boundary) already decided wins over a generic deadline
// inference: an HTTP-client timeout produces context.DeadlineExceeded while the
// caller's own context may still be live, so a nested deadline is never
// evidence of withdrawal. The two cancellation filters are therefore
// asymmetric: context.Canceled - which only a cancelled context produces - is
// filtered before the typed outcomes, while context.DeadlineExceeded is
// filtered only after them, where nothing claimed the error. Caller
// cancellation also reaches the classifier as the context's own error, because
// the origin that owns the context (the transport and the normalizer) returns
// ctx.Err() directly.
func Classify(err error) (*ClassifiedError, bool) {
	if err == nil {
		return nil, false
	}
	var alreadyClassified *ClassifiedError
	if errors.As(err, &alreadyClassified) && alreadyClassified != nil {
		return alreadyClassified, true
	}
	// A caller who withdrew is not reporting a connector failure. Only
	// context.Canceled proves withdrawal - an HTTP-client timeout yields
	// context.DeadlineExceeded while the caller's own context stays live - so
	// this filter is narrow and a refresh outcome carrying a real deadline
	// keeps its typed class below.
	if errors.Is(err, context.Canceled) {
		return nil, false
	}
	// The refresh outcome is checked before the local credential sentinels:
	// the registration path wraps a refresh failure in
	// ErrRegistrationUnusable, and a terminal refresh must keep its own
	// class rather than becoming a generic invalid-registration failure.
	if classified, ok := classifyRefreshOutcome(err); ok {
		return classified, true
	}
	// An UNTYPED deadline is not a refresh outcome: nothing above claimed it,
	// and a cancellation is the caller's decision, not a connector failure
	// class; the host owns what to do with it.
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, false
	}
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		return classifyHTTPError(err, httpErr), true
	}
	var streamFailure *StreamFailure
	if errors.As(err, &streamFailure) && streamFailure != nil {
		return classifyStreamFailure(err, streamFailure), true
	}
	var transportErr *TransportError
	if errors.As(err, &transportErr) && transportErr != nil {
		return classifyTransportError(err, transportErr), true
	}
	if classified, ok := classifyProjectionError(err); ok {
		return classified, true
	}
	var policyErr *PolicyError
	if errors.As(err, &policyErr) && policyErr != nil {
		return classifyPolicyError(err, policyErr), true
	}
	if classified, ok := classifyLocalSentinel(err); ok {
		return classified, true
	}
	return nil, false
}

// classifyRefreshOutcome maps the 2.5 serialized-refresh classes.
//
// The class is read through the siwc sentinels, so the connector reuses that
// vocabulary instead of re-deriving it from a status or a code. When the token
// endpoint answered, its already-sanitized status, machine-readable code and
// bounded description are preserved on the classification.
func classifyRefreshOutcome(err error) (*ClassifiedError, bool) {
	type outcome struct {
		class  ErrorClass
		reason string
	}
	var resolved outcome
	switch {
	case errors.Is(err, siwc.ErrRefreshTerminal):
		resolved = outcome{ErrorClassRefreshTerminal, siwc.RefreshClassTerminal}
	case errors.Is(err, siwc.ErrRefreshTransient):
		resolved = outcome{ErrorClassRefreshTransient, siwc.RefreshClassTransient}
	case errors.Is(err, siwc.ErrRefreshInvalidClient):
		// The client configuration must be fixed; the credential is not
		// the fault.
		resolved = outcome{ErrorClassInvalidUserContext, siwc.RefreshClassInvalidClient}
	case errors.Is(err, siwc.ErrRefreshRejected):
		// The same request must not be repeated unchanged.
		resolved = outcome{ErrorClassInvalidUserContext, siwc.RefreshClassRejected}
	case errors.Is(err, siwc.ErrRefreshUnreadable):
		// The provider's failure could not be read; it is neither absent
		// nor temporary.
		resolved = outcome{ErrorClassProtocolViolation, siwc.RefreshClassUnreadable}
	case errors.Is(err, siwc.ErrRefreshUnconfirmed):
		// The local record was published and its durability is unproven;
		// the registration state is indeterminate.
		resolved = outcome{ErrorClassInvalidUserContext, siwc.RefreshClassUnconfirmed}
	default:
		return nil, false
	}
	classified := &ClassifiedError{
		Class:     resolved.class,
		Phase:     ErrorPhaseCredential,
		Source:    ErrorSourceLocal,
		Retryable: retryableClass(resolved.class),
		Reason:    resolved.reason,
		cause:     err,
	}
	// The RefreshError keeps the provider's own identity (already redacted
	// and bounded by the 2.5 refresh boundary). Only a provider ERROR status
	// is the provider's report: a success answer whose bytes the connector
	// judged unusable, and a redirect the connector refused to follow, are
	// the connector's own judgement. Their discriminators therefore never
	// occupy the provider code field, while the observed status is preserved.
	var refreshErr *siwc.RefreshError
	if errors.As(err, &refreshErr) && refreshErr != nil {
		classified.Status = refreshErr.StatusCode
		classified.Diagnostic = refreshErr.Description
		if refreshProviderReported(refreshErr.StatusCode) {
			classified.Source = ErrorSourceProvider
			classified.Code = refreshErr.Code
		}
	}
	return classified, true
}

// refreshProviderReported reports whether a refresh outcome carries the
// provider's own failure report.
//
// The refresh producer records the observed status on every outcome that
// reached the token endpoint. Only an HTTP error status is the provider
// saying that the attempt failed; a success answer the connector could not
// read (malformed JSON, a tokenless object) and a redirect the connector
// refused to follow are the provider ANSWERING, not the provider reporting a
// failure. Those paths are the connector's judgement, so they keep no
// provider code.
func refreshProviderReported(status int) bool {
	return status >= 400
}

// classifyHTTPError maps a provider answer that could not be used.
//
// Source follows WHO reported the failure, not which side supplied the status.
// A provider error document (a non-200 answer) is the provider's report, so its
// status, code and request id stay provider data. A 200 answer that is not an
// event stream is the opposite: the provider reported success, the connector
// refused to use the bytes, so the source is local while the observed status
// and request id are still preserved for correlation.
func classifyHTTPError(err error, httpErr *HTTPError) *ClassifiedError {
	class := classForProviderStatus(httpErr.Status)
	if mapped, ok := providerCodeClasses[httpErr.Code]; ok {
		class = mapped
	}
	source := ErrorSourceProvider
	reason := ""
	if errors.Is(httpErr.Err, ErrUnexpectedContentType) {
		// A success status whose answer is not an event stream is a
		// connector judgement of the provider's bytes, never a temporary
		// condition.
		class = ErrorClassProtocolViolation
		source = ErrorSourceLocal
		reason = "unexpected_content_type"
	}
	return &ClassifiedError{
		Class:      class,
		Phase:      ErrorPhaseAdmission,
		Source:     source,
		Retryable:  retryableClass(class),
		Status:     httpErr.Status,
		Code:       httpErr.Code,
		RequestID:  httpErr.RequestID,
		Diagnostic: httpErr.Diagnostic,
		ProfileID:  safeProviderText(httpErr.ProfileID, maxProfileIDRunes),
		Reason:     reason,
		cause:      err,
	}
}

// classifyStreamFailure maps a normalized stream failure.
//
// The provider code is copied ONLY from a provider-origin failure. A local
// failure's Code is a discriminator the connector derived, so it is reported
// through Reason and never through Code.
func classifyStreamFailure(err error, failure *StreamFailure) *ClassifiedError {
	source := ErrorSourceLocal
	if failure.Origin == FailureOriginProvider {
		source = ErrorSourceProvider
	}
	class := ErrorClassProtocolViolation
	code := ""
	if source == ErrorSourceProvider {
		code = failure.Code
		class = ErrorClassProviderFailure
		if mapped, ok := providerCodeClasses[failure.Code]; ok {
			class = mapped
		}
	} else if failure.Reason == StreamFailureUnrepresentableItem {
		// The provider sent a documented output item this connector cannot
		// carry: the answer is valid, the capability is missing.
		class = ErrorClassUnsupportedCapability
	}
	return &ClassifiedError{
		Class:      class,
		Phase:      ErrorPhaseStream,
		Source:     source,
		Retryable:  retryableClass(class),
		Code:       code,
		RequestID:  failure.RequestID,
		Diagnostic: failure.Diagnostic,
		ProfileID:  safeProviderText(failure.ProfileID, maxProfileIDRunes),
		Reason:     string(failure.Reason),
		cause:      err,
	}
}

// classifyTransportError maps a request that never produced an answer.
func classifyTransportError(err error, transportErr *TransportError) *ClassifiedError {
	return &ClassifiedError{
		Class:      ErrorClassTemporaryUnavailable,
		Phase:      ErrorPhaseTransport,
		Source:     ErrorSourceLocal,
		Retryable:  true,
		Diagnostic: transportErr.Diagnostic,
		ProfileID:  safeProviderText(transportErr.ProfileID, maxProfileIDRunes),
		Reason:     "transport_failed",
		cause:      err,
	}
}

// classifyProjectionError maps one canonical projection refusal.
//
// The 4.2 projector owns this refusal and AdmitCall propagates it unchanged, so
// the classification preserves that identity: the reason is the typed
// ProjectionReason, and the cause still satisfies
// errors.Is(err, ErrCallNotRepresentable). The provider reported nothing at all,
// so status, request id, code and diagnostic stay empty and the refusal detail
// is never replayed as provider text.
func classifyProjectionError(err error) (*ClassifiedError, bool) {
	var projectionErr *ProjectionError
	hasProjection := errors.As(err, &projectionErr) && projectionErr != nil
	if !hasProjection && !errors.Is(err, ErrCallNotRepresentable) {
		return nil, false
	}
	reason := "call_not_representable"
	if hasProjection && projectionErr.Reason != "" {
		reason = string(projectionErr.Reason)
	}
	return &ClassifiedError{
		Class:  ErrorClassUnsupportedCapability,
		Phase:  ErrorPhaseLocal,
		Source: ErrorSourceLocal,
		Reason: reason,
		cause:  err,
	}, true
}

// classifyPolicyError maps one preview-policy refusal.
func classifyPolicyError(err error, policyErr *PolicyError) *ClassifiedError {
	class := ErrorClassInvalidUserContext
	switch policyErr.Reason {
	case PolicyReasonUnsupportedField, PolicyReasonUnsupportedTool,
		PolicyReasonGenerationOption, PolicyReasonCacheHint, PolicyReasonProtocolExtension,
		PolicyReasonWebSearchDeclaration:
		// The request asks for semantics this route cannot represent: the
		// same body cannot succeed.
		class = ErrorClassUnsupportedCapability
	}
	reason := string(policyErr.Reason)
	if reason == "" {
		reason = "request_not_admissible"
	}
	return &ClassifiedError{
		Class:     class,
		Phase:     ErrorPhaseLocal,
		Source:    ErrorSourceLocal,
		Retryable: false,
		Reason:    reason,
		cause:     err,
	}
}

// classifyLocalSentinel maps the connector's own value-free local failures,
// including a canonical call that violates its own invariants.
func classifyLocalSentinel(err error) (*ClassifiedError, bool) {
	type localFailure struct {
		class  ErrorClass
		phase  ErrorPhase
		reason string
		retry  bool
	}
	var resolved localFailure
	switch {
	case errors.Is(err, ErrNotPublicEndpoint):
		resolved = localFailure{ErrorClassRoute, ErrorPhaseLocal, "not_public_endpoint", false}
	case errors.Is(err, ErrRequestNotAdmissible):
		resolved = localFailure{ErrorClassUnsupportedCapability, ErrorPhaseLocal, "request_not_admissible", false}
	case errors.Is(err, aipapi.ErrInvalidCall):
		// The canonical call violates its own invariants (or carries an
		// unusable prompt-cache hint). The request is unusable as written,
		// so the recovery is a corrected request, never a retry.
		resolved = localFailure{ErrorClassInvalidUserContext, ErrorPhaseLocal, "invalid_canonical_call", false}
	case errors.Is(err, ErrUnknownPolicyMode):
		resolved = localFailure{ErrorClassInvalidUserContext, ErrorPhaseLocal, "unknown_policy_mode", false}
	case errors.Is(err, ErrRequestTooLarge):
		resolved = localFailure{ErrorClassInvalidUserContext, ErrorPhaseLocal, "request_too_large", false}
	case errors.Is(err, ErrInvalidWireRequest):
		resolved = localFailure{ErrorClassInvalidUserContext, ErrorPhaseLocal, "invalid_wire_request", false}
	case errors.Is(err, ErrRequestNotStreaming):
		resolved = localFailure{ErrorClassInvalidUserContext, ErrorPhaseLocal, "request_not_streaming", false}
	case errors.Is(err, ErrNoSelectedProfile):
		resolved = localFailure{ErrorClassInvalidUserContext, ErrorPhaseLocal, "no_selected_profile", false}
	case errors.Is(err, ErrRegistrationUnusable):
		resolved = localFailure{ErrorClassInvalidUserContext, ErrorPhaseLocal, "registration_unusable", false}
	case errors.Is(err, ErrReplayAfterVisibleOutput):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseLocal, "replay_after_visible_output", false}
	case errors.Is(err, ErrUnexpectedContentType):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseLocal, "unexpected_content_type", false}
	case errors.Is(err, ErrErrorBodyTooLarge):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseLocal, "error_body_too_large", false}
	case errors.Is(err, ErrErrorBodyUnreadable):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseLocal, "error_body_unreadable", false}
	case errors.Is(err, ErrUnexpectedStatus):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseLocal, "unexpected_status", false}
	case errors.Is(err, ErrTransportFailed):
		resolved = localFailure{ErrorClassTemporaryUnavailable, ErrorPhaseTransport, "transport_failed", true}
	case errors.Is(err, ErrStreamUnreadable):
		resolved = localFailure{ErrorClassTemporaryUnavailable, ErrorPhaseStream, "stream_unreadable", true}
	case errors.Is(err, ErrStreamTooLarge):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseStream, "stream_too_large", false}
	case errors.Is(err, ErrStreamEventTooLarge):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseStream, "stream_event_too_large", false}
	case errors.Is(err, ErrStreamClosed):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseStream, "stream_closed", false}
	case errors.Is(err, ErrStreamFailed):
		resolved = localFailure{ErrorClassProtocolViolation, ErrorPhaseStream, "stream_failed", false}
	default:
		return nil, false
	}
	classified := &ClassifiedError{
		Class:     resolved.class,
		Phase:     resolved.phase,
		Source:    ErrorSourceLocal,
		Retryable: resolved.retry,
		Reason:    resolved.reason,
		cause:     err,
	}
	// A raw stream failure carries the provenance of the answer it interrupted.
	// The cause stays the constant, value-free sentinel; only the provider
	// request id and the sanitized local profile id are added.
	var readFailure *StreamReadFailure
	if errors.As(err, &readFailure) && readFailure != nil {
		classified.RequestID = readFailure.RequestID
		classified.ProfileID = safeProviderText(readFailure.ProfileID, maxProfileIDRunes)
	}
	return classified, true
}

// classForProviderStatus maps a provider HTTP status without a documented
// code.
//
// The status is the provider's ACTUAL answer, not an inference from the
// request shape. A documented code always wins over this fallback.
func classForProviderStatus(status int) ErrorClass {
	switch {
	case status == 429:
		// HTTP throttling. Only the documented plan-limit code establishes
		// usage exhaustion; a 429 alone says "slow down", so the supported
		// recovery is a retry of the same request, not a permanent pause.
		return ErrorClassTemporaryUnavailable
	case status >= 500, status == 408, status == 425:
		return ErrorClassTemporaryUnavailable
	case status == 401, status == 403:
		return ErrorClassInvalidUserContext
	case status == 404, status == 405:
		return ErrorClassRoute
	case status >= 400:
		return ErrorClassInvalidUserContext
	case status >= 300:
		// The redirect was refused; the documented route did not serve
		// the request.
		return ErrorClassRoute
	default:
		return ErrorClassProtocolViolation
	}
}

// retryableClass reports whether a class may be retried with the same request
// unchanged.
func retryableClass(class ErrorClass) bool {
	return class == ErrorClassTemporaryUnavailable || class == ErrorClassRefreshTransient
}
