package domain

import (
	"crypto/subtle"
	"time"
)

// SessionID is a proxy-owned opaque session identifier.
type SessionID string

// ResumeToken is a bearer resume proof (never persist in store records; use TokenFingerprint).
type ResumeToken string

// TokenFingerprint is the stored HMAC-SHA-256 digest of a resume token.
type TokenFingerprint [32]byte

// Equal reports whether a and b are the same fingerprint in constant time.
func (a TokenFingerprint) Equal(b TokenFingerprint) bool {
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// PrincipalRef binds a session to an authenticated identity.
type PrincipalRef struct {
	ID     string
	Issuer string
	Tenant string
}

// WorkspaceRef is an optional workspace scope for policy and isolation.
type WorkspaceRef struct {
	ID string
}

// ClientHints are non-authoritative correlation inputs from the client.
type ClientHints struct {
	ClientSessionID     string
	AgentIdentityDigest string
}

// PolicyMetadata captures stored treatment and recording policy flags.
type PolicyMetadata struct {
	PolicyVersion            string
	TranscriptEnabled        bool
	EffectiveTreatment       string
	StricterPolicyResolution string
	RouteHint                string
	RedactionProfile         string
	AuditMode                string
}

// UsageTotals aggregates usage for summaries and diagnostics.
type UsageTotals struct {
	SessionID    SessionID
	InputTokens  int64
	OutputTokens int64
	Attempts     int
}

// UsageDelta increments per-session usage and optional per-attempt accounting fields.
type UsageDelta struct {
	SessionID SessionID
	TurnID    TurnID
	BLegID    string

	InputTokens  int64
	OutputTokens int64

	CacheReadTokens          int64
	CacheWriteTokens         int64
	NonCachedInputTokens     int64
	ReasoningTokens          int64
	NonReasoningOutputTokens int64
	TotalTokens              int64
	CostNanoUnits            int64
	CostMinorUnits           int64
	Currency                 string
	CostSource               string
	RawUsageJSON             string
	BillingUnavailable       bool

	RequestStartedAt         time.Time
	FirstRemoteEventAt       time.Time
	FirstMeaningfulTokenAt   time.Time
	RemoteCompletedAt        time.Time
	ProxyCompletedAt         time.Time
	TTFTMillis               int64
	RemoteDurationMillis     int64
	CompletionDurationMillis int64
	// CompletionTPSMilli stores tokens-per-second with milliprecision: 5000 means 5.000 TPS.
	CompletionTPSMilli int64
}

// TranscriptItem is one ordered transcript entry.
type TranscriptItem struct {
	SessionID  SessionID
	TurnID     TurnID
	Seq        int64
	EventKind  string
	PayloadRef string
	CreatedAt  time.Time
}

// AuditItem is one ordered audit log entry.
type AuditItem struct {
	SessionID SessionID
	TurnID    TurnID
	Seq       int64
	Action    string
	Result    string
	CreatedAt time.Time
}

// TurnID identifies a user turn within a session.
type TurnID string

// ActivitySource classifies what updated last activity.
type ActivitySource string

const (
	ActivityClientRequest ActivitySource = "client_request"
	ActivityRemoteEvent   ActivitySource = "remote_event"
	ActivitySystem        ActivitySource = "system"
)

// SurfaceState describes how a backend attempt was exposed to the client.
type SurfaceState string

const (
	SurfaceSurfaced  SurfaceState = "surfaced"
	SurfaceSwallowed SurfaceState = "swallowed"
	SurfaceFailed    SurfaceState = "failed"
	SurfaceTimeout   SurfaceState = "timeout"
)

// ReadOptions bounds transcript/audit reads.
type ReadOptions struct {
	Limit    int
	AfterSeq int64
}

// SummaryQuery filters session summaries for operators.
type SummaryQuery struct {
	OwnerID     string
	WorkspaceID string
	Limit       int
}

// Summary is a roll-up row for operator views.
type Summary struct {
	SessionID      SessionID
	OwnerID        string
	WorkspaceID    string
	LastActivityAt time.Time
	TurnCount      int
	AttemptCount   int

	Status            SessionStatus
	ResumeEligible    bool
	ALegID            string
	PolicyVersion     string
	TranscriptEnabled bool
	RedactionProfile  string
	AuditMode         string
	UsageInputTokens  int64
	UsageOutputTokens int64
}

// AttemptSettings snapshots execution-affecting parameters for an attempt.
type AttemptSettings struct {
	Temperature          *float64
	MaxTokens            *int
	Timeout              time.Duration
	ReasoningEffort      string
	Verbosity            string
	Streaming            bool
	ToolSummary          []string
	BackendOptionsDigest string
}

// AttemptTrace captures routing and backend binding at B-leg open.
type AttemptTrace struct {
	SessionID       SessionID
	TurnID          TurnID
	ALegID          string
	BLegID          string
	AttemptSeq      int
	RequestedModel  string
	RequestedAlias  string
	ResolvedBackend string
	ResolvedModel   string
	RouteSource     string
	RouteReason     string
	Settings        AttemptSettings
	StartedAt       time.Time
	// OpenFailed marks an attempt whose backend Open acquired no stream: the
	// trace records that the attempt was made, never a successful acquisition.
	// The zero value is a stream-acquiring attempt, so existing rows read as
	// they always did.
	OpenFailed bool
}

// AttemptOutcome captures terminal state for a B-leg attempt.
type AttemptOutcome struct {
	SessionID      SessionID
	TurnID         TurnID
	BLegID         string
	Success        bool
	SurfaceState   SurfaceState
	HTTPStatus     int
	ProviderStatus string
	ErrorCode      string
	TimeoutClass   string
	DebugReason    string
	EndedAt        time.Time
}

// AttemptAccounting attaches usage/cost signals to a B-leg attempt.
type AttemptAccounting struct {
	BLegID                   string
	InputTokens              int64
	OutputTokens             int64
	CacheReadTokens          int64
	CacheWriteTokens         int64
	NonCachedInputTokens     int64
	ReasoningTokens          int64
	NonReasoningOutputTokens int64
	TotalTokens              int64
	CostNanoUnits            int64
	CostMinorUnits           int64
	Currency                 string
	CostSource               string
	RawUsageJSON             string
	BillingUnavailable       bool

	RequestStartedAt         time.Time
	FirstRemoteEventAt       time.Time
	FirstMeaningfulTokenAt   time.Time
	RemoteCompletedAt        time.Time
	ProxyCompletedAt         time.Time
	TTFTMillis               int64
	RemoteDurationMillis     int64
	CompletionDurationMillis int64
	// CompletionTPSMilli stores tokens-per-second with milliprecision: 5000 means 5.000 TPS.
	CompletionTPSMilli int64
}

// MergeAttemptAccounting overlays a usage/timing delta onto an existing attempt accounting row.
// Numeric token and cost counters accumulate; optional timing and descriptive fields keep the
// latest non-zero/non-empty value. A delta for a different B-leg replaces the base.
func MergeAttemptAccounting(base, delta AttemptAccounting) AttemptAccounting {
	if base.BLegID == "" {
		return delta
	}
	if delta.BLegID == "" {
		return base
	}
	if base.BLegID != delta.BLegID {
		return delta
	}
	base.InputTokens += delta.InputTokens
	base.OutputTokens += delta.OutputTokens
	base.CacheReadTokens += delta.CacheReadTokens
	base.CacheWriteTokens += delta.CacheWriteTokens
	base.NonCachedInputTokens += delta.NonCachedInputTokens
	base.ReasoningTokens += delta.ReasoningTokens
	base.NonReasoningOutputTokens += delta.NonReasoningOutputTokens
	base.TotalTokens = mergeAbsoluteTotal(base.TotalTokens, delta.TotalTokens)
	if delta.CostNanoUnits != 0 {
		base.CostNanoUnits += delta.CostNanoUnits
	}
	if delta.CostMinorUnits != 0 {
		base.CostMinorUnits += delta.CostMinorUnits
	}
	if delta.Currency != "" {
		base.Currency = delta.Currency
	}
	if delta.CostSource != "" {
		base.CostSource = delta.CostSource
	}
	if delta.RawUsageJSON != "" {
		base.RawUsageJSON = delta.RawUsageJSON
	}
	base.BillingUnavailable = base.BillingUnavailable || delta.BillingUnavailable
	if !delta.RequestStartedAt.IsZero() {
		base.RequestStartedAt = delta.RequestStartedAt
	}
	if !delta.FirstRemoteEventAt.IsZero() {
		base.FirstRemoteEventAt = delta.FirstRemoteEventAt
	}
	if !delta.FirstMeaningfulTokenAt.IsZero() {
		base.FirstMeaningfulTokenAt = delta.FirstMeaningfulTokenAt
	}
	if !delta.RemoteCompletedAt.IsZero() {
		base.RemoteCompletedAt = delta.RemoteCompletedAt
	}
	if !delta.ProxyCompletedAt.IsZero() {
		base.ProxyCompletedAt = delta.ProxyCompletedAt
	}
	if delta.TTFTMillis != 0 {
		base.TTFTMillis = delta.TTFTMillis
	}
	if delta.RemoteDurationMillis != 0 {
		base.RemoteDurationMillis = delta.RemoteDurationMillis
	}
	if delta.CompletionDurationMillis != 0 {
		base.CompletionDurationMillis = delta.CompletionDurationMillis
	}
	if delta.CompletionTPSMilli != 0 {
		base.CompletionTPSMilli = delta.CompletionTPSMilli
	}
	return base
}

func mergeAbsoluteTotal(cur, next int64) int64 {
	if next == 0 {
		return cur
	}
	return next
}

// AttemptEvidence joins trace, terminal outcome, and usage for operator diagnostics (Req 14.8).
type AttemptEvidence struct {
	Trace      AttemptTrace
	Outcome    AttemptOutcome
	Accounting AttemptAccounting
}

// Record is persisted secure-session state (no raw resume token).
type Record struct {
	SessionID               SessionID
	ResumeFingerprint       TokenFingerprint
	Owner                   PrincipalRef
	Workspace               WorkspaceRef
	ClientHints             ClientHints
	Policy                  PolicyMetadata
	ALegID                  string
	Status                  SessionStatus
	QuarantinedAt           time.Time
	QuarantineReasonCode    string
	QuarantineEventID       string
	ResumeEligible          bool
	LastActivityAt          time.Time
	LastActivitySource      ActivitySource
	CreatedAt               time.Time
	LatestAttemptTrace      AttemptTrace
	LatestAttemptOutcome    AttemptOutcome
	LatestAttemptAccounting AttemptAccounting
}

// CreateRecord is input for initial session persistence.
type CreateRecord struct {
	SessionID         SessionID
	ResumeFingerprint TokenFingerprint
	Owner             PrincipalRef
	Workspace         WorkspaceRef
	ClientHints       ClientHints
	Policy            PolicyMetadata
	ALegID            string
	ResumeEligible    bool
	CreatedAt         time.Time
}
