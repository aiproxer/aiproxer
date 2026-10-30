package runtimebundle_test

// The U1-U12 user-control certification cases that were not already covered by
// the composed model-policy and claim-settings proofs.
//
// Every case here runs the same real path as the task 2.2 and 3.2 fixtures:
// the production process background scheduler bound to the real generation
// executor runner, the real feature worker, the real generic-core runtime
// executor and a scripted provider that observes the actual backend, model and
// canonical options of each open. The billing seams are the fixture's normal
// composition (credit gate, exposure admission and terminal usage sink); only
// the provider's content is stubbed.

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/infra/proxymemorystore"
	"github.com/aiproxer/aiproxer/internal/plugins/features/proxymemory"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/uptrace/bun"
)

// proxyMemoryMultiModelBackend declares the extra models the user-control
// cases select. The fixture's default backend declares only openai/test-model,
// and the deployment's installation fact is proven from this frozen inventory.
func proxyMemoryMultiModelBackend(t *testing.T, cfg *config.Config) {
	t.Helper()
	require.NotEmpty(t, cfg.Plugins.Backends, "the fixture must declare the backend it serves")
	const inventory = `models:
  source: inline
  items:
    - canonical_id: openai/test-model
      native_id: test-model
      display_name: Test Model
    - canonical_id: openai/alternate
      native_id: alternate
      display_name: Alternate Model
    - canonical_id: openai/third-model
      native_id: third-model
      display_name: Third Model
`
	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(inventory), &node))
	require.NotEmpty(t, node.Content)
	cfg.Plugins.Backends[0].Config = *node.Content[0]
}

// proxyMemoryPhasePolicy replaces one phase's declared defaults and lock set,
// so a case can state a per-phase operator policy without restating the whole
// feature configuration.
func proxyMemoryPhasePolicy(t *testing.T, cfg *config.Config, phase, defaults, locks string) {
	t.Helper()
	proxyMemoryPolicyEditFeature(t, cfg,
		"    "+phase+":\n        defaults: {}\n        locks: {}",
		"    "+phase+":\n        defaults: {"+defaults+"}\n        locks: {"+locks+"}")
}

// proxyMemoryWriteOwnerSettings writes one owner's stored overrides through
// the production store, with the write-time dependency context the
// authenticated settings service records for an accepted target. The context
// names the effective target the override was accepted against, so the claim
// path distinguishes a removed default from a narrowed inventory exactly as it
// does for a service write.
func proxyMemoryWriteOwnerSettings(
	t *testing.T,
	memory *bun.DB,
	tenant, principal string,
	extraction proxymemory.TargetOverrides,
	extractionModel string,
	consolidation proxymemory.TargetOverrides,
	consolidationModel string,
) {
	t.Helper()
	contextFor := func(overrides proxymemory.TargetOverrides, model string) proxymemory.DependencyContext {
		return proxymemory.DependencyContext{
			Present:          true,
			Backend:          "openai",
			Model:            model,
			InheritedBackend: overrides.Backend == "",
			InheritedModel:   overrides.Model == "",
		}
	}
	store := proxymemorystore.New(memory, nil)
	require.NoError(t, store.WriteSettings(context.Background(), proxymemory.Principal{
		TenantID: tenant, PrincipalID: principal,
	}, proxymemory.StoredOwnerSettings{
		Extraction: proxymemory.StoredPhaseSettings{
			Overrides:  extraction,
			Dependency: contextFor(extraction, extractionModel),
		},
		Consolidation: proxymemory.StoredPhaseSettings{
			Overrides:  consolidation,
			Dependency: contextFor(consolidation, consolidationModel),
		},
	}))
}

// proxyMemorySeedOwnedSession inserts one claimable session for the named
// owner, so one composed run can carry several users.
func proxyMemorySeedOwnedSession(t *testing.T, memory *bun.DB, aLegID, tenant, principal string, activityMS int64) {
	t.Helper()
	_, err := memory.NewRaw(`INSERT INTO proxy_memory_sessions
		(a_leg_id, tenant_id, principal_id, project_key, project_label, transcript, revision, last_activity_ms, source_seq)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, 1)`,
		aLegID, tenant, principal, "project-j5", "/w/project", "[]", activityMS,
	).Exec(context.Background())
	require.NoError(t, err)
}

// proxyMemoryLegsByRole returns the terminal leg rows of one auxiliary phase
// role: the billing evidence names the target that actually served the phase.
func proxyMemoryLegsByRole(legs []usageevidence.CallLegUsageRecord, role usageevidence.WorkloadRole) []usageevidence.CallLegUsageRecord {
	var out []usageevidence.CallLegUsageRecord
	for _, leg := range legs {
		if leg.Workload.Role == role {
			out = append(out, leg)
		}
	}
	return out
}

// proxyMemoryOpenForRoute returns the single provider open whose canonical
// route is the given selector. A missing or duplicated open fails, so one
// owner's expected extraction open cannot be satisfied by another owner's or
// another phase's open.
func proxyMemoryOpenForRoute(t *testing.T, opens []proxyMemoryPhaseCall, route string) proxyMemoryPhaseCall {
	t.Helper()
	var found []proxyMemoryPhaseCall
	for _, open := range opens {
		if open.route == route {
			found = append(found, open)
		}
	}
	require.Len(t, found, 1, "exactly one provider open must carry route %q", route)
	return found[0]
}

// proxyMemoryAssertServedOpen asserts the actual provider-open arguments of one
// phase, then follows the open's own B-leg identity to the terminal usage row
// of the same attempt. The provider-facing backend, model and canonical effort
// are the primary evidence; the usage row is linked to them rather than being
// treated as a substitute for them.
func proxyMemoryAssertServedOpen(
	t *testing.T,
	opens []proxyMemoryPhaseCall,
	legs []usageevidence.CallLegUsageRecord,
	route, backend, model, effort string,
	role usageevidence.WorkloadRole,
) {
	t.Helper()
	open := proxyMemoryOpenForRoute(t, opens, route)
	require.Equal(t, backend, open.backend,
		"the provider open must address the expected backend")
	require.Equal(t, model, open.model,
		"the provider open must address the expected model")
	require.Equal(t, effort, open.options.ReasoningEffort,
		"the provider open must carry the expected canonical reasoning effort")
	require.NotEmpty(t, open.bLegID,
		"the provider open must carry its own attempt identity")

	var served *usageevidence.CallLegUsageRecord
	for i := range legs {
		if legs[i].BLegID != open.bLegID {
			continue
		}
		require.Nil(t, served, "the provider open must have exactly one usage row")
		served = &legs[i]
	}
	require.NotNil(t, served, "the provider open must have its own terminal usage row")
	require.Equal(t, role, served.Workload.Role,
		"the linked usage row must name the phase that opened the provider")
	require.Equal(t, backend, served.BackendID,
		"the linked usage row must describe the served backend")
	require.Equal(t, model, served.ModelID,
		"the linked usage row must describe the served model")
}

// proxyMemoryThreeModelInventory allows the third model the U1 operator default
// names. With the operator default on a model neither owner selected, a claim
// that ignores either owner's stored model serves the third model and is
// observably wrong instead of coinciding with the owner's own value.
const proxyMemoryThreeModelInventory = `
      backends: [openai]
      models: [openai/test-model, openai/alternate, openai/third-model]
      targets:
        - backend: openai
          model: openai/test-model
          reasoning_efforts: [low, medium, high]
        - backend: openai
          model: openai/alternate
          reasoning_efforts: [low, medium, high]
        - backend: openai
          model: openai/third-model
          reasoning_efforts: [low, medium, high]`

// TestProxyMemoryUserControlTwoUsersChooseDifferentModels is U1: two
// different users choose different extraction models, and each user's job
// reaches the provider with that user's own model.
//
// The two ticks and the one-owner-per-tick sessions make the owner identity of
// each provider open independent of a set comparison: Alice's open is asserted
// against Alice's route/backend/model/effort and linked to Alice's phase-1
// usage row, then Bob's open is asserted the same way. A reciprocal settings
// lookup swap (Alice reading Bob and Bob reading Alice) fails at Alice's route
// assertion; a final-open model/effort rewrite fails at the open assertion.
func TestProxyMemoryUserControlTwoUsersChooseDifferentModels(t *testing.T) {
	const (
		aliceModel           = "openai/test-model"
		bobModel             = "openai/alternate"
		operatorDefaultModel = "openai/third-model"
	)
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryThreeModelInventory, true, func(cfg *config.Config) {
		proxyMemoryMultiModelBackend(t, cfg)
		proxyMemoryPhasePolicy(t, cfg, "extraction", "backend: openai, model: "+operatorDefaultModel, "")
		proxyMemoryPhasePolicy(t, cfg, "consolidation", "backend: openai, model: "+operatorDefaultModel, "")
	}, nil)

	proxyMemoryWriteOwnerSettings(t, fixture.memory, "tenant-j5", "alice",
		proxymemory.TargetOverrides{Model: aliceModel}, aliceModel,
		proxymemory.TargetOverrides{}, operatorDefaultModel)
	proxyMemoryWriteOwnerSettings(t, fixture.memory, "tenant-j5", "bob",
		proxymemory.TargetOverrides{Model: bobModel}, bobModel,
		proxymemory.TargetOverrides{}, operatorDefaultModel)

	// Both owners' distinguishable settings rows are present before the first
	// claim. Only the session of the owner under observation is seeded, so the
	// provider open observed after each tick belongs to that owner's job.
	proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-alice", "tenant-j5", "alice", time.Now().Add(-48*time.Hour).UnixMilli())

	fixture.worker.Tick(context.Background())
	aliceOpens := fixture.backend.observed()
	proxyMemoryAssertServedOpen(t, aliceOpens, fixture.sink.legsSnapshot(),
		"openai:"+aliceModel, "openai", aliceModel, "low",
		usageevidence.WorkloadRoleProxyMemoryPhase1)

	// Bob's session is seeded only after Alice's job finished. The second
	// observation therefore belongs to Bob's job, while both owners' settings
	// rows are still present.
	beforeBob := len(aliceOpens)
	proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-bob", "tenant-j5", "bob", time.Now().Add(-24*time.Hour).UnixMilli())
	fixture.worker.Tick(context.Background())
	bobOpens := fixture.backend.observed()[beforeBob:]
	proxyMemoryAssertServedOpen(t, bobOpens, fixture.sink.legsSnapshot(),
		"openai:"+bobModel, "openai", bobModel, "low",
		usageevidence.WorkloadRoleProxyMemoryPhase1)

	phase1 := proxyMemoryLegsByRole(fixture.sink.legsSnapshot(), usageevidence.WorkloadRoleProxyMemoryPhase1)
	require.Len(t, phase1, 2, "each user's extraction phase must reach the provider")
	models := make([]string, 0, len(phase1))
	for _, leg := range phase1 {
		models = append(models, leg.ModelID)
	}
	require.ElementsMatch(t, []string{aliceModel, bobModel}, models,
		"the linked usage rows must still name each user's own selected model")
	require.NotContains(t, models, operatorDefaultModel,
		"neither user's extraction phase may fall back to the operator default")
}

// TestProxyMemoryUserControlPerPhaseModelsAreServed is U2: one owner uses
// model A for extraction and model B for consolidation, and both phases
// dispatch to their own model in one composed run.
//
// The actual provider opens are asserted for backend, model, route and
// canonical effort, and each open is linked by its own B-leg to the usage row
// of its phase. A consolidation claim that reuses the extraction settings fails
// at the consolidation route/model; a final-open rewrite fails at the open
// assertion.
func TestProxyMemoryUserControlPerPhaseModelsAreServed(t *testing.T) {
	const (
		extractionModel    = "openai/test-model"
		consolidationModel = "openai/alternate"
	)
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryMaskedInventory, true, func(cfg *config.Config) {
		proxyMemoryMultiModelBackend(t, cfg)
		// Each phase's operator default is the other phase's model, so a claim
		// that ignores either owner override serves the wrong model for that
		// phase and the assertion below fails.
		proxyMemoryPhasePolicy(t, cfg, "extraction", "backend: openai, model: "+consolidationModel, "")
		proxyMemoryPhasePolicy(t, cfg, "consolidation", "backend: openai, model: "+extractionModel, "")
	}, nil)
	proxyMemoryWriteOwnerSettings(t, fixture.memory, "tenant-j5", "alice",
		proxymemory.TargetOverrides{Model: extractionModel}, extractionModel,
		proxymemory.TargetOverrides{Model: consolidationModel}, consolidationModel)
	proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-per-phase", "tenant-j5", "alice", time.Now().Add(-24*time.Hour).UnixMilli())

	fixture.worker.Tick(context.Background())

	opens := fixture.backend.observed()
	legs := fixture.sink.legsSnapshot()
	proxyMemoryAssertServedOpen(t, opens, legs,
		"openai:"+extractionModel, "openai", extractionModel, "low",
		usageevidence.WorkloadRoleProxyMemoryPhase1)
	proxyMemoryAssertServedOpen(t, opens, legs,
		"openai:"+consolidationModel, "openai", consolidationModel, "medium",
		usageevidence.WorkloadRoleProxyMemoryPhase2)

	require.Len(t, proxyMemoryLegsByRole(legs, usageevidence.WorkloadRoleProxyMemoryPhase1), 1,
		"the extraction phase must reach the provider exactly once")
	require.Len(t, proxyMemoryLegsByRole(legs, usageevidence.WorkloadRoleProxyMemoryPhase2), 1,
		"the consolidation phase must reach the provider exactly once")
}

// TestProxyMemoryUserControlPerPhaseEffortsAreServed is U3: one owner sets a
// different reasoning effort for each phase, and each effort reaches the
// provider as the canonical option of its own phase.
//
// The mutant this catches: a single effort reused for both phases, or an
// effort dropped before the provider open, would leave the two opens with the
// same (or no) reasoning effort.
func TestProxyMemoryUserControlPerPhaseEffortsAreServed(t *testing.T) {
	const model = "openai/test-model"
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryMaskedInventory, true, func(cfg *config.Config) {
		proxyMemoryPhasePolicy(t, cfg, "extraction", "backend: openai, model: "+model, "")
		proxyMemoryPhasePolicy(t, cfg, "consolidation", "backend: openai, model: "+model, "")
	}, nil)
	proxyMemoryWriteOwnerSettings(t, fixture.memory, "tenant-j5", "alice",
		proxymemory.TargetOverrides{ReasoningEffort: "high"}, model,
		proxymemory.TargetOverrides{ReasoningEffort: "medium"}, model)
	proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-efforts", "tenant-j5", "alice", time.Now().Add(-24*time.Hour).UnixMilli())

	fixture.worker.Tick(context.Background())

	opens := fixture.backend.observed()
	require.Len(t, opens, 2, "each phase must open the provider exactly once")
	require.Equal(t, "high", opens[0].options.ReasoningEffort,
		"the extraction phase must carry its own reasoning effort")
	require.Equal(t, "medium", opens[1].options.ReasoningEffort,
		"the consolidation phase must carry its own reasoning effort")
}

// TestProxyMemoryUserControlPartialOverrideKeepsOperatorDefaults is U4: an
// owner overrides only the model of a phase, and every unnamed field keeps the
// operator's default on the real dispatch path.
//
// The mutant this catches: a partial override that replaces the whole target
// (losing the operator's backend or effort), or one that applies the operator
// default after the owner value (losing the model).
func TestProxyMemoryUserControlPartialOverrideKeepsOperatorDefaults(t *testing.T) {
	const (
		operatorModel = "openai/test-model"
		ownerModel    = "openai/alternate"
	)
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryMaskedInventory, true, func(cfg *config.Config) {
		proxyMemoryMultiModelBackend(t, cfg)
		proxyMemoryPhasePolicy(t, cfg, "extraction",
			"backend: openai, model: "+operatorModel+", reasoning_effort: high", "")
		proxyMemoryPhasePolicy(t, cfg, "consolidation",
			"backend: openai, model: "+operatorModel+", reasoning_effort: high", "")
	}, nil)
	proxyMemoryWriteOwnerSettings(t, fixture.memory, "tenant-j5", "alice",
		proxymemory.TargetOverrides{Model: ownerModel}, ownerModel,
		proxymemory.TargetOverrides{}, operatorModel)
	proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-partial", "tenant-j5", "alice", time.Now().Add(-24*time.Hour).UnixMilli())

	fixture.worker.Tick(context.Background())

	opens := fixture.backend.observed()
	require.NotEmpty(t, opens, "the extraction phase must reach the provider")
	require.Equal(t, "openai", opens[0].backend,
		"the unnamed backend must keep the operator's default")
	require.Equal(t, ownerModel, opens[0].model,
		"the named model must be the owner's value")
	require.Equal(t, "high", opens[0].options.ReasoningEffort,
		"the unnamed effort must keep the operator's default")
}

// TestProxyMemoryUserControlLockRefusesTheStaleOwnerValue is U5 on the runtime
// path: an operator lock is in force, an owner override that would change the
// locked field is stored, and an ordinary request transform tries to re-apply
// the owner's value. The claim freezes the locked value and the pre-open guard
// refuses the transform before any provider I/O.
//
// The mutant this catches: a claim path that applies the stored owner value
// despite the lock would freeze medium, the transform would match it, and the
// provider would open. The locked operator value low is the only expectation
// that refuses the open. The unlocked control proves the same stored value and
// the same transform do reach the provider when the operator does not lock the
// field, so the refusal is the lock and not a broken fixture.
func TestProxyMemoryUserControlLockRefusesTheStaleOwnerValue(t *testing.T) {
	const model = "openai/test-model"
	cases := []struct {
		name       string
		locks      string
		wantOpens  int
		wantEffort string
	}{
		{name: "the lock is in force", locks: "reasoning_effort: true", wantOpens: 0},
		{name: "control: without the lock the stale value is served", locks: "", wantOpens: 2, wantEffort: "medium"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryMaskedInventory, true, func(cfg *config.Config) {
				proxyMemoryPhasePolicy(t, cfg, "extraction",
					"backend: openai, model: "+model+", reasoning_effort: low", tc.locks)
				proxyMemoryPhasePolicy(t, cfg, "consolidation",
					"backend: openai, model: "+model, "")
			}, func(exec *runtime.Executor) {
				exec.Bus = hooks.New(hooks.Config{RequestPartHooks: []sdkhooks.RequestPartHook{proxyMemoryEffortHook{}}})
			})
			proxyMemoryWriteOwnerSettings(t, fixture.memory, "tenant-j5", "alice",
				proxymemory.TargetOverrides{ReasoningEffort: "medium"}, model,
				proxymemory.TargetOverrides{}, model)
			proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-lock", "tenant-j5", "alice", time.Now().Add(-24*time.Hour).UnixMilli())

			fixture.worker.Tick(context.Background())

			opens := fixture.backend.observed()
			require.Len(t, opens, tc.wantOpens,
				"the locked case must refuse the transform before the provider open, and the control must serve it")
			if tc.wantOpens > 0 {
				require.Equal(t, tc.wantEffort, opens[0].options.ReasoningEffort,
					"the unlocked control must serve the stored owner effort")
			}
		})
	}
}

// TestProxyMemoryUserControlLockIntroducedAfterOverrideWins is U6: an owner
// override exists before the operator introduces a lock on that field, and the
// lock wins on the real dispatch path while the raw stored override survives.
//
// The mutant this catches: a claim path that keeps the stale owner model would
// dispatch openai/alternate (the inventory permits it, so no later check would
// refuse it) instead of the locked operator model openai/test-model.
func TestProxyMemoryUserControlLockIntroducedAfterOverrideWins(t *testing.T) {
	const (
		operatorModel = "openai/test-model"
		staleModel    = "openai/alternate"
	)
	fixture := proxyMemoryPolicyFixtureFor(t, proxyMemoryMaskedInventory, true, func(cfg *config.Config) {
		proxyMemoryMultiModelBackend(t, cfg)
		proxyMemoryPhasePolicy(t, cfg, "extraction",
			"backend: openai, model: "+operatorModel, "model: true")
		proxyMemoryPhasePolicy(t, cfg, "consolidation",
			"backend: openai, model: "+operatorModel, "")
	}, nil)
	proxyMemoryWriteOwnerSettings(t, fixture.memory, "tenant-j5", "alice",
		proxymemory.TargetOverrides{Model: staleModel}, staleModel,
		proxymemory.TargetOverrides{}, operatorModel)
	proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-late-lock", "tenant-j5", "alice", time.Now().Add(-24*time.Hour).UnixMilli())

	fixture.worker.Tick(context.Background())

	opens := fixture.backend.observed()
	require.NotEmpty(t, opens, "the extraction phase must reach the provider")
	require.Equal(t, operatorModel, opens[0].model,
		"a lock introduced after the override must win over the stale owner model")

	var stored string
	require.NoError(t, fixture.memory.NewRaw(`SELECT model FROM proxy_memory_settings
		WHERE tenant_id='tenant-j5' AND principal_id='alice' AND phase='extraction'`).Scan(context.Background(), &stored))
	require.Equal(t, staleModel, stored,
		"the lock must not rewrite the owner's raw stored override")
}

// TestProxyMemoryUserControlQueryParameterCannotBypassTheInventory is U9 on
// the query-parameter leg: the operator's alias moves the effort to high
// through a route parameter, while the phase inventory allows only low and
// medium for the compiled target. The phase is refused before the provider
// open because the check runs against the final compiled actual target.
//
// The mutant this catches: a check that reads the phase call's pre-merge
// effort (low, allowed) instead of the compiled candidate's merged options
// would dispatch the call with the out-of-inventory high effort. The positive
// control serves the same route parameter under an inventory that allows high,
// so the refusal is the inventory and not a malformed alias.
func TestProxyMemoryUserControlQueryParameterCannotBypassTheInventory(t *testing.T) {
	const narrowedInventory = `
      backends: [openai]
      models: [openai/test-model]
      targets:
        - backend: openai
          model: openai/test-model
          reasoning_efforts: [low, medium]`
	cases := []struct {
		name      string
		inventory string
		wantOpens int
	}{
		{name: "the route parameter is outside the inventory", inventory: narrowedInventory, wantOpens: 0},
		{name: "control: the same route parameter is served when the inventory allows it", inventory: proxyMemoryPermittedInventory, wantOpens: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := proxyMemoryPolicyFixtureFor(t, tc.inventory, true, func(cfg *config.Config) {
				cfg.ModelAliases[0].Replacement = "openai:openai/test-model?reasoning_effort=high"
			}, nil)
			proxyMemorySeedOwnedSession(t, fixture.memory, "aleg-query", "tenant-j5", "alice", time.Now().Add(-24*time.Hour).UnixMilli())

			fixture.worker.Tick(context.Background())

			opens := fixture.backend.observed()
			require.Len(t, opens, tc.wantOpens,
				"a route parameter outside the allowed inventory must be refused before the provider open")
			if tc.wantOpens > 0 {
				require.Equal(t, "high", opens[0].options.ReasoningEffort,
					"the permitted route parameter must reach the provider as the merged extraction effort")
				require.Equal(t, "medium", opens[1].options.ReasoningEffort,
					"the consolidation phase must keep its own default effort")
			}
		})
	}
}
