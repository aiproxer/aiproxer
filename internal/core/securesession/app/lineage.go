package app

import "context"

// LineageALeg is the minimal A-leg view the secure-session manager needs from B2BUA lineage.
type LineageALeg struct {
	ALegID                string
	ContinuityKey         string
	WeightedFirstConsumed bool
}

// LineageStore is an app-owned port over A-leg allocation and routing flags.
// Implementations live under securesession/adapters (e.g. b2bualineage).
type LineageStore interface {
	CreateALeg(ctx context.Context, continuityKey string) (LineageALeg, error)
	FetchALeg(ctx context.Context, aLegID string) (LineageALeg, error)
	SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error
}
