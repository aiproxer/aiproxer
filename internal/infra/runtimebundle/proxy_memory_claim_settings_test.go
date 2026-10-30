package runtimebundle_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/db"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

// This is worker -> process scheduler -> executor -> Backend.Open, not a
// BackgroundClient fake. Billing seam doubles are inherited from the compiler
// fixture; this test does not claim financial settlement certification.
func TestProxyMemoryClaimSettingsFreezeBeforeDispatchAndNextClaimReadsTheChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	memoryPath := filepath.Join(t.TempDir(), "memory.db")
	const claimRoute = "openai:openai/test-model"
	gen := proxyMemoryJ5FirstGeneration()
	gen.extractRoute = claimRoute
	cfg := proxyMemoryJ5Config(t, memoryPath, filepath.Join(t.TempDir(), "continuity.db"), gen, proxyMemoryPermittedInventory)
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))
	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg: cfg, Log: testkit.DiscardLogger(), Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ps.Close()) })
	worker, exec, dispose, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{Process: ps, Candidate: cfg})
	if dispose != nil {
		t.Cleanup(func() { require.NoError(t, dispose()) })
	}
	require.NoError(t, err)
	backend, sink := &proxyMemoryPhaseBackend{}, &proxyMemoryPolicySink{}
	proxyMemoryPolicyScript(exec, backend, "openai", sink, true)
	memory, err := db.OpenSQLiteBun(ctx, memoryPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, memory.Close()) })
	writeEffort := func(effort string) {
		t.Helper()
		_, writeErr := memory.NewRaw(`INSERT INTO proxy_memory_settings
			(tenant_id,principal_id,phase,backend,model,reasoning_effort)
			VALUES ('tenant-j5','alice','extraction','','',?)
			ON CONFLICT (tenant_id,principal_id,phase) DO UPDATE SET reasoning_effort=excluded.reasoning_effort`, effort).Exec(ctx)
		require.NoError(t, writeErr)
	}

	// The durable worker job exists before its preferences change.
	proxyMemoryJ5SeedSession(t, memory, "claim-before-change", time.Now().Add(-24*time.Hour).UnixMilli())
	writeEffort("high")
	compiled, allowCompile := make(chan struct{}), make(chan struct{})
	opened, allowOpen := make(chan struct{}), make(chan struct{})
	var firstCompile, firstOpen, releaseCompile, releaseOpen sync.Once
	be := exec.Backends["openai"]
	originalOpen := be.Open
	be.Open = func(openCtx context.Context, call aipapi.Call, candidate routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		if call.Route.Selector == claimRoute {
			firstOpen.Do(func() {
				close(opened)
				select {
				case <-allowOpen:
				case <-openCtx.Done():
				}
			})
		}
		return originalOpen(openCtx, call, candidate)
	}
	exec.Backends = map[string]execbackend.Backend{"openai": be}
	exec.CapsResolver = capabilities.MapResolver{"openai": func(resolveCtx context.Context, _ routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
		if call.Route.Selector == claimRoute {
			firstCompile.Do(func() {
				close(compiled)
				select {
				case <-allowCompile:
				case <-resolveCtx.Done():
				}
			})
		}
		return be.Caps
	}}
	done := make(chan struct{})
	go func() { defer close(done); worker.Tick(ctx) }()
	t.Cleanup(func() {
		releaseCompile.Do(func() { close(allowCompile) })
		releaseOpen.Do(func() { close(allowOpen) })
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker tick did not join")
		}
	})
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal("worker did not reach the expected claim/dispatch boundary")
		}
	}
	wait(compiled) // worker claim snapshot exists; auxiliary submission has not happened
	writeEffort("medium")
	releaseCompile.Do(func() { close(allowCompile) })
	wait(opened) // runtime has admitted the same call and is opening the provider
	writeEffort("low")
	releaseOpen.Do(func() { close(allowOpen) })
	wait(done)
	first := backend.extractions(claimRoute)
	require.Len(t, first, 1)
	require.Equal(t, "high", first[0].options.ReasoningEffort, "neither pre-submit nor running settings changes may alter the claimed request")
	require.Equal(t, "openai/test-model", first[0].model)
	proxyMemoryJ5SeedSession(t, memory, "claim-after-change", time.Now().Add(-24*time.Hour).UnixMilli())
	worker.Tick(ctx)
	observed := backend.extractions(claimRoute)
	require.Len(t, observed, 2)
	require.Equal(t, "low", observed[1].options.ReasoningEffort, "next worker claim must read new preferences")
	var stored string
	require.NoError(t, memory.NewRaw(`SELECT reasoning_effort FROM proxy_memory_settings WHERE tenant_id='tenant-j5' AND principal_id='alice' AND phase='extraction'`).Scan(ctx, &stored))
	require.Equal(t, "low", stored)
}

// Candidate recompilation exercises the production generation binding. It does
// not publish/retire a generation or claim cross-node reload certification.
func TestProxyMemoryClaimSettingsNewGenerationMasksRemovedLockedDefault(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	memoryPath := filepath.Join(t.TempDir(), "memory.db")
	continuityPath := filepath.Join(t.TempDir(), "continuity.db")
	gen := proxyMemoryJ5FirstGeneration()
	gen.extractRoute = "openai:openai/test-model"
	first := proxyMemoryJ5Config(t, memoryPath, continuityPath, gen, proxyMemoryPermittedInventory)
	proxyMemoryExtractionPolicy(t, first, "reasoning_effort: high", "reasoning_effort: true")
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))
	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg: first, Log: testkit.DiscardLogger(), Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ps.Close()) })
	worker, exec, dispose, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{Process: ps, Candidate: first})
	if dispose != nil {
		t.Cleanup(func() { require.NoError(t, dispose()) })
	}
	require.NoError(t, err)
	backend, sink := &proxyMemoryPhaseBackend{}, &proxyMemoryPolicySink{}
	proxyMemoryPolicyScript(exec, backend, "openai", sink, true)
	memory, err := db.OpenSQLiteBun(ctx, memoryPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, memory.Close()) })
	_, err = memory.NewRaw(`INSERT INTO proxy_memory_settings
		(tenant_id,principal_id,phase,backend,model,reasoning_effort)
		VALUES ('tenant-j5','alice','extraction','','','high')`).Exec(ctx)
	require.NoError(t, err)
	proxyMemoryJ5SeedSession(t, memory, "claim-old-default", time.Now().Add(-24*time.Hour).UnixMilli())
	worker.Tick(ctx)
	oldCalls := backend.extractions(gen.extractRoute)
	require.Len(t, oldCalls, 1)
	require.Equal(t, "high", oldCalls[0].options.ReasoningEffort)

	// The explicit default disappears, but its lock remains. The owner row is
	// intentionally not deleted or rewritten by the generation projection.
	second := proxyMemoryJ5Config(t, memoryPath, continuityPath, gen, proxyMemoryPermittedInventory)
	proxyMemoryExtractionPolicy(t, second, "", "reasoning_effort: true")
	nextWorker, nextExec, disposeNext, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{Process: ps, Candidate: second})
	if disposeNext != nil {
		t.Cleanup(func() { require.NoError(t, disposeNext()) })
	}
	require.NoError(t, err)
	proxyMemoryPolicyScript(nextExec, backend, "openai", sink, true)
	proxyMemoryJ5SeedSession(t, memory, "claim-new-default", time.Now().Add(-24*time.Hour).UnixMilli())
	nextWorker.Tick(ctx)
	calls := backend.extractions(gen.extractRoute)
	require.Len(t, calls, 2)
	require.Equal(t, "low", calls[1].options.ReasoningEffort, "the retained lock must mask high after its explicit operator default was removed")
	require.Equal(t, "high", calls[0].options.ReasoningEffort, "the earlier admitted call remains unchanged")
	var stored string
	require.NoError(t, memory.NewRaw(`SELECT reasoning_effort FROM proxy_memory_settings WHERE tenant_id='tenant-j5' AND principal_id='alice' AND phase='extraction'`).Scan(ctx, &stored))
	require.Equal(t, "high", stored, "reload must not rewrite unprojected owner preferences")
}

// proxyMemoryMaskedInventory is the current operator inventory of the masked
// composed case: both models are allowed per target, so the effort high the
// owner stored is still a legal value; only the changed inherited target makes
// it dependent on a removed default.
const proxyMemoryMaskedInventory = `
      backends: [openai]
      models: [openai/test-model, openai/alternate]
      targets:
        - backend: openai
          model: openai/test-model
          reasoning_efforts: [low, medium, high]
        - backend: openai
          model: openai/alternate
          reasoning_efforts: [low, medium, high]`

// proxyMemoryNarrowedInventory is the same inventory after the operator
// removed high from the current target, so the unchanged target plus the
// narrowed inventory must still refuse the stored effort.
const proxyMemoryNarrowedInventory = `
      backends: [openai]
      models: [openai/test-model, openai/alternate]
      targets:
        - backend: openai
          model: openai/test-model
          reasoning_efforts: [low, medium]
        - backend: openai
          model: openai/alternate
          reasoning_efforts: [low, medium, high]`

// TestProxyMemoryClaimSettingsMasksRemovedDefaultOnTheDispatchPath exercises the
// two-history distinction across the production scheduler, runtime and
// Backend.Open. The write-time context is seeded as the authenticated settings
// service stores it (the service and store contracts prove that write); this
// test proves the masked claim reaches the provider with the operator's current
// target and effort, and that the same raw row with an unchanged inherited
// target and a narrowed inventory is still refused before any provider open.
func TestProxyMemoryClaimSettingsMasksRemovedDefaultOnTheDispatchPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	memoryPath := filepath.Join(t.TempDir(), "memory.db")
	continuityPath := filepath.Join(t.TempDir(), "continuity.db")
	const claimRoute = "openai:openai/test-model"
	gen := proxyMemoryJ5FirstGeneration()
	gen.extractRoute = claimRoute
	current := proxyMemoryJ5Config(t, memoryPath, continuityPath, gen, proxyMemoryMaskedInventory)
	proxyMemoryExtractionPolicy(t, current, "", "model: true")
	reg := pluginreg.NewRegistry()
	require.NoError(t, standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}))
	ps, err := runtimebundle.NewProcessServices(ctx, runtimebundle.ProcessServicesInput{
		Cfg: current, Log: testkit.DiscardLogger(), Opts: &runtimebundle.BuildOptions{PluginRegistry: reg},
		Tracing: runtimebundle.ProcessTracing{Shutdown: func(context.Context) error { return nil }},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, ps.Close()) })
	worker, exec, dispose, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{Process: ps, Candidate: current})
	if dispose != nil {
		t.Cleanup(func() { require.NoError(t, dispose()) })
	}
	require.NoError(t, err)
	backend, sink := &proxyMemoryPhaseBackend{}, &proxyMemoryPolicySink{}
	proxyMemoryPolicyScript(exec, backend, "openai", sink, true)
	memory, err := db.OpenSQLiteBun(ctx, memoryPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, memory.Close()) })

	// The trusted context says the accepted effort depended on the operator's
	// now removed "alternate" default. The raw owner values stay untouched.
	const contextRow = `INSERT INTO proxy_memory_settings
		(tenant_id,principal_id,phase,backend,model,reasoning_effort,
		 dependency_present,dependency_backend,dependency_model,inherited_backend,inherited_model)
		VALUES ('tenant-j5','alice','extraction','','','high',1,'openai','openai/alternate',1,1)`
	_, err = memory.NewRaw(contextRow).Exec(ctx)
	require.NoError(t, err)
	proxyMemoryJ5SeedSession(t, memory, "claim-removed-default", time.Now().Add(-24*time.Hour).UnixMilli())
	worker.Tick(ctx)
	observed := backend.extractions(claimRoute)
	require.Len(t, observed, 1, "the dependent effort must be masked onto the current target instead of refused")
	require.Equal(t, "openai/test-model", observed[0].model)
	require.Equal(t, "low", observed[0].options.ReasoningEffort,
		"the masked claim must carry the operator's current default effort")

	var effort, contextModel string
	var inheritedModel int
	require.NoError(t, memory.NewRaw(`SELECT reasoning_effort, dependency_model, inherited_model
		FROM proxy_memory_settings WHERE tenant_id='tenant-j5' AND principal_id='alice' AND phase='extraction'`).Scan(ctx,
		&effort, &contextModel, &inheritedModel))
	require.Equal(t, "high", effort, "masking must not rewrite the raw owner override")
	require.Equal(t, "openai/alternate", contextModel, "masking must not rewrite the trusted context")
	require.Equal(t, 1, inheritedModel)

	// The same raw row, but the accepted effort depended on the current target.
	// The operator then widened the allowed efforts and narrowed them again:
	// this history must refuse, not mask.
	_, err = memory.NewRaw(`UPDATE proxy_memory_settings SET dependency_model='openai/test-model'
		WHERE tenant_id='tenant-j5' AND principal_id='alice' AND phase='extraction'`).Exec(ctx)
	require.NoError(t, err)
	narrowed := proxyMemoryJ5Config(t, memoryPath, continuityPath, gen, proxyMemoryNarrowedInventory)
	proxyMemoryExtractionPolicy(t, narrowed, "", "model: true")
	nextWorker, nextExec, disposeNext, err := runtimebundle.ProxyMemoryGenerationForTest(ctx, runtimebundle.GenerationCompileInput{Process: ps, Candidate: narrowed})
	if disposeNext != nil {
		t.Cleanup(func() { require.NoError(t, disposeNext()) })
	}
	require.NoError(t, err)
	proxyMemoryPolicyScript(nextExec, backend, "openai", sink, true)
	proxyMemoryJ5SeedSession(t, memory, "claim-narrowed", time.Now().Add(-24*time.Hour).UnixMilli())
	nextWorker.Tick(ctx)
	require.Len(t, backend.extractions(claimRoute), 1,
		"a stored effort the current target no longer allows must be refused before any provider open")
}
