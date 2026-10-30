package proxymemory

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// stubTargetCompiler is the target-compiler port of the feature tests. It
// records the calls it received and answers with the compiled target the test
// names, or with an injected error.
//
// The stub is selector-aware: a selector the bySelector map names answers
// with that candidate, every other selector answers with the default one.
// The compiled-domain lock compares the operator's compiled reference with
// the compiled candidate, so a stub that ignored the selector could not model
// a reference that resolves elsewhere.
type stubTargetCompiler struct {
	calls []aipapi.Call
	// identityCalls are the calls of the identity-only path.
	identityCalls []aipapi.Call
	// backend, model and effort are the compiled target the stub answers
	// with; effort is the reasoning effort of the compiled options.
	backend string
	model   string
	effort  string
	// parallelTools is the parallel tool call option of the compiled
	// options.
	parallelTools bool
	caps          aipapi.BackendCaps
	// extra are the further compiled candidates the stub reports after the
	// primary one: the runtime may fail over to a later candidate, so the
	// policy must cover them too.
	extra []stubCompiledCandidate
	err   error
	// bySelector maps a route selector to the compiled candidate the stub
	// answers with for that selector.
	bySelector map[string]stubCompiledCandidate
	// reference are the leaves the identity-only path reports. It is empty
	// when the identity-only path reports the same leaves as the capability
	// path; a non-empty value models a divergence between the operator's
	// reference and the phase's own target.
	reference []stubCompiledCandidate
}

// stubCompiledCandidate is one further compiled candidate of the stub.
type stubCompiledCandidate struct {
	backend string
	model   string
	effort  string
	caps    aipapi.BackendCaps
}

// key is the runtime's opaque identity of the compiled leaf: the backend,
// the model and the route parameters.
func (c stubCompiledCandidate) key() string {
	return c.backend + ":" + c.model
}

// primary returns the compiled candidate the stub answers with for one
// selector.
func (c *stubTargetCompiler) primary(selector string) stubCompiledCandidate {
	if named, ok := c.bySelector[selector]; ok {
		return named
	}
	return stubCompiledCandidate{backend: c.backend, model: c.model, effort: c.effort, caps: c.caps}
}

func (c *stubTargetCompiler) CompileTarget(_ context.Context, call aipapi.Call, visit func(key, backend, model string, options aipapi.GenerationOptions, capabilities aipapi.BackendCaps)) error {
	c.calls = append(c.calls, call)
	if c.err != nil {
		return c.err
	}
	primary := c.primary(call.Route.Selector)
	options := aipapi.GenerationOptions{ReasoningEffort: primary.effort}
	if c.parallelTools {
		enabled := true
		options.ParallelToolCalls = &enabled
	}
	caps := primary.caps
	if caps == nil {
		caps = aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
	}
	visit(primary.key(), primary.backend, primary.model, options, caps)
	for _, extra := range c.extra {
		extraCaps := extra.caps
		if extraCaps == nil {
			extraCaps = aipapi.NewBackendCaps(aipapi.CapabilityStreaming)
		}
		visit(extra.key(), extra.backend, extra.model, aipapi.GenerationOptions{ReasoningEffort: extra.effort}, extraCaps)
	}
	return nil
}

// CompileIdentity answers the identity-only path of the port.
//
// The path reports the operator's reference leaves: the same leaves as the
// capability path, unless the test declares a divergent reference, which
// models the operator's reference and the phase's own target differing.
func (c *stubTargetCompiler) CompileIdentity(_ context.Context, call aipapi.Call, visit func(backend, model string)) error {
	c.identityCalls = append(c.identityCalls, call)
	if c.err != nil {
		return c.err
	}
	if c.reference != nil {
		for _, leaf := range c.reference {
			visit(leaf.backend, leaf.model)
		}
		return nil
	}
	primary := c.primary(call.Route.Selector)
	visit(primary.backend, primary.model)
	for _, extra := range c.extra {
		visit(extra.backend, extra.model)
	}
	return nil
}

// TestTargetCompilerIsNilSafe pins the fail-closed rule of design §1: a
// configuration that declares a model policy and carries no bound port
// refuses the phase explicitly instead of dispatching it against an
// unchecked target.
func TestTargetCompilerIsNilSafe(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.ModelsDeclared = true
	cfg.TargetCompiler = nil
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call)
	if !errors.Is(err, ErrTargetCompilerUnavailable) {
		t.Fatalf("error = %v, want %v", err, ErrTargetCompilerUnavailable)
	}
}

// TestTargetCompilerRefusesAFailedCompilation pins the same fail-closed rule
// for a port that answers with an error: the phase is refused, and the
// refusal names the phase and never the value.
func TestTargetCompilerRefusesAFailedCompilation(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.ModelsDeclared = true
	cfg.TargetCompiler = &stubTargetCompiler{err: errors.New("unknown backend")}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call)
	if !errors.Is(err, ErrTargetCompilationFailed) {
		t.Fatalf("error = %v, want %v", err, ErrTargetCompilationFailed)
	}
}

// TestTargetCompilerKeepsTheCancellationIdentity pins the error identity of
// the refusal: a cancelled compilation stays a cancellation for the caller
// that classifies it, and not a target defect.
func TestTargetCompilerKeepsTheCancellationIdentity(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.ModelsDeclared = true
	cfg.TargetCompiler = &stubTargetCompiler{err: context.Canceled}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want a cancellation", err)
	}
	if !errors.Is(err, ErrTargetCompilationFailed) {
		t.Fatalf("error = %v, want %v", err, ErrTargetCompilationFailed)
	}
}

// TestCompiledTargetNegotiatesTheMergedOptions pins the negotiation of design
// §1: the required capabilities follow every merged option — the reasoning
// effort and the parallel tool calls alike — and not the effort alone.
func TestCompiledTargetNegotiatesTheMergedOptions(t *testing.T) {
	t.Parallel()

	compiler := &stubTargetCompiler{
		backend:       "openai",
		model:         "gpt-5",
		effort:        "high",
		parallelTools: true,
		caps:          aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
	}
	call := aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	target, err := CompileTarget(context.Background(), compiler, PhaseExtraction, call)
	if err != nil {
		t.Fatalf("CompileTarget: %v", err)
	}
	if len(target.Candidates) != 1 {
		t.Fatalf("compiled candidates = %d, want 1", len(target.Candidates))
	}
	negotiation := target.Candidates[0].Negotiation(call)
	if got, want := negotiation.Kind, aipapi.NegotiationDowngrade; got != want {
		t.Fatalf("negotiation kind = %q, want %q", got, want)
	}
	for _, capability := range []aipapi.Capability{aipapi.CapabilityReasoning, aipapi.CapabilityParallelToolCalls} {
		if !slicesContains(negotiation.Downgraded, capability) {
			t.Fatalf("negotiated downgrades = %v, want %s among them", negotiation.Downgraded, capability)
		}
	}
}

// TestPhaseDispatchCompilesTheTargetWithoutChangingTheCall pins the dispatch
// seam of design §1: the phase compiles its target through the bound port
// before the dispatch, and the dispatch baseline keeps the phase's own
// options — the runtime merges each attempt's route parameters itself.
func TestPhaseDispatchCompilesTheTargetWithoutChangingTheCall(t *testing.T) {
	cfg := workerTestConfig()
	cfg.ModelsDeclared = true
	// The scripted target declares the capabilities it actually serves: it
	// carries a reasoning effort, so it declares the reasoning capability.
	compiler := &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "high", caps: reasoningCaps()}
	cfg.TargetCompiler = compiler
	h := newWorkerTestHarness(t, cfg)
	h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}

	h.worker.Tick(context.Background())

	submits := h.aux.submitCalls()
	if len(submits) != 1 {
		t.Fatalf("auxiliary submissions = %d, want 1", len(submits))
	}
	if got, want := submits[0].req.Call.Options.ReasoningEffort, defaultPhaseEffort(PhaseExtraction); got != want {
		t.Fatalf("dispatched effort = %q, want %q", got, want)
	}
	if len(compiler.calls) != 1 {
		t.Fatalf("target compilations = %d, want 1", len(compiler.calls))
	}
	if got, want := compiler.calls[0].Route.Selector, workerTestExtractRM; got != want {
		t.Fatalf("compiled route = %q, want %q", got, want)
	}
}

// TestPhaseDispatchKeepsTheLaterCandidateIsolated pins the candidate
// isolation of design §1: a first candidate whose route parameters raise the
// effort does not change the effort a later candidate without route
// parameters receives.
func TestPhaseDispatchKeepsTheLaterCandidateIsolated(t *testing.T) {
	cfg := workerTestConfig()
	cfg.ModelsDeclared = true
	cfg.ExtractRoute = "openai:first?reasoning_effort=high | openai:second"
	compiler := &stubTargetCompiler{backend: "openai", model: "first", effort: "high", caps: reasoningCaps()}
	cfg.TargetCompiler = compiler
	h := newWorkerTestHarness(t, cfg)
	h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}

	h.worker.Tick(context.Background())

	submits := h.aux.submitCalls()
	if len(submits) != 1 {
		t.Fatalf("auxiliary submissions = %d, want 1", len(submits))
	}
	dispatched := submits[0].req.Call
	if got, want := dispatched.Options.ReasoningEffort, defaultPhaseEffort(PhaseExtraction); got != want {
		t.Fatalf("dispatched effort = %q, want %q", got, want)
	}
	// The later candidate merges its own (absent) route parameters into the
	// dispatch baseline, so it keeps the phase's own effort.
	later, err := aipapi.MergeRouteQueryIntoGenerationOptions(dispatched.Options, nil)
	if err != nil {
		t.Fatalf("MergeRouteQueryIntoGenerationOptions: %v", err)
	}
	if got, want := later.ReasoningEffort, defaultPhaseEffort(PhaseExtraction); got != want {
		t.Fatalf("later candidate effort = %q, want %q", got, want)
	}
}

// TestPhaseRefusesExplicitlyWhenTheCompilerIsUnavailable pins the dispatch
// refusal of design §1: a phase whose target cannot be compiled is never
// dispatched, and the refusal is counted on the ordinary failure path.
func TestPhaseRefusesExplicitlyWhenTheCompilerIsUnavailable(t *testing.T) {
	cfg := workerTestConfig()
	cfg.ModelsDeclared = true
	cfg.TargetCompiler = nil
	h := newWorkerTestHarness(t, cfg)
	h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}

	h.worker.Tick(context.Background())

	if submits := h.aux.submitCalls(); len(submits) != 0 {
		t.Fatalf("auxiliary submissions = %d, want 0", len(submits))
	}
	fails := h.store.sessionFailCalls()
	if len(fails) != 1 {
		t.Fatalf("FailSession calls = %d, want 1", len(fails))
	}
	if got, want := fails[0].retryAtMS, h.base.Add(cfg.RetryBackoff).UnixMilli(); got != want {
		t.Fatalf("retryAtMS = %d, want %d", got, want)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
}

// TestBothPhasesRefuseAnUncompilableTarget pins the refusal of design §1 for
// both background phases: neither phase dispatches against an unchecked
// target, and both count the refusal on their ordinary failure path.
func TestBothPhasesRefuseAnUncompilableTarget(t *testing.T) {
	t.Parallel()

	cfg := workerTestConfig()
	cfg.ModelsDeclared = true
	cfg.TargetCompiler = &stubTargetCompiler{err: errors.New("unknown backend")}
	h := newWorkerTestHarness(t, cfg)
	h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}
	h.store.banks = []ClaimedBank{workerClaimedBank()}
	h.store.extractions = []Extraction{workerExtraction(h.base)}

	h.worker.Tick(context.Background())

	if submits := h.aux.submitCalls(); len(submits) != 0 {
		t.Fatalf("auxiliary submissions = %d, want 0", len(submits))
	}
	if got := len(h.store.sessionFailCalls()); got != 1 {
		t.Fatalf("FailSession calls = %d, want 1", got)
	}
	if got := len(h.store.bankFailCalls()); got != 1 {
		t.Fatalf("FailBank calls = %d, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase2Failed); got != 1 {
		t.Fatalf("phase2_failed = %v, want 1", got)
	}
}

// TestLegacyConfigDispatchesWithoutTheCompiledTarget pins the legacy
// behaviour a configuration without a model policy keeps: the phase dispatches
// with the operator's route and today's effort, and no target is compiled.
func TestLegacyConfigDispatchesWithoutTheCompiledTarget(t *testing.T) {
	cfg := workerTestConfig()
	h := newWorkerTestHarness(t, cfg)
	h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}

	h.worker.Tick(context.Background())

	submits := h.aux.submitCalls()
	if len(submits) != 1 {
		t.Fatalf("auxiliary submissions = %d, want 1", len(submits))
	}
	if got, want := submits[0].req.Call.Options.ReasoningEffort, defaultPhaseEffort(PhaseExtraction); got != want {
		t.Fatalf("dispatched effort = %q, want %q", got, want)
	}
}

// slicesContains reports whether the slice holds the capability.
func slicesContains(capabilities []aipapi.Capability, want aipapi.Capability) bool {
	return slices.Contains(capabilities, want)
}
