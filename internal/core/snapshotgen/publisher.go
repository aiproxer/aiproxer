package snapshotgen

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// RuntimeGeneration is one immutable published set of usage, concurrency, and
// rating snapshots (design: Publication). Metadata views remain for compatibility;
// enforcement uses ExecutableGeneration (D10).
type RuntimeGeneration struct {
	ID          int64
	PublishedAt time.Time
	State       economics.SnapshotState
	Reason      string
	Usage       economics.Snapshot[economics.PolicyRulesView]
	Concurrency economics.Snapshot[economics.PolicyRulesView]
	Rating      economics.Snapshot[economics.RatingCatalogView]
}

// Publisher atomically publishes immutable generations for request binding.
type Publisher struct {
	active     atomic.Pointer[RuntimeGeneration]
	executable atomic.Pointer[ExecutableGeneration]
	retained   sync.Map // int64 -> *ExecutableGeneration
	seq        atomic.Int64
}

// NewPublisher returns an empty publisher (Current is nil until Publish).
func NewPublisher() *Publisher {
	return &Publisher{}
}

// Current returns the active generation pointer, or nil when none published.
func (p *Publisher) Current() *RuntimeGeneration {
	if p == nil {
		return nil
	}
	return p.active.Load()
}

// PublishMetadata stores gen as the active metadata compatibility view with a
// new monotonic ID (requirement 11.2). Callers must treat returned generations
// as immutable. Enforcement uses PublishExecutable (D10).
func (p *Publisher) PublishMetadata(gen RuntimeGeneration) *RuntimeGeneration {
	if p == nil {
		return nil
	}
	if gen.PublishedAt.IsZero() {
		gen.PublishedAt = time.Now().UTC()
	}
	if gen.State == "" {
		gen.State = economics.SnapshotReady
	}
	gen.ID = p.seq.Add(1)
	cp := gen
	p.active.Store(&cp)
	return &cp
}

// Publish stores gen as the active metadata compatibility view with a new
// monotonic ID. Callers must treat returned generations as immutable.
//
// Deprecated: metadata-only publication is not an enforcement path (D10).
// Use PublishExecutable for admission/settlement evaluator objects, or
// PublishMetadata for additive source-fetch compatibility views (requirement 11.2).
func (p *Publisher) Publish(gen RuntimeGeneration) *RuntimeGeneration {
	return p.PublishMetadata(gen)
}

// MarkUnusable updates readiness of the active generation without replacing
// snapshot Values with an unrelated version (requirements 11.3, 11.7).
// Unknown states are rejected and the prior generation is preserved.
func (p *Publisher) MarkUnusable(state economics.SnapshotState, reason string) *RuntimeGeneration {
	if p == nil {
		return nil
	}
	if !state.IsKnown() || state == economics.SnapshotReady {
		return p.Current()
	}
	cur := p.active.Load()
	if cur == nil {
		gen := RuntimeGeneration{
			State:  state,
			Reason: reason,
		}
		return p.Publish(gen)
	}
	next := *cur
	next.State = state
	next.Reason = reason
	next.PublishedAt = time.Now().UTC()
	// Preserve Value identities (ID/Version) on Usage/Concurrency/Rating.
	next.ID = p.seq.Add(1)
	p.active.Store(&next)
	return &next
}
