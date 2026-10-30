package conversationview

import (
	"math"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

func TestBootstrap_MemoryRevisionExhaustionDoesNotConsumeFirstSlot(t *testing.T) {
	t.Parallel()
	for _, slot := range []bool{false, true} {
		t.Run(map[bool]string{true: "slot", false: "revision"}[slot], func(t *testing.T) {
			a, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
			require.NoError(t, err)
			leg, err := a.CreateALeg(t.Context(), "")
			require.NoError(t, err)
			s := NewReferenceStore()
			require.NoError(t, s.CreateALeg(t.Context(), leg.ALegID))
			live := s.legs[leg.ALegID]
			if slot {
				live.nextSlot = math.MaxUint64 - 1
			} else {
				live.revision = math.MaxUint64 - 1
			}
			beforeRevision, beforeSlot := live.revision, live.nextSlot
			atomic := NewAuthorizedReferenceStore(s, a)
			decide := func() (BootstrapDecision, error) {
				batch := make([]PutSteeringRequest, 2)
				for i, id := range []string{"owner.one", "owner.two"} {
					batch[i] = PutSteeringRequest{OverlayID: id, Message: StoredMessageV1{Role: aipapi.RoleSystem, Text: "text"}, Placement: StoredPlacement{Kind: PlacementStablePrefix}, AnchorMissingPolicy: AnchorStablePrefixFallback, Reason: "bootstrap"}
				}
				return BootstrapDecision{Outcome: BootstrapMatched, Overlays: batch}, nil
			}
			out, err := atomic.BootstrapSteering(t.Context(), leg.ALegID, "owner", decide)
			require.ErrorIs(t, err, ErrRevisionExhausted)
			require.Empty(t, out.Mutations)
			require.Equal(t, beforeRevision, live.revision)
			require.Equal(t, beforeSlot, live.nextSlot)
			require.Empty(t, live.steering)
			require.Empty(t, live.bootstrap)
		})
	}
}
