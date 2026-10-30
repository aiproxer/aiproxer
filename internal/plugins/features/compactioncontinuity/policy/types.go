// Package policy resolves the continuity egress policy at request time.
//
// The resolver has no canonical Call input by design. Only proxy-owned typed
// execution context may supply a session override; client headers, session
// hints, extensions, and prompt content are not policy authority.
package policy

import "time"

// Categories is the allowlist of semantic continuity categories.
type Categories struct {
	Plan                 bool
	UserDecisions        bool
	Constraints          bool
	Rationale            bool
	RejectedAlternatives bool
}

// CategoryPatch preserves omitted category values when labels provide only a
// subset of approved session controls.
type CategoryPatch struct {
	Plan                 *bool
	UserDecisions        *bool
	Constraints          *bool
	Rationale            *bool
	RejectedAlternatives *bool
}

// Extractor is the route and semantic child budget selected for one request.
// It intentionally carries no branch, account, session, or prompt identifiers.
type Extractor struct {
	Route           string
	Inherit         bool
	Timeout         time.Duration
	MaxInputTokens  int
	MaxOutputTokens int
}

// Limits contains the remaining bounded continuity resources.
type Limits struct {
	Timeout          time.Duration
	MaxInputTokens   int
	MaxOutputTokens  int
	BarrierTimeout   time.Duration
	CapsuleMaxTokens int
	CapsuleMaxBytes  int
	SourceMaxBytes   int
	ResultMaxBytes   int
	ResultMaxCount   int
}

// Defaults are immutable global feature defaults for one generation.
type Defaults struct {
	Enabled   bool
	Preserve  Categories
	Extractor Extractor
	Limits    Limits
}

// HardMaxima are operator safety limits. Enabled=false is a hard global
// disable. A non-empty ApprovedRoutes list is the only route allowlist for
// trusted per-session route replacement.
type HardMaxima struct {
	Enabled             bool
	Preserve            Categories
	ApprovedRoutes      []string
	AllowTranscriptRead bool
	AllowInherit        bool
	Limits              Limits
}

// LimitOverride contains optional trusted tighter bounds. A session value is
// accepted only when it is positive and no greater than both the global value
// and the operator maximum.
type LimitOverride struct {
	Timeout          *time.Duration
	MaxInputTokens   *int
	MaxOutputTokens  *int
	BarrierTimeout   *time.Duration
	CapsuleMaxTokens *int
	CapsuleMaxBytes  *int
	SourceMaxBytes   *int
	ResultMaxBytes   *int
	ResultMaxCount   *int
}

// Override is the explicitly approved, proxy-owned per-session control set.
// Preserve is a complete category value when non-nil; omitted categories are
// not guessed from client data.
type Override struct {
	Enabled       *bool
	Preserve      *Categories
	PreservePatch *CategoryPatch
	Route         string
	RouteSet      bool
	Inherit       *bool
	Limits        LimitOverride

	// routeApproved is set only by the proxy-owned context constructor or the
	// reserved trusted session label parser; it is intentionally not a wire
	// field callers can deserialize.
	routeApproved bool
}

// TranscriptAuthorization is the scope retained for an optional authorized
// transcript read. It must not be passed to the extractor prompt.
type TranscriptAuthorization struct {
	SessionID   string
	PrincipalID string
	TenantID    string
	WorkspaceID string
}

// Effective is the request-local policy after precedence and hard bounds.
type Effective struct {
	Enabled        bool
	Preserve       Categories
	Extractor      Extractor
	Limits         Limits
	TrustedSession bool
	TranscriptRead bool
}
