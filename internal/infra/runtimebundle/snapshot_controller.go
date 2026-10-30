package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/infra/snapshotsource"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// DefaultSnapshotRefreshTimeout bounds injectable source reads during Refresh.
const DefaultSnapshotRefreshTimeout = 5 * time.Second

// SnapshotController retains injectable snapshot sources and publishes immutable
// generations through a Publisher (requirements 11.3, 11.6, 11.7).
//
// Refresh is an explicit publication API: callers invoke it after Build and on
// enterprise source updates. No unmanaged background polling is started.
// Metadata planes remain additive compatibility/source-fetch views; enforcement
// objects are republished via PublishExecutableFromProduction (D10, task 5.5).
type SnapshotController struct {
	refreshMu sync.Mutex
	pub       *snapshotgen.Publisher
	cfg       *config.Config
	prod      ProductionOptions
	clock     func() time.Time
	timeout   time.Duration

	usage       economics.RuleSnapshotSource
	concurrency economics.RuleSnapshotSource
	rating      economics.RatingSnapshotSource
}

func newSnapshotController(cfg *config.Config, testing TestingOptions, prod ProductionOptions) *SnapshotController {
	clock := time.Now
	if testing.Clock != nil {
		clock = testing.Clock
	}
	return &SnapshotController{
		pub:         snapshotgen.NewPublisher(),
		cfg:         cfg,
		prod:        prod,
		clock:       clock,
		timeout:     DefaultSnapshotRefreshTimeout,
		usage:       prod.UsageSnapshotSource,
		concurrency: prod.ConcurrencySnapshotSource,
		rating:      prod.RatingSnapshotSource,
	}
}

// Publisher returns the atomic generation publisher used for admit-time binding.
func (c *SnapshotController) Publisher() *snapshotgen.Publisher {
	if c == nil {
		return nil
	}
	return c.pub
}

// Refresh re-reads injectable sources (when present), composes an immutable
// generation from static config baselines plus source results, and publishes it
// atomically. Source failures preserve prior Value versions and expose
// degraded/unavailable posture without substituting an unrelated version.
//
// A non-nil error means at least one injected source failed; a generation is
// still published so readiness/admission see the explicit posture.
func (c *SnapshotController) Refresh(ctx context.Context) error {
	if c == nil || c.pub == nil {
		return fmt.Errorf("runtimebundle: nil snapshot controller")
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	ctx = ctxOrBackground(ctx)
	timeout := c.timeout
	if timeout <= 0 {
		timeout = DefaultSnapshotRefreshTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	now := c.clock().UTC()
	gen := c.baseGeneration(now)
	prior := c.pub.Current()
	var errs []error

	if c.usage != nil {
		snap, err := c.usage.Snapshot(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("usage snapshot: %w", err))
			gen.Usage = preserveRulePlane(prior, gen.Usage, planeUsage, economics.SnapshotUnavailable)
			gen.State = economics.SnapshotDegraded
			if gen.Reason == "" {
				gen.Reason = "usage_snapshot_refresh_failed"
			}
		} else {
			gen.Usage = snap
		}
	}
	if c.concurrency != nil {
		snap, err := c.concurrency.Snapshot(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("concurrency snapshot: %w", err))
			gen.Concurrency = preserveRulePlane(prior, gen.Concurrency, planeConcurrency, economics.SnapshotUnavailable)
			gen.State = economics.SnapshotDegraded
			if gen.Reason == "" {
				gen.Reason = "concurrency_snapshot_refresh_failed"
			}
		} else {
			gen.Concurrency = snap
		}
	}
	if c.rating != nil {
		snap, err := c.rating.Snapshot(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("rating snapshot: %w", err))
			gen.Rating = preserveRatingPlane(prior, gen.Rating, economics.SnapshotUnavailable)
			gen.State = economics.SnapshotDegraded
			if gen.Reason == "" {
				gen.Reason = "rating_snapshot_refresh_failed"
			}
		} else {
			gen.Rating = snap
		}
	}

	if len(errs) > 0 && gen.State == economics.SnapshotReady {
		gen.State = economics.SnapshotDegraded
	}
	c.pub.PublishMetadata(gen)
	// Source-fetch failures leave the prior executable generation active (9.6)
	// and report degraded metadata planes separately from executable readiness.
	if len(errs) == 0 {
		_, _ = PublishExecutableFromProduction(c.pub, c.cfg, c.prod, now)
	}
	return errors.Join(errs...)
}

type rulePlane int

const (
	planeUsage rulePlane = iota
	planeConcurrency
)

func preserveRulePlane(
	prior *snapshotgen.RuntimeGeneration,
	base economics.Snapshot[economics.PolicyRulesView],
	plane rulePlane,
	state economics.SnapshotState,
) economics.Snapshot[economics.PolicyRulesView] {
	out := base
	if prior != nil {
		switch plane {
		case planeUsage:
			if prior.Usage.Version != "" || prior.Usage.State != "" {
				out = prior.Usage
			}
		case planeConcurrency:
			if prior.Concurrency.Version != "" || prior.Concurrency.State != "" {
				out = prior.Concurrency
			}
		}
	}
	out.State = state
	return out
}

func preserveRatingPlane(
	prior *snapshotgen.RuntimeGeneration,
	base economics.Snapshot[economics.RatingCatalogView],
	state economics.SnapshotState,
) economics.Snapshot[economics.RatingCatalogView] {
	out := base
	if prior != nil && (prior.Rating.Version != "" || prior.Rating.State != "") {
		out = prior.Rating
	}
	out.State = state
	return out
}

func (c *SnapshotController) baseGeneration(now time.Time) snapshotgen.RuntimeGeneration {
	gen := snapshotgen.RuntimeGeneration{
		PublishedAt: now,
		State:       economics.SnapshotReady,
	}
	cfg := c.cfg
	if cfg == nil {
		return gen
	}
	usageVer := "static"
	if v := cfg.Accounting.Authority.SnapshotVersion; v != "" {
		usageVer = v
	}
	if cfg.Accounting.Authority.Enabled {
		gen.Usage = economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: usageVer, EffectiveAt: now, FetchedAt: now,
			State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		}
	} else {
		gen.Usage = economics.Snapshot[economics.PolicyRulesView]{
			ID: "usage_authority", Version: usageVer, State: economics.SnapshotDisabled,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
		}
	}
	concVer := "static"
	if v := cfg.Accounting.Concurrency.SnapshotVersion; v != "" {
		concVer = v
	}
	if cfg.Accounting.Concurrency.Enabled {
		gen.Concurrency = economics.Snapshot[economics.PolicyRulesView]{
			ID: "concurrency", Version: concVer, EffectiveAt: now, FetchedAt: now,
			State: economics.SnapshotReady,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency},
		}
	} else {
		gen.Concurrency = economics.Snapshot[economics.PolicyRulesView]{
			ID: "concurrency", Version: concVer, State: economics.SnapshotDisabled,
			Value: economics.PolicyRulesView{Kind: economics.PolicyKindConcurrency},
		}
	}
	catVer := cfg.Accounting.Pricing.CatalogVersion
	gen.Rating = snapshotsource.StaticRatingFromCatalog("rating", catVer, cfg.Accounting.Pricing.Currency, now)
	return gen
}
