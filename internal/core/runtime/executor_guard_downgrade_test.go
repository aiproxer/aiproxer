package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// The optional negotiated-downgrade extension of the pre-open guard seam: the
// core consults it at the two places where it applies a negotiated downgrade,
// before the downgrade is applied, and a bound guard that refuses a
// proxy-memory effort strip refuses the whole operation.

// downgradeRefusingGuard refuses one negotiated downgrade and records the
// content-free input it received.
type downgradeRefusingGuard struct {
	seen []AttemptDowngradeInput
}

func (g *downgradeRefusingGuard) CheckAttempt(_ context.Context, _ AttemptGuardInput) error {
	return nil
}

func (g *downgradeRefusingGuard) CheckNegotiatedDowngrade(_ context.Context, in AttemptDowngradeInput) error {
	g.seen = append(g.seen, in)
	return errors.New("the policy refuses the negotiated strip")
}

// downgradeRecordingGuard records the downgrade input and accepts it.
type downgradeRecordingGuard struct {
	seen []AttemptDowngradeInput
}

func (g *downgradeRecordingGuard) CheckAttempt(_ context.Context, _ AttemptGuardInput) error {
	return nil
}

func (g *downgradeRecordingGuard) CheckNegotiatedDowngrade(_ context.Context, in AttemptDowngradeInput) error {
	g.seen = append(g.seen, in)
	return nil
}

// guardWithoutDowngradeExtension implements only the pre-open method, so the
// optional extension stays unbound.
type guardWithoutDowngradeExtension struct{}

func (guardWithoutDowngradeExtension) CheckAttempt(_ context.Context, _ AttemptGuardInput) error {
	return nil
}

// downgradeTestBaseline returns the frozen dispatched call the attempt runs
// from. It carries the effort the served call must keep.
func downgradeTestBaseline() aipapi.Call {
	return aipapi.Call{Options: aipapi.GenerationOptions{ReasoningEffort: "low"}}
}

// downgradeTestCand returns one candidate the attempt addresses.
func downgradeTestCand() routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Key: "openai:openai/test-model",
		Primary: routing.Primary{
			Backend: "openai",
			Model:   "openai/test-model",
		},
	}
}

// TestNegotiatedDowngradeGuardRefusalIsOperationLevel pins the refusal
// semantics: a guard that refuses the negotiated strip fails the whole
// operation through the existing pre-open denial, so the runtime never falls
// back to another candidate after the strip.
func TestNegotiatedDowngradeGuardRefusalIsOperationLevel(t *testing.T) {
	t.Parallel()

	guard := &downgradeRefusingGuard{}
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
	res := aipapi.NegotiationResult{
		Kind:       aipapi.NegotiationDowngrade,
		Downgraded: []aipapi.Capability{aipapi.CapabilityReasoning},
	}

	err := e.checkAttemptDowngradeGuard(context.Background(), downgradeTestBaseline(), "proxy_memory_phase1", downgradeTestCand(), "low", res)
	if err == nil {
		t.Fatal("checkAttemptDowngradeGuard: want a refusal, got nil")
	}
	if !isAttemptGuardDenial(err) {
		t.Fatalf("error = %v, want an attempt-guard denial", err)
	}
	if len(guard.seen) != 1 {
		t.Fatalf("downgrade consultations = %d, want 1", len(guard.seen))
	}
	seen := guard.seen[0]
	if got, want := seen.AuxiliaryRole, "proxy_memory_phase1"; got != want {
		t.Fatalf("auxiliary role = %q, want %q", got, want)
	}
	if got, want := seen.ReasoningEffort, "low"; got != want {
		t.Fatalf("reasoning effort = %q, want %q", got, want)
	}
	if got, want := seen.ExpectedReasoningEffort, "low"; got != want {
		t.Fatalf("expected reasoning effort = %q, want %q", got, want)
	}
	if got, want := seen.CandidateKey, "openai:openai/test-model"; got != want {
		t.Fatalf("candidate key = %q, want %q", got, want)
	}
	if len(seen.Downgraded) != 1 || seen.Downgraded[0] != aipapi.CapabilityReasoning {
		t.Fatalf("downgraded = %v, want the reasoning capability", seen.Downgraded)
	}
}

// TestNegotiatedDowngradeGuardReceivesThePreStripEffort pins the ordering: the
// guard is consulted before the strip is applied, so it sees the effort the
// runtime would strip and not the empty value that follows.
func TestNegotiatedDowngradeGuardReceivesThePreStripEffort(t *testing.T) {
	t.Parallel()

	guard := &downgradeRecordingGuard{}
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
	res := aipapi.NegotiationResult{
		Kind:       aipapi.NegotiationDowngrade,
		Downgraded: []aipapi.Capability{aipapi.CapabilityReasoning},
	}

	if err := e.checkAttemptDowngradeGuard(context.Background(), downgradeTestBaseline(), "proxy_memory_phase1", downgradeTestCand(), "high", res); err != nil {
		t.Fatalf("checkAttemptDowngradeGuard: %v", err)
	}
	if len(guard.seen) != 1 || guard.seen[0].ReasoningEffort != "high" {
		t.Fatalf("guard input = %+v, want the pre-strip effort high", guard.seen)
	}
}

// TestNegotiatedDowngradeGuardIsInertWithoutTheExtension pins the fallback:
// a guard that implements only the pre-open method changes no behaviour, so
// the existing generic refusal at the open chokepoint still applies.
func TestNegotiatedDowngradeGuardIsInertWithoutTheExtension(t *testing.T) {
	t.Parallel()

	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guardWithoutDowngradeExtension{}}}}
	res := aipapi.NegotiationResult{
		Kind:       aipapi.NegotiationDowngrade,
		Downgraded: []aipapi.Capability{aipapi.CapabilityReasoning},
	}
	if err := e.checkAttemptDowngradeGuard(context.Background(), downgradeTestBaseline(), "proxy_memory_phase1", downgradeTestCand(), "low", res); err != nil {
		t.Fatalf("checkAttemptDowngradeGuard: %v", err)
	}
}

// TestNegotiatedDowngradeGuardIsInertWithoutABoundGuard pins the unbound
// case: a generation with no guard behaves exactly as before.
func TestNegotiatedDowngradeGuardIsInertWithoutABoundGuard(t *testing.T) {
	t.Parallel()

	e := &Executor{}
	res := aipapi.NegotiationResult{
		Kind:       aipapi.NegotiationDowngrade,
		Downgraded: []aipapi.Capability{aipapi.CapabilityReasoning},
	}
	if err := e.checkAttemptDowngradeGuard(context.Background(), downgradeTestBaseline(), "proxy_memory_phase1", downgradeTestCand(), "low", res); err != nil {
		t.Fatalf("checkAttemptDowngradeGuard: %v", err)
	}
}

// TestNegotiatedDowngradeGuardIgnoresNonDowngradeResults pins the scope: a
// lossless negotiation never consults the extension.
func TestNegotiatedDowngradeGuardIgnoresNonDowngradeResults(t *testing.T) {
	t.Parallel()

	guard := &downgradeRecordingGuard{}
	e := &Executor{CoreRuntime: CoreRuntime{CorePorts: CorePorts{AttemptGuard: guard}}}
	if err := e.checkAttemptDowngradeGuard(context.Background(), downgradeTestBaseline(), "proxy_memory_phase1", downgradeTestCand(), "low", aipapi.NegotiationResult{Kind: aipapi.NegotiationLossless}); err != nil {
		t.Fatalf("checkAttemptDowngradeGuard: %v", err)
	}
	if len(guard.seen) != 0 {
		t.Fatalf("downgrade consultations = %d, want 0", len(guard.seen))
	}
}
