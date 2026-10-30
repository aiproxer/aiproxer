package observers_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	"github.com/stretchr/testify/require"
)

func TestB2BUADecorator_ForwardsActualAllocationAuthority(t *testing.T) {
	t.Parallel()
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	require.NoError(t, err)
	leg, err := s.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	d := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{Delegate: s})
	require.NoError(t, d.WithBLegAllocationAuthority(t.Context(), leg.ALegID, func(has bool) error { require.False(t, has); return nil }))
	_, err = d.NextBLeg(t.Context(), leg.ALegID)
	require.NoError(t, err)
	require.NoError(t, d.WithBLegAllocationAuthority(t.Context(), leg.ALegID, func(has bool) error { require.True(t, has); return nil }))
	unsupported := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{Delegate: &fakeB2BUAStore{}})
	err = unsupported.WithBLegAllocationAuthority(t.Context(), leg.ALegID, func(bool) error { t.Error("unsupported callback invoked"); return nil })
	require.ErrorIs(t, err, b2bua.ErrBLegAllocationAuthorityUnsupported)
}
