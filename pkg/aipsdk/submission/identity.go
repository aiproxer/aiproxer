// Package submission defines the provider-neutral trust boundary for customer
// submission identity. A frontend or harness adapter supplies the authority;
// request payload fields never participate in binding.
package submission

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxIdentityBytes = 512

// Kind describes how a request relates to a customer submission.
type Kind string

const (
	KindNewSubmission    Kind = "new_submission"
	KindToolContinuation Kind = "tool_continuation"
	KindHistoricalReplay Kind = "historical_replay"
	KindTransportRetry   Kind = "transport_retry"
	KindFollowUp         Kind = "follow_up"
	KindLocalCommand     Kind = "local_command"
	// KindNew is a short spelling for KindNewSubmission.
	KindNew = KindNewSubmission
)

// Source identifies the trusted authority that classified a request.
type Source string

const (
	SourceAuthenticatedCurrentTurn  Source = "authenticated_current_turn"
	SourceAuthenticatedContinuation Source = "authenticated_continuation"
	SourceHarnessAdapter            Source = "harness_adapter"
	SourceLocalCommand              Source = "local_command"
	// SourceClient is intentionally never accepted by Bind. It is exposed only
	// so adapters can classify an attempted client override explicitly.
	SourceClient Source = "client"
)

// Status is the outcome of submission attribution. Unsupported and incomplete
// are explicit non-billable outcomes; they do not prevent ordinary inference.
type Status string

const (
	StatusTrusted     Status = "trusted"
	StatusUnsupported Status = "unsupported"
	StatusIncomplete  Status = "incomplete"
	StatusRejected    Status = "rejected"
)

var (
	// ErrInvalidAuthority identifies malformed or unknown authority data.
	ErrInvalidAuthority = errors.New("submission: invalid authority")
	// ErrUntrustedAuthority identifies an authority that did not come from an
	// accepted authenticated or harness source.
	ErrUntrustedAuthority = errors.New("submission: untrusted authority")
	// ErrScopeMismatch identifies a continuation bound to another call, A-leg,
	// tenant, principal, session, or store.
	ErrScopeMismatch = errors.New("submission: scope mismatch")
	// ErrLifecycleConflict identifies an attempt to reuse a closed invocation
	// for a new follow-up submission.
	ErrLifecycleConflict = errors.New("submission: lifecycle conflict")
	// ErrIncomplete identifies trusted attribution that cannot support a strict
	// submission-priced offer.
	ErrIncomplete = errors.New("submission: incomplete attribution")
)

// Scope is the trusted isolation boundary against which a supplied authority
// is checked. Empty values mean that the corresponding dimension is not known
// at that boundary; they are never inferred from request payload data.
type Scope struct {
	TenantID    string
	PrincipalID string
	SessionID   string
	ALegID      string
	StoreID     string
	CallID      string
}

// Equal reports exact equality of all scope dimensions.
func (s Scope) Equal(other Scope) bool {
	return s.TenantID == other.TenantID &&
		s.PrincipalID == other.PrincipalID &&
		s.SessionID == other.SessionID &&
		s.ALegID == other.ALegID &&
		s.StoreID == other.StoreID &&
		s.CallID == other.CallID
}

// Authority is the trusted, immutable identity supplied by an authenticated
// frontend current-turn/continuation authority or an explicitly supported
// harness adapter. It contains no raw credential or request payload data.
type Authority struct {
	SubmissionID string
	// BillingCallID is optional for a trusted continuation. When omitted, the
	// runtime may allocate a fresh call grouping while retaining SubmissionID;
	// when present, it must be reused rather than replaced.
	BillingCallID string
	Kind          Kind
	Source        Source
	Scope         Scope
}

// Clone returns a value copy suitable for context or store boundaries.
func (a Authority) Clone() Authority { return a }

// BindingInput carries the runtime-owned identity used to validate an
// authority. The runtime, not the caller payload, builds this input.
type BindingInput struct {
	Scope Scope
}

// Result is the immutable result of one attribution attempt.
type Result struct {
	Status     Status
	Authority  Authority
	ReasonCode string
}

// BindingResult is the descriptive spelling of Result for adapter APIs.
type BindingResult = Result

// Trusted reports whether the result can be used for submission-scoped
// commercial rules.
func (r Result) Trusted() bool { return r.Status == StatusTrusted }

// Billable reports whether a submission identity is available for a
// submission-scoped charge. Local commands remain explicitly non-billable.
func (r Result) Billable() bool {
	return r.Trusted() && r.Authority.SubmissionID != "" && r.Authority.Kind != KindLocalCommand
}

// Bind validates and freezes one trusted authority against the runtime-owned
// scope. A missing authority, or a trusted authority without enough identity
// material, returns an explicit unsupported/incomplete result so inference can
// continue without a prompt-priced offer. Malformed, untrusted, or
// cross-scope authorities are rejected before execution state is opened.
func Bind(input BindingInput, supplied *Authority) (Result, error) {
	if supplied == nil {
		return Result{Status: StatusUnsupported, ReasonCode: "submission_identity_unavailable"}, nil
	}
	if err := input.Scope.validate(); err != nil {
		return Result{Status: StatusRejected, ReasonCode: reasonCode(err)}, err
	}
	authority := supplied.Clone()
	if err := authority.validate(); err != nil {
		if errors.Is(err, ErrIncomplete) {
			// Scope isolation still applies when the authority is incomplete. Do
			// not let a missing identity field turn a cross-tenant, cross-call,
			// or cross-A-leg continuation into an apparently harmless fallback.
			bound, scopeErr := bindScope(authority, input.Scope)
			if scopeErr != nil {
				return Result{Status: StatusRejected, Authority: authority, ReasonCode: reasonCode(scopeErr)}, scopeErr
			}
			return Result{Status: StatusIncomplete, Authority: bound, ReasonCode: "submission_identity_incomplete"}, nil
		}
		status := StatusRejected
		return Result{Status: status, Authority: authority, ReasonCode: reasonCode(err)}, err
	}

	bound, err := bindScope(authority, input.Scope)
	if err != nil {
		return Result{Status: StatusRejected, Authority: authority, ReasonCode: reasonCode(err)}, err
	}
	if requiresALeg(bound.Kind) && strings.TrimSpace(bound.Scope.ALegID) == "" {
		return Result{Status: StatusIncomplete, Authority: bound, ReasonCode: "continuation_a_leg_unavailable"}, nil
	}
	return Result{Status: StatusTrusted, Authority: bound, ReasonCode: "submission_identity_bound"}, nil
}

func (a Authority) validate() error {
	if !validKind(a.Kind) {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidAuthority, a.Kind)
	}
	if !validSource(a.Source) || a.Source == SourceClient {
		return fmt.Errorf("%w: source %q is not trusted", ErrUntrustedAuthority, a.Source)
	}
	if err := validateSourceKind(a.Source, a.Kind); err != nil {
		return err
	}
	if err := validateOptionalID("submission_id", a.SubmissionID); err != nil {
		return err
	}
	if err := validateOptionalID("billing_call_id", a.BillingCallID); err != nil {
		return err
	}
	if err := a.Scope.validate(); err != nil {
		return err
	}
	if a.Kind == KindLocalCommand {
		if a.SubmissionID != "" || a.BillingCallID != "" {
			return fmt.Errorf("%w: local command cannot carry submission or billing call identity", ErrLifecycleConflict)
		}
		return nil
	}
	if strings.TrimSpace(a.SubmissionID) == "" {
		return fmt.Errorf("%w: submission_id is required", ErrIncomplete)
	}
	if (a.Kind == KindNewSubmission || a.Kind == KindFollowUp) && strings.TrimSpace(a.BillingCallID) != "" {
		return fmt.Errorf("%w: new submission must not reuse billing call identity", ErrLifecycleConflict)
	}
	return nil
}

func (s Scope) validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "tenant_id", value: s.TenantID},
		{name: "principal_id", value: s.PrincipalID},
		{name: "session_id", value: s.SessionID},
		{name: "a_leg_id", value: s.ALegID},
		{name: "store_id", value: s.StoreID},
		{name: "call_id", value: s.CallID},
	} {
		if err := validateOptionalID(field.name, field.value); err != nil {
			return err
		}
	}
	return nil
}

func bindScope(authority Authority, expected Scope) (Authority, error) {
	bound := authority
	var err error
	bound.Scope.TenantID, err = bindDimension("tenant_id", bound.Scope.TenantID, expected.TenantID)
	if err != nil {
		return authority, err
	}
	bound.Scope.PrincipalID, err = bindDimension("principal_id", bound.Scope.PrincipalID, expected.PrincipalID)
	if err != nil {
		return authority, err
	}
	bound.Scope.SessionID, err = bindDimension("session_id", bound.Scope.SessionID, expected.SessionID)
	if err != nil {
		return authority, err
	}
	bound.Scope.ALegID, err = bindDimension("a_leg_id", bound.Scope.ALegID, expected.ALegID)
	if err != nil {
		return authority, err
	}
	bound.Scope.StoreID, err = bindDimension("store_id", bound.Scope.StoreID, expected.StoreID)
	if err != nil {
		return authority, err
	}
	bound.Scope.CallID, err = bindDimension("call_id", bound.Scope.CallID, expected.CallID)
	if err != nil {
		return authority, err
	}
	return bound, nil
}

func bindDimension(name, supplied, expected string) (string, error) {
	supplied = strings.TrimSpace(supplied)
	expected = strings.TrimSpace(expected)
	if supplied != "" && expected != "" && supplied != expected {
		return supplied, fmt.Errorf("%w: %s", ErrScopeMismatch, name)
	}
	if supplied != "" {
		return supplied, nil
	}
	return expected, nil
}

func validateSourceKind(source Source, kind Kind) error {
	switch source {
	case SourceAuthenticatedCurrentTurn:
		if kind != KindNewSubmission && kind != KindFollowUp {
			return fmt.Errorf("%w: current-turn source cannot classify %q", ErrUntrustedAuthority, kind)
		}
	case SourceAuthenticatedContinuation:
		if !isContinuation(kind) {
			return fmt.Errorf("%w: continuation source cannot classify %q", ErrUntrustedAuthority, kind)
		}
	case SourceLocalCommand:
		if kind != KindLocalCommand {
			return fmt.Errorf("%w: local source cannot classify %q", ErrUntrustedAuthority, kind)
		}
	case SourceHarnessAdapter:
		// A harness is a supported explicit adapter and may model every
		// classification, including local commands.
	default:
		return fmt.Errorf("%w: source %q", ErrUntrustedAuthority, source)
	}
	return nil
}

func validKind(kind Kind) bool {
	switch kind {
	case KindNewSubmission, KindToolContinuation, KindHistoricalReplay, KindTransportRetry, KindFollowUp, KindLocalCommand:
		return true
	default:
		return false
	}
}

func validSource(source Source) bool {
	switch source {
	case SourceAuthenticatedCurrentTurn, SourceAuthenticatedContinuation, SourceHarnessAdapter, SourceLocalCommand, SourceClient:
		return true
	default:
		return false
	}
}

func isContinuation(kind Kind) bool {
	return kind == KindToolContinuation || kind == KindHistoricalReplay || kind == KindTransportRetry
}

func requiresALeg(kind Kind) bool {
	return isContinuation(kind)
}

func validateOptionalID(name, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > maxIdentityBytes {
		return fmt.Errorf("%w: %s exceeds %d bytes", ErrInvalidAuthority, name, maxIdentityBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid UTF-8", ErrInvalidAuthority, name)
	}
	if strings.TrimSpace(value) != value {
		return fmt.Errorf("%w: %s has surrounding whitespace", ErrInvalidAuthority, name)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return fmt.Errorf("%w: %s contains unsafe characters", ErrInvalidAuthority, name)
		}
	}
	return nil
}

func reasonCode(err error) string {
	switch {
	case errors.Is(err, ErrUntrustedAuthority):
		return "untrusted_submission_authority"
	case errors.Is(err, ErrScopeMismatch):
		return "submission_scope_mismatch"
	case errors.Is(err, ErrLifecycleConflict):
		return "submission_lifecycle_conflict"
	default:
		return "invalid_submission_authority"
	}
}

type authorityContextKey struct{}

// WithAuthority attaches an authority supplied by a trusted frontend or
// explicitly supported harness adapter. Nil contexts are tolerated.
func WithAuthority(ctx context.Context, authority Authority) context.Context {
	if ctx == nil {
		ctx = context.TODO()
	}
	return context.WithValue(ctx, authorityContextKey{}, authority.Clone())
}

// AuthorityFromContext returns a defensive value copy of the authority carried
// by [WithAuthority]. It never reads request payload metadata.
func AuthorityFromContext(ctx context.Context) (Authority, bool) {
	if ctx == nil {
		return Authority{}, false
	}
	authority, ok := ctx.Value(authorityContextKey{}).(Authority)
	if !ok {
		return Authority{}, false
	}
	return authority.Clone(), true
}
