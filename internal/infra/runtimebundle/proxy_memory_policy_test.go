package runtimebundle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// proxyMemoryCatalogStub is the model-catalog resolver of the target
// compiler tests. It drops one capability from the effective capabilities, so
// the test can prove the catalog's per-model facts reach the compiled target
// and not only the backend's own declaration.
type proxyMemoryCatalogStub struct {
	dropped aipapi.Capability
}

func (s proxyMemoryCatalogStub) Resolve(_ context.Context, _ routing.AttemptCandidate, _ aipapi.Call, backend aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	effective := modelcatalog.CloneBackendCaps(backend)
	delete(effective, s.dropped)
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
			// The catalog entry states the capability it establishes; an
			// entry that states nothing is unproven, not supported.
			Reasoning: modelcatalog.CapabilitySupported,
		},
		BackendCaps:   backend,
		EffectiveCaps: effective,
		Matched:       true,
	}
}

// proxyMemoryTestExecutor builds one generation's executor with the facts the
// target compilation reads: the alias resolver, the default backend, the
// known-backend map with its static capabilities and their dynamic
// resolution, and the model catalog's per-model facts.
func proxyMemoryTestExecutor(t *testing.T, aliases *routing.AliasResolver) *runtime.Executor {
	t.Helper()
	backends := map[string]execbackend.Backend{
		"openai": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			ResolveCaps: func(context.Context, aipapi.Call, routing.AttemptCandidate) aipapi.BackendCaps {
				return aipapi.NewBackendCaps(
					aipapi.CapabilityStreaming,
					aipapi.CapabilityTools,
					aipapi.CapabilityStructuredOutputs,
				)
			},
		},
	}
	caps := capabilities.MapResolver{}
	for id, backend := range backends {
		caps[id] = func(ctx context.Context, cand routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
			return execbackend.EffectiveCaps(ctx, backend, call, cand)
		}
	}
	return runtime.NewExecutor(runtime.ExecutorConfig{
		Core: runtime.CoreRuntime{Backends: backends},
		Routing: runtime.RoutingRuntime{
			DefaultBackend:             "openai",
			SelectorAliases:            aliases,
			CapsResolver:               caps,
			CatalogResolver:            proxyMemoryCatalogStub{dropped: aipapi.CapabilityStructuredOutputs},
			ExecutionCompositionPolicy: config.ExecutionCompositionSafe,
			BackendExecutionResolver: routing.BackendExecutionResolverFunc(func(string) (aipsdk.BackendExecutionClass, bool) {
				return aipsdk.BackendExecutionInference, true
			}),
		},
	})
}

// proxyMemoryTestCompiler returns one generation's bound target compiler.
func proxyMemoryTestCompiler(t *testing.T, aliases *routing.AliasResolver) *proxyMemoryTargetCompiler {
	t.Helper()
	compiler := newProxyMemoryTargetCompiler()
	compiler.bind(proxyMemoryTestExecutor(t, aliases))
	return compiler
}

// proxyMemoryTestCall builds one phase call. The phase names the refusal
// only: the feature adds it to the error it reports.
func proxyMemoryTestCall(route, effort string) aipapi.Call {
	return aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: route},
		Options: aipapi.GenerationOptions{ReasoningEffort: effort},
	}
}

// proxyMemoryCompiledCandidate is one compiled candidate the binding reports.
type proxyMemoryCompiledCandidate struct {
	key          string
	backend      string
	model        string
	options      aipapi.GenerationOptions
	capabilities aipapi.BackendCaps
}

// proxyMemoryCompile compiles one phase call through the bound compiler and
// returns every candidate the compiled route can address, in the runtime's own
// candidate order.
func proxyMemoryCompile(compiler *proxyMemoryTargetCompiler, route, effort string) ([]proxyMemoryCompiledCandidate, error) {
	var candidates []proxyMemoryCompiledCandidate
	err := compiler.CompileTarget(
		context.Background(),
		proxyMemoryTestCall(route, effort),
		func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps) {
			candidates = append(candidates, proxyMemoryCompiledCandidate{
				key:          key,
				backend:      backend,
				model:        model,
				options:      options,
				capabilities: capabilities,
			})
		},
	)
	return candidates, err
}

// proxyMemoryPrimary compiles one phase call and returns its first compiled
// candidate.
func proxyMemoryPrimary(t *testing.T, compiler *proxyMemoryTargetCompiler, route, effort string) proxyMemoryCompiledCandidate {
	t.Helper()
	candidates, err := proxyMemoryCompile(compiler, route, effort)
	if err != nil {
		t.Fatalf("CompileTarget: %v", err)
	}
	if len(candidates) == 0 {
		t.Fatal("CompileTarget reported no candidate")
	}
	return candidates[0]
}

// TestProxyMemoryTargetCompilerResolvesTheAlias pins the alias resolution of
// design §1: the compiled target names the target the alias resolves to, not
// the one the phase asked for.
func TestProxyMemoryTargetCompilerResolvesTheAlias(t *testing.T) {
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "gpt5", Replacement: "openai:gpt-5"},
	})
	if err != nil {
		t.Fatalf("NewAliasResolver: %v", err)
	}
	compiler := proxyMemoryTestCompiler(t, aliases)

	primary := proxyMemoryPrimary(t, compiler, "gpt5", "low")
	if got, want := primary.backend, "openai"; got != want {
		t.Fatalf("compiled backend = %q, want %q", got, want)
	}
	if got, want := primary.model, "gpt-5"; got != want {
		t.Fatalf("compiled model = %q, want %q", got, want)
	}
}

// TestProxyMemoryTargetCompilerMergesTheRouteParameterEffort pins the route
// parameters of design §1: the runtime merges them per attempt after the
// candidate is chosen, and they override the call's own options, the
// reasoning effort included. The compiled target therefore carries the
// options that will serve the call.
func TestProxyMemoryTargetCompilerMergesTheRouteParameterEffort(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)

	primary := proxyMemoryPrimary(t, compiler, "openai:gpt-5?reasoning_effort=high", "low")
	if got, want := primary.options.ReasoningEffort, "high"; got != want {
		t.Fatalf("compiled effort = %q, want %q", got, want)
	}
}

// TestProxyMemoryTargetCompilerKeepsTheCallEffortWithoutRouteParameters
// pins the other side of the same rule: a route without an effort parameter
// keeps the phase's own effort, so the compiled target never strips it.
func TestProxyMemoryTargetCompilerKeepsTheCallEffortWithoutRouteParameters(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)

	primary := proxyMemoryPrimary(t, compiler, "openai:gpt-5", "low")
	if got, want := primary.options.ReasoningEffort, "low"; got != want {
		t.Fatalf("compiled effort = %q, want %q", got, want)
	}
}

// TestProxyMemoryTargetCompilerCarriesTheTargetCapabilities pins the
// capability facts of design §1: the compiled target carries the backend's
// static capabilities with their dynamic resolution, intersected with the
// model catalog's per-model facts.
func TestProxyMemoryTargetCompilerCarriesTheTargetCapabilities(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)

	primary := proxyMemoryPrimary(t, compiler, "openai:gpt-5", "low")
	if _, ok := primary.capabilities[aipapi.CapabilityTools]; !ok {
		t.Fatal("compiled capabilities carry no tools capability")
	}
	if _, ok := primary.capabilities[aipapi.CapabilityStructuredOutputs]; ok {
		t.Fatal("compiled capabilities carry the structured outputs capability the model catalog dropped")
	}
	if _, ok := primary.capabilities[aipapi.CapabilityReasoning]; ok {
		t.Fatal("compiled capabilities carry a reasoning capability the backend never declared")
	}
}

// TestProxyMemoryTargetCompilerResolvesCapabilitiesAgainstTheMergedOptions
// pins the call-sensitive half of design §1: the capability resolution reads
// the same effective options the compiled target carries, so a resolver that
// depends on the merged effort sees the merged effort and not the phase's own.
func TestProxyMemoryTargetCompilerResolvesCapabilitiesAgainstTheMergedOptions(t *testing.T) {
	exec := proxyMemoryTestExecutor(t, nil)
	exec.CapsResolver = capabilities.MapResolver{"openai": func(_ context.Context, _ routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
		if call.Options.ReasoningEffort == "high" {
			return aipapi.NewBackendCaps(aipapi.CapabilityReasoning)
		}
		return aipapi.BackendCaps{}
	}}
	compiler := newProxyMemoryTargetCompiler()
	compiler.bind(exec)

	primary := proxyMemoryPrimary(t, compiler, "openai:gpt-5?reasoning_effort=high", "low")
	if got, want := primary.options.ReasoningEffort, "high"; got != want {
		t.Fatalf("compiled effort = %q, want %q", got, want)
	}
	if _, ok := primary.capabilities[aipapi.CapabilityReasoning]; !ok {
		t.Fatal("the dynamic capability resolver saw the phase's own effort, not the merged one")
	}
}

// TestProxyMemoryTargetCompilerVisitsEveryCandidate pins the candidate
// coverage of Requirement 1.5: the binding reports every candidate the
// compiled route can address, so a later failover candidate is checked
// against the phase's policy too and not only the first one.
func TestProxyMemoryTargetCompilerVisitsEveryCandidate(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)

	candidates, err := proxyMemoryCompile(compiler, "openai:first | openai:second", "low")
	if err != nil {
		t.Fatalf("CompileTarget: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("compiled candidates = %d, want 2", len(candidates))
	}
	if got, want := candidates[0].model, "first"; got != want {
		t.Fatalf("first candidate model = %q, want %q", got, want)
	}
	if got, want := candidates[1].model, "second"; got != want {
		t.Fatalf("second candidate model = %q, want %q", got, want)
	}
}

// TestProxyMemoryTargetCompilerMergesEachCandidateRouteParameters pins the
// per-candidate route parameters: the runtime merges them per attempt after
// the candidate is chosen, so each candidate carries the options that serve
// it and not the first candidate's.
func TestProxyMemoryTargetCompilerMergesEachCandidateRouteParameters(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)

	candidates, err := proxyMemoryCompile(compiler, "openai:first?reasoning_effort=high | openai:second", "low")
	if err != nil {
		t.Fatalf("CompileTarget: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("compiled candidates = %d, want 2", len(candidates))
	}
	if got, want := candidates[0].options.ReasoningEffort, "high"; got != want {
		t.Fatalf("first candidate effort = %q, want %q", got, want)
	}
	if got, want := candidates[1].options.ReasoningEffort, "low"; got != want {
		t.Fatalf("second candidate effort = %q, want %q", got, want)
	}
}

// TestProxyMemoryTargetCompilerRefusesAnUnknownBackend pins the installed
// check of design §1: a route that names a backend the generation does not
// know is refused instead of compiled against an unchecked target.
func TestProxyMemoryTargetCompilerRefusesAnUnknownBackend(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)

	_, err := proxyMemoryCompile(compiler, "ghost:gpt-5", "low")
	if err == nil {
		t.Fatal("CompileTarget accepted an unknown backend")
	}
	if !strings.Contains(err.Error(), "unknown backend") {
		t.Fatalf("error = %v, want an unknown backend refusal", err)
	}
}

// TestProxyMemoryTargetCompilerFailsClosedWhileUnbound pins the fail-closed
// rule of design §1: a binding that carries no generation yet refuses
// explicitly, so a phase is never dispatched against an unchecked target.
func TestProxyMemoryTargetCompilerFailsClosedWhileUnbound(t *testing.T) {
	compiler := newProxyMemoryTargetCompiler()

	_, err := proxyMemoryCompile(compiler, "openai:gpt-5", "low")
	if !errors.Is(err, errProxyMemoryTargetUnbound) {
		t.Fatalf("error = %v, want %v", err, errProxyMemoryTargetUnbound)
	}
}

// TestProxyMemoryTargetCompilerFailsClosedOnANilExecutor pins the same rule
// for a nil executor: the binding carries no facts, so it stays unavailable
// and the known-backend rejection is not disabled by empty facts.
func TestProxyMemoryTargetCompilerFailsClosedOnANilExecutor(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)
	compiler.bind(nil)

	_, err := proxyMemoryCompile(compiler, "ghost:model", "low")
	if !errors.Is(err, errProxyMemoryTargetUnbound) {
		t.Fatalf("error = %v, want %v", err, errProxyMemoryTargetUnbound)
	}
}

// TestProxyMemoryTargetCompilerFailsClosedAfterANilExecutorBinding pins the
// same rule for a generation that was bound and then bound again without an
// executor: the binding becomes unavailable instead of keeping the retired
// generation's facts.
func TestProxyMemoryTargetCompilerFailsClosedAfterANilExecutorBinding(t *testing.T) {
	compiler := proxyMemoryTestCompiler(t, nil)
	if _, err := proxyMemoryCompile(compiler, "openai:gpt-5", "low"); err != nil {
		t.Fatalf("CompileTarget: %v", err)
	}
	compiler.bind(nil)

	_, err := proxyMemoryCompile(compiler, "openai:gpt-5", "low")
	if !errors.Is(err, errProxyMemoryTargetUnbound) {
		t.Fatalf("error = %v, want %v", err, errProxyMemoryTargetUnbound)
	}
}

// TestProxyMemoryTargetCompilerFailsClosedOnATypedNilReceiver pins the
// fail-closed rule for a typed-nil receiver.
func TestProxyMemoryTargetCompilerFailsClosedOnATypedNilReceiver(t *testing.T) {
	var compiler *proxyMemoryTargetCompiler

	_, err := proxyMemoryCompile(compiler, "openai:gpt-5", "low")
	if !errors.Is(err, errProxyMemoryTargetUnbound) {
		t.Fatalf("error = %v, want %v", err, errProxyMemoryTargetUnbound)
	}
}

// proxyMemoryReasoningCatalog is the model-catalog resolver of the
// reasoning-capability cases: it states the per-model fact the test names for
// a matched catalog entry, and mirrors the backend's own capability set as the
// intersection the resolver would otherwise apply.
type proxyMemoryReasoningCatalog struct {
	reasoning modelcatalog.CapabilityTriState
	matched   bool
}

func (c proxyMemoryReasoningCatalog) Resolve(_ context.Context, _ routing.AttemptCandidate, _ aipapi.Call, backend aipapi.BackendCaps) modelcatalog.EffectiveFacts {
	return modelcatalog.EffectiveFacts{
		Facts: modelcatalog.ModelFacts{
			Source:    modelcatalog.FactSourceCatalog,
			MatchKind: modelcatalog.MatchExact,
			Reasoning: c.reasoning,
		},
		BackendCaps:   backend,
		EffectiveCaps: modelcatalog.CloneBackendCaps(backend),
		Matched:       c.matched,
	}
}

// TestProxyMemoryTargetCompilerDoesNotProjectAnUnprovenReasoningCapability
// pins the tri-state of the model catalog: the compiled target carries the
// reasoning capability only when the model fact proves it. A false fact and an
// unknown fact both leave the capability out, so the policy refuses a
// requested effort instead of treating the unknown state as support. A
// backend-declaration-only compilation (no catalog fact) keeps the backend's
// boolean capability.
func TestProxyMemoryTargetCompilerDoesNotProjectAnUnprovenReasoningCapability(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		reasoning  modelcatalog.CapabilityTriState
		matched    bool
		noCatalog  bool
		wantReason bool
	}{
		{name: "the catalog proves the capability", reasoning: modelcatalog.CapabilitySupported, matched: true, wantReason: true},
		{name: "the catalog denies the capability", reasoning: modelcatalog.CapabilityUnsupported, matched: true},
		{name: "the catalog does not establish the capability", reasoning: modelcatalog.CapabilityUnknown, matched: true},
		{name: "no catalog fact applies to the target", reasoning: modelcatalog.CapabilityUnknown, matched: false, wantReason: true},
		{name: "the generation carries no catalog resolver", noCatalog: true, wantReason: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			exec := proxyMemoryTestExecutor(t, nil)
			exec.CapsResolver = capabilities.MapResolver{"openai": func(context.Context, routing.AttemptCandidate, aipapi.Call) aipapi.BackendCaps {
				return aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityReasoning)
			}}
			if tc.noCatalog {
				exec.CatalogResolver = nil
			} else {
				exec.CatalogResolver = proxyMemoryReasoningCatalog{reasoning: tc.reasoning, matched: tc.matched}
			}
			compiler := newProxyMemoryTargetCompiler()
			compiler.bind(exec)

			primary := proxyMemoryPrimary(t, compiler, "openai:gpt-5", "low")
			_, hasReasoning := primary.capabilities[aipapi.CapabilityReasoning]
			if hasReasoning != tc.wantReason {
				t.Fatalf("compiled reasoning capability = %v, want %v", hasReasoning, tc.wantReason)
			}
		})
	}
}
