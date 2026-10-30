package runtimebundle

import (
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/runtimegen"
)

// CompileGenerationContribution maps static config + production registrations
// into the public GenerationContribution shape (requirement 11.6; design D15).
func CompileGenerationContribution(cfg *config.Config, prod ProductionOptions, now time.Time) runtimegen.GenerationContribution {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	version := "static"
	if cfg != nil {
		if v := strings.TrimSpace(cfg.Accounting.Concurrency.SnapshotVersion); v != "" {
			version = v
		} else if v := strings.TrimSpace(cfg.Accounting.Authority.SnapshotVersion); v != "" {
			version = v
		}
	}
	contrib := runtimegen.GenerationContribution{
		SourceID:                "static-config",
		Version:                 version,
		EffectiveAt:             now,
		State:                   economics.SnapshotReady,
		RequestRegistrations:    append([]authority.RequestRegistration(nil), prod.RequestRegistrations...),
		AttemptRegistrations:    append([]authority.AttemptRegistration(nil), prod.AttemptRegistrations...),
		ConcurrencyRegistration: prod.ConcurrencyRegistration,
		MaxActiveRequests:       maxActiveFromConfig(cfg),
	}
	return contrib
}

func maxActiveFromConfig(cfg *config.Config) int {
	if cfg == nil || !cfg.Accounting.Concurrency.Enabled {
		return 0
	}
	maxActive := 0
	for _, rule := range cfg.Accounting.Concurrency.Rules {
		if rule.MaxActiveRequests > maxActive {
			maxActive = rule.MaxActiveRequests
		}
	}
	return maxActive
}

// PublishExecutableFromProduction compiles and publishes when contribution is non-empty.
// Failed compile/publish leaves the prior executable generation active.
func PublishExecutableFromProduction(pub *snapshotgen.Publisher, cfg *config.Config, prod ProductionOptions, now time.Time) (*snapshotgen.ExecutableGeneration, error) {
	if pub == nil {
		return nil, fmt.Errorf("runtimebundle: nil snapshot publisher")
	}
	contrib := CompileGenerationContribution(cfg, prod, now)
	if err := contrib.Validate(); err != nil {
		return pub.CurrentExecutable(), err
	}
	gen, err := snapshotgen.CompileExecutable(contrib)
	if err != nil {
		return pub.CurrentExecutable(), err
	}
	if err := validateProviderRemovals(pub, gen); err != nil {
		return pub.CurrentExecutable(), err
	}
	if prior := pub.Current(); prior != nil {
		// Copy source-fetch metadata planes for compatibility views only.
		// Executable State remains the contribution/enforcement posture (9.6, 12.1).
		gen.Usage = prior.Usage
		gen.Concurrency = prior.Concurrency
		gen.Rating = prior.Rating
	}
	return pub.PublishExecutable(gen)
}

func validateProviderRemovals(pub *snapshotgen.Publisher, next *snapshotgen.ExecutableGeneration) error {
	prior := pub.CurrentExecutable()
	if prior == nil || next == nil {
		return nil
	}
	nextIDs := providerIDSet(next)
	for _, id := range providerIDs(prior) {
		if _, ok := nextIDs[id]; ok {
			continue
		}
		if !pub.CanRemoveProvider(id) {
			return fmt.Errorf("runtimebundle: cannot remove provider %q while live or pending refs remain", id)
		}
	}
	return nil
}

func providerIDs(g *snapshotgen.ExecutableGeneration) []string {
	if g == nil {
		return nil
	}
	out := make([]string, 0, len(g.RequestRegistrations)+len(g.AttemptRegistrations)+1)
	seen := map[string]struct{}{}
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if g.ConcurrencyRegistration != nil {
		add(g.ConcurrencyRegistration.Descriptor.ID)
	}
	for _, reg := range g.RequestRegistrations {
		add(reg.Descriptor.ID)
	}
	for _, reg := range g.AttemptRegistrations {
		add(reg.Descriptor.ID)
	}
	return out
}

func providerIDSet(g *snapshotgen.ExecutableGeneration) map[string]struct{} {
	out := map[string]struct{}{}
	for _, id := range providerIDs(g) {
		out[id] = struct{}{}
	}
	return out
}
