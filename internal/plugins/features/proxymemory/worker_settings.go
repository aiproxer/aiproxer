package proxymemory

import (
	"context"
	"errors"
	"time"
)

// claimedTarget contains only owned strings. It is local to one worker claim,
// never cached by owner or recomputed when the auxiliary queue runs the call.
type claimedTarget struct {
	route  string
	effort string
}

func (w *Worker) claimTarget(ctx context.Context, owner Owner, phase Phase, deadline time.Time) (claimedTarget, error) {
	if !w.cfg.ModelsDeclared {
		route, effort, err := phaseCallTarget(w.cfg, phase)
		return claimedTarget{route: route, effort: effort}, err
	}
	bounded, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	stored, err := w.store.ReadSettings(bounded, Principal{TenantID: owner.TenantID, PrincipalID: owner.PrincipalID})
	if bounded.Err() != nil {
		return claimedTarget{}, bounded.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return claimedTarget{}, err
	}
	if err != nil {
		// Only an unavailable store falls back. Invalid persisted policy values
		// below refuse the job; substituting a target would hide a narrowing.
		w.metrics.Inc(EventStoreUnavailable)
		w.log.Warn("proxy-memory settings unavailable; using operator defaults", "event", EventStoreUnavailable, "phase", phase)
		stored = StoredOwnerSettings{}
	}
	settings, err := w.cfg.projectStoredPhase(phase, stored.For(phase))
	if err != nil {
		return claimedTarget{}, err
	}
	if settings.DependencyMasked {
		// The owner's effort depended on an operator target that changed. The
		// mask is explicit and counted; the raw override is untouched, and the
		// ordinary target compilation, locks and factual checks still run
		// before any dispatch.
		w.metrics.Inc(EventSettingsDependencyMasked)
		w.log.Warn("proxy-memory settings masked an override dependent on a changed operator target",
			"event", EventSettingsDependencyMasked,
			"phase", phase,
		)
	}
	route, err := DeriveRouteSelector(phase, w.cfg.phaseRoute(phase), settings.Declared.Backend, settings.Declared.Model)
	if err != nil {
		return claimedTarget{}, err
	}
	return claimedTarget{route: route, effort: settings.Effective.ReasoningEffort}, nil
}
