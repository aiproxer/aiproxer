package app

import (
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// SessionWire carries session identifiers from the orchestration boundary into [Manager.BeginTurn].
// Fields align with [github.com/aiproxer/aiproxer/pkg/aipapi.SessionRef] without importing aipapi.
type SessionWire struct {
	ClientSessionID string
	ContinuityKey   string
	ALegID          string
	SessionID       string
	ResumeToken     string
}

// ManagerConfig configures [Manager] policy gates and crypto inputs.
type ManagerConfig struct {
	// ResumeWindow is inactivity-based resume limit; zero means unbounded.
	ResumeWindow time.Duration
	// StoreDurable is true when the secure-session store is durable (e.g. SQLite).
	StoreDurable bool
	// RequireDurableStore rejects BeginTurn when durable evidence is required but StoreDurable is false.
	RequireDurableStore bool
	// FingerprintKey fingerprints resume tokens (must match store configuration).
	FingerprintKey []byte
	// ObserveActivityTouch, when set, is called with wall time spent in last-activity [Store.TouchActivity] in BeginTurn.
	ObserveActivityTouch func(seconds float64)
	// ResumeFingerprintPrincipalOnly when true fingerprints resume tokens using only [PrincipalRef.ID]
	// as domain-separation material (HMAC still includes raw token bytes). Agent/client digest drift
	// does not invalidate resumes (see secure_session.resume_token_bind_principal_only).
	ResumeFingerprintPrincipalOnly bool
}

// BeginInput is the secure-session gate input for one client-visible turn.
type BeginInput struct {
	Now     time.Time
	TraceID string

	Session            SessionWire
	Principal          domain.PrincipalRef
	Workspace          domain.WorkspaceRef
	GlobalPolicy       domain.PolicyMetadata
	ClientHints        domain.ClientHints
	FirstMessageDigest string

	// WorkspaceMatchRequired rejects the turn when a workspace id is required but missing on the request.
	WorkspaceMatchRequired bool
}

// BeginResult is the validated secure-session context for routing and continuity.
type BeginResult struct {
	Record          domain.Record
	TurnID          domain.TurnID
	IsNew           bool
	Response        ResponseMetadata
	EffectivePolicy domain.PolicyMetadata
}

// ResponseMetadata carries wire-safe session authority for new sessions only.
// Raw resume tokens must never be loaded from storage; they appear here only on create.
type ResponseMetadata struct {
	SessionID   string
	ResumeToken domain.ResumeToken
}

// TurnOutcomeKind classifies how a turn ended for audit and diagnostics.
type TurnOutcomeKind int

const (
	TurnOutcomeUnknown TurnOutcomeKind = iota
	TurnOutcomeSuccess
	TurnOutcomePreOutputDenied
	TurnOutcomeSurfacedFailure
	TurnOutcomePostOutputRecorderFailure
)

// TurnOutcome is input to [Manager.FinishTurn] for durable evidence.
type TurnOutcome struct {
	Kind TurnOutcomeKind
}
