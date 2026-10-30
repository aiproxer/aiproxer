package proxymemory

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The dispatch-time enforcement of the per-phase model policy against the
// final compiled actual target (design §2 and §3; Requirements 1.4 and 1.5).

// modelPolicyTestConfig returns a configuration with a declared model policy:
// the operator's default target, the lock set and the allowed inventory the
// test names. The routes name one concrete target, so the inherited target
// of a phase is that target.
func modelPolicyTestConfig(extract, consolidation PhasePolicy) Config {
	cfg := DefaultConfig()
	cfg.Principals = []string{workerTestPrincipal}
	cfg.ExtractRoute = "openai:gpt-5"
	cfg.ConsolidateRoute = "openai:gpt-5"
	cfg.ModelsDeclared = true
	cfg.Models.Extraction = extract
	cfg.Models.Consolidation = consolidation
	return cfg
}

// reasoningCaps are the capabilities of a target that carries a reasoning
// effort: the scripted target declares the capabilities it actually serves.
func reasoningCaps() aipapi.BackendCaps {
	return aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityStructuredOutputs,
		aipapi.CapabilityReasoning,
	)
}

// permittedPolicy is the operator's policy of a phase whose declared target
// and allowed inventory cover the "openai:gpt-5" target with the "low" and
// "medium" efforts.
func permittedPolicy() PhasePolicy {
	return PhasePolicy{
		Defaults: PhaseTarget{Backend: "openai", Model: "gpt-5", ReasoningEffort: "low"},
		Inventory: PhaseInventory{
			Backends: []string{"openai"},
			Models:   []string{"gpt-5"},
			Targets: []TargetPolicy{
				{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium"}},
			},
		},
	}
}

// policyRefusal returns the policy refusal of err, or fails the test.
func policyRefusal(t *testing.T, err error) *PolicyError {
	t.Helper()
	var policyErr *PolicyError
	if !errors.As(err, &policyErr) {
		t.Fatalf("error = %v, want a policy refusal", err)
	}
	return policyErr
}

// TestCompiledTargetAcceptsThePermittedTarget pins the allowed case: a
// compiled actual target the phase's policy covers is dispatched unchanged.
func TestCompiledTargetAcceptsThePermittedTarget(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "low", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err != nil {
		t.Fatalf("enforceCompiledTarget: %v", err)
	}
}

// TestCompiledTargetRefusesAnOutOfInventoryModel pins the inventory check of
// design §3: a model the phase's allowed inventory does not cover is
// refused explicitly, not silently substituted.
func TestCompiledTargetRefusesAnOutOfInventoryModel(t *testing.T) {
	t.Parallel()

	policy := permittedPolicy()
	policy.Inventory.Models = []string{"gpt-5-mini"}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "low", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}
}

// TestCompiledTargetRefusesAnOutOfInventoryBackend pins the backend half of
// the same check.
func TestCompiledTargetRefusesAnOutOfInventoryBackend(t *testing.T) {
	t.Parallel()

	policy := permittedPolicy()
	policy.Inventory.Backends = []string{"anthropic"}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "low", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldBackend {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldBackend)
	}
}

// TestCompiledTargetRefusesAnAliasOutsideTheInventory pins the alias half of
// the inventory check: the route names an alias the operator allows, and the
// alias resolves to a model the inventory does not cover, so the phase is
// refused instead of running an unvetted target.
func TestCompiledTargetRefusesAnAliasOutsideTheInventory(t *testing.T) {
	t.Parallel()

	policy := permittedPolicy()
	policy.Defaults = PhaseTarget{}
	policy.Inventory.Models = []string{"gpt5"}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.ExtractRoute = "gpt5"
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-4", effort: "low", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "gpt5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}
}

// TestCompiledTargetRefusesAQueryEffortOutsideTheInventory pins the
// query-parameter half of the inventory check: the route parameter raises
// the effort to a value the phase's per-target effort policy does not
// allow, so the phase is refused.
func TestCompiledTargetRefusesAQueryEffortOutsideTheInventory(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "high", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5?reasoning_effort=high"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldReasoningEffort {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
	}
}

// TestCompiledTargetAcceptsAPermittedQueryEffort pins the other side of the
// same case: a route parameter that raises the effort to a value the phase's
// per-target effort policy allows is dispatched unchanged.
func TestCompiledTargetAcceptsAPermittedQueryEffort(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "medium", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5?reasoning_effort=medium"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err != nil {
		t.Fatalf("enforceCompiledTarget: %v", err)
	}
}

// TestCompiledTargetRefusesALockedFieldTheRouteMoved pins the lock check of
// design §3 against the final compiled actual target: a route parameter that
// moves a locked effort is a bypass of the operator's hold, so the phase is
// refused.
func TestCompiledTargetRefusesALockedFieldTheRouteMoved(t *testing.T) {
	t.Parallel()

	policy := permittedPolicy()
	policy.Locks.ReasoningEffort = true
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "high", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5?reasoning_effort=high"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldReasoningEffort {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
	}
}

// TestCompiledTargetRefusesALockedModelTheAliasMoved pins the same rule for
// the model field: an alias that moves a locked model is a bypass of the
// operator's hold.
func TestCompiledTargetRefusesALockedModelTheAliasMoved(t *testing.T) {
	t.Parallel()

	policy := permittedPolicy()
	policy.Locks.Model = true
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-4", effort: "low", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}
}

// TestCompiledTargetRefusesAnUnsupportedEffort pins the capability check of
// design §2: the actual target does not carry the reasoning capability, so
// the runtime would strip the effort. The phase is refused before the
// dispatch instead of being degraded downstream.
func TestCompiledTargetRefusesAnUnsupportedEffort(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	// The target declares the capabilities it actually serves: no
	// reasoning capability, so the effort would be negotiated away.
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5",
		effort:  "low",
		caps:    aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs),
	}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldReasoningEffort {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
	}
}

// TestCompiledTargetCapabilityRefusalCarriesItsKind pins the bounded error
// kind of the capability refusal: an actual target that does not prove the
// reasoning capability — false or unknown — is classified as the same
// explicit refusal, so the caller can count it without parsing a message.
func TestCompiledTargetCapabilityRefusalCarriesItsKind(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5",
		effort:  "low",
		caps:    aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
	}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call)
	if !errors.Is(err, ErrReasoningCapabilityRefused) {
		t.Fatalf("error = %v, want %v", err, ErrReasoningCapabilityRefused)
	}
}

// TestCompiledTargetChecksEveryCandidate pins the candidate coverage of
// Requirement 1.5: the runtime may fail over to a later candidate, so a
// candidate the policy does not cover is refused even when the first
// candidate is permitted.
func TestCompiledTargetChecksEveryCandidate(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5",
		effort:  "low",
		caps:    reasoningCaps(),
		extra:   []stubCompiledCandidate{{backend: "openai", model: "gpt-4", effort: "low", caps: reasoningCaps()}},
	}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}
}

// TestCompiledTargetKeepsTheOperatorRouteWithAnEmptyInventory pins the
// meaning of an empty inventory (design §3): it accepts no user-selectable
// value, so the phase keeps the operator's own route — it does not mean that
// the phase runs anything.
func TestCompiledTargetKeepsTheOperatorRouteWithAnEmptyInventory(t *testing.T) {
	t.Parallel()

	cfg := modelPolicyTestConfig(PhasePolicy{}, PhasePolicy{})
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "low", caps: reasoningCaps()}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err != nil {
		t.Fatalf("enforceCompiledTarget: %v", err)
	}
}

// TestPhaseRefusesAnOutOfInventoryTargetBeforeDispatch pins the dispatch
// refusal of design §3: a phase whose compiled actual target is not covered
// is never dispatched, and the refusal is counted on the ordinary failure
// path.
func TestPhaseRefusesAnOutOfInventoryTargetBeforeDispatch(t *testing.T) {
	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	policy := permittedPolicy()
	policy.Inventory.Models = []string{"gpt-5-mini"}
	cfg.Models.Extraction = policy
	cfg.TargetCompiler = &stubTargetCompiler{backend: "openai", model: "gpt-5", effort: "low", caps: reasoningCaps()}
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

// TestPhaseRefusesAnUnsupportedEffortBeforeDispatch pins the dispatch
// refusal of design §2: the phase never reaches the auxiliary client when
// the actual target cannot carry the effort, so the effort is never
// silently downgraded.
func TestPhaseRefusesAnUnsupportedEffortBeforeDispatch(t *testing.T) {
	cfg := modelPolicyTestConfig(permittedPolicy(), permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5",
		effort:  "low",
		caps:    aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityStructuredOutputs),
	}
	h := newWorkerTestHarness(t, cfg)
	h.store.sessions = []ClaimedSession{workerClaimedSession(workerTestALeg1, h.base)}

	h.worker.Tick(context.Background())

	if submits := h.aux.submitCalls(); len(submits) != 0 {
		t.Fatalf("auxiliary submissions = %d, want 0", len(submits))
	}
	if got := len(h.store.sessionFailCalls()); got != 1 {
		t.Fatalf("FailSession calls = %d, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventPhase1Failed); got != 1 {
		t.Fatalf("phase1_failed = %v, want 1", got)
	}
	if got := captureCounter(t, h.metrics, EventModelPolicyRefusedReasoning); got != 1 {
		t.Fatalf("model-policy capability refusals = %v, want 1", got)
	}
}
