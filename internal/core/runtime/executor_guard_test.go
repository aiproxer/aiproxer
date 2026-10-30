package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The generic pre-open guard of design §2: the core owns the chokepoint, the
// bound guard refuses the whole operation, and an unbound guard is inert.

// refusingGuard refuses every attempt it is asked about.
type refusingGuard struct {
	seen int
}

func (g *refusingGuard) CheckAttempt(_ context.Context, _ AttemptGuardInput) error {
	g.seen++
	return errors.New("the policy refuses this attempt")
}

// guardTestFacts returns the frozen facts of one guarded attempt.
func guardTestFacts() requestFacts {
	return requestFacts{recvTurnFacts: recvTurnFacts{
		baseline: aipapi.Call{
			Route:   aipapi.RouteIntent{Selector: "openai:openai/test-model"},
			Options: aipapi.GenerationOptions{ReasoningEffort: "low"},
		},
		auxiliaryRole: "proxy_memory_phase1",
	}}
}

// TestAttemptGuardRefusalIsOperationLevel pins the refusal semantics of
// design §2: the guard's refusal leaves the attempt-open path as an error, so
// the operation fails explicitly instead of excluding the candidate and
// serving another one.
func TestAttemptGuardRefusalIsOperationLevel(t *testing.T) {
	t.Parallel()

	guard := &refusingGuard{}
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
	tx := &attemptTx{reqFacts: guardTestFacts()}
	cand := routing.AttemptCandidate{
		Key: "openai:openai/test-model",
		Primary: routing.Primary{
			Backend: "openai",
			Model:   "openai/test-model",
		},
	}
	openCall := aipapi.Call{Options: aipapi.GenerationOptions{ReasoningEffort: "medium"}}

	err := e.checkAttemptGuard(context.Background(), tx, cand, openCall, execbackend.Backend{})
	if err == nil {
		t.Fatal("checkAttemptGuard: want a refusal, got nil")
	}
	if !isAttemptGuardDenial(err) {
		t.Fatalf("error = %v, want an attempt-guard denial", err)
	}
	if guard.seen != 1 {
		t.Fatalf("guard consultations = %d, want 1", guard.seen)
	}
}

// TestUnboundAttemptGuardIsInert pins the inertness of design §2: a
// generation without a bound guard behaves exactly as before.
func TestUnboundAttemptGuardIsInert(t *testing.T) {
	t.Parallel()

	e := &Executor{}
	tx := &attemptTx{reqFacts: guardTestFacts()}
	cand := routing.AttemptCandidate{
		Key: "openai:openai/test-model",
		Primary: routing.Primary{
			Backend: "openai",
			Model:   "openai/test-model",
		},
	}
	if err := e.checkAttemptGuard(context.Background(), tx, cand, aipapi.Call{}, execbackend.Backend{}); err != nil {
		t.Fatalf("checkAttemptGuard: %v", err)
	}
}

// TestAttemptGuardExpectationIsPerCandidate pins the exactness of design §2:
// the expectation is the frozen baseline merged with the actual candidate's
// route parameters, so two arms that share a backend and a model and differ
// only in their parameters get their own expectation.
func TestAttemptGuardExpectationIsPerCandidate(t *testing.T) {
	t.Parallel()

	var seen AttemptGuardInput
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: recordingGuard{seen: &seen}}}}
	tx := &attemptTx{reqFacts: guardTestFacts()}
	cand := routing.AttemptCandidate{
		Key: "openai:openai/test-model?reasoning_effort=high",
		Primary: routing.Primary{
			Backend: "openai",
			Model:   "openai/test-model",
			Params:  map[string][]string{"reasoning_effort": {"high"}},
		},
	}
	openCall := aipapi.Call{Options: aipapi.GenerationOptions{ReasoningEffort: "high"}}

	if err := e.checkAttemptGuard(context.Background(), tx, cand, openCall, execbackend.Backend{}); err != nil {
		t.Fatalf("checkAttemptGuard: %v", err)
	}
	if got, want := seen.ExpectedOptions.ReasoningEffort, "high"; got != want {
		t.Fatalf("expected effort = %q, want %q", got, want)
	}
	if got, want := seen.CandidateKey, cand.Key; got != want {
		t.Fatalf("candidate key = %q, want %q", got, want)
	}
	if got, want := seen.AuxiliaryRole, "proxy_memory_phase1"; got != want {
		t.Fatalf("auxiliary role = %q, want %q", got, want)
	}
}

// recordingGuard records the facts of one pre-open check.
type recordingGuard struct {
	seen *AttemptGuardInput
}

func (g recordingGuard) CheckAttempt(_ context.Context, in AttemptGuardInput) error {
	*g.seen = in
	return nil
}

// scopedTestGuard is a test guard implementing both AttemptGuard and
// AttemptGuardRoleScope. It governs exactly the roles in govern.
type scopedTestGuard struct {
	govern     map[string]bool
	scopeCalls int
	checkCalls int
	seen       AttemptGuardInput
}

func (g *scopedTestGuard) ShouldCheckAttempt(role string) bool {
	g.scopeCalls++
	return g.govern[role]
}

func (g *scopedTestGuard) CheckAttempt(_ context.Context, in AttemptGuardInput) error {
	g.checkCalls++
	g.seen = in
	return nil
}

// scopedGuardTestFacts returns frozen facts with the given auxiliary role.
func scopedGuardTestFacts(role string) requestFacts {
	facts := guardTestFacts()
	facts.auxiliaryRole = role
	return facts
}

// scopedGuardCandidate is one guarded attempt candidate.
func scopedGuardCandidate() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Key: "openai:openai/test-model",
		Primary: routing.Primary{
			Backend: "openai",
			Model:   "openai/test-model",
		},
	}
}

// TestAttemptGuardRoleScopeSkipsUngovernedWithoutFacts pins the fast path:
// an ungoverned role consults the scope once and gathers no pre-open facts:
// no CheckAttempt and no ResolveEffortSupport invocation.
func TestAttemptGuardRoleScopeSkipsUngovernedWithoutFacts(t *testing.T) {
	t.Parallel()

	for _, role := range []string{"", "some_unrelated_role"} {
		t.Run("role="+role, func(t *testing.T) {
			t.Parallel()

			guard := &scopedTestGuard{govern: map[string]bool{
				"proxy_memory_phase1": true,
				"proxy_memory_phase2": true,
			}}
			e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
			tx := &attemptTx{reqFacts: scopedGuardTestFacts(role)}
			effortCalls := 0
			be := execbackend.Backend{ResolveEffortSupport: func(context.Context, aipapi.Call, routing.AttemptCandidate, string) execbackend.EffortSupportState {
				effortCalls++
				return execbackend.EffortSupportSupported
			}}
			openCall := aipapi.Call{Options: aipapi.GenerationOptions{ReasoningEffort: "low"}}

			if err := e.checkAttemptGuard(context.Background(), tx, scopedGuardCandidate(), openCall, be); err != nil {
				t.Fatalf("checkAttemptGuard: %v", err)
			}
			if guard.scopeCalls != 1 {
				t.Fatalf("scope consultations = %d, want 1", guard.scopeCalls)
			}
			if guard.checkCalls != 0 {
				t.Fatalf("guard consultations = %d, want 0", guard.checkCalls)
			}
			if effortCalls != 0 {
				t.Fatalf("effort-support queries = %d, want 0", effortCalls)
			}
		})
	}
}

// TestAttemptGuardRoleScopeConsultsGovernedRoles pins the governed path:
// both memory phases still receive the exact installation and per-value
// support tri-states with the frozen role.
func TestAttemptGuardRoleScopeConsultsGovernedRoles(t *testing.T) {
	t.Parallel()

	for _, role := range []string{"proxy_memory_phase1", "proxy_memory_phase2"} {
		t.Run("role="+role, func(t *testing.T) {
			t.Parallel()

			guard := &scopedTestGuard{govern: map[string]bool{
				"proxy_memory_phase1": true,
				"proxy_memory_phase2": true,
			}}
			e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
			tx := &attemptTx{reqFacts: scopedGuardTestFacts(role)}
			effortCalls := 0
			be := execbackend.Backend{ResolveEffortSupport: func(context.Context, aipapi.Call, routing.AttemptCandidate, string) execbackend.EffortSupportState {
				effortCalls++
				return execbackend.EffortSupportSupported
			}}
			openCall := aipapi.Call{Options: aipapi.GenerationOptions{ReasoningEffort: "low"}}

			if err := e.checkAttemptGuard(context.Background(), tx, scopedGuardCandidate(), openCall, be); err != nil {
				t.Fatalf("checkAttemptGuard: %v", err)
			}
			if guard.scopeCalls != 1 {
				t.Fatalf("scope consultations = %d, want 1", guard.scopeCalls)
			}
			if guard.checkCalls != 1 {
				t.Fatalf("guard consultations = %d, want 1", guard.checkCalls)
			}
			if effortCalls != 1 {
				t.Fatalf("effort-support queries = %d, want 1", effortCalls)
			}
			if guard.seen.AuxiliaryRole != role {
				t.Fatalf("auxiliary role = %q, want %q", guard.seen.AuxiliaryRole, role)
			}
			if guard.seen.EffortSupport != execbackend.EffortSupportSupported {
				t.Fatalf("effort support = %v, want supported", guard.seen.EffortSupport)
			}
			if guard.seen.CandidateKey != "openai:openai/test-model" {
				t.Fatalf("candidate key = %q, want %q", guard.seen.CandidateKey, "openai:openai/test-model")
			}
		})
	}
}

// TestUnscopedAttemptGuardStillSeesOrdinaryTraffic pins backward
// compatibility: a generic guard without the scope extension is still
// consulted on ordinary traffic and still receives the effort-support fact.
func TestUnscopedAttemptGuardStillSeesOrdinaryTraffic(t *testing.T) {
	t.Parallel()

	var seen AttemptGuardInput
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: recordingGuard{seen: &seen}}}}
	facts := guardTestFacts()
	facts.auxiliaryRole = ""
	tx := &attemptTx{reqFacts: facts}
	effortCalls := 0
	be := execbackend.Backend{ResolveEffortSupport: func(context.Context, aipapi.Call, routing.AttemptCandidate, string) execbackend.EffortSupportState {
		effortCalls++
		return execbackend.EffortSupportSupported
	}}
	openCall := aipapi.Call{Options: aipapi.GenerationOptions{ReasoningEffort: "low"}}

	if err := e.checkAttemptGuard(context.Background(), tx, scopedGuardCandidate(), openCall, be); err != nil {
		t.Fatalf("checkAttemptGuard: %v", err)
	}
	if seen.AuxiliaryRole != "" {
		t.Fatalf("auxiliary role = %q, want %q", seen.AuxiliaryRole, "")
	}
	if effortCalls != 1 {
		t.Fatalf("effort-support queries = %d, want 1", effortCalls)
	}
	if seen.EffortSupport != execbackend.EffortSupportSupported {
		t.Fatalf("effort support = %v, want supported", seen.EffortSupport)
	}
}

// panickingScopeGuard panics in the scope selector.
type panickingScopeGuard struct {
	checkCalls int
}

func (g *panickingScopeGuard) ShouldCheckAttempt(string) bool {
	panic("boom-scope-selector")
}

func (g *panickingScopeGuard) CheckAttempt(_ context.Context, _ AttemptGuardInput) error {
	g.checkCalls++
	return nil
}

// TestAttemptGuardRoleScopePanicFailsClosed pins the containment: a scope
// selector panic refuses the attempt before provider I/O, gathers no facts
// and leaks no panic payload.
func TestAttemptGuardRoleScopePanicFailsClosed(t *testing.T) {
	t.Parallel()

	guard := &panickingScopeGuard{}
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
	tx := &attemptTx{reqFacts: scopedGuardTestFacts("")}
	effortCalls := 0
	be := execbackend.Backend{ResolveEffortSupport: func(context.Context, aipapi.Call, routing.AttemptCandidate, string) execbackend.EffortSupportState {
		effortCalls++
		return execbackend.EffortSupportSupported
	}}

	err := e.checkAttemptGuard(context.Background(), tx, scopedGuardCandidate(), aipapi.Call{}, be)
	if err == nil {
		t.Fatal("checkAttemptGuard: want a refusal, got nil")
	}
	if !isAttemptGuardDenial(err) {
		t.Fatalf("error = %v, want an attempt-guard denial", err)
	}
	if guard.checkCalls != 0 {
		t.Fatalf("guard consultations = %d, want 0", guard.checkCalls)
	}
	if effortCalls != 0 {
		t.Fatalf("effort-support queries = %d, want 0", effortCalls)
	}
	if msg := err.Error(); containsPanicPayload(msg, "boom-scope-selector") {
		t.Fatalf("denial leaks the panic payload: %q", msg)
	}
}

// cancellingScopedGuard returns a cancellation from CheckAttempt.
type cancellingScopedGuard struct {
	scopeCalls int
}

func (g *cancellingScopedGuard) ShouldCheckAttempt(string) bool {
	g.scopeCalls++
	return true
}

func (g *cancellingScopedGuard) CheckAttempt(ctx context.Context, _ AttemptGuardInput) error {
	return ctx.Err()
}

// TestAttemptGuardRoleScopeCancellationPropagates pins that a governed
// cancellation is still a cancellation, not a policy refusal.
func TestAttemptGuardRoleScopeCancellationPropagates(t *testing.T) {
	t.Parallel()

	guard := &cancellingScopedGuard{}
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
	tx := &attemptTx{reqFacts: scopedGuardTestFacts("proxy_memory_phase1")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := e.checkAttemptGuard(ctx, tx, scopedGuardCandidate(), aipapi.Call{}, execbackend.Backend{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if isAttemptGuardDenial(err) {
		t.Fatalf("error = %v, a cancellation must not be a guard denial", err)
	}
}

func containsPanicPayload(msg, payload string) bool {
	return strings.Contains(msg, payload)
}
