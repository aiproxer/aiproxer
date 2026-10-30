package snapshotgen

import (
	"fmt"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// PublishExecutable validates and publishes an executable generation.
// On validation failure the prior executable generation remains active (9.6).
func (p *Publisher) PublishExecutable(gen *ExecutableGeneration) (*ExecutableGeneration, error) {
	if p == nil {
		return nil, fmt.Errorf("snapshotgen: nil publisher")
	}
	if gen == nil {
		return nil, fmt.Errorf("snapshotgen: nil executable generation")
	}
	if err := gen.ValidateComplete(); err != nil {
		return p.CurrentExecutable(), err
	}
	if gen.PublishedAt.IsZero() {
		gen.PublishedAt = time.Now().UTC()
	}
	if gen.State == "" {
		gen.State = economics.SnapshotReady
	}
	gen.ID = p.seq.Add(1)
	p.executable.Store(gen)
	p.retainExecutable(gen)
	meta := RuntimeGeneration{
		ID:          gen.ID,
		PublishedAt: gen.PublishedAt,
		State:       gen.State,
		Reason:      gen.Reason,
		Usage:       gen.Usage,
		Concurrency: gen.Concurrency,
		Rating:      gen.Rating,
	}
	if prior := p.active.Load(); prior != nil {
		if meta.Usage.Version == "" && meta.Usage.State == "" {
			meta.Usage = prior.Usage
		}
		if meta.Concurrency.Version == "" && meta.Concurrency.State == "" {
			meta.Concurrency = prior.Concurrency
		}
		if meta.Rating.Version == "" && meta.Rating.State == "" {
			meta.Rating = prior.Rating
		}
	}
	p.active.Store(&meta)
	return gen, nil
}

// CurrentExecutable returns the active executable generation, if any.
func (p *Publisher) CurrentExecutable() *ExecutableGeneration {
	if p == nil {
		return nil
	}
	return p.executable.Load()
}

// LookupExecutable returns a retained generation by ID (in-flight / pending drain).
func (p *Publisher) LookupExecutable(id int64) *ExecutableGeneration {
	if p == nil || id <= 0 {
		return nil
	}
	if v, ok := p.retained.Load(id); ok {
		gen, ok := v.(*ExecutableGeneration)
		if !ok {
			return nil
		}
		return gen
	}
	return nil
}

// UnresolvedProviderIDs aggregates pending provider IDs across retained generations.
func (p *Publisher) UnresolvedProviderIDs() []string {
	if p == nil {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	p.retained.Range(func(_, value any) bool {
		gen, ok := value.(*ExecutableGeneration)
		if !ok || gen == nil {
			return true
		}
		for _, id := range gen.PendingProviderIDs() {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
		return true
	})
	return out
}

func (p *Publisher) retainExecutable(gen *ExecutableGeneration) {
	if p == nil || gen == nil {
		return
	}
	p.retained.Store(gen.ID, gen)
}

// CanRemoveProvider reports whether providerID may be removed across retained generations.
func (p *Publisher) CanRemoveProvider(providerID string) bool {
	if p == nil {
		return true
	}
	ok := true
	p.retained.Range(func(_, value any) bool {
		gen, typed := value.(*ExecutableGeneration)
		if !typed || gen == nil {
			return true
		}
		if !gen.CanRemoveProvider(providerID) {
			ok = false
			return false
		}
		return true
	})
	return ok
}

// ClearPendingProvider clears a pending provider ref on the named generation.
func (p *Publisher) ClearPendingProvider(generationID int64, providerID string) {
	if p == nil {
		return
	}
	if gen := p.LookupExecutable(generationID); gen != nil {
		gen.ClearPendingProvider(providerID)
	}
}

// AddPendingWork registers WorkID-keyed pending ownership on the named generation.
func (p *Publisher) AddPendingWork(generationID int64, workID, providerID string) bool {
	if p == nil {
		return false
	}
	gen := p.LookupExecutable(generationID)
	if gen == nil {
		return false
	}
	return gen.AddPendingWork(workID, providerID)
}

// ClearPendingWork clears WorkID-keyed pending ownership on the named generation.
func (p *Publisher) ClearPendingWork(generationID int64, workID string) {
	if p == nil {
		return
	}
	if gen := p.LookupExecutable(generationID); gen != nil {
		gen.ClearPendingWork(workID)
	}
}
