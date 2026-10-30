package continuity

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipsdkc "github.com/aiproxer/aiproxer/pkg/aipsdk/continuity"
)

// SDKStore wraps a b2bua.Store as a pkg/aipsdk/continuity.Store for stable API boundaries.
func SDKStore(inner b2bua.Store) aipsdkc.Store {
	if inner == nil {
		return nil
	}
	return sdkStore{inner: inner}
}

type sdkStore struct {
	inner b2bua.Store
}

func toALeg(r b2bua.ALegRecord) aipsdkc.ALegRecord {
	return aipsdkc.ALegRecord{
		ALegID:                r.ALegID,
		ContinuityKey:         r.ContinuityKey,
		CreatedAt:             r.CreatedAt,
		LastSeenAt:            r.LastSeenAt,
		WeightedFirstConsumed: r.WeightedFirstConsumed,
	}
}

func toBLeg(r b2bua.BLegRecord) aipsdkc.BLegRecord {
	return aipsdkc.BLegRecord{BLegID: r.BLegID, ALegID: r.ALegID, Seq: r.Seq}
}

func (s sdkStore) ResolveALeg(ctx context.Context, continuityKey string) (aipsdkc.ALegRecord, error) {
	r, err := s.inner.ResolveALeg(ctx, continuityKey)
	if err != nil {
		return aipsdkc.ALegRecord{}, err
	}
	return toALeg(r), nil
}

func (s sdkStore) CreateALeg(ctx context.Context, continuityKey string) (aipsdkc.ALegRecord, error) {
	r, err := s.inner.CreateALeg(ctx, continuityKey)
	if err != nil {
		return aipsdkc.ALegRecord{}, err
	}
	return toALeg(r), nil
}

func (s sdkStore) FetchALeg(ctx context.Context, aLegID string) (aipsdkc.ALegRecord, error) {
	r, err := s.inner.FetchALeg(ctx, aLegID)
	if err != nil {
		return aipsdkc.ALegRecord{}, err
	}
	return toALeg(r), nil
}

func (s sdkStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	return s.inner.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

func (s sdkStore) NextBLeg(ctx context.Context, aLegID string) (aipsdkc.BLegRecord, error) {
	r, err := s.inner.NextBLeg(ctx, aLegID)
	if err != nil {
		return aipsdkc.BLegRecord{}, err
	}
	return toBLeg(r), nil
}

func (s sdkStore) RecordAttempt(ctx context.Context, rec aipapi.AttemptRecord) error {
	return s.inner.RecordAttempt(ctx, rec)
}

func (s sdkStore) LoadAttempts(ctx context.Context, aLegID string) ([]aipapi.AttemptRecord, error) {
	return s.inner.LoadAttempts(ctx, aLegID)
}

var _ aipsdkc.Store = sdkStore{}
