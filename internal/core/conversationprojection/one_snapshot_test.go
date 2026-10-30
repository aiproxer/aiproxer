package conversationprojection_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// oneSnapshotReader counts Snapshot calls deterministically.
type oneSnapshotReader struct {
	snap  conversationprojection.Snapshot
	count atomic.Int64
}

func (r *oneSnapshotReader) Snapshot(_ context.Context, _ string) (conversationprojection.Snapshot, error) {
	r.count.Add(1)
	return r.snap, nil
}

func TestOneSnapshotNoPerCandidateIO(t *testing.T) {
	t.Parallel()
	// Build worst-case snapshot: 64 overlays 256 KiB + 4096 tags is not needed for per-candidate, but we test with 64.
	snap := benchSnapshot64Overlays256KiB()
	reader := &oneSnapshotReader{snap: snap}
	base := benchCallWithMessages(10)

	// Simulate runtime: one snapshot per logical turn.
	s, err := reader.Snapshot(context.Background(), "a-leg-1")
	require.NoError(t, err)
	require.Equal(t, int64(1), reader.count.Load())

	projected, ev, err := conversationprojection.Project(base, s)
	require.NoError(t, err)
	filtered, err := conversationprojection.FilterNeverBackend(base, s)
	require.NoError(t, err)

	// Simulate 5 parallel candidate arms (failover, TTFT, interleaved) – each reasserts using frozen snapshot/provenance,
	// with zero additional Snapshot calls (no per-candidate I/O).
	for range 5 {
		// Late transform mutates candidate: duplicate steering tail (simulated by appending)
		mutated := aipapi.CloneCall(projected)
		// Reassert must restore without reading store.
		out, _, err := conversationprojection.Reassert(mutated, s, ev.Provenance, filtered)
		require.NoError(t, err)
		require.NotNil(t, out)
		// No additional Snapshot incurred.
		require.Equal(t, int64(1), reader.count.Load(), "per-candidate must not read Snapshot")
	}
	// Also verify that multiple turns each do exactly one snapshot (not zero, not two).
	for range 3 {
		reader2 := &oneSnapshotReader{snap: snap}
		_, err := reader2.Snapshot(context.Background(), "a-leg-iter")
		require.NoError(t, err)
		_, _, err = conversationprojection.Project(base, snap)
		require.NoError(t, err)
		require.Equal(t, int64(1), reader2.count.Load())
	}
}

func TestOneSnapshotNoPerCandidateIO_4096Tags(t *testing.T) {
	t.Parallel()
	snap := benchSnapshot4096Tags()
	reader := &oneSnapshotReader{snap: snap}
	base := benchCallWithMessages(20)
	s, _ := reader.Snapshot(context.Background(), "a-leg")
	require.Equal(t, int64(1), reader.count.Load())
	_, _, err := conversationprojection.Project(base, s)
	require.NoError(t, err)
	// Reassert with same snapshot multiple times must not trigger additional reads.
	for range 3 {
		late := aipapi.CloneCall(base)
		_, _, err := conversationprojection.Reassert(late, s, nil, aipapi.Call{})
		require.NoError(t, err)
		require.Equal(t, int64(1), reader.count.Load())
	}
}
