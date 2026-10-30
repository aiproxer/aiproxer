package conversationview_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/aiproxer/aiproxer/internal/infra/conversationview/storecontract"
)

func newReferenceDeps(t *testing.T) storecontract.Deps {
	t.Helper()
	s := conversationview.NewReferenceStore()
	return storecontract.Deps{
		Store: s,
		CreateALeg: func(ctx context.Context, aLegID string) error {
			return s.CreateALeg(ctx, aLegID)
		},
		DeleteALeg: func(ctx context.Context, aLegID string) error {
			return s.DeleteALeg(ctx, aLegID)
		},
		GetOverlay: func(ctx context.Context, aLegID, overlayID string) (conversationview.SteeringOverlay, error) {
			return s.GetOverlay(ctx, aLegID, overlayID)
		},
	}
}

func TestReferenceStoreContract(t *testing.T) {
	t.Parallel()
	storecontract.Run(t, storecontract.Env{
		New: func(t *testing.T) storecontract.Deps {
			t.Helper()
			return newReferenceDeps(t)
		},
		Spawn: func(fn func()) { go fn() },
	})
}
