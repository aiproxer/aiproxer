// Package continuity defines the stable persistence contract for A-leg / B-leg continuity
// and attempt lineage used by documentation and optional external tooling.
//
// Store, ALegRecord, and BLegRecord are the public continuity contract; the core
// implementation in internal/core/b2bua implements this contract (same field names,
// types, and Store method set), not vice versa. Drift is caught by the boundary
// assertions in TestContinuityContract_* (internal/core/b2bua/store_contract_test.go),
// which compare both directions reflectively.
package continuity

import (
	"context"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ALegRecord is the logical client session row for routing and lineage.
type ALegRecord struct {
	ALegID                string
	ContinuityKey         string
	CreatedAt             time.Time
	LastSeenAt            time.Time
	WeightedFirstConsumed bool
}

// BLegRecord identifies one backend attempt slot within an A-leg.
type BLegRecord struct {
	BLegID string
	ALegID string
	Seq    int
}

// Store persists continuity and attempt lineage; internal/core/b2bua.Store implements
// this public contract (drift guard: TestContinuityContract_StoreInterfaceMatchesSDK).
type Store interface {
	ResolveALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	CreateALeg(ctx context.Context, continuityKey string) (ALegRecord, error)
	FetchALeg(ctx context.Context, aLegID string) (ALegRecord, error)
	SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error
	NextBLeg(ctx context.Context, aLegID string) (BLegRecord, error)
	RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error
	LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error)
}
