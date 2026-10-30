package secretguard

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// Outcome is the secret-guard decision for one Evaluate invocation.
type Outcome string

const (
	OutcomePass     Outcome = "pass"
	OutcomeLog      Outcome = "log"
	OutcomeRedacted Outcome = "redacted"
	OutcomeBlock    Outcome = "block"
)

// SourceCategory classifies where a matched secret reference originated.
type SourceCategory string

const (
	SourceCategoryProxyEnv    SourceCategory = "proxy_env"
	SourceCategoryPopularEnv  SourceCategory = "popular_env"
	SourceCategoryOperatorEnv SourceCategory = "operator_env"
	SourceCategoryRequestCred SourceCategory = "request_credential"
	SourceCategoryUnknown     SourceCategory = "unknown"
)

// Detector IDs are closed because they are emitted to operator-facing
// diagnostics and audit sinks. Empty is retained for compatibility with
// existing exact-match producers that predate detector provenance.
const (
	DetectorIDExact       = "exact"
	DetectorIDBetterLeaks = "betterleaks"
	// FailureKindDetectorFailure is a bounded decision classifier for a
	// detector scan error or finding-cap exhaustion. It is valid on log
	// decisions, which preserve the request while recording safe failure
	// metadata.
	FailureKindDetectorFailure = "detector_failure"
	// FailureKindUnrewritableDetectedSecret is returned when redact discovers a
	// secret whose original representation cannot be safely rewritten.
	FailureKindUnrewritableDetectedSecret = "unrewritable_detected_secret"
)

// Confidence values are the only confidence bands that may cross the SDK
// boundary. Empty means that the detector did not provide confidence metadata.
const (
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// Finding is safe match metadata. It must never carry a secret value or content excerpt.
type Finding struct {
	SecretRefName   string
	Aliases         []string
	SourceCategory  SourceCategory
	Location        string
	OccurrenceCount int
	// DetectorID identifies the closed detector vocabulary that produced this
	// finding. It may be empty for legacy exact findings.
	DetectorID string
	// RuleID is a bounded detector rule identifier. BetterLeaks findings must
	// carry the identifier resolved from the pinned detector inventory.
	RuleID string
	// Confidence is low, medium, high, or empty when unavailable.
	Confidence string
}

// PositionalOccurrence is safe positional attribution for one exact match.
// Start and End are byte offsets into the input passed to ScanOccurrences and
// describe the half-open span input[Start:End], with
// 0 <= Start < End <= len(input). Finding.OccurrenceCount is 1; DetectorID may
// be empty for legacy exact matchers. Implementations must not expose secret
// bytes, hashes, or detector objects.
type PositionalOccurrence struct {
	Start   int
	End     int
	Finding Finding
}

// PositionalMatcher is an optional Matcher capability. Feature-owned code may
// use it to deduplicate detector reports by admitted-content span while the
// Matcher contract remains safe-finding based. Each returned occurrence refers
// to the supplied input bytes; positions must not refer to another buffer.
type PositionalMatcher interface {
	Matcher
	ScanOccurrences(input []byte) []PositionalOccurrence
}

// Decision is the Evaluate result: outcome, safe findings, and scan metadata.
type Decision struct {
	Outcome       Outcome
	Findings      []Finding
	MutationCount int
	ScanLimitHit  bool
	// FailureKind is a bounded operator-safe classifier (e.g. scan_limit, provider_error).
	FailureKind string
	// FailureReason is a bounded operator-safe reason; never secret material or excerpts.
	FailureReason string
}

// Meta carries authoritative request context and sanitized ingress attribution.
// It must not include bearer tokens, resume tokens, or raw secret values.
type Meta struct {
	TraceID string

	Principal execview.PrincipalView
	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace workspace.WorkspaceView

	PeerIP              string
	FrontendID          string
	Operation           string
	UserAgentDigest     string
	AgentIdentityDigest string
	DeviceID            string
	KeyID               string
	Fingerprint         string
}

// Services are opaque capabilities available to a Guard. No raw secret accessor is provided.
type Services struct {
	MatcherResolver MatcherResolver
	// Capability is an opaque generation-frozen feature service. Generic guards
	// and runtime do not interpret its concrete value; the owning feature may
	// type-assert it inside its private boundary.
	Capability any
}

// MatcherResolver resolves the request-scoped opaque Matcher. Implementations own secret bytes privately.
type MatcherResolver interface {
	Resolve(ctx context.Context) (Matcher, error)
}

// Matcher scans and redacts textual inputs, returning only safe findings.
type Matcher interface {
	ScanBytes(ctx context.Context, input []byte) ([]Finding, error)
	ScanString(ctx context.Context, input string) ([]Finding, error)
	RedactBytes(ctx context.Context, input []byte) (redacted []byte, findings []Finding, err error)
	RedactString(ctx context.Context, input string) (redacted string, findings []Finding, err error)
}

// Guard evaluates one secrets-guard feature instance against a canonical call.
type Guard interface {
	ID() string
	Order() int
	FailureMode() FailureMode
	Evaluate(ctx context.Context, call *aipapi.Call, meta Meta, services Services) (Decision, error)
}
