package conversationview_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	cv "github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/stretchr/testify/require"
)

func TestBootstrap_SharedCapacityAndOrphanCollisionsRollback(t *testing.T) {
	t.Parallel()
	for _, memory := range []bool{true, false} {
		for _, mode := range []string{"count", "total", "collision"} {
			t.Run(fmt.Sprintf("memory=%v/%s", memory, mode), func(t *testing.T) {
				var store cv.Store
				var atomic cv.BootstrapStore
				id := "bootstrap-capacity"
				if memory {
					a, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
					require.NoError(t, err)
					leg, err := a.CreateALeg(t.Context(), "")
					require.NoError(t, err)
					id = leg.ALegID
					s := cv.NewReferenceStore()
					require.NoError(t, s.CreateALeg(t.Context(), id))
					store, atomic = s, cv.NewAuthorizedReferenceStore(s, a)
				} else {
					s, cleanup := newTestBunStore(t)
					t.Cleanup(cleanup)
					require.NoError(t, s.CreateALeg(t.Context(), id))
					store, atomic = s, s
				}
				n, text := 63, "existing"
				if mode == "total" {
					n, text = 3, strings.Repeat("x", cv.MaxSteeringTextBytes)
				}
				if mode == "collision" {
					n = 1
				}
				for i := range n {
					overlayID := fmt.Sprintf("other.%d", i)
					if mode == "collision" {
						overlayID = "owner.orphan"
					}
					_, err := store.PutSteering(t.Context(), id, bootstrapPut(overlayID, text))
					require.NoError(t, err)
				}
				before, err := store.Snapshot(t.Context(), id)
				require.NoError(t, err)
				batch := []cv.PutSteeringRequest{bootstrapPut("owner.one", "one"), bootstrapPut("owner.two", "two")}
				if mode == "total" {
					batch[0].Message.Text = strings.Repeat("y", cv.MaxSteeringTextBytes)
					batch[1].Message.Text = "overflow"
				}
				expected := cv.ErrSteeringLimitExceeded
				if mode == "collision" {
					batch = nil
					expected = cv.ErrBootstrapCollision
				}
				outcome := cv.BootstrapMatched
				if batch == nil {
					outcome = cv.BootstrapNoMatch
				}
				out, err := atomic.BootstrapSteering(t.Context(), id, "owner", func() (cv.BootstrapDecision, error) {
					return cv.BootstrapDecision{Outcome: outcome, Overlays: batch}, nil
				})
				require.ErrorIs(t, err, expected)
				require.Empty(t, out.Mutations)
				after, err := store.Snapshot(t.Context(), id)
				require.NoError(t, err)
				require.Equal(t, before, after)
				if mode == "collision" {
					return
				}
				_, err = store.DeactivateSteering(t.Context(), id, "other.0")
				require.NoError(t, err)
				out, err = atomic.BootstrapSteering(t.Context(), id, "owner", func() (cv.BootstrapDecision, error) {
					return cv.BootstrapDecision{Outcome: cv.BootstrapMatched, Overlays: batch}, nil
				})
				require.NoError(t, err)
				require.False(t, out.Reused)
				require.EqualValues(t, n+1, out.Mutations[0].SlotOrdinal)
				require.EqualValues(t, n+2, out.Mutations[0].StateRevision)
			})
		}
	}
}
