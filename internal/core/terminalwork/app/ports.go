package app

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Clock is an injectable time source for claim/retry decisions.
type Clock interface {
	Now() time.Time
}

// WorkStore is the consumer-owned persistence port for processor mutations.
// Adapters (memory/SQLite/PostgreSQL) implement these workstore commands.
type WorkStore interface {
	ClaimDue(ctx context.Context, cmd terminalwork.ClaimDueCommand) ([]terminalwork.WorkRecord, error)
	RenewClaim(ctx context.Context, cmd terminalwork.RenewClaimCommand) error
	Complete(ctx context.Context, cmd terminalwork.CompleteCommand) error
	ScheduleRetry(ctx context.Context, cmd terminalwork.ScheduleRetryCommand) error
	Quarantine(ctx context.Context, cmd terminalwork.QuarantineCommand) error
}

// RecoveryStore is the composition-root terminal-work backing used for processor
// claims, durable intent accept, and bounded operator queries (task 4.5).
type RecoveryStore interface {
	WorkStore
	IntentStore
	QueryStore
}

// EffectProvider is a provider-neutral terminal-effect handler routed by stable ID.
type EffectProvider interface {
	ProviderID() string
	SupportedKinds() []sdk.WorkKind
	Version() string
	Invoke(ctx context.Context, rec terminalwork.WorkRecord, idempotencyKey string) error
}
