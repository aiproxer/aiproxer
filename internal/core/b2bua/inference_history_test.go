package b2bua

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryStore_AllocationAuthoritySerializesNextBLeg(t *testing.T) {
	t.Parallel()
	s, err := NewMemoryStore(MemoryStoreOptions{})
	require.NoError(t, err)
	leg, err := s.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	require.NoError(t, s.WithBLegAllocationAuthority(t.Context(), leg.ALegID, func(has bool) error {
		require.False(t, has)
		// The contract explicitly requires the very mutex used by NextBLeg,
		// not a second lock. TryLock proves that authority deterministically.
		if s.mu.TryLock() {
			s.mu.Unlock()
			t.Error("NextBLeg mutex not held")
		}
		return nil
	}))
	_, err = s.NextBLeg(t.Context(), leg.ALegID)
	require.NoError(t, err)
	attempts, err := s.LoadAttempts(t.Context(), leg.ALegID)
	require.NoError(t, err)
	require.Empty(t, attempts)
	require.NoError(t, s.WithBLegAllocationAuthority(t.Context(), leg.ALegID, func(has bool) error { require.True(t, has); return nil }))
}

func TestMemoryStore_ExpiredAuthorityNotifiesAfterUnlock(t *testing.T) {
	t.Parallel()
	now := time.Unix(1, 0)
	s, err := NewMemoryStore(MemoryStoreOptions{TTL: time.Second, Now: func() time.Time { return now }})
	require.NoError(t, err)
	leg, err := s.CreateALeg(t.Context(), "")
	require.NoError(t, err)
	retired := false
	s.SetALegRetirementObserver(func(id string) {
		_, e := s.FetchALeg(context.Background(), id)
		require.ErrorIs(t, e, ErrALegNotFound)
		retired = true
	})
	now = now.Add(time.Second)
	err = s.WithBLegAllocationAuthority(t.Context(), leg.ALegID, func(bool) error { t.Fatal("expired callback invoked"); return nil })
	require.ErrorIs(t, err, ErrALegNotFound)
	require.True(t, retired)
}
