package runtimebundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The generation-bound binding of the proxy-memory feature's target-compiler
// port (design §1).
//
// The feature cannot compile the final actual target itself: the alias
// resolver, the default backend and the known-backend set live on the
// generation's executor, which the composition root builds after the feature
// host compiles, and the feature package may not import core implementations.
// The composition root therefore binds the feature-owned port from the
// generation's executor, next to the route-override validator binding.
//
// The binding is per generation: a reload re-derives it, so a retired
// generation's executor never answers for the running one. It is not a
// registry, not a resolver chain and not a framework.

// errProxyMemoryTargetUnbound refuses a compilation whose generation carries
// no executor. The refusal is fail-closed: the phase is never dispatched
// against an unchecked target.
var errProxyMemoryTargetUnbound = errors.New("runtimebundle: the generation's proxy-memory target compiler is not bound")

// proxyMemoryTargetCompiler is the generation-bound binding of the
// feature-owned target-compiler port.
//
// The feature host compiles before the generation's executor exists, so the
// binding is created first and the generation's executor is published into it
// once the executor is built.
type proxyMemoryTargetCompiler struct {
	mu   sync.Mutex
	exec *runtime.Executor
}

// newProxyMemoryTargetCompiler returns the unbound binding of one
// generation's target compiler.
func newProxyMemoryTargetCompiler() *proxyMemoryTargetCompiler {
	return &proxyMemoryTargetCompiler{}
}

// compileStandardFeatures compiles the standard feature host for one
// generation and returns the generation-bound target compiler the
// proxy-memory feature consumes (design §1). The binding is created here and
// published into once the generation's executor is built.
func compileStandardFeatures(ctx context.Context, host *featurehost.Runtime, in featurehost.GenerationInput) (featurehost.GenerationOutput, *proxyMemoryTargetCompiler, error) {
	compiler := newProxyMemoryTargetCompiler()
	in.ProxyMemoryTargetCompiler = compiler
	out, err := host.CompileGeneration(ctx, in)
	if err != nil {
		return featurehost.GenerationOutput{}, nil, err
	}
	return out, compiler, nil
}

// bind publishes one generation's executor into the binding and returns it.
//
// A nil executor carries no facts, so the binding stays unavailable: the
// compilation refuses instead of accepting a target no backend backs.
func (c *proxyMemoryTargetCompiler) bind(exec *runtime.Executor) *proxyMemoryTargetCompiler {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	c.exec = exec
	c.mu.Unlock()
	return c
}

// CompileTarget implements the feature-owned target-compiler port. It fails
// closed while the generation's executor is not bound.
func (c *proxyMemoryTargetCompiler) CompileTarget(ctx context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps)) error {
	if c == nil || visit == nil {
		return errProxyMemoryTargetUnbound
	}
	exec, err := c.executor()
	if err != nil {
		return err
	}
	return compileProxyMemoryTarget(ctx, exec, call, true, false, func(candidate routing.AttemptCandidate, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, _ proxyMemoryTargetFacts) {
		visit(candidate.Key, candidate.Primary.Backend, candidate.Primary.Model, options, capabilities)
	})
}

// CompileTargetFacts implements the feature's optional facts extension: it
// compiles the call and answers the deployment facts of every candidate.
//
// The facts are the ones the pre-open guard receives at the provider open: the
// installation state of the candidate's (backend, model) pair, read from the
// generation's frozen model-registry publication, and the provider's own answer
// for the effort value the candidate carries. They are resolved for the actual
// compiled candidate — the native, backend-facing projection — so an alias or a
// route parameter never yields the fact of another leaf.
//
// The two facts travel as provider-neutral tri-states (0 unknown, 1
// present or supported, 2 absent or unsupported) because the composition root
// may not import the feature package. The composition root maps its own
// vocabulary into them explicitly, so no alignment is implicit.
func (c *proxyMemoryTargetCompiler) CompileTargetFacts(ctx context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, installation, effortSupport int)) error {
	if c == nil || visit == nil {
		return errProxyMemoryTargetUnbound
	}
	exec, err := c.executor()
	if err != nil {
		return err
	}
	return compileProxyMemoryTarget(ctx, exec, call, true, true, func(candidate routing.AttemptCandidate, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, facts proxyMemoryTargetFacts) {
		visit(candidate.Key, candidate.Primary.Backend, candidate.Primary.Model, options, capabilities, facts.installation, facts.effortSupport)
	})
}

// CompileIdentity implements the identity-only path of the feature-owned
// target-compiler port. It fails closed while the generation's executor is
// not bound.
//
// The path compiles the same selector through the same routing authority and
// resolves no capability, so the compilation performs no provider I/O: the
// operator's locked reference is compared without a provider round trip.
func (c *proxyMemoryTargetCompiler) CompileIdentity(ctx context.Context, call aipapi.Call, visit func(backend, model string)) error {
	if c == nil || visit == nil {
		return errProxyMemoryTargetUnbound
	}
	exec, err := c.executor()
	if err != nil {
		return err
	}
	return compileProxyMemoryTarget(ctx, exec, call, false, false, func(candidate routing.AttemptCandidate, _ aipapi.GenerationOptions, _ aipapi.BackendCaps, _ proxyMemoryTargetFacts) {
		visit(candidate.Primary.Backend, candidate.Primary.Model)
	})
}

// executor returns the generation's executor the binding publishes into.
func (c *proxyMemoryTargetCompiler) executor() (*runtime.Executor, error) {
	if c == nil {
		return nil, errProxyMemoryTargetUnbound
	}
	c.mu.Lock()
	exec := c.exec
	c.mu.Unlock()
	if exec == nil {
		return nil, errProxyMemoryTargetUnbound
	}
	return exec, nil
}

// compileProxyMemoryTarget compiles one phase target into the final compiled
// actual target and visits every candidate the compiled route can address.
//
// The compilation reuses the authoritative routing path — the same compile,
// defaulting and candidate-extraction rules the runtime applies — so the
// compiled candidates are the candidates the runtime can address, and never a
// second parse of the selector.
//
// Every candidate is visited, not only the first one: the runtime may fail
// over to a later candidate or run a parallel leg, so a candidate the phase's
// policy does not cover would otherwise serve the call unchecked
// (Requirement 1.5). The route parameters the runtime merges per attempt
// override the call's own options, the reasoning effort included, so each
// candidate carries the options that will serve it.
//
// resolveCaps selects whether the compilation resolves the capabilities of
// each candidate. The capability resolution is the one that costs a provider
// round trip for an executable connector, so the identity-only path skips it
// and performs no provider I/O.
//
// resolveFacts selects whether the compilation resolves the deployment facts
// of each candidate: the installation state and the provider's answer for the
// effort value. The effort answer is a provider round trip as well, so only
// the facts path asks for it.
func compileProxyMemoryTarget(
	ctx context.Context,
	exec *runtime.Executor,
	call aipapi.Call,
	resolveCaps bool,
	resolveFacts bool,
	visit func(candidate routing.AttemptCandidate, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, facts proxyMemoryTargetFacts),
) error {
	route := strings.TrimSpace(call.Route.Selector)
	if route == "" {
		return fmt.Errorf("runtimebundle: proxy-memory target: the route selector is empty")
	}
	// The generation's frozen registry publication is the native model
	// resolver, so the provider's own facts are asked about the native id the
	// backend serves; it is the same snapshot the installation fact uses.
	selector, err := routing.PrepareSelector(route, exec.SelectorAliases, exec.DefaultBackend, exec.BackendExecutionResolver, exec.ExecutionCompositionPolicy, exec.BoundRegistry)
	if err != nil {
		return fmt.Errorf("runtimebundle: proxy-memory target: %w", err)
	}
	if err := routing.RejectUnknownBackends(selector, knownBackendsOf(exec)); err != nil {
		return fmt.Errorf("runtimebundle: proxy-memory target: %w", err)
	}
	candidates := routing.InitialCandidates(selector)
	if len(candidates) == 0 {
		return fmt.Errorf("runtimebundle: proxy-memory target: the route compiles to no candidate")
	}
	for _, candidate := range candidates {
		options, err := aipapi.MergeRouteQueryIntoGenerationOptions(call.Options, candidate.Primary.Params)
		if err != nil {
			return fmt.Errorf("runtimebundle: proxy-memory target: %w", err)
		}
		var capabilities aipapi.BackendCaps
		effective := call
		effective.Options = options
		if resolveCaps {
			// The capabilities are resolved against the same effective call the
			// merged options describe, so a call-sensitive resolver and the model
			// catalog read the merged options and not the phase's own.
			capabilities = proxyMemoryTargetCapabilities(ctx, exec, candidate, effective)
		}
		var facts proxyMemoryTargetFacts
		if resolveFacts {
			facts = proxyMemoryTargetFactsOf(ctx, exec, candidate, effective, options.ReasoningEffort)
		}
		// The candidate key is the runtime's own opaque identity of the
		// leaf: the backend, the model and the route parameters. The
		// feature compares it and never parses it.
		visit(candidate, options, capabilities, facts)
	}
	return nil
}

// proxyMemoryTargetFacts are the deployment facts of one compiled candidate.
//
// The facts are provider-neutral tri-states: 0 unknown, 1 present or
// supported, 2 absent or unsupported.
type proxyMemoryTargetFacts struct {
	installation  int
	effortSupport int
}

// proxyMemoryTargetFactsOf resolves the deployment facts of one compiled
// candidate: the installation state of its (backend, model) pair and the
// provider's own answer for the effort value it carries.
//
// The installation state comes from the generation's frozen model-registry
// publication, so an unproven model is unknown rather than absent. The effort
// answer comes from the provider's own callback, so a provider that publishes
// no graded fact answers unknown rather than refusing.
func proxyMemoryTargetFactsOf(
	ctx context.Context,
	exec *runtime.Executor,
	candidate routing.AttemptCandidate,
	effective aipapi.Call,
	effort string,
) proxyMemoryTargetFacts {
	wire := routing.BackendFacingCandidate(candidate)
	facts := proxyMemoryTargetFacts{
		installation: proxyMemoryInstallationState(exec.BoundRegistry.InstallationState(candidate.Primary.Backend, candidate.Primary.Model)),
	}
	if be, ok := exec.Backends[candidate.Primary.Backend]; ok {
		facts.effortSupport = proxyMemoryEffortSupportState(execbackend.EffectiveEffortSupport(ctx, be, effective, wire, effort))
	}
	return facts
}

// proxyMemoryInstallationState maps the registry's installation fact into the
// provider-neutral tri-state the port carries.
func proxyMemoryInstallationState(state modelregistry.InstallationState) int {
	switch state {
	case modelregistry.InstallationPresent:
		return 1
	case modelregistry.InstallationAbsent:
		return 2
	}
	return 0
}

// proxyMemoryEffortSupportState maps the provider's effort-value answer into
// the provider-neutral tri-state the port carries.
func proxyMemoryEffortSupportState(state execbackend.EffortSupportState) int {
	switch state {
	case execbackend.EffortSupportSupported:
		return 1
	case execbackend.EffortSupportUnsupported:
		return 2
	}
	return 0
}

// proxyMemoryTargetCapabilities resolves the capabilities the compiled target
// can serve: the backend's static capabilities with their dynamic resolution,
// intersected with the model catalog's per-model facts.
//
// The resolution mirrors the runtime's own admission path — the same
// backend-facing candidate, the same call, the same resolvers and the same
// fallback — so the policy's capability check agrees with the runtime about
// the target it compiled. The runtime may still change the attempt after the
// dispatch, so the feature re-asserts the frozen target and the frozen
// reasoning effort at the provider-open chokepoint, where the runtime's own
// negotiated downgrade and any request transform are already applied.
//
// The reasoning capability is a tri-state fact, and the policy never treats an
// unproven fact as support (Requirement 1.4; design §2): when the model
// catalog matched the candidate but its per-model fact does not prove the
// capability, the policy's projection leaves the capability out, exactly as an
// explicit denial would. The catalog's own tri-state is unchanged and the
// runtime's negotiation for ordinary traffic is untouched; only the
// proxy-memory policy's compiled target is narrowed. A compilation without a
// matching catalog fact keeps the backend's own boolean capability, which is
// the only fact the deployment publishes for that target.
func proxyMemoryTargetCapabilities(ctx context.Context, exec *runtime.Executor, candidate routing.AttemptCandidate, call aipapi.Call) aipapi.BackendCaps {
	wire := routing.BackendFacingCandidate(candidate)
	base := aipapi.BackendCaps{}
	switch {
	case exec.CapsResolver != nil:
		base = exec.CapsResolver.DescribeCandidate(ctx, wire, call)
	default:
		if be, ok := exec.Backends[candidate.Primary.Backend]; ok {
			base = execbackend.EffectiveCaps(ctx, be, call, wire)
		}
	}
	if exec.CatalogResolver == nil {
		return base
	}
	facts := exec.CatalogResolver.Resolve(ctx, candidate, call, base)
	effective := facts.EffectiveCaps
	if effective == nil {
		effective = base
	}
	if facts.Matched && facts.Facts.Reasoning != modelcatalog.CapabilitySupported {
		if _, ok := effective[aipapi.CapabilityReasoning]; ok {
			effective = modelcatalog.CloneBackendCaps(effective)
			delete(effective, aipapi.CapabilityReasoning)
		}
	}
	return effective
}
