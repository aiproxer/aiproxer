package diag

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// Store is the narrow read-only secure-session surface used by diagnostics HTTP
// handlers. Write/create/update ownership stays above HTTP composition.
type Store interface {
	Summary(ctx context.Context, query domain.SummaryQuery) ([]domain.Summary, error)
	LoadByID(ctx context.Context, id domain.SessionID) (domain.Record, error)
	LoadByALegID(ctx context.Context, aLegID string) (domain.Record, error)
	Transcript(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.TranscriptItem, error)
	Audit(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AuditItem, error)
	ListAttemptEvidence(ctx context.Context, id domain.SessionID, opts domain.ReadOptions) ([]domain.AttemptEvidence, error)
}

// SessionUsageRollup is an optional diagnostics extension for per-session token totals.
type SessionUsageRollup interface {
	UsageTokenTotals(ctx context.Context, id domain.SessionID) (inputTokens, outputTokens int64, err error)
}
