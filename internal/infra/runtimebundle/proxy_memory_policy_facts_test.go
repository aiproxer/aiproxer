package runtimebundle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// The write-path factual validation of design §3: a settings write resolves the
// proposed effective target against the final compiled actual target and
// refuses a target the deployment does not carry or the provider does not
// support, before it persists anything (Requirement 1.4; Error Handling:141).

// factsSettingsStore records the settings writes the service performs.
type factsSettingsStore struct {
	proxymemory.Store
	stored proxymemory.OwnerOverrides
	writes int
}

func (s *factsSettingsStore) WriteSettings(_ context.Context, _ proxymemory.Principal, values proxymemory.StoredOwnerSettings) error {
	s.writes++
	s.stored = values.Overrides()
	return nil
}

// factsInventory is a model-inventory provider whose fresh enumeration carries
// the given models, so the registry publication proves or disproves the
// installation of a model.
type factsInventory struct {
	models []modelinventory.Model
}

func (p factsInventory) LoadModels(context.Context) (modelinventory.Snapshot, error) {
	return modelinventory.Snapshot{
		Source: modelinventory.SourceRemote,
		Models: append([]modelinventory.Model(nil), p.models...),
	}, nil
}

// factsRegistry returns the frozen model-registry publication of an inventory
// that carries the given models.
func factsRegistry(t *testing.T, models ...modelinventory.Model) modelregistry.BoundView {
	t.Helper()
	runtime := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       "openai",
			Kind:            "openai-responses",
			BackendPrefixes: []string{"openai"},
			Provider:        factsInventory{models: models},
		}},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	})
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("start the model registry: %v", err)
	}
	return runtime.BoundView()
}

// factsSettingsConfig returns the operator policy the factual cases share: both
// phases run the "openai:gpt-5" target and allow the "medium" and "high"
// efforts on it.
func factsSettingsConfig(strict bool) proxymemory.Config {
	cfg := proxymemory.DefaultConfig()
	cfg.ModelsDeclared = true
	cfg.Models.Strict = strict
	cfg.ExtractRoute = "openai:gpt-5"
	cfg.ConsolidateRoute = "openai:gpt-5"
	policy := proxymemory.PhasePolicy{
		Defaults: proxymemory.PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "medium"},
		Inventory: proxymemory.PhaseInventory{
			Backends: []string{"openai"}, Models: []string{"gpt-5"},
			Targets: []proxymemory.TargetPolicy{{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"medium", "high"}}},
		},
	}
	cfg.Models.Extraction = policy
	cfg.Models.Consolidation = policy
	return cfg
}

// factsExecutor returns an executor whose single backend answers the
// provider-owned per-value question with the given support state, and whose
// frozen registry publication carries the given models.
func factsExecutor(t *testing.T, support execbackend.EffortSupportState, registry modelregistry.BoundView) *runtime.Executor {
	t.Helper()
	exec := proxyMemoryTestExecutor(t, nil)
	exec.CapsResolver = nil
	exec.CatalogResolver = nil
	exec.BoundRegistry = registry
	exec.Backends = map[string]execbackend.Backend{"openai": {
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning),
		ResolveEffortSupport: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate, effort string) execbackend.EffortSupportState {
			if effort == "high" {
				return support
			}
			return execbackend.EffortSupportSupported
		},
	}}
	return exec
}

// TestSettingsWriteRefusesTheFactsTheActualTargetDoesNotProve pins the
// write-path factual validation over the real composition-root binding: the
// provider's own answer and the deployment's installation state decide the
// write, and a refused write persists nothing.
func TestSettingsWriteRefusesTheFactsTheActualTargetDoesNotProve(t *testing.T) {
	t.Parallel()

	installed := modelinventory.Model{CanonicalID: "openai/gpt-5", NativeID: "gpt-5"}
	other := modelinventory.Model{CanonicalID: "openai/gpt-4", NativeID: "gpt-4"}

	cases := []struct {
		name        string
		strict      bool
		support     execbackend.EffortSupportState
		registry    modelregistry.BoundView
		wantRefusal bool
	}{
		{
			name:        "the provider explicitly rejects the effort",
			support:     execbackend.EffortSupportUnsupported,
			registry:    factsRegistry(t, installed),
			wantRefusal: true,
		},
		{
			name:        "the strict policy refuses an unproven support",
			strict:      true,
			support:     execbackend.EffortSupportUnknown,
			registry:    factsRegistry(t, installed),
			wantRefusal: true,
		},
		{
			name:        "the deployment does not carry the model",
			support:     execbackend.EffortSupportSupported,
			registry:    factsRegistry(t, other),
			wantRefusal: true,
		},
		{
			name:     "the default policy serves an unproven support",
			support:  execbackend.EffortSupportUnknown,
			registry: factsRegistry(t, installed),
		},
		{
			name:     "the strict policy serves the proven facts",
			strict:   true,
			support:  execbackend.EffortSupportSupported,
			registry: factsRegistry(t, installed),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := factsSettingsConfig(tc.strict)
			cfg.TargetCompiler = newProxyMemoryTargetCompiler().bind(factsExecutor(t, tc.support, tc.registry))
			store := &factsSettingsStore{stored: proxymemory.OwnerOverrides{
				Extraction: proxymemory.TargetOverrides{ReasoningEffort: "medium"},
			}}
			service := proxymemory.NewService(cfg, store, nil, nil)

			_, err := service.UpdateSettings(t.Context(), proxymemory.Principal{
				TenantID: "facts-tenant", PrincipalID: "facts-owner",
			}, proxymemory.OwnerOverrides{
				Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "high"},
			})

			if tc.wantRefusal {
				if err == nil {
					t.Fatal("UpdateSettings() = nil error, want a refusal against the actual target")
				}
				if store.writes != 0 {
					t.Fatalf("the refused write persisted %d rows, want none", store.writes)
				}
				if store.stored.Extraction.ReasoningEffort != "medium" {
					t.Fatalf("the stored row = %+v, want the seeded row unchanged", store.stored)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateSettings() error: %v", err)
			}
			if store.writes != 1 {
				t.Fatalf("the accepted write persisted %d rows, want one", store.writes)
			}
		})
	}
}

// canonicalSettingsConfig returns the operator policy the canonical-model
// cases share: both phases address the canonical id "openai/gpt-5", whose
// native id the deployment's registry publishes as "gpt-5".
//
// The canonical spelling and the native spelling are deliberately different,
// so a query about the logical name is distinguishable from a query about the
// model the provider actually serves.
func canonicalSettingsConfig(strict bool) proxymemory.Config {
	cfg := factsSettingsConfig(strict)
	cfg.Principals = []string{"owner"}
	cfg.ExtractRoute = "openai:openai/gpt-5"
	cfg.ConsolidateRoute = "openai:openai/gpt-5"
	policy := cfg.Models.Extraction
	policy.Defaults.Model = "openai/gpt-5"
	policy.Inventory.Models = []string{"openai/gpt-5"}
	policy.Inventory.Targets = []proxymemory.TargetPolicy{
		{Backend: "openai", Model: "openai/gpt-5", ReasoningEfforts: []string{"medium", "high"}},
	}
	cfg.Models.Extraction = policy
	cfg.Models.Consolidation = policy
	return cfg
}

// TestSettingsWriteConsultsTheProviderAboutTheNativeModel pins the
// write-path factual validation of design §3 against the *actual* model the
// provider serves: the provider-owned per-value effort fact is asked about the
// deployment's native id and not about the logical canonical name, so a
// provider that refuses the effort on its native model refuses the write and
// the stored row stays unchanged (Requirement 1.4, Requirement 1.5;
// Error Handling:141).
func TestSettingsWriteConsultsTheProviderAboutTheNativeModel(t *testing.T) {
	t.Parallel()

	registry := factsRegistry(t, modelinventory.Model{CanonicalID: "openai/gpt-5", NativeID: "gpt-5"})
	native, ok := registry.ResolveNative("openai", "openai/gpt-5")
	if !ok || native != "gpt-5" {
		t.Fatalf("the frozen registry binding = %q,%v, want gpt-5,true", native, ok)
	}

	cases := []struct {
		name        string
		strict      bool
		support     execbackend.EffortSupportState
		wantRefusal bool
	}{
		{
			name:        "the provider refuses the effort on its native model",
			support:     execbackend.EffortSupportUnsupported,
			wantRefusal: true,
		},
		{
			name:        "the strict policy refuses an unproven support",
			strict:      true,
			support:     execbackend.EffortSupportUnknown,
			wantRefusal: true,
		},
		{
			name:    "the provider supports the effort on its native model",
			support: execbackend.EffortSupportSupported,
		},
		{
			name:    "the strict policy serves the proven facts",
			strict:  true,
			support: execbackend.EffortSupportSupported,
		},
		{
			name:    "the default policy serves an unproven support",
			support: execbackend.EffortSupportUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exec := factsExecutor(t, execbackend.EffortSupportSupported, registry)
			// The provider publishes a graded fact for its native model only:
			// every other spelling is an unproven model for it.
			asked := map[string]string{}
			backend := exec.Backends["openai"]
			backend.ResolveEffortSupport = func(_ context.Context, _ aipapi.Call, cand routing.AttemptCandidate, effort string) execbackend.EffortSupportState {
				asked[effort] = cand.Primary.Model
				if cand.Primary.Model != "gpt-5" {
					return execbackend.EffortSupportUnknown
				}
				if effort == "high" {
					return tc.support
				}
				return execbackend.EffortSupportSupported
			}
			exec.Backends["openai"] = backend

			cfg := canonicalSettingsConfig(tc.strict)
			cfg.TargetCompiler = newProxyMemoryTargetCompiler().bind(exec)
			if err := cfg.Validate(); err != nil {
				t.Fatalf("the operator configuration is not valid: %v", err)
			}
			old := proxymemory.OwnerOverrides{Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "medium"}}
			store := &factsSettingsStore{stored: old}
			service := proxymemory.NewService(cfg, store, nil, nil)

			_, err := service.UpdateSettings(t.Context(), proxymemory.Principal{
				TenantID: "facts-tenant", PrincipalID: "facts-owner",
			}, proxymemory.OwnerOverrides{
				Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "high"},
			})

			if asked["high"] != "gpt-5" {
				t.Fatalf("the provider was asked about %q, want the native id gpt-5 (err=%v writes=%d)", asked["high"], err, store.writes)
			}
			if tc.wantRefusal {
				if err == nil {
					t.Fatalf("the write was accepted, want a refusal against the actual target")
				}
				if store.writes != 0 {
					t.Fatalf("the refused write persisted %d rows, want none", store.writes)
				}
				if store.stored != old {
					t.Fatalf("the stored row = %+v, want the seeded row unchanged", store.stored)
				}
				return
			}
			if err != nil {
				t.Fatalf("UpdateSettings() error: %v", err)
			}
			if store.writes != 1 {
				t.Fatalf("the accepted write persisted %d rows, want one", store.writes)
			}
		})
	}
}

// TestSettingsWriteRefusesACanonicalOfAnotherBackend pins the bounded
// refusal of a route that names a canonical id the deployment publishes for
// another backend: the write path now agrees with the dispatch path, which
// fails closed on the same leaf, and the refused write persists nothing.
func TestSettingsWriteRefusesACanonicalOfAnotherBackend(t *testing.T) {
	t.Parallel()

	registry := factsRegistry(t, modelinventory.Model{CanonicalID: "openai/gpt-5", NativeID: "gpt-5"})
	exec := factsExecutor(t, execbackend.EffortSupportSupported, registry)
	// The route names the canonical id under a backend the deployment does
	// not publish it for.
	exec.Backends["anthropic"] = execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning),
	}
	cfg := canonicalSettingsConfig(false)
	cfg.ExtractRoute = "anthropic:openai/gpt-5"
	cfg.ConsolidateRoute = "anthropic:openai/gpt-5"
	policy := cfg.Models.Extraction
	policy.Defaults.Backend = "anthropic"
	policy.Inventory.Backends = []string{"anthropic"}
	policy.Inventory.Targets = []proxymemory.TargetPolicy{
		{Backend: "anthropic", Model: "openai/gpt-5", ReasoningEfforts: []string{"medium", "high"}},
	}
	cfg.Models.Extraction = policy
	cfg.Models.Consolidation = policy
	cfg.TargetCompiler = newProxyMemoryTargetCompiler().bind(exec)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("the operator configuration is not valid: %v", err)
	}

	old := proxymemory.OwnerOverrides{Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "medium"}}
	store := &factsSettingsStore{stored: old}
	service := proxymemory.NewService(cfg, store, nil, nil)

	_, err := service.UpdateSettings(t.Context(), proxymemory.Principal{
		TenantID: "facts-tenant", PrincipalID: "facts-owner",
	}, proxymemory.OwnerOverrides{
		Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "high"},
	})
	if err == nil {
		t.Fatalf("the write was accepted, want the bounded refusal of a target that does not compile")
	}
	var policyErr *proxymemory.PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("UpdateSettings() error %v is not a *PolicyError", err)
	}
	if policyErr.Field != "route" {
		t.Fatalf("refusal = %+v, want the route field", policyErr)
	}
	if store.writes != 0 {
		t.Fatalf("the refused write persisted %d rows, want none", store.writes)
	}
	if store.stored != old {
		t.Fatalf("the stored row = %+v, want the seeded row unchanged", store.stored)
	}
}

// TestSettingsWriteRefusesAnUnknownReasoningCapability pins the write path as
// the primary refusal (Requirement 1.4): the deployment proves the model's
// installation and the provider carries the effort value, but the model
// catalog does not prove the model's reasoning capability. The unknown state
// is not support, so the write is refused and persists nothing.
func TestSettingsWriteRefusesAnUnknownReasoningCapability(t *testing.T) {
	t.Parallel()

	registry := factsRegistry(t, modelinventory.Model{CanonicalID: "openai/gpt-5", NativeID: "gpt-5"})
	exec := factsExecutor(t, execbackend.EffortSupportSupported, registry)
	exec.CatalogResolver = proxyMemoryReasoningCatalog{
		reasoning: modelcatalog.CapabilityUnknown,
		matched:   true,
	}
	cfg := factsSettingsConfig(false)
	cfg.TargetCompiler = newProxyMemoryTargetCompiler().bind(exec)
	store := &factsSettingsStore{stored: proxymemory.OwnerOverrides{
		Extraction: proxymemory.TargetOverrides{ReasoningEffort: "medium"},
	}}
	service := proxymemory.NewService(cfg, store, nil, nil)

	_, err := service.UpdateSettings(t.Context(), proxymemory.Principal{
		TenantID: "facts-tenant", PrincipalID: "facts-owner",
	}, proxymemory.OwnerOverrides{
		Consolidation: proxymemory.TargetOverrides{ReasoningEffort: "high"},
	})
	if err == nil {
		t.Fatal("UpdateSettings() = nil error, want a refusal for the unproven reasoning capability")
	}
	var policyErr *proxymemory.PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("UpdateSettings() error %v is not a *PolicyError", err)
	}
	if policyErr.Field != proxymemory.FieldReasoningEffort {
		t.Fatalf("refusal = %+v, want the reasoning-effort field", policyErr)
	}
	if store.writes != 0 {
		t.Fatalf("the refused write persisted %d rows, want none", store.writes)
	}
	if store.stored.Extraction.ReasoningEffort != "medium" {
		t.Fatalf("the stored row = %+v, want the seeded row unchanged", store.stored)
	}
}
