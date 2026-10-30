package proxymemory

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The settings service of design §4: the owner's stored overrides, the
// effective values they resolve to, and the refusals that keep a write from
// changing what the operator holds.

// settingsPrincipal is the owner of the settings cases.
var settingsPrincipal = Principal{TenantID: "tenant-settings", PrincipalID: "settings-alice"}

// newSettingsService builds a service over the given store and the operator's
// per-phase model policy. The service carries no target compiler: the read
// path never compiles a target.
func newSettingsService(t *testing.T, store Store) *Service {
	t.Helper()
	cfg := decodeModelPolicyConfig(t, configModelsLocks)
	return NewService(cfg, store, nil, nil)
}

// settingsTargetCompiler is the generation-bound target compiler of the
// settings write cases: it answers with the candidate the derived selector
// names, so the compiled actual target of a phase is the phase's own target.
//
// The reported effort is the compiled effort of that target, and the reported
// capabilities carry the reasoning capability the phase's effort needs.
func settingsTargetCompiler() *stubTargetCompiler {
	return &stubTargetCompiler{bySelector: map[string]stubCompiledCandidate{
		"openai:gpt-5-mini": {backend: "openai", model: "gpt-5-mini", effort: "low", caps: reasoningCaps()},
		"openai:gpt-5":      {backend: "openai", model: "gpt-5", effort: "high", caps: reasoningCaps()},
	}}
}

// TestOwnerSettings_AChangedDefaultRecomputesTheEffectiveValues pins
// Requirement 2.1 and Requirement 2.4: the effective value of every field is
// recomputed on read, so a changed operator default takes effect at once
// while the stored override stays exactly as the owner wrote it.
func TestOwnerSettings_AChangedDefaultRecomputesTheEffectiveValues(t *testing.T) {
	t.Parallel()

	store := newServiceStoreStub()
	store.overrides = OwnerOverrides{Extraction: TargetOverrides{Backend: "anthropic"}}
	service := newSettingsService(t, store)

	projection, err := service.Settings(t.Context(), settingsPrincipal)
	if err != nil {
		t.Fatalf("Settings() error: %v", err)
	}
	extraction := phaseOf(t, projection, PhaseExtraction)
	if want := (PhaseTarget{Backend: "anthropic", Model: "gpt-5-mini", ReasoningEffort: "low"}); extraction.Effective != want {
		t.Fatalf("effective target = %+v, want %+v", extraction.Effective, want)
	}
	if want := (TargetOverrides{Backend: "anthropic"}); projection.Stored.Extraction != want {
		t.Fatalf("stored overrides = %+v, want %+v", projection.Stored.Extraction, want)
	}

	// The operator changes the default model of the phase.
	changed := decodeModelPolicyConfig(t, configModelsLocks)
	changed.Models.Extraction.Defaults.Model = "gpt-5"
	changedService := NewService(changed, store, nil, nil)

	projection, err = changedService.Settings(t.Context(), settingsPrincipal)
	if err != nil {
		t.Fatalf("Settings() error after the default changed: %v", err)
	}
	extraction = phaseOf(t, projection, PhaseExtraction)
	if want := (PhaseTarget{Backend: "anthropic", Model: "gpt-5", ReasoningEffort: "low"}); extraction.Effective != want {
		t.Fatalf("effective target after the default changed = %+v, want %+v", extraction.Effective, want)
	}
	if want := (TargetOverrides{Backend: "anthropic"}); projection.Stored.Extraction != want {
		t.Fatalf("stored overrides after the default changed = %+v, want %+v: the stored row is never rewritten", projection.Stored.Extraction, want)
	}
}

// TestOwnerSettings_ALockMasksTheEffectiveValueWithoutRewritingTheStoredRow
// pins Requirement 1.2 and Requirement 2.4 on the durable read path: a lock
// the operator introduces after the owner wrote masks the effective value,
// while the stored override stays raw and untouched.
func TestOwnerSettings_ALockMasksTheEffectiveValueWithoutRewritingTheStoredRow(t *testing.T) {
	t.Parallel()

	store := newServiceStoreStub()
	store.overrides = OwnerOverrides{Extraction: TargetOverrides{Model: "gpt-5"}}
	// The phase locks nothing yet, so the owner's model is in effect.
	unlocked := decodeModelPolicyConfig(t, configModelsLocks)
	unlocked.Models.Extraction.Locks = TargetLocks{}
	service := NewService(unlocked, store, nil, nil)

	projection, err := service.Settings(t.Context(), settingsPrincipal)
	if err != nil {
		t.Fatalf("Settings() error: %v", err)
	}
	extraction := phaseOf(t, projection, PhaseExtraction)
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "low"}); extraction.Effective != want {
		t.Fatalf("effective target = %+v, want %+v", extraction.Effective, want)
	}
	if want := (TargetOverrides{Model: "gpt-5"}); projection.Stored.Extraction != want {
		t.Fatalf("stored overrides = %+v, want %+v", projection.Stored.Extraction, want)
	}

	// The operator locks the model of the phase.
	locked := decodeModelPolicyConfig(t, configModelsLocks)
	locked.Models.Extraction.Locks = TargetLocks{Model: true}
	lockedService := NewService(locked, store, nil, nil)

	projection, err = lockedService.Settings(t.Context(), settingsPrincipal)
	if err != nil {
		t.Fatalf("Settings() error under the lock: %v", err)
	}
	extraction = phaseOf(t, projection, PhaseExtraction)
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5-mini", ReasoningEffort: "low"}); extraction.Effective != want {
		t.Fatalf("effective target under the lock = %+v, want %+v: the lock wins", extraction.Effective, want)
	}
	if want := (TargetLocks{Model: true}); extraction.Locks != want {
		t.Fatalf("lock flags = %+v, want %+v", extraction.Locks, want)
	}
	if want := (TargetOverrides{Model: "gpt-5"}); projection.Stored.Extraction != want {
		t.Fatalf("stored overrides under the lock = %+v, want %+v: the stored row is never rewritten", projection.Stored.Extraction, want)
	}
}

// TestOwnerSettings_ARefusedWriteStoresNothing pins Requirement 1.2,
// Requirement 1.4 and Requirement 2.2: a write that changes a locked field or
// names a value outside the allowed inventory is refused, and the refusal
// persists nothing.
func TestOwnerSettings_ARefusedWriteStoresNothing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		proposed OwnerOverrides
	}{
		{
			name:     "a locked field",
			proposed: OwnerOverrides{Extraction: TargetOverrides{Model: "gpt-5"}},
		},
		{
			name:     "a value outside the allowed inventory",
			proposed: OwnerOverrides{Extraction: TargetOverrides{Backend: "gemini"}},
		},
		{
			name:     "a value outside the allowed inventory of the other phase",
			proposed: OwnerOverrides{Consolidation: TargetOverrides{Backend: "anthropic"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newServiceStoreStub()
			service := newSettingsService(t, store)

			_, err := service.UpdateSettings(t.Context(), settingsPrincipal, tc.proposed)
			if err == nil {
				t.Fatal("UpdateSettings() = nil error, want a policy refusal")
			}
			if _, ok := errors.AsType[*PolicyError](err); !ok {
				t.Fatalf("UpdateSettings() error %v is not a *PolicyError", err)
			}
			if len(store.writtenOverrides) != 0 {
				t.Fatalf("the refused write stored %d override sets, want none", len(store.writtenOverrides))
			}
		})
	}
}

// TestOwnerSettings_AWriteIsRefusedWhenTheActualTargetDoesNotCoverIt pins
// the write-path validation of design §3 and Requirements 1.4 and 1.5: the
// proposed target is checked against the final compiled actual target before
// anything is stored, so a target the actual target does not cover is refused
// on the write path instead of being persisted.
//
// The proposed effort is locally permitted: the consolidation phase allows the
// "high" effort, so only the compiled actual target can refuse it.
func TestOwnerSettings_AWriteIsRefusedWhenTheActualTargetDoesNotCoverIt(t *testing.T) {
	t.Parallel()

	proposed := OwnerOverrides{Consolidation: TargetOverrides{ReasoningEffort: "high"}}

	cases := []struct {
		name string
		// compiler is the generation-bound compiler of the case.
		compiler *stubTargetCompiler
		// wantPhase is the phase whose actual target refused the write; it is
		// empty when the refusal may name either governed phase.
		wantPhase Phase
	}{
		{
			name:     "the compiler is unavailable",
			compiler: &stubTargetCompiler{err: errors.New("the target compiler is unavailable")},
		},
		{
			name: "the compiled target is outside the allowed inventory",
			compiler: &stubTargetCompiler{bySelector: map[string]stubCompiledCandidate{
				"openai:gpt-5-mini": {backend: "openai", model: "gpt-5-mini", effort: "low", caps: reasoningCaps()},
				"openai:gpt-5":      {backend: "not-allowed", model: "not-allowed", effort: "high", caps: reasoningCaps()},
			}},
			wantPhase: PhaseConsolidation,
		},
		{
			name: "the compiled target cannot carry the reasoning effort",
			compiler: &stubTargetCompiler{bySelector: map[string]stubCompiledCandidate{
				"openai:gpt-5-mini": {backend: "openai", model: "gpt-5-mini", effort: "low", caps: reasoningCaps()},
				"openai:gpt-5":      {backend: "openai", model: "gpt-5", effort: "high", caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming)},
			}},
			wantPhase: PhaseConsolidation,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newServiceStoreStub()
			// The owner already stored a row: a refused write must leave it
			// exactly as it is.
			stored := OwnerOverrides{Consolidation: TargetOverrides{ReasoningEffort: "medium"}}
			store.overrides = stored
			cfg := decodeModelPolicyConfig(t, configModelsLocks)
			cfg.TargetCompiler = tc.compiler
			service := NewService(cfg, store, nil, nil)

			_, err := service.UpdateSettings(t.Context(), settingsPrincipal, proposed)
			if err == nil {
				t.Fatal("UpdateSettings() = nil error, want a refusal against the compiled actual target")
			}
			var policyErr *PolicyError
			if !errors.As(err, &policyErr) {
				t.Fatalf("UpdateSettings() error %v is not a *PolicyError", err)
			}
			if policyErr.Phase != PhaseExtraction && policyErr.Phase != PhaseConsolidation {
				t.Fatalf("refusal phase = %q, want a governed phase", policyErr.Phase)
			}
			if tc.wantPhase != "" && policyErr.Phase != tc.wantPhase {
				t.Fatalf("refusal phase = %q, want %q", policyErr.Phase, tc.wantPhase)
			}
			if len(store.writtenOverrides) != 0 {
				t.Fatalf("the refused write stored %d override sets, want none", len(store.writtenOverrides))
			}
			if store.overrides != stored {
				t.Fatalf("the stored overrides = %+v, want %+v: the refused write changed the stored row", store.overrides, stored)
			}
			if len(tc.compiler.calls) == 0 {
				t.Fatal("the write never compiled the actual target")
			}
		})
	}
}

// TestOwnerSettings_AnAcceptedWriteStoresTheOwnersOwnValues pins the write
// path: the accepted set is the owner's own permitted values, and the
// operator's locked fields carry no owner value.
func TestOwnerSettings_AnAcceptedWriteStoresTheOwnersOwnValues(t *testing.T) {
	t.Parallel()

	store := newServiceStoreStub()
	cfg := decodeModelPolicyConfig(t, configModelsLocks)
	cfg.TargetCompiler = settingsTargetCompiler()
	service := NewService(cfg, store, nil, nil)

	proposed := OwnerOverrides{Consolidation: TargetOverrides{ReasoningEffort: "high"}}
	projection, err := service.UpdateSettings(t.Context(), settingsPrincipal, proposed)
	if err != nil {
		t.Fatalf("UpdateSettings() error: %v", err)
	}
	if len(store.writtenOverrides) != 1 {
		t.Fatalf("stored override sets = %d, want 1", len(store.writtenOverrides))
	}
	if want := (OwnerOverrides{
		Consolidation: TargetOverrides{ReasoningEffort: "high"},
	}); store.writtenOverrides[0] != want {
		t.Fatalf("stored overrides = %+v, want %+v", store.writtenOverrides[0], want)
	}
	if len(store.writtenPrincipals) != 1 || store.writtenPrincipals[0] != settingsPrincipal {
		t.Fatalf("stored principals = %+v, want [%+v]", store.writtenPrincipals, settingsPrincipal)
	}
	if want := (PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "high"}); phaseOf(t, projection, PhaseConsolidation).Effective != want {
		t.Fatalf("effective target = %+v, want %+v", phaseOf(t, projection, PhaseConsolidation).Effective, want)
	}
}

// TestOwnerSettings_AWriteConsultsTheDeploymentFacts pins the write-path
// factual validation of design §3: the proposed target is checked against the
// deployment facts of the compiled actual target, a refused write persists
// nothing, and the seeded row stays unchanged.
//
// The facts arrive through the compiler's facts extension, the same extension the
// composition root implements: the installation state of the candidate's
// (backend, model) pair and the provider's own answer for the effort value.
func TestOwnerSettings_AWriteConsultsTheDeploymentFacts(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		installation  int
		effortSupport int
		strict        bool
		wantRefusal   bool
	}{
		{
			name:          "the deployment does not carry the model",
			installation:  2,
			effortSupport: 1,
			wantRefusal:   true,
		},
		{
			name:          "the provider does not support the effort",
			installation:  1,
			effortSupport: 2,
			wantRefusal:   true,
		},
		{
			name:          "the strict policy refuses an unproven fact",
			installation:  0,
			effortSupport: 0,
			strict:        true,
			wantRefusal:   true,
		},
		{
			name:          "the default policy serves an unproven fact",
			installation:  0,
			effortSupport: 0,
		},
		{
			name:          "the proven facts pass",
			installation:  1,
			effortSupport: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			store := newServiceStoreStub()
			store.overrides = OwnerOverrides{Consolidation: TargetOverrides{ReasoningEffort: "medium"}}
			cfg := decodeModelPolicyConfig(t, configModelsLocks)
			cfg.Models.Strict = tc.strict
			cfg.TargetCompiler = &factsTargetCompiler{
				stubTargetCompiler: stubTargetCompiler{bySelector: map[string]stubCompiledCandidate{
					"openai:gpt-5-mini": {backend: "openai", model: "gpt-5-mini", effort: "low", caps: reasoningCaps()},
					"openai:gpt-5":      {backend: "openai", model: "gpt-5", effort: "high", caps: reasoningCaps()},
				}},
				installation:  tc.installation,
				effortSupport: tc.effortSupport,
			}
			service := NewService(cfg, store, nil, nil)

			_, err := service.UpdateSettings(t.Context(), settingsPrincipal, OwnerOverrides{
				Consolidation: TargetOverrides{ReasoningEffort: "high"},
			})
			if tc.wantRefusal {
				if err == nil {
					t.Fatal("UpdateSettings() = nil error, want a refusal against the deployment facts")
				}
				if _, ok := errors.AsType[*PolicyError](err); !ok {
					t.Fatalf("UpdateSettings() error %v is not a *PolicyError", err)
				}
				if len(store.writtenOverrides) != 0 {
					t.Fatalf("the refused write stored %d override sets, want none", len(store.writtenOverrides))
				}
				if store.overrides != (OwnerOverrides{Consolidation: TargetOverrides{ReasoningEffort: "medium"}}) {
					t.Fatalf("the stored overrides = %+v, want the seeded row unchanged", store.overrides)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateSettings() error: %v", err)
			}
			if len(store.writtenOverrides) != 1 {
				t.Fatalf("the accepted write stored %d override sets, want one", len(store.writtenOverrides))
			}
		})
	}
}

// factsTargetCompiler is the target compiler of the factual write cases: it
// answers the deployment facts of every candidate beside the compiled target.
type factsTargetCompiler struct {
	stubTargetCompiler
	installation  int
	effortSupport int
}

func (c *factsTargetCompiler) CompileTargetFacts(
	ctx context.Context,
	call aipapi.Call,
	visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, installation, effortSupport int),
) error {
	return c.CompileTarget(ctx, call, func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps) {
		visit(key, backend, model, options, capabilities, c.installation, c.effortSupport)
	})
}

// TestOwnerSettings_TheDeleteRevokesTheCallersSettings pins the delete path:
// the revocation reaches the store with the caller's own principal.
func TestOwnerSettings_TheDeleteRevokesTheCallersSettings(t *testing.T) {
	t.Parallel()

	store := newServiceStoreStub()
	service := newSettingsService(t, store)

	if err := service.DeleteSettings(t.Context(), settingsPrincipal); err != nil {
		t.Fatalf("DeleteSettings() error: %v", err)
	}
	if len(store.deletedPrincipals) != 1 || store.deletedPrincipals[0] != settingsPrincipal {
		t.Fatalf("deleted principals = %+v, want [%+v]", store.deletedPrincipals, settingsPrincipal)
	}
}

// TestOwnerSettings_WithoutAModelPolicyTheSurfaceIsUnknown pins that a
// deployment without a per-phase model policy has no per-phase settings to
// show, so the resource answers as an unknown one.
func TestOwnerSettings_WithoutAModelPolicyTheSurfaceIsUnknown(t *testing.T) {
	t.Parallel()

	store := newServiceStoreStub()
	cfg := DefaultConfig()
	service := NewService(cfg, store, nil, nil)

	if _, err := service.Settings(t.Context(), settingsPrincipal); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Settings() error = %v, want ErrNotFound", err)
	}
	if _, err := service.UpdateSettings(t.Context(), settingsPrincipal, OwnerOverrides{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateSettings() error = %v, want ErrNotFound", err)
	}
}

// TestOwnerSettings_AnInvalidLaterLeafOfTheOperatorRouteStaysRefused pins
// that every leaf of the operator's own route is compiled and checked, not
// only the first one: a later leaf the phase's inventory does not cover
// refuses the write, and the seeded rows of both phases stay unchanged.
//
// The refusal names the later leaf's model, which proves that the leaf was
// really compiled and checked.
func TestOwnerSettings_AnInvalidLaterLeafOfTheOperatorRouteStaysRefused(t *testing.T) {
	t.Parallel()

	store := newServiceStoreStub()
	stored := OwnerOverrides{
		Extraction:    TargetOverrides{ReasoningEffort: "low"},
		Consolidation: TargetOverrides{ReasoningEffort: "medium"},
	}
	store.overrides = stored
	cfg := multiTargetConfig()
	// The phase allows the first leaf of the operator's route only, so the
	// second leaf is outside its allowed inventory.
	policy := multiTargetPolicy(PhaseExtraction)
	policy.Inventory.Models = []string{"gpt-5"}
	policy.Inventory.Targets = []TargetPolicy{
		{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium", "high"}},
	}
	cfg.Models.Extraction = policy
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the operator configuration is not valid: %v", err)
	}
	compiler := &failoverTargetCompiler{leaves: []stubCompiledCandidate{
		{backend: "openai", model: "gpt-5", caps: reasoningCaps()},
		{backend: "openai", model: "gpt-4", caps: reasoningCaps()},
	}}
	cfg.TargetCompiler = compiler
	service := NewService(cfg, store, nil, nil)

	_, err := service.UpdateSettings(t.Context(), settingsPrincipal, OwnerOverrides{
		Consolidation: TargetOverrides{ReasoningEffort: "high"},
	})
	if err == nil {
		t.Fatal("UpdateSettings() = nil error, want the refusal of the later leaf")
	}
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("UpdateSettings() error %v is not a *PolicyError", err)
	}
	if policyErr.Phase != PhaseExtraction || policyErr.Field != FieldModel {
		t.Fatalf("refusal = %+v, want the extraction phase's model field", policyErr)
	}
	if len(store.writtenOverrides) != 0 {
		t.Fatalf("the refused write stored %d override sets, want none", len(store.writtenOverrides))
	}
	if store.overrides != stored {
		t.Fatalf("the stored overrides = %+v, want %+v: the refused write changed the stored rows", store.overrides, stored)
	}
}

// phaseOf returns the projection entry of the named phase.
func phaseOf(t *testing.T, projection SettingsProjection, phase Phase) PhaseSettings {
	t.Helper()
	for _, entry := range projection.Phases {
		if entry.Phase == phase {
			return entry
		}
	}
	t.Fatalf("the projection carries no %q phase", phase)
	return PhaseSettings{}
}

// multiTargetRoute is the operator's own route: a single target whose alias
// the runtime expands to two leaves, and the phase names no target of its
// own, so the route stays the operator's. A declared policy refuses a route
// that names several targets itself (design §2), so an alias is how one
// phase route still reaches more than one leaf.
const multiTargetRoute = "openai:memory-pool"

// multiTargetPolicy returns the policy of a phase whose operator named no
// target: the phase keeps the operator's own route, which may name several
// targets, and the owner may choose a reasoning effort for it.
func multiTargetPolicy(phase Phase) PhasePolicy {
	return PhasePolicy{
		Defaults: PhaseTarget{ReasoningEffort: defaultPhaseEffort(phase)},
		Inventory: PhaseInventory{
			Backends: []string{"openai"},
			Models:   []string{"gpt-5", "gpt-4"},
			// The owner's effort is checked against the requested target,
			// the alias, and every compiled leaf is checked against its own
			// entry.
			Targets: []TargetPolicy{
				{Backend: "openai", Model: "memory-pool", ReasoningEfforts: []string{"low", "medium", "high"}},
				{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium", "high"}},
				{Backend: "openai", Model: "gpt-4", ReasoningEfforts: []string{"low", "medium", "high"}},
			},
		},
	}
}

// multiTargetConfig returns the operator configuration of the multi-target
// route cases: both phases keep the operator's own failover route and name no
// target of their own.
func multiTargetConfig() Config {
	cfg := DefaultConfig()
	cfg.Principals = []string{"settings-alice"}
	cfg.ModelsDeclared = true
	cfg.ExtractRoute = multiTargetRoute
	cfg.ConsolidateRoute = multiTargetRoute
	cfg.Models.Extraction = multiTargetPolicy(PhaseExtraction)
	cfg.Models.Consolidation = multiTargetPolicy(PhaseConsolidation)
	return cfg
}

// failoverTargetCompiler is the target compiler of the operator's aliased
// route cases: the alias expands to two leaves, so the compilation reports
// both of them, the way the runtime's candidate extraction does, and it
// records what it was asked to compile.
type failoverTargetCompiler struct {
	compilations []failoverCompilation
	leaves       []stubCompiledCandidate
}

// failoverCompilation is one compilation the failover compiler received: the
// selector it was asked to compile and the number of leaves it reported.
type failoverCompilation struct {
	selector string
	leaves   int
}

func (c *failoverTargetCompiler) CompileTarget(_ context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps)) error {
	c.compilations = append(c.compilations, failoverCompilation{selector: call.Route.Selector, leaves: len(c.leaves)})
	for _, leaf := range c.leaves {
		visit(leaf.key(), leaf.backend, leaf.model,
			aipapi.GenerationOptions{ReasoningEffort: call.Options.ReasoningEffort}, leaf.caps)
	}
	return nil
}

func (c *failoverTargetCompiler) CompileIdentity(_ context.Context, call aipapi.Call, visit func(backend, model string)) error {
	c.compilations = append(c.compilations, failoverCompilation{selector: call.Route.Selector, leaves: len(c.leaves)})
	for _, leaf := range c.leaves {
		visit(leaf.backend, leaf.model)
	}
	return nil
}

// TestOwnerSettings_AnUntouchedMultiTargetRouteDoesNotBlockAnOtherPhaseWrite
// pins Requirement 1.3, Requirement 2.1 and Requirement 2.2 on the write
// path: a phase whose operator named no target keeps the operator's own
// aliased route, so an owner write to the *other* phase is not refused by
// the untouched phase, and the whole operator route is compiled and checked
// instead of being collapsed to its first leaf.
func TestOwnerSettings_AnUntouchedMultiTargetRouteDoesNotBlockAnOtherPhaseWrite(t *testing.T) {
	t.Parallel()

	store := newServiceStoreStub()
	stored := OwnerOverrides{Consolidation: TargetOverrides{ReasoningEffort: "medium"}}
	store.overrides = stored
	cfg := multiTargetConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the operator configuration is not valid: %v", err)
	}
	compiler := &failoverTargetCompiler{leaves: []stubCompiledCandidate{
		{backend: "openai", model: "gpt-5", caps: reasoningCaps()},
		{backend: "openai", model: "gpt-4", caps: reasoningCaps()},
	}}
	cfg.TargetCompiler = compiler
	service := NewService(cfg, store, nil, nil)

	_, err := service.UpdateSettings(t.Context(), settingsPrincipal, OwnerOverrides{
		Consolidation: TargetOverrides{ReasoningEffort: "high"},
	})
	if err != nil {
		t.Fatalf("UpdateSettings() error: %v", err)
	}
	if len(store.writtenOverrides) != 1 {
		t.Fatalf("stored override sets = %d, want 1", len(store.writtenOverrides))
	}
	if want := (OwnerOverrides{Consolidation: TargetOverrides{ReasoningEffort: "high"}}); store.writtenOverrides[0] != want {
		t.Fatalf("stored overrides = %+v, want %+v", store.writtenOverrides[0], want)
	}
	// Both phases keep the operator's own route, so both of its leaves are
	// compiled and checked for each of them: the untouched phase is not
	// collapsed to the first leaf of the route.
	if len(compiler.compilations) != 2 {
		t.Fatalf("compilations = %+v, want one per phase", compiler.compilations)
	}
	for _, compilation := range compiler.compilations {
		if compilation.selector != multiTargetRoute {
			t.Fatalf("compiled selector = %q, want the operator's own route %q", compilation.selector, multiTargetRoute)
		}
		if compilation.leaves != 2 {
			t.Fatalf("compiled leaves = %d, want every leaf of the operator's route", compilation.leaves)
		}
	}
}
