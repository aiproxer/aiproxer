package proxymemory

import (
	"context"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The target-compiler port of design §1.
//
// The feature cannot compile the final actual target itself: the alias
// resolver, the default backend and the known-backend set live on the
// runtime executor, which is built after the feature host compiles, and the
// feature package may not import core implementations. The feature therefore
// owns a narrow port that the composition root binds, with the shape of the
// route-override admin's selector validator: one method that takes the
// phase's target and reports the final compiled actual target — the
// candidates it can address, the canonical options those candidates serve and
// the capabilities they can serve — or an error.
//
// The port reports every candidate the compiled route can address, not only
// the first one: the runtime may fail over to a later candidate or run a
// parallel leg, so a candidate the phase's policy does not cover would
// otherwise serve the call unchecked (Requirement 1.5). The candidates travel
// through a visit callback because the port speaks plain canonical types on
// purpose: the composition root implements it from the generation's executor
// without importing a concrete feature package, and the feature host hands
// the bound port to the feature.
//
// The port is generation-bound: the composition root rebuilds it per
// generation, so a reload re-derives it. It is not a registry, not a
// resolver chain and not a framework.

// TargetCompiler compiles one phase target into its final compiled actual
// target.
//
// The call carries the phase's content, so the compilation reads only its
// route selector, its options and its shape. The compiled target it reports
// carries no content.
type TargetCompiler interface {
	// CompileTarget compiles the phase's call and visits every candidate the
	// compiled route can address, in the runtime's own candidate order.
	//
	// Each visited candidate names the opaque candidate key the runtime uses
	// for that leaf, the backend and the model it addresses, the canonical
	// options the attempt carries once the route parameters are merged, and
	// the capabilities that candidate can serve.
	//
	// The key is opaque: a consumer compares it, it never parses it. Two
	// legitimate failover arms may share a backend and a model and differ
	// only in their route parameters, so the key — not the backend and model
	// pair — identifies the leaf.
	CompileTarget(ctx context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps)) error

	// CompileIdentity compiles the phase's call the same way but resolves no
	// capability. The compilation therefore performs no provider I/O, so a
	// caller that compares identities only may use it without a provider
	// round trip.
	//
	// It is the same compilation on the same port: the same selector
	// compilation, the same candidate extraction and the same candidate
	// order. The only difference is that the capabilities of a candidate are
	// not resolved, and resolving them is what costs a provider round trip.
	CompileIdentity(ctx context.Context, call aipapi.Call, visit func(backend, model string)) error
}

// TargetFactsCompiler is the optional extension of the target-compiler port
// that answers the deployment facts about every compiled candidate.
//
// The write path needs the two facts the dispatch path gets at the provider
// open: whether the deployment carries the candidate's model, and what the
// provider answers for the effort value the candidate carries. The extension
// answers them in the same compilation, so the write path performs no
// additional compilation and no additional round trip beyond the effort query
// the fact needs.
//
// The facts are provider-neutral tri-states: 0 unknown, 1 present or
// supported, 2 absent or unsupported. They are numbers because the
// composition root, which may not import the feature package, is the port's
// implementer; both sides map the core's own vocabulary explicitly, so no
// value alignment is ever implicit.
//
// The extension is optional: a compiler that does not implement it leaves the
// facts unproven, which the default policy serves and counts and the strict
// policy refuses.
type TargetFactsCompiler interface {
	// CompileTargetFacts compiles the phase's call and visits every candidate
	// the compiled route can address, in the runtime's own candidate order,
	// with the deployment facts of each candidate.
	CompileTargetFacts(ctx context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, installation, effortSupport int)) error
}

// CompiledIdentity is the compiled identity of one leaf of a target: the
// backend and the model the leaf addresses.
//
// It carries no content: it names the target and nothing else.
type CompiledIdentity struct {
	// Backend is the backend the leaf addresses.
	Backend string
	// Model is the model the leaf addresses, after the alias resolution.
	Model string
}

// CompiledCandidate is one compiled candidate of the final compiled actual
// target: the opaque candidate key that identifies it, the backend and the
// model it addresses, the canonical options the attempt carries once the
// route parameters are merged, and the capabilities that candidate can serve.
//
// The candidate carries no content: it names the target and what that
// target can do, never a prompt, a transcript or a memory.
type CompiledCandidate struct {
	// Key is the runtime's opaque identity of this candidate: the backend,
	// the model and the route parameters. The feature compares it and never
	// parses it.
	Key string
	// Backend is the backend the candidate addresses.
	Backend string
	// Model is the model the candidate addresses, after the alias
	// resolution.
	Model string
	// Options are the canonical options the attempt carries once the route
	// parameters are merged. The reasoning effort is
	// Options.ReasoningEffort: the route parameters the runtime merges per
	// attempt override the call's own option, the effort included, so this
	// is the effort that will serve the call and not the one the phase
	// asked for.
	Options aipapi.GenerationOptions
	// Capabilities are the capabilities the candidate can serve: the
	// backend's static capabilities with their dynamic resolution,
	// intersected with the model catalog's per-model facts.
	Capabilities aipapi.BackendCaps
	// Installation is the deployment fact about the candidate's (backend,
	// model) pair: whether the deployment carries the model. It is unknown
	// when the deployment does not prove it (design §3).
	Installation InstallationState
	// EffortSupport is the provider's own answer for the effort value the
	// candidate carries. It is unknown when the provider published no graded
	// fact (design §2).
	EffortSupport EffortSupportState
}

// Negotiation returns the canonical capability negotiation of the phase's
// effective call — the call with the candidate's merged options — against
// the candidate's capabilities.
//
// A downgrade names the capabilities that would be stripped; the refusal
// decision belongs to the policy that consumes the compiled target, not to
// the compilation.
func (c CompiledCandidate) Negotiation(call aipapi.Call) aipapi.NegotiationResult {
	effective := call
	effective.Options = c.Options
	return aipapi.Negotiate(aipapi.RequiredCapabilities(effective), c.Capabilities)
}

// CompiledTarget is the final compiled actual target of one phase: every
// candidate the compiled route can address, in the runtime's own order. The
// first candidate is the one the runtime addresses first.
//
// The compiled target carries no content: it names the targets and what
// those targets can do, never a prompt, a transcript or a memory.
type CompiledTarget struct {
	Candidates []CompiledCandidate
}

// ErrTargetCompilerUnavailable refuses a phase whose target cannot be
// compiled because the port is not bound: the composition root did not bind
// one for this generation, or the generation it belonged to is gone.
var ErrTargetCompilerUnavailable = errors.New(ID + ": the target compiler is unavailable")

// ErrTargetCompilationFailed refuses a phase whose target does not compile:
// the route does not resolve, names an unknown backend, or carries route
// parameters that do not merge into the call's options.
var ErrTargetCompilationFailed = errors.New(ID + ": the target does not compile")

// CompileTarget compiles one phase's call through the bound port and returns
// the final compiled actual target.
//
// A nil port refuses explicitly: the phase is never dispatched against an
// unchecked target (design §1; Requirement 1.5). A compiled target that
// names no candidate is refused as well: such a target cannot be checked
// against anything.
//
// The compiler's error keeps its identity, so a cancelled compilation stays
// a cancellation for the caller that classifies it.
func CompileTarget(ctx context.Context, compiler TargetCompiler, phase Phase, call aipapi.Call) (CompiledTarget, error) {
	if compiler == nil {
		return CompiledTarget{}, fmt.Errorf("%w: %s", ErrTargetCompilerUnavailable, phase)
	}
	var candidates []CompiledCandidate
	err := compiler.CompileTarget(ctx, call, func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps) {
		candidates = append(candidates, CompiledCandidate{
			Key:          key,
			Backend:      backend,
			Model:        model,
			Options:      options,
			Capabilities: capabilities,
		})
	})
	if err != nil {
		return CompiledTarget{}, fmt.Errorf("%w: %s: %w", ErrTargetCompilationFailed, phase, err)
	}
	return compiledTarget(phase, candidates)
}

// compileWithFacts compiles the phase's call and returns the compiled target
// with the deployment facts of every candidate.
//
// The compiler's facts extension answers the facts in the same compilation, so
// the caller performs one compilation and no extra round trip. A compiler
// without the extension leaves the facts unproven: the compiled target is still
// returned, and the unproven facts are the default policy's unknown state.
func compileWithFacts(ctx context.Context, compiler TargetCompiler, phase Phase, call aipapi.Call) (CompiledTarget, error) {
	if compiler == nil {
		return CompiledTarget{}, fmt.Errorf("%w: %s", ErrTargetCompilerUnavailable, phase)
	}
	extension, ok := compiler.(TargetFactsCompiler)
	if !ok {
		return CompileTarget(ctx, compiler, phase, call)
	}
	var candidates []CompiledCandidate
	err := extension.CompileTargetFacts(ctx, call, func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps, installation, effortSupport int) {
		candidates = append(candidates, CompiledCandidate{
			Key:           key,
			Backend:       backend,
			Model:         model,
			Options:       options,
			Capabilities:  capabilities,
			Installation:  installationState(installation),
			EffortSupport: effortSupportState(effortSupport),
		})
	})
	if err != nil {
		return CompiledTarget{}, fmt.Errorf("%w: %s: %w", ErrTargetCompilationFailed, phase, err)
	}
	return compiledTarget(phase, candidates)
}

// compiledTarget validates the candidates one compilation collected.
func compiledTarget(phase Phase, candidates []CompiledCandidate) (CompiledTarget, error) {
	if len(candidates) == 0 {
		return CompiledTarget{}, &PolicyError{
			Phase:  phase,
			Field:  FieldBackend,
			Reason: "the compiled target names no candidate",
		}
	}
	if candidates[0].Backend == "" {
		return CompiledTarget{}, &PolicyError{
			Phase:  phase,
			Field:  FieldBackend,
			Reason: "the compiled target names no backend",
		}
	}
	return CompiledTarget{Candidates: candidates}, nil
}

// installationState maps the port's provider-neutral tri-state into the
// feature's installation vocabulary.
func installationState(state int) InstallationState {
	switch state {
	case 1:
		return InstallationPresent
	case 2:
		return InstallationAbsent
	}
	return InstallationUnknown
}

// effortSupportState maps the port's provider-neutral tri-state into the
// feature's effort-support vocabulary.
func effortSupportState(state int) EffortSupportState {
	switch state {
	case 1:
		return EffortSupportSupported
	case 2:
		return EffortSupportUnsupported
	}
	return EffortSupportUnknown
}

// CompileIdentity compiles one phase's call through the bound port's
// identity-only path and returns the compiled identity of every leaf the
// compiled route can address.
//
// The identity-only path resolves no capability, so the compilation performs
// no provider I/O: it is the path the operator's locked reference uses, and
// the reference needs identities only.
//
// A nil port refuses explicitly, and a compilation that names no leaf is
// refused as well: such a target cannot be compared with anything.
func CompileIdentity(ctx context.Context, compiler TargetCompiler, phase Phase, call aipapi.Call) ([]CompiledIdentity, error) {
	if compiler == nil {
		return nil, fmt.Errorf("%w: %s", ErrTargetCompilerUnavailable, phase)
	}
	var identities []CompiledIdentity
	if err := compiler.CompileIdentity(ctx, call, func(backend, model string) {
		identities = append(identities, CompiledIdentity{Backend: backend, Model: model})
	}); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTargetCompilationFailed, phase, err)
	}
	if len(identities) == 0 {
		return nil, &PolicyError{
			Phase:  phase,
			Field:  FieldBackend,
			Reason: "the compiled target names no candidate",
		}
	}
	return identities, nil
}

// enforceCompiledTarget compiles the phase's dispatched call through the
// bound port and refuses the phase when the target cannot be compiled or
// when the compiled actual target is not covered by the phase's policy.
//
// A configuration that declares no model policy keeps the legacy behaviour:
// the phase dispatches with the operator's route and today's effort, and no
// target is compiled. A configuration that declares a model policy always
// compiles, because the port is the only way to learn the final actual
// target, and a phase is never dispatched against an unchecked target
// (design §1; Requirement 1.5).
//
// The compiled target never replaces the call's options. The runtime merges
// each attempt's route parameters itself, so writing the first candidate's
// merged options into the dispatch baseline would contaminate the later
// candidates of a multi-target route (Requirement 1.5; design §1:90).
func enforceCompiledTarget(ctx context.Context, cfg Config, phase Phase, call *aipapi.Call) error {
	if !cfg.ModelsDeclared {
		return nil
	}
	if call == nil {
		return fmt.Errorf("%w: %s: no call to compile", ErrTargetCompilationFailed, phase)
	}
	target, err := CompileTarget(ctx, cfg.TargetCompiler, phase, *call)
	if err != nil {
		return err
	}
	return cfg.CheckCompiledTarget(ctx, phase, *call, target)
}
