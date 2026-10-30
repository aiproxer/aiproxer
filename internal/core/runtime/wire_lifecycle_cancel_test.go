package runtime

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

type panicCancelStream struct {
	aipapi.EventStream
}

func (p *panicCancelStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	panic("inner stream cancel exploded")
}

func TestWireLifecycleEventStream_Cancel_CleanupRunsOnPanic(t *testing.T) {
	t.Parallel()

	var cleanedUp atomic.Bool
	wles := &wireLifecycleEventStream{
		EventStream: &panicCancelStream{EventStream: aipapi.NewFixedEventStream(nil)},
		cleanup: func(err error) {
			cleanedUp.Store(true)
		},
	}

	require.PanicsWithValue(t, "inner stream cancel exploded", func() {
		wles.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	})

	require.True(t, cleanedUp.Load(), "cleanup callback must run even if inner ManagedEventStream.Cancel panics")
}
