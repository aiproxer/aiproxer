package proxymemory

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// The two histories of a valid effort-only owner override that converge to the
// same current policy and raw row (design §4 Reload; Requirement 2.4):
//
//  1. the override was accepted against an operator default target that was
//     later removed, so the dependent effort is masked onto the operator's
//     current default effort;
//  2. the override was accepted against the current target and the target's
//     inventory was later narrowed, so the effort stays an explicit refusal.
//
// Raw rows cannot distinguish the histories: only the trusted write-time
// dependency context can. This file is the feature-level proof of the
// distinction, on the same projection and claim functions the resource and the
// worker use.

// reloadSettingsStore is the in-memory settings authority shared by the
// settings service and the worker, like the isolated feature store they share
// in production.
type reloadSettingsStore struct {
	*workerStoreStub
	stored StoredOwnerSettings
	writes int
}

func (s *reloadSettingsStore) ReadSettings(_ context.Context, _ Principal) (StoredOwnerSettings, error) {
	return s.stored, nil
}

func (s *reloadSettingsStore) WriteSettings(_ context.Context, _ Principal, value StoredOwnerSettings) error {
	s.writes++
	s.stored = value
	return nil
}

// maskingPolicy is one phase's operator policy of the histories: the default
// target, the model lock and the per-target effort inventory. An empty default
// model means the operator declared no default target.
func maskingPolicy(defaultModel, defaultEffort string, baseEfforts []string) PhasePolicy {
	defaults := PhaseTarget{}
	if defaultModel != "" {
		defaults.Model = defaultModel
	}
	if defaultEffort != "" {
		defaults.ReasoningEffort = defaultEffort
	}
	return PhasePolicy{
		Defaults: defaults,
		Locks:    TargetLocks{Model: true},
		Inventory: PhaseInventory{
			Backends: []string{"openai"},
			Models:   []string{"base", "chosen"},
			Targets: []TargetPolicy{
				{Backend: "openai", Model: "base", ReasoningEfforts: append([]string(nil), baseEfforts...)},
				{Backend: "openai", Model: "chosen", ReasoningEfforts: []string{"high"}},
			},
		},
	}
}

// maskingConfig builds one operator generation with the same policy for both
// phases, so each phase exercises the same history independently.
func maskingConfig(policy PhasePolicy) Config {
	cfg := claimSettingsConfig()
	cfg.Models.Extraction = policy
	cfg.Models.Consolidation = policy
	return cfg
}

// maskingOverrides is the owner's effort-only override: the backend and the
// model are inherited from the operator, so the accepted effort depended on
// the operator's target.
func maskingOverrides() OwnerOverrides {
	return OwnerOverrides{
		Extraction:    TargetOverrides{ReasoningEffort: "high"},
		Consolidation: TargetOverrides{ReasoningEffort: "high"},
	}
}

// claimCurrentWorker drives one real worker claim of the phase with the current
// generation over the shared settings authority.
func claimCurrentWorker(t *testing.T, phase Phase, current Config, settings *reloadSettingsStore) *workerTestHarness {
	t.Helper()
	h := newWorkerTestHarness(t, current)
	settings.workerStoreStub = h.store
	h.worker.store = settings
	switch phase {
	case PhaseExtraction:
		h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
		h.worker.phase1(t.Context())
	case PhaseConsolidation:
		h.store.banks = []ClaimedBank{workerClaimedBank()}
		h.store.extractions = []Extraction{workerExtraction(h.base)}
		h.aux.text = workerPhase2Output
		h.worker.phase2(t.Context())
	}
	return h
}

func TestRemovedDefaultMasksDependentEffortWhileNarrowingRefuses(t *testing.T) {
	for _, phase := range []Phase{PhaseExtraction, PhaseConsolidation} {
		t.Run(string(phase), func(t *testing.T) {
			histories := []struct {
				name       string
				prior      PhasePolicy
				current    PhasePolicy
				wantMasked bool
			}{
				{
					name:       "removed default masks the dependent effort",
					prior:      maskingPolicy("chosen", "high", []string{"low", "medium"}),
					current:    maskingPolicy("", "", []string{"low", "medium"}),
					wantMasked: true,
				},
				{
					name:       "unchanged target with narrowed inventory refuses",
					prior:      maskingPolicy("base", "", []string{"low", "medium", "high"}),
					current:    maskingPolicy("base", "", []string{"low", "medium"}),
					wantMasked: false,
				},
			}
			for _, history := range histories {
				t.Run(history.name, func(t *testing.T) {
					settings := &reloadSettingsStore{workerStoreStub: newWorkerStoreStub()}
					prior := maskingConfig(history.prior)
					service := NewService(prior, settings, nil, func() time.Time { return time.UnixMilli(1_800_000_000_000) })
					raw := maskingOverrides()
					if _, err := service.UpdateSettings(t.Context(), settingsPrincipalOf(), raw); err != nil {
						t.Fatalf("the prior authority did not accept the effort-only override: %v", err)
					}
					if settings.writes != 1 {
						t.Fatalf("accepted writes=%d, want one atomic write", settings.writes)
					}
					if !reflect.DeepEqual(settings.stored.Overrides(), raw) {
						t.Fatalf("stored raw overrides=%+v, want %+v", settings.stored.Overrides(), raw)
					}
					wantContextModel := history.prior.Defaults.Model
					if wantContextModel == "" {
						wantContextModel = "base"
					}
					for _, storedPhase := range []StoredPhaseSettings{settings.stored.Extraction, settings.stored.Consolidation} {
						if !storedPhase.Dependency.Present {
							t.Fatalf("the authenticated write stored no trusted dependency context: %+v", storedPhase)
						}
						if got := storedPhase.Dependency.Backend; got != "openai" {
							t.Fatalf("stored context backend=%q, want the write-time effective target openai", got)
						}
						if got := storedPhase.Dependency.Model; got != wantContextModel {
							t.Fatalf("stored context model=%q, want the write-time effective target %q", got, wantContextModel)
						}
						if !storedPhase.Dependency.InheritedBackend || !storedPhase.Dependency.InheritedModel {
							t.Fatalf("stored context inheritance=%+v, want both dimensions inherited from the operator", storedPhase.Dependency)
						}
					}

					current := maskingConfig(history.current)
					storedDependencyBeforeClaim := settings.stored
					projection, projectionErr := NewService(current, settings, nil, func() time.Time { return time.UnixMilli(1_800_000_000_000) }).Settings(t.Context(), settingsPrincipalOf())
					h := claimCurrentWorker(t, phase, current, settings)
					calls := h.aux.submitCalls()
					if history.wantMasked {
						if projectionErr != nil {
							t.Fatalf("the effective projection refused a dependent effort the operator removed its default for: %v", projectionErr)
						}
						if got, want := projection.Phases[phaseIndex(phase)].Effective.ReasoningEffort, defaultPhaseEffort(phase); got != want {
							t.Fatalf("effective effort=%q, want the operator's current default %q", got, want)
						}
						if len(calls) != 1 {
							t.Fatalf("submissions=%d failures=%d, want one masked claim dispatch", len(calls), len(h.store.sessionFailCalls())+len(h.store.bankFailCalls()))
						}
						if got, want := calls[0].req.Call.Route.Selector, "openai:base"; got != want {
							t.Fatalf("masked route=%q, want the operator's current target %q", got, want)
						}
						if got, want := calls[0].req.Call.Options.ReasoningEffort, defaultPhaseEffort(phase); got != want {
							t.Fatalf("masked effort=%q, want the operator's current default %q", got, want)
						}
						if got := captureCounter(t, h.metrics, EventSettingsDependencyMasked); got != 1 {
							t.Fatalf("settings_dependency_masked=%v, want one bounded masking event", got)
						}
					} else {
						if projectionErr == nil {
							t.Fatal("the effective projection served an effort the narrowed inventory no longer allows")
						}
						var policyErr *PolicyError
						if !errors.As(projectionErr, &policyErr) || policyErr.Field != FieldReasoningEffort {
							t.Fatalf("projection error=%v, want an explicit reasoning-effort refusal", projectionErr)
						}
						if len(calls) != 0 {
							t.Fatalf("submissions=%d, want no dispatch after a narrowing refusal", len(calls))
						}
						if got := len(h.store.sessionFailCalls()) + len(h.store.bankFailCalls()); got != 1 {
							t.Fatalf("claim failures=%d, want the ordinary failure path", got)
						}
					}
					if !reflect.DeepEqual(settings.stored.Overrides(), raw) {
						t.Fatalf("stored raw overrides changed to %+v, must stay %+v", settings.stored.Overrides(), raw)
					}
					if !reflect.DeepEqual(settings.stored, storedDependencyBeforeClaim) {
						t.Fatalf("the projection or claim rewrote the stored settings: %+v, want %+v", settings.stored, storedDependencyBeforeClaim)
					}
				})
			}
		})
	}
}

// TestCompleteOwnerTargetIsNotMaskedByAnUnrelatedDefaultChange guards the other
// side of the distinction: when the owner named the whole target, the effort
// does not depend on an inherited default and the override stays effective.
func TestCompleteOwnerTargetIsNotMaskedByAnUnrelatedDefaultChange(t *testing.T) {
	for _, phase := range []Phase{PhaseExtraction, PhaseConsolidation} {
		t.Run(string(phase), func(t *testing.T) {
			settings := &reloadSettingsStore{workerStoreStub: newWorkerStoreStub()}
			prior := maskingConfig(maskingPolicy("base", "", []string{"low", "medium"}))
			prior.Models.Extraction.Locks.Model = false
			prior.Models.Consolidation.Locks.Model = false
			service := NewService(prior, settings, nil, func() time.Time { return time.UnixMilli(1_800_000_000_000) })
			raw := OwnerOverrides{
				Extraction:    TargetOverrides{Model: "chosen", ReasoningEffort: "high"},
				Consolidation: TargetOverrides{Model: "chosen", ReasoningEffort: "high"},
			}
			if _, err := service.UpdateSettings(t.Context(), settingsPrincipalOf(), raw); err != nil {
				t.Fatalf("the prior authority did not accept the independent target: %v", err)
			}
			current := maskingConfig(maskingPolicy("", "", []string{"low", "medium"}))
			current.Models.Extraction.Locks.Model = false
			current.Models.Consolidation.Locks.Model = false
			projection, err := NewService(current, settings, nil, func() time.Time { return time.UnixMilli(1_800_000_000_000) }).Settings(t.Context(), settingsPrincipalOf())
			if err != nil {
				t.Fatalf("the effective projection refused an independent owner target: %v", err)
			}
			if got, want := projection.Phases[phaseIndex(phase)].Effective.Model, "chosen"; got != want {
				t.Fatalf("effective model=%q, want the owner's independent model %q", got, want)
			}
			if got, want := projection.Phases[phaseIndex(phase)].Effective.ReasoningEffort, "high"; got != want {
				t.Fatalf("effective effort=%q, want the owner's independent effort %q", got, want)
			}
			h := claimCurrentWorker(t, phase, current, settings)
			calls := h.aux.submitCalls()
			if len(calls) != 1 {
				t.Fatalf("submissions=%d, want one dispatch of the independent target", len(calls))
			}
			if got, want := calls[0].req.Call.Route.Selector, "openai:chosen"; got != want {
				t.Fatalf("route=%q, want %q", got, want)
			}
			if got, want := calls[0].req.Call.Options.ReasoningEffort, "high"; got != want {
				t.Fatalf("effort=%q, want %q", got, want)
			}
			if got := captureCounter(t, h.metrics, EventSettingsDependencyMasked); got != 0 {
				t.Fatalf("settings_dependency_masked=%v, want no masking of an independent target", got)
			}
		})
	}
}

// TestLegacyStoredOverrideWithoutContextStillRefusesInvalidEffort proves the
// compatibility rule: a row written before trusted context existed never gets
// a fabricated history, so an effort the current inventory no longer allows is
// refused exactly as before.
func TestLegacyStoredOverrideWithoutContextStillRefusesInvalidEffort(t *testing.T) {
	for _, phase := range []Phase{PhaseExtraction, PhaseConsolidation} {
		t.Run(string(phase), func(t *testing.T) {
			settings := &reloadSettingsStore{workerStoreStub: newWorkerStoreStub()}
			switch phase {
			case PhaseExtraction:
				settings.stored.Extraction.Overrides = TargetOverrides{ReasoningEffort: "high"}
			case PhaseConsolidation:
				settings.stored.Consolidation.Overrides = TargetOverrides{ReasoningEffort: "high"}
			}
			current := maskingConfig(maskingPolicy("base", "", []string{"low", "medium"}))
			projection, err := NewService(current, settings, nil, func() time.Time { return time.UnixMilli(1_800_000_000_000) }).Settings(t.Context(), settingsPrincipalOf())
			if err == nil {
				t.Fatal("a legacy row without context fabricated a dependency and masked its effort")
			}
			var policyErr *PolicyError
			if !errors.As(err, &policyErr) || policyErr.Field != FieldReasoningEffort {
				t.Fatalf("projection error=%v, want the existing explicit reasoning-effort refusal", err)
			}
			_ = projection
			h := claimCurrentWorker(t, phase, current, settings)
			if calls := h.aux.submitCalls(); len(calls) != 0 {
				t.Fatalf("submissions=%d, want no dispatch of a legacy invalid effort", len(calls))
			}
		})
	}
}

// settingsPrincipalOf is the owner of the settings histories. It is the same
// shape the authenticated resource derives; no project is part of the key.
func settingsPrincipalOf() Principal {
	return Principal{TenantID: workerTestTenant, PrincipalID: workerTestPrincipal}
}

// phaseIndex maps a phase to its entry in the settings projection.
func phaseIndex(phase Phase) int {
	if phase == PhaseConsolidation {
		return 1
	}
	return 0
}
