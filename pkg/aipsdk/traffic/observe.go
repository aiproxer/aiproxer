package traffic

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// Leg identifies one hop in the four-leg observation model (design section 10).
type Leg string

const (
	LegCTP Leg = "client_to_proxy"
	LegPTB Leg = "proxy_to_backend"
	LegBTP Leg = "backend_to_proxy"
	LegPTC Leg = "proxy_to_client"
)

// Observation is the stable traffic observer contract (hexagonal task 5.2): only correlation and
// routing metadata plus a redacted or non-sensitive body snapshot. It must not carry transport
// types (for example *http.Request), provider SDK handles, executor-private structs, or raw
// privileged payloads unless policy explicitly places them on the redacted path. Callers of
// [Observer.OnObservation] run after [PortBundle.Emit] applies redactors; [Body] is the
// post-redaction bytes passed to the observer.
//
// Scope is optional safe principal/scope attribution (requirement 6.4). It is additive metadata
// only; existing observers may ignore it. Scope must never be injected into [Body]; [Body]
// remains the wire payload for the leg (requirements 7.1, 7.4).
type Observation struct {
	Leg         Leg
	TraceID     string
	ALegID      string
	BLegID      string
	PrincipalID string
	SessionID   string
	AttemptSeq  int
	BackendID   string
	FrontendID  string
	Protocol    string
	ContentType string
	Body        []byte
	Scope       scope.PrincipalScopeView
	RecordedAt  time.Time
}

// Observer receives non-mutating traffic observations (design section 10). Implementations should treat
// [Observation] as read-only data for logging, transcript, or metrics adapters; they must not
// mutate the slice backing [Observation.Body] in place if they retain it beyond the call.
type Observer interface {
	OnObservation(ctx context.Context, ev Observation) error
}

// NoopObserver drops observations (safe default for tests and early wiring).
type NoopObserver struct{}

func (NoopObserver) OnObservation(context.Context, Observation) error { return nil }

// CaptureMeta is correlation metadata for traffic legs: request trace, attempt lineage, principal
// and session identifiers, and route-facing backend/frontend labels. It intentionally excludes
// transport and provider concrete types (hexagonal task 5.2). Scope is optional safe attribution
// propagated to observers as metadata; it is never injected into payload bytes (req 6.4, 7.4).
type CaptureMeta struct {
	TraceID     string
	ALegID      string
	BLegID      string
	PrincipalID string
	SessionID   string
	AttemptSeq  int
	BackendID   string
	FrontendID  string
	Scope       scope.PrincipalScopeView
}

// RawCaptureSink receives verbatim bytes for privileged capture paths (design §10), using the
// same [CaptureMeta] correlation fields as structured observers. It is not a general byte sink
// for arbitrary adapter internals.
type RawCaptureSink interface {
	WriteRaw(ctx context.Context, leg Leg, meta CaptureMeta, payload []byte) error
}

// DisabledRawCapture rejects raw capture until explicitly granted by core policy.
type DisabledRawCapture struct{}

func (DisabledRawCapture) WriteRaw(context.Context, Leg, CaptureMeta, []byte) error {
	return ErrNotConfigured
}
