package proxymemory

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The compiled-domain lock of design §3 against the final compiled actual
// target (Requirement 1.2 and 1.5).
//
// The operator's reference is the operator's **effective** value: the
// operator's declared value completed by the operator's default route. The
// reference keeps every leaf it compiles to, and the comparison is made leaf
// by leaf on the locked fields only.

// lockedPolicy is the policy of a phase whose model is locked and whose
// inventory covers the two targets the reference resolves to.
func lockedPolicy() PhasePolicy {
	policy := permittedPolicy()
	policy.Locks.Model = true
	policy.Inventory.Models = []string{"gpt-5", "gpt-5-mini"}
	policy.Inventory.Targets = []TargetPolicy{
		{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium"}},
		{Backend: "openai", Model: "gpt-5-mini", ReasoningEfforts: []string{"low", "medium"}},
	}
	return policy
}

// TestCompiledTargetRefusesAChangedTargetUnderAnInheritedModelLock pins the
// effective reference of design §3: the operator named no model default, so
// the operator's default route supplies the model, and the lock pins that
// inherited value. A target that carries another model is refused.
//
// Before the repair the reference was compiled from the *declared* target,
// which is empty here, so the lock was skipped and the changed target was
// accepted.
func TestCompiledTargetRefusesAChangedTargetUnderAnInheritedModelLock(t *testing.T) {
	t.Parallel()

	policy := permittedPolicy()
	policy.Locks.Model = true
	policy.Inventory.Models = []string{"gpt-5", "gpt-4"}
	// The changed target is permitted by the effort policy as well, so the
	// model is the only field the policy refuses.
	policy.Inventory.Targets = []TargetPolicy{
		{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium"}},
		{Backend: "openai", Model: "gpt-4", ReasoningEfforts: []string{"low", "medium"}},
	}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	// The operator named no model default: the route supplies it.
	policy.Defaults = PhaseTarget{}
	cfg.Models.Extraction = policy
	compiler := &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-4",
		effort:  "low",
		caps:    reasoningCaps(),
		// The operator's effective reference is the route's own target.
		reference: []stubCompiledCandidate{{backend: "openai", model: "gpt-5"}},
	}
	cfg.TargetCompiler = compiler

	effective, err := cfg.EffectiveTarget(PhaseExtraction)
	if err != nil {
		t.Fatalf("EffectiveTarget: %v", err)
	}
	if got, want := effective.Model, "gpt-5"; got != want {
		t.Fatalf("effective model = %q, want %q", got, want)
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

// TestCompiledTargetAcceptsTheOperatorsUnchangedInheritedModelLock pins the
// positive side of the same rule: the operator's own inherited target is
// served, so the lock does not refuse the operator's own route.
func TestCompiledTargetAcceptsTheOperatorsUnchangedInheritedModelLock(t *testing.T) {
	t.Parallel()

	policy := permittedPolicy()
	policy.Locks.Model = true
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	policy.Defaults = PhaseTarget{}
	cfg.Models.Extraction = policy
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5",
		effort:  "low",
		caps:    reasoningCaps(),
		reference: []stubCompiledCandidate{
			{backend: "openai", model: "gpt-5"},
		},
	}

	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err != nil {
		t.Fatalf("enforceCompiledTarget: %v", err)
	}
}

// TestCompiledTargetAcceptsALaterLeafOfTheOperatorsReference pins the full
// reference set of design §3: the operator's unchanged alias resolves to
// several leaves, and every one of them is the operator's own identity, so
// a candidate that addresses the later leaf is not a lock violation.
//
// Before the repair the reference kept only its first candidate, so the
// operator's own unchanged alias was refused.
func TestCompiledTargetAcceptsALaterLeafOfTheOperatorsReference(t *testing.T) {
	t.Parallel()

	policy := lockedPolicy()
	policy.Defaults = PhaseTarget{Model: "gpt5"}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.ExtractRoute = "gpt5"
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5",
		effort:  "low",
		caps:    reasoningCaps(),
		extra:   []stubCompiledCandidate{{backend: "openai", model: "gpt-5-mini", effort: "low", caps: reasoningCaps()}},
	}

	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "gpt5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err != nil {
		t.Fatalf("enforceCompiledTarget: %v", err)
	}
}

// TestCompiledTargetRefusesAChangedModelUnderAMultiLeafReference pins the
// negative side of the full reference set: a model that is on no leaf of the
// operator's reference is refused, even when the other leaves of the
// reference are permitted.
func TestCompiledTargetRefusesAChangedModelUnderAMultiLeafReference(t *testing.T) {
	t.Parallel()

	policy := lockedPolicy()
	policy.Defaults = PhaseTarget{Model: "gpt5"}
	policy.Inventory.Models = []string{"gpt-5", "gpt-5-mini", "gpt-4"}
	// The changed model is permitted by the effort policy as well, so the
	// model is the only field the policy refuses.
	policy.Inventory.Targets = []TargetPolicy{
		{Backend: "openai", Model: "gpt-5", ReasoningEfforts: []string{"low", "medium"}},
		{Backend: "openai", Model: "gpt-5-mini", ReasoningEfforts: []string{"low", "medium"}},
		{Backend: "openai", Model: "gpt-4", ReasoningEfforts: []string{"low", "medium"}},
	}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.ExtractRoute = "gpt5"
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-4",
		effort:  "low",
		caps:    reasoningCaps(),
		reference: []stubCompiledCandidate{
			{backend: "openai", model: "gpt-5"},
			{backend: "openai", model: "gpt-5-mini"},
		},
	}

	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "gpt5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldModel {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldModel)
	}
}

// TestCompiledTargetRefusesACrossCombinedLockedIdentity pins the unit of
// identity: the leaf is the unit, so a candidate that combines the backend
// of one leaf with the model of another is not the operator's own identity,
// even when every locked field's value is carried by some leaf.
func TestCompiledTargetRefusesACrossCombinedLockedIdentity(t *testing.T) {
	t.Parallel()

	policy := lockedPolicy()
	policy.Locks.Backend = true
	policy.Defaults = PhaseTarget{Backend: "openai", Model: "gpt5"}
	policy.Inventory.Backends = []string{"openai", "anthropic"}
	policy.Inventory.Models = []string{"gpt-5", "gpt-5-mini"}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.ExtractRoute = "gpt5"
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5-mini",
		effort:  "low",
		caps:    reasoningCaps(),
		reference: []stubCompiledCandidate{
			{backend: "openai", model: "gpt-5"},
			{backend: "anthropic", model: "gpt-5-mini"},
		},
	}

	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "gpt5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err == nil {
		t.Fatal("enforceCompiledTarget: want a refusal for the cross-combined identity")
	}
}

// TestCompiledTargetCompilesTheReferenceIndependently pins the independence
// of the reference compile: the operator's reference is compiled through its
// own identity-only port call, so a later divergence between the operator's
// reference and the phase's own target cannot make the lock tautological.
func TestCompiledTargetCompilesTheReferenceIndependently(t *testing.T) {
	t.Parallel()

	policy := lockedPolicy()
	policy.Defaults = PhaseTarget{Model: "gpt5"}
	cfg := modelPolicyTestConfig(policy, permittedPolicy())
	cfg.ExtractRoute = "gpt5"
	compiler := &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5-mini",
		effort:  "low",
		caps:    reasoningCaps(),
		// The operator's reference resolves to two leaves, and the phase's
		// own target addresses the later one.
		reference: []stubCompiledCandidate{
			{backend: "openai", model: "gpt-5"},
			{backend: "openai", model: "gpt-5-mini"},
		},
	}
	cfg.TargetCompiler = compiler

	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "gpt5"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err != nil {
		t.Fatalf("enforceCompiledTarget: %v", err)
	}
	if len(compiler.identityCalls) != 1 {
		t.Fatalf("identity-only compilations = %d, want 1", len(compiler.identityCalls))
	}
	if len(compiler.calls) != 1 {
		t.Fatalf("capability compilations = %d, want 1", len(compiler.calls))
	}
	// The reference compile is content-free: it carries the reference
	// selector and no options.
	if got := compiler.identityCalls[0].Options; got != (aipapi.GenerationOptions{}) {
		t.Fatalf("reference compile options = %+v, want none", got)
	}
}

// TestCompiledTargetKeepsAnUnlockedEffortMovedByARouteParameter pins the
// projection: a route parameter that moves an unlocked effort is not a lock
// violation, while a locked effort that the same parameter moves is.
func TestCompiledTargetKeepsAnUnlockedEffortMovedByARouteParameter(t *testing.T) {
	t.Parallel()

	unlocked := permittedPolicy()
	cfg := modelPolicyTestConfig(unlocked, permittedPolicy())
	cfg.TargetCompiler = &stubTargetCompiler{
		backend: "openai",
		model:   "gpt-5",
		effort:  "medium",
		caps:    reasoningCaps(),
	}
	call := &aipapi.Call{
		Route:   aipapi.RouteIntent{Selector: "openai:gpt-5?reasoning_effort=medium"},
		Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
	}
	if err := enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call); err != nil {
		t.Fatalf("enforceCompiledTarget: %v", err)
	}

	locked := permittedPolicy()
	locked.Locks.ReasoningEffort = true
	cfg.Models.Extraction = locked
	refusal := policyRefusal(t, enforceCompiledTarget(context.Background(), cfg, PhaseExtraction, call))
	if refusal.Field != FieldReasoningEffort {
		t.Fatalf("refused field = %q, want %q", refusal.Field, FieldReasoningEffort)
	}
}
