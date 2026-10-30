package proxymemory

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The owner-scoped settings surface of design §4: the persisted per-phase
// overrides, the effective values they resolve to, and the authenticated
// resource that reads and changes them.
//
// The stored row holds the owner's own unprojected per-field overrides. The
// effective value of every field is recomputed on every read from the
// operator's defaults, the operator's locks and those overrides, so a changed
// default or a newly introduced lock takes effect at once and the stored row
// never has to be rewritten (Requirement 2.1, Requirement 2.4).
//
// The owner is the authenticated principal of the request, never a value the
// caller supplies: the settings service takes the principal and reads nothing
// else about the caller.

// DependencyContext is the trusted, write-time context of one phase's stored
// overrides: the target the accepted effort was checked against, and which of
// its dimensions the owner inherited from the operator's defaults.
//
// It is provenance, not authority: it never replaces the current operator
// policy and it is never accepted from a client. The settings service computes
// it after both phase targets passed validation and persists it atomically
// with the raw overrides; a projection compares it with the current policy to
// tell a removed default (mask the dependent effort) from a narrowed inventory
// (refuse the invalid effort).
type DependencyContext struct {
	// Present reports whether this phase row carries trusted write-time
	// context. A row written before the context existed has no history and
	// never gets one fabricated from the current configuration.
	Present bool
	// Backend and Model are the phase's effective target when the override
	// was accepted: the declared target completed with the operator's
	// default route.
	Backend string
	Model   string
	// InheritedBackend and InheritedModel report whether the owner named no
	// value for the dimension, so its write-time value came from the
	// operator's defaults or default route.
	InheritedBackend bool
	InheritedModel   bool
}

// StoredPhaseSettings is one phase's persisted settings: the owner's raw,
// unprojected overrides and the trusted write-time dependency context that
// travels with them.
type StoredPhaseSettings struct {
	// Overrides are the owner's own unprojected values, exactly as stored.
	Overrides TargetOverrides
	// Dependency is the write-time context of an accepted override.
	Dependency DependencyContext
}

// StoredOwnerSettings is the owner's complete persisted settings: the two
// phase rows and their trusted write-time context, read and written as one
// owner-scoped record.
type StoredOwnerSettings struct {
	Extraction    StoredPhaseSettings
	Consolidation StoredPhaseSettings
}

// For returns the stored settings of one phase.
func (s StoredOwnerSettings) For(phase Phase) StoredPhaseSettings {
	switch phase {
	case PhaseExtraction:
		return s.Extraction
	case PhaseConsolidation:
		return s.Consolidation
	}
	return StoredPhaseSettings{}
}

// Overrides returns the owner's raw, unprojected per-phase overrides.
func (s StoredOwnerSettings) Overrides() OwnerOverrides {
	return OwnerOverrides{
		Extraction:    s.Extraction.Overrides,
		Consolidation: s.Consolidation.Overrides,
	}
}

// SettingsProjection is the settings projection of one owner: the stored,
// unprojected overrides and the per-phase effective state.
//
// The two parts travel together because the resource shows them together: the
// stored overrides are what the owner actually has, and the effective values
// are what the phases run with (design §4).
type SettingsProjection struct {
	// Stored are the owner's stored, unprojected per-phase overrides: the
	// exact values the store holds.
	Stored OwnerOverrides
	// Phases carries one entry per phase, in phase order: the effective
	// target and the per-field lock flags.
	Phases []PhaseSettings
}

// Settings returns the caller's own settings: the stored overrides and, per
// phase, the effective target and the per-field lock flags.
//
// The effective values are recomputed on every read, so a changed operator
// default or a newly introduced lock takes effect at once while the stored
// overrides stay exactly as the owner wrote them (Requirement 2.1,
// Requirement 2.4).
//
// A deployment that declares no per-phase model policy has no per-phase
// settings to show, so the resource answers as an unknown one.
func (s *Service) Settings(ctx context.Context, p Principal) (SettingsProjection, error) {
	if !s.cfg.ModelsDeclared {
		return SettingsProjection{}, ErrNotFound
	}
	stored, err := s.store.ReadSettings(ctx, p)
	if err != nil {
		return SettingsProjection{}, err
	}
	return s.projectSettings(stored)
}

// UpdateSettings replaces the caller's own per-phase overrides and returns the
// resulting projection.
//
// The write is refused whole: both phases are checked against the operator's
// policy and against the final compiled actual target before anything is
// stored, so a write that changes a locked field, names a value outside the
// allowed inventory, or names a target the actual target does not cover
// persists nothing (Requirement 1.2, Requirement 1.4, Requirement 2.2).
//
// What is stored is the owner's own permitted override set and, beside it, the
// trusted write-time dependency context of each phase: the effective target
// the accepted effort was checked against and the dimensions the owner left to
// the operator. The operator's locked fields carry no owner value, so the
// stored row stays minimal and never goes stale (design §4).
//
// The context is computed only after both phases passed the checks above and
// is written in the same transaction as the overrides. It is server-owned: no
// caller supplies it, no read changes it, and a revoked settings row takes it
// away.
func (s *Service) UpdateSettings(ctx context.Context, p Principal, proposed OwnerOverrides) (SettingsProjection, error) {
	if !s.cfg.ModelsDeclared {
		return SettingsProjection{}, ErrNotFound
	}
	extraction, err := s.cfg.ApplyOwnerOverrides(PhaseExtraction, proposed.Extraction)
	if err != nil {
		s.metrics.Inc(EventSettingsRefused)
		return SettingsProjection{}, err
	}
	consolidation, err := s.cfg.ApplyOwnerOverrides(PhaseConsolidation, proposed.Consolidation)
	if err != nil {
		s.metrics.Inc(EventSettingsRefused)
		return SettingsProjection{}, err
	}
	// The proposed effective targets are checked against the final compiled
	// actual target before either phase is stored: the write replaces both
	// phases, so both targets must hold before anything is persisted.
	//
	// The selector is derived from the phase's *declared* target, so a phase
	// whose target nobody selected keeps the operator's own route, exactly as
	// the dispatch path derives it. The effort is the proposed effective one.
	for _, proposedTarget := range []struct {
		phase    Phase
		declared PhaseTarget
		effort   string
	}{
		{phase: PhaseExtraction, declared: extraction.Declared, effort: extraction.Effective.ReasoningEffort},
		{phase: PhaseConsolidation, declared: consolidation.Declared, effort: consolidation.Effective.ReasoningEffort},
	} {
		if err := s.checkProposedTarget(ctx, proposedTarget.phase, proposedTarget.declared, proposedTarget.effort); err != nil {
			s.metrics.Inc(EventSettingsRefused)
			return SettingsProjection{}, err
		}
	}
	accepted := StoredOwnerSettings{
		Extraction: StoredPhaseSettings{
			Overrides:  extraction.Overrides,
			Dependency: dependencyContext(extraction),
		},
		Consolidation: StoredPhaseSettings{
			Overrides:  consolidation.Overrides,
			Dependency: dependencyContext(consolidation),
		},
	}
	if err := s.store.WriteSettings(ctx, p, accepted); err != nil {
		return SettingsProjection{}, err
	}
	return s.projectSettings(accepted)
}

// dependencyContext records the write-time context of one accepted phase
// target.
//
// It is computed only after the phase passed the existing validation and
// factual checks, so a refused write never persists context. The recorded
// target is the phase's effective target — the target the effort was checked
// against — and a dimension the owner named no value for is marked inherited,
// because its value came from the operator's defaults or default route.
func dependencyContext(settings PhaseSettings) DependencyContext {
	return DependencyContext{
		Present:          true,
		Backend:          settings.Effective.Backend,
		Model:            settings.Effective.Model,
		InheritedBackend: settings.Overrides.Backend == "",
		InheritedModel:   settings.Overrides.Model == "",
	}
}

// checkProposedTarget compiles the proposed effective target of one phase and
// runs the phase's inventory, lock, capability and factual checks against the
// final compiled actual target.
//
// The authority is the existing one: the same generation-bound compiler port,
// the same derived selector and the same policy checks the dispatch runs
// (design §2 and §3; Requirements 1.4 and 1.5). A write is therefore
// refused before it is stored when the actual target cannot be compiled, when
// it addresses a target the phase's inventory does not cover, when it cannot
// carry the chosen reasoning effort, when the deployment does not carry the
// model, or when the provider does not support the effort value.
//
// The compiled call carries the phase's derived selector and the proposed
// effort and nothing else: the write decides the owner's target, and the
// phase's own content requirements belong to the operator and are checked at
// dispatch.
//
// The selector is derived from the phase's *declared* target — the target the
// operator and the owner named — and not from the effective triple, which the
// operator's default route has already completed. A phase whose target nobody
// selected therefore keeps the operator's own route, the same route the
// dispatch path derives, instead of being read as a replacement of the route's
// first leaf.
func (s *Service) checkProposedTarget(ctx context.Context, phase Phase, declared PhaseTarget, effort string) error {
	selector, err := DeriveRouteSelector(phase, s.cfg.phaseRoute(phase), declared.Backend, declared.Model)
	if err != nil {
		return err
	}
	call := aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: selector},
		Options: aipapi.GenerationOptions{ReasoningEffort: effort},
	}
	target, err := compileWithFacts(ctx, s.cfg.TargetCompiler, phase, call)
	if err != nil {
		return boundedTargetRefusal(phase, err)
	}
	if err := s.cfg.CheckCompiledTarget(ctx, phase, call, target); err != nil {
		if errors.Is(err, ErrReasoningCapabilityRefused) {
			s.metrics.Inc(EventModelPolicyRefusedReasoning)
		}
		return err
	}
	return s.checkTargetFacts(ctx, phase, target)
}

// checkTargetFacts applies the deployment's factual policy to every compiled
// candidate of the phase.
//
// Every candidate is checked, not only the first one: the runtime may fail
// over to a later candidate or run a parallel leg, so a candidate whose facts
// the policy refuses would otherwise serve the call unchecked.
func (s *Service) checkTargetFacts(ctx context.Context, phase Phase, target CompiledTarget) error {
	for _, candidate := range target.Candidates {
		err := s.cfg.factualRefusal(phase, candidate.Installation, candidate.EffortSupport, func(event, field string) {
			s.metrics.Inc(event)
			if s.log != nil {
				s.log.WarnContext(ctx, "proxy-memory model policy served an unverified fact",
					"event", event,
					"phase", string(phase),
					"field", field,
				)
			}
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// boundedTargetRefusal turns a compilation failure into a bounded refusal.
//
// The refusal names the phase and the field and never the value, so it can be
// answered and logged without carrying an owner-supplied or provider-supplied
// string. The compilation error stays in the chain, so a caller can still
// classify it: a policy refusal that already names its field is kept, and a
// cancelled compilation stays a cancellation.
func boundedTargetRefusal(phase Phase, cause error) error {
	if _, ok := errors.AsType[*PolicyError](cause); ok {
		return cause
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	refusal := &PolicyError{Phase: phase, Field: FieldRoute, Reason: "the actual target does not compile"}
	return fmt.Errorf("%w: %w", refusal, cause)
}

// DeleteSettings removes the caller's own stored settings, the trusted
// dependency context included. An owner with no stored settings is
// ErrNotFound.
//
// The deletion never depends on the operator's model policy: a deployment
// that dropped its policy still lets the owner revoke what it stored.
func (s *Service) DeleteSettings(ctx context.Context, p Principal) error {
	return s.store.DeleteSettings(ctx, p)
}

// projectSettings resolves the effective state of both phases from the
// operator's defaults, the operator's locks and the stored overrides.
//
// The projection is pure: it reads no store, mutates nothing and does not
// change the caller's stored row. A stored value the phase's inventory no
// longer permits is refused by the same authority that refuses it at dispatch
// time, so the caller sees the refusal instead of a silently substituted
// value. An effort whose trusted write-time context depended on an operator
// target that changed is masked instead: the effective target carries the
// operator's current default effort while the stored overrides stay as the
// owner wrote them (design §4; Requirements 1.4 and 2.4).
func (s *Service) projectSettings(stored StoredOwnerSettings) (SettingsProjection, error) {
	phases := make([]PhaseSettings, 0, 2)
	for _, phase := range []Phase{PhaseExtraction, PhaseConsolidation} {
		settings, err := s.cfg.projectStoredPhase(phase, stored.For(phase))
		if err != nil {
			return SettingsProjection{}, err
		}
		if settings.DependencyMasked {
			s.metrics.Inc(EventSettingsDependencyMasked)
			if s.log != nil {
				s.log.Warn("proxy-memory settings masked an override dependent on a changed operator target",
					"event", EventSettingsDependencyMasked,
					"phase", string(phase),
				)
			}
		}
		phases = append(phases, settings)
	}
	return SettingsProjection{Stored: stored.Overrides(), Phases: phases}, nil
}

// projectStoredPhase resolves one phase from its stored state: the raw
// overrides, and the trusted write-time context that tells a removed operator
// default from a narrowed inventory.
//
// The context is only ever evidence about the past, never authority over the
// present: the current operator defaults, locks, inventory and compiled actual
// target still decide the effective values and every dispatch check.
func (c Config) projectStoredPhase(phase Phase, stored StoredPhaseSettings) (PhaseSettings, error) {
	if !stored.Dependency.Present ||
		(!stored.Dependency.InheritedBackend && !stored.Dependency.InheritedModel) {
		// No trustworthy dependency context or an owner-named whole target:
		// the existing projection is the answer. A legacy invalid value stays
		// an explicit refusal instead of gaining a fabricated history.
		return c.OwnerSettings(phase, stored.Overrides)
	}
	policy, ok := c.Models.For(phase)
	if !ok {
		return c.OwnerSettings(phase, stored.Overrides)
	}
	if sanitizeOverrides(policy.Locks, stored.Overrides).ReasoningEffort == "" {
		// No owner effort is actually in force: a lock or an empty value
		// already decides the field, so there is nothing to mask.
		return c.OwnerSettings(phase, stored.Overrides)
	}
	current, err := c.ownerTargetIdentity(phase, stored.Overrides)
	if err != nil {
		return PhaseSettings{}, err
	}
	if !stored.Dependency.targetChanged(current) {
		// The accepted effort depended on the same target it does today, so
		// an effort the current inventory forbids is a genuine narrowing.
		return c.OwnerSettings(phase, stored.Overrides)
	}
	masked := stored.Overrides
	masked.ReasoningEffort = ""
	settings, err := c.OwnerSettings(phase, masked)
	if err != nil {
		return PhaseSettings{}, err
	}
	settings.DependencyMasked = true
	return settings, nil
}

// targetChanged reports whether a changed inherited dimension moved the
// accepted target. Only the dimensions the owner inherited are compared: an
// owner-named dimension cannot have depended on an operator default.
func (d DependencyContext) targetChanged(current PhaseTarget) bool {
	if d.InheritedBackend && d.Backend != current.Backend {
		return true
	}
	return d.InheritedModel && d.Model != current.Model
}
