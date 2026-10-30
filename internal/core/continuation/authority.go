package continuation

import (
	"context"

	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// MemoryStore is a compatibility name for the SDK-owned continuation store.
// Core uses the public protocol-neutral implementation and adds no mutable
// continuation state of its own.
type MemoryStore = aipcont.MemoryStore

func NewMemoryStore() *MemoryStore { return aipcont.NewMemoryStore() }

func NewMemoryStoreWithLimits(limits aipcont.StorageLimits) *MemoryStore {
	return aipcont.NewMemoryStoreWithLimits(limits)
}

// StreamRecorder is a compatibility name for the SDK-owned recorder.
type StreamRecorder = aipcont.StreamRecorder

func NewStreamRecorder(recorder aipcont.Recorder, record aipcont.ContinuationRecord, cleanup func()) *StreamRecorder {
	return aipcont.NewStreamRecorder(recorder, record, cleanup)
}

// NewResponseID delegates ID generation to the SDK authority.
func NewResponseID(ctx context.Context) (aipcont.ResponseID, error) {
	return aipcont.NewResponseID(ctx)
}
