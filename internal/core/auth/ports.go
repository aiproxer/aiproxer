package auth

import (
	"context"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
)

// OSIdentitySnapshot is non-secret principal material resolved from the OS or explicit
// environment hints. FallbackUsed is true when neither OS account nor env hints yielded
// a stable identity (operator-visible).
type OSIdentitySnapshot struct {
	PrincipalID  string
	DisplayName  string
	FallbackUsed bool
}

// OSIdentityProvider resolves the current process identity for local no-op authentication.
// Implementations live in infrastructure (e.g. os/user + env); core consumes this port only.
type OSIdentityProvider interface {
	Current(ctx context.Context) (OSIdentitySnapshot, error)
}

// LocalUnknownOSPrincipalID is the stable principal id when OS identity cannot be resolved
// (must match infra osidentity fallback for operator-visible consistency).
const LocalUnknownOSPrincipalID = "aip_local_unknown"

// Authenticator performs local auth (no-op, API key) using protocol-neutral metadata.
type Authenticator interface {
	Authenticate(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error)
}

// RemoteDecider is the consumer-side port for delegated (remote) auth. Implementations
// are wired at the composition root; this package does not include transport.
type RemoteDecider interface {
	Decide(ctx context.Context, req sdkauth.InboundCallMeta) (sdkauth.Decision, error)
}

// EventFailurePolicy controls whether event sink errors fail the request path.
type EventFailurePolicy string

const (
	EventFailureBestEffort EventFailurePolicy = "best_effort"
	EventFailureFailClosed EventFailurePolicy = "fail_closed"
)

// EventSink receives non-secret auth and session events. Implementations are wired at the
// composition root (e.g. structured logging). [EventDispatcher] serializes calls per dispatcher
// instance; sinks should still avoid long blocking work because delivery remains on request paths.
// OnAuthDecision: do not treat [sdkauth.AuthDecisionEvent]
// fields as proof of absence of secrets in upstream state; log only stable, operator-approved
// attributes (the default JSON sink logs [sdkauth.AuthDecisionEvent.PrincipalSafeClaims] keys only,
// not map values). New fields on event DTOs require explicit non-secret data classification before use;
// [EventDispatcher.DispatchAuthDecision] sanitizes [sdkauth.AuthDecisionEvent.ChallengeSummary] for
// every sink including custom implementations.
type EventSink interface {
	OnAuthDecision(ctx context.Context, ev sdkauth.AuthDecisionEvent) error
	OnSessionStart(ctx context.Context, ev sdkauth.SessionStartEvent) error
}
