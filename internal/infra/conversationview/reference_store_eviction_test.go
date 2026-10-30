package conversationview_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/conversationview"
	"github.com/stretchr/testify/require"
)

func TestReferenceStore_BoundedEviction_PastMaxLegs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Now()
	store := conversationview.NewReferenceStoreWithClock(func() time.Time { return now })
	store.SetMaxLegs(2)

	require.NoError(t, store.CreateALeg(ctx, "aleg-1"))
	now = now.Add(time.Second)
	require.NoError(t, store.CreateALeg(ctx, "aleg-2"))
	now = now.Add(time.Second)
	require.NoError(t, store.CreateALeg(ctx, "aleg-3"))

	// aleg-1 must be evicted because maxLegs is 2 and aleg-1 is the oldest.
	_, err := store.Snapshot(ctx, "aleg-1")
	require.ErrorIs(t, err, conversationview.ErrALegNotFound, "oldest A-leg must be evicted past MaxLegs")
}
