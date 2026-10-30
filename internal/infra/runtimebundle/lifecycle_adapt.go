package runtimebundle

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	aipplugin "github.com/aiproxer/aiproxer/pkg/aipsdk/plugin"
)

// ErrUnsafeLifecycleOverlap is returned when a feature/plugin lifecycle cannot
// safely Start/Stop under candidate overlap (req 8.8, design lifecycle adapt).
var ErrUnsafeLifecycleOverlap = errors.New("runtimebundle: lifecycle change cannot overlap safely")

// ErrCandidateFaultInjected is returned when TestingOptions injects a candidate fault.
var ErrCandidateFaultInjected = errors.New("runtimebundle: candidate fault injected")

// CandidateOverlapSafe marks a Lifecycle that may Start while another generation
// remains active. Unmarked Lifecycles are rejected before publication.
type CandidateOverlapSafe interface {
	SafeUnderCandidateOverlap() bool
}

// ClassifyFeatureLifecycles rejects lifecycles that are not explicitly overlap-safe.
func ClassifyFeatureLifecycles(lifes []aipplugin.Lifecycle) error {
	for i, life := range lifes {
		if life == nil {
			continue
		}
		safe, ok := life.(CandidateOverlapSafe)
		if !ok || !safe.SafeUnderCandidateOverlap() {
			return fmt.Errorf("%w: index=%d type=%T", ErrUnsafeLifecycleOverlap, i, life)
		}
	}
	return nil
}

// ClassifyBackendOverlap rejects enabled candidate backends whose factory kinds
// disallow concurrent instance handles while a live generation still holds that
// kind. Returns the typed restart-required classification before any candidate
// resource acquisition (req 7.3, 8.8; task 4.2).
func ClassifyBackendOverlap(reg *pluginreg.Registry, cfg *config.Config, liveFactoryKinds map[string]int) error {
	if reg == nil || cfg == nil || len(liveFactoryKinds) == 0 {
		return nil
	}
	for _, p := range cfg.Plugins.Backends {
		if !p.Enabled {
			continue
		}
		fid := p.FactoryID()
		pol, ok := reg.BackendReloadPolicy(fid)
		if !ok || pol.AllowsCandidateOverlap {
			continue
		}
		if liveFactoryKinds[fid] <= 0 {
			continue
		}
		rr := &configreload.RestartRequiredError{
			RestartRequiredFields: []string{"plugins.backends"},
			TotalBlocked:          1,
		}
		return fmt.Errorf("%w: %w: factory %q instance %q", rr, ErrUnsafeLifecycleOverlap, fid, p.InstanceID())
	}
	return nil
}

// QuiescePhaseLifecycles opt into PhaseQuiesce stop semantics: their Stop
// runs at generation retirement (Quiesce) instead of only at final close.
// Admission-independent workers such as keep-warm maintenance must implement
// it so a retired-but-not-closed generation stops background work at
// retirement (Requirement 6.5: quiesce ordering).
type QuiescePhaseLifecycles interface {
	aipplugin.Lifecycle
	QuiescePhase() bool
}

// AdaptOverlapSafeLifecycles registers Start on prepare and Stop on
// close/rollback for lifecycles already classified as overlap-safe.
// Lifecycles implementing QuiescePhaseLifecycles with a true QuiescePhase
// additionally stop at generation retirement via the PhaseQuiesce ledger
// entry, preserving quiesce ordering for admission-independent workers.
func AdaptOverlapSafeLifecycles(ledger *ResourceLedger, lifes []aipplugin.Lifecycle) error {
	if ledger == nil {
		return fmt.Errorf("runtimebundle: nil resource ledger")
	}
	if err := ClassifyFeatureLifecycles(lifes); err != nil {
		return err
	}
	for i, life := range lifes {
		if life == nil {
			continue
		}
		name := fmt.Sprintf("feature-lifecycle-%d", i)
		ledger.AddAction(
			name, PhasePrepare,
			func(ctx context.Context) error { return life.Start(ctx) },
			func(ctx context.Context) error { return life.Stop(ctx) },
		)
		if qp, ok := life.(QuiescePhaseLifecycles); ok && qp.QuiescePhase() {
			stop := func(ctx context.Context) error { return life.Stop(ctx) }
			ledger.Add(name+"-quiesce", PhaseQuiesce, stop)
		}
	}
	return nil
}
