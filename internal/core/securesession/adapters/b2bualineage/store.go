// Package b2bualineage implements [app.LineageStore] over [b2bua.Store].
package b2bualineage

import (
	"context"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
)

// Store wraps a [b2bua.Store] as [app.LineageStore].
type Store struct {
	S b2bua.Store
}

var _ app.LineageStore = (*Store)(nil)

// New returns an adapter or nil if s is nil.
func New(s b2bua.Store) *Store {
	if s == nil {
		return nil
	}
	return &Store{S: s}
}

func (a Store) CreateALeg(ctx context.Context, continuityKey string) (app.LineageALeg, error) {
	r, err := a.S.CreateALeg(ctx, continuityKey)
	if err != nil {
		return app.LineageALeg{}, err
	}
	return fromB2BUA(r), nil
}

func (a Store) FetchALeg(ctx context.Context, aLegID string) (app.LineageALeg, error) {
	r, err := a.S.FetchALeg(ctx, aLegID)
	if err != nil {
		return app.LineageALeg{}, err
	}
	return fromB2BUA(r), nil
}

func (a Store) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	return a.S.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

func fromB2BUA(r b2bua.ALegRecord) app.LineageALeg {
	return app.LineageALeg{
		ALegID:                r.ALegID,
		ContinuityKey:         r.ContinuityKey,
		WeightedFirstConsumed: r.WeightedFirstConsumed,
	}
}
