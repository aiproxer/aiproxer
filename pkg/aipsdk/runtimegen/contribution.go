package runtimegen

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// GenerationContribution is one provider-neutral input to an executable generation
// (requirements 9.1–9.2; design D4, D10).
type GenerationContribution struct {
	SourceID    string
	Version     string
	EffectiveAt time.Time
	State       economics.SnapshotState

	RequestRegistrations    []authority.RequestRegistration
	AttemptRegistrations    []authority.AttemptRegistration
	ConcurrencyRegistration *authority.ConcurrencyRegistration

	// MaxActiveRequests is the compiled concurrency enforcement limit for this
	// contribution when a concurrency registration is present (requirement 9.7).
	MaxActiveRequests int
}

// GenerationSource returns the next contribution for publication.
type GenerationSource interface {
	Contribution(ctx context.Context) (GenerationContribution, error)
}

// Validate checks identity, registrations, and required completeness for publication.
func (c GenerationContribution) Validate() error {
	if strings.TrimSpace(c.SourceID) == "" {
		return fmt.Errorf("runtimegen: source_id required")
	}
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("runtimegen: version required")
	}
	if c.State != "" && !c.State.IsKnown() {
		return fmt.Errorf("runtimegen: unknown state %q", c.State)
	}
	seen := map[string]struct{}{}
	for i, reg := range c.RequestRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("runtimegen: request registration[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, ok := seen[id]; ok {
			return fmt.Errorf("runtimegen: duplicate provider id %q", id)
		}
		seen[id] = struct{}{}
	}
	for i, reg := range c.AttemptRegistrations {
		if err := reg.Validate(); err != nil {
			return fmt.Errorf("runtimegen: attempt registration[%d]: %w", i, err)
		}
		id := strings.TrimSpace(reg.Descriptor.ID)
		if _, ok := seen[id]; ok {
			return fmt.Errorf("runtimegen: duplicate provider id %q", id)
		}
		seen[id] = struct{}{}
	}
	if c.ConcurrencyRegistration != nil {
		if err := c.ConcurrencyRegistration.Validate(); err != nil {
			return fmt.Errorf("runtimegen: concurrency registration: %w", err)
		}
		id := strings.TrimSpace(c.ConcurrencyRegistration.Descriptor.ID)
		if _, ok := seen[id]; ok {
			return fmt.Errorf("runtimegen: duplicate provider id %q", id)
		}
		if c.MaxActiveRequests <= 0 {
			return fmt.Errorf("runtimegen: max_active_requests required with concurrency registration")
		}
	}
	if c.ConcurrencyRegistration == nil && len(c.RequestRegistrations) == 0 &&
		len(c.AttemptRegistrations) == 0 {
		return fmt.Errorf("runtimegen: contribution has no executable registrations")
	}
	return nil
}

// StaticSource returns a fixed contribution (static YAML / config compilation result).
type StaticSource struct {
	Value GenerationContribution
}

// Contribution implements GenerationSource.
func (s StaticSource) Contribution(context.Context) (GenerationContribution, error) {
	return s.Value, nil
}
