package configsource

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/core/config"
)

// Source adapts validated concurrency config into an immutable rule snapshot.
type Source struct {
	snap app.RuleSnapshot
}

// New constructs a config-backed concurrency rule source.
func New(cfg config.ConcurrencyAuthorityConfig) (*Source, error) {
	rules, err := cfg.DomainRules()
	if err != nil {
		return nil, err
	}
	ready := domain.Readiness{State: domain.ReadinessStateReady}
	if !cfg.Enabled {
		ready = domain.Readiness{State: domain.ReadinessStateDisabled}
	}
	now := time.Now().UTC()
	version := strings.TrimSpace(cfg.SnapshotVersion)
	if version == "" {
		version = "static"
	}
	return &Source{snap: app.RuleSnapshot{
		ID:          "concurrency",
		Version:     version,
		EffectiveAt: now,
		FetchedAt:   now,
		State:       app.SnapshotStateFromReadiness(ready),
		Readiness:   ready,
		Rules:       cloneRules(rules),
	}}, nil
}

// Snapshot returns a deep copy of the stored rule snapshot.
func (s *Source) Snapshot(ctx context.Context) (app.RuleSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return app.RuleSnapshot{}, err
	}
	if s == nil {
		ready := domain.Readiness{State: domain.ReadinessStateDisabled}
		return app.RuleSnapshot{
			ID:        "concurrency",
			State:     app.SnapshotStateFromReadiness(ready),
			Readiness: ready,
		}, nil
	}
	out := s.snap
	out.Rules = cloneRules(s.snap.Rules)
	return out, nil
}

func cloneRules(in []domain.Rule) []domain.Rule {
	if len(in) == 0 {
		return nil
	}
	out := make([]domain.Rule, len(in))
	copy(out, in)
	for i := range out {
		if len(in[i].Match.Labels) == 0 {
			continue
		}
		out[i].Match.Labels = make(map[string]domain.DimensionMatcher, len(in[i].Match.Labels))
		maps.Copy(out[i].Match.Labels, in[i].Match.Labels)
	}
	return out
}

var _ app.RuleSource = (*Source)(nil)
