package auth

import (
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// AccessMode is deployment access posture at the time of an event (string for stable wire/logging).
type AccessMode string

const (
	AccessSingleUser AccessMode = "single_user"
	AccessMultiUser  AccessMode = "multi_user"
)

// SessionCertainty describes how confidently the runtime associated this session with prior state.
type SessionCertainty string

const (
	SessionCertaintyKnown   SessionCertainty = "known"
	SessionCertaintyUnknown SessionCertainty = "unknown"
	SessionCertaintyPartial SessionCertainty = "partial"
)

// AuthDecisionEvent is a non-secret audit record for a single auth decision. It does not
// include raw bearer material, API keys, SSO or resume tokens, or personal OAuth access tokens.
//
// PrincipalSafeClaims carries only claim key names (empty string values) when populated from
// stdhttp auth wiring; custom composition-root sinks must still avoid treating other event fields
// as proof that upstream state is free of secrets.
//
// Any new exported field must be vetted as operator-safe and non-secret; custom sinks and log
// backends remain responsible for redaction policy beyond the core dispatcher's challenge-summary
// sanitization.
type AuthDecisionEvent struct {
	Time    time.Time
	TraceID string
	// Access and policy context (immutable snapshot at decision time)
	AccessMode    AccessMode
	RequiredLevel RequiredLevel
	HandlerKind   HandlerKind
	Frontend      string
	Outcome       DecisionOutcome
	ReasonCode    string
	// Principal is a safe snapshot: IDs and non-secret display metadata only.
	PrincipalID          string
	PrincipalDisplayName string
	PrincipalRoles       []string
	// PrincipalSafeClaims is keyed by non-empty trimmed claim names; values must remain empty
	// on audit paths—do not place secret-bearing strings in map values.
	PrincipalSafeClaims map[string]string
	// Device is a non-secret snapshot: identifiers and redacted fingerprint only.
	DeviceID          string
	DeviceKeyID       string
	DeviceFingerprint string
	// Challenge, when applicable, is non-secret metadata.
	ChallengeKind    ChallengeKind
	ChallengeSummary string
	// Scope is an optional safe principal/scope snapshot from a trusted auth decision. It is
	// nil when no scope was supplied. Raw secrets, transport headers, and resume authority
	// must never be placed here (requirements 6.1, 2.6, 5.2).
	Scope *scope.PrincipalScopeView
}

// SessionStartEvent is a non-secret record of a new or uncertain proxy-recognized session.
// ClientSessionRef is an opaque, non-secret client correlation id (not a resume proof or token).
// ALegID is a correlation id when present.
//
// Any new exported field must be vetted as operator-safe and non-secret for logging and custom sinks.
type SessionStartEvent struct {
	Time                 time.Time
	TraceID              string
	AccessMode           AccessMode
	RequiredLevel        RequiredLevel
	HandlerKind          HandlerKind
	Frontend             string
	SessionID            string
	ClientSessionRef     string
	ALegID               string
	Certainty            SessionCertainty
	IsNew                bool
	PrincipalID          string
	PrincipalDisplayName string
}
