package reasoningpreservation

import (
	"context"
	"time"
)

// compressionCleanupTimeout is the small hard/config-independent timeout for
// local store cleanup after the original is committed. It follows repo
// cleanup patterns (e.g., 2s authority cleanup) and is independent of
// CompressionConfig.Timeout which governs provider submission.
const compressionCleanupTimeout = 2 * time.Second

// compressionCleanupContext returns a bounded context for local cleanup that
// preserves trusted values from parent but ignores parent cancellation/deadline.
// It uses context.WithoutCancel to retain scope/genpin/values, then bounds
// with WithTimeout to avoid hanging on a blocked store. Caller must cancel.
func compressionCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), compressionCleanupTimeout)
}

// clearCompressionWithCleanup performs a best-effort ClearCompression using a
// detached cleanup context. It preserves CAS expectedReservationID so stale
// cleanup cannot delete a replacement reservation. Errors are intentionally
// ignored (fail-open, original retained) but the cleanup context is bounded
// so a blocking store cannot hang the observer.
func clearCompressionWithCleanup(parent context.Context, store CompressionStore, partition SessionPartition, artifactID, expectedReservationID string) {
	if store == nil || artifactID == "" {
		return
	}
	ctx, cancel := compressionCleanupContext(parent)
	defer cancel()
	_ = store.ClearCompression(ctx, partition, artifactID, expectedReservationID)
}
