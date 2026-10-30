package routing_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestInitialCandidates_NilOrEmpty(t *testing.T) {
	t.Parallel()
	if got := routing.InitialCandidates(nil); got != nil {
		t.Fatalf("expected nil for nil selector, got %v", got)
	}
	sel := &routing.Selector{}
	if got := routing.InitialCandidates(sel); got != nil {
		t.Fatalf("expected nil for empty alternatives, got %v", got)
	}
}

func TestInitialCandidates_Primary(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("be1:m1")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	cands := routing.InitialCandidates(sel)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Primary.Backend != "be1" || cands[0].Primary.Model != "m1" {
		t.Fatalf("unexpected candidate: %#v", cands[0])
	}
	if cands[0].Key != "be1:m1" {
		t.Fatalf("unexpected key: %q", cands[0].Key)
	}
}

func TestInitialCandidates_SequentialFailover(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("be1:m1 | be2:m2 | be3:m3")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	cands := routing.InitialCandidates(sel)
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(cands))
	}
	expected := []struct {
		backend string
		model   string
	}{
		{"be1", "m1"},
		{"be2", "m2"},
		{"be3", "m3"},
	}
	for i, exp := range expected {
		if cands[i].Primary.Backend != exp.backend || cands[i].Primary.Model != exp.model {
			t.Fatalf("cand[%d] = %v:%v, want %v:%v", i, cands[i].Primary.Backend, cands[i].Primary.Model, exp.backend, exp.model)
		}
	}
}

func TestInitialCandidates_Weighted(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("[weight=7][first]be1:m1^[weight=3]be2:m2")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	cands := routing.InitialCandidates(sel)
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if cands[0].Primary.Backend != "be1" || cands[0].Primary.Model != "m1" || !cands[0].MarkedFirst {
		t.Fatalf("cand[0] unexpected: %#v", cands[0])
	}
	if cands[1].Primary.Backend != "be2" || cands[1].Primary.Model != "m2" || cands[1].MarkedFirst {
		t.Fatalf("cand[1] unexpected: %#v", cands[1])
	}
}

func TestInitialCandidates_ParallelRace(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("be1:m1![handicap=5]be2:m2")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	cands := routing.InitialCandidates(sel)
	if len(cands) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(cands))
	}
	if !cands[0].IsParallel || cands[0].Handicap != 0 {
		t.Fatalf("cand[0] unexpected parallel/handicap: %#v", cands[0])
	}
	if !cands[1].IsParallel || cands[1].Handicap != 5*time.Second {
		t.Fatalf("cand[1] unexpected parallel/handicap: %#v", cands[1])
	}
}

func TestInitialCandidates_ThinkerHybridParallel(t *testing.T) {
	t.Parallel()
	sel, err := routing.Parse("[thinker]thinkbe:m^exec1:m!exec2:m")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	cands := routing.InitialCandidates(sel)
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates (1 thinker + 2 parallel legs), got %d", len(cands))
	}
	if cands[0].Primary.Backend != "thinkbe" || cands[0].InterleavedRole != interleavedstate.RoleThinker {
		t.Fatalf("cand[0] unexpected thinker candidate: %#v", cands[0])
	}
	if cands[1].Primary.Backend != "exec1" || !cands[1].IsParallel {
		t.Fatalf("cand[1] unexpected parallel leg: %#v", cands[1])
	}
	if cands[2].Primary.Backend != "exec2" || !cands[2].IsParallel {
		t.Fatalf("cand[2] unexpected parallel leg: %#v", cands[2])
	}
}

func TestCandidateSetsEqual(t *testing.T) {
	t.Parallel()
	c1 := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "b1", Model: "m1"},
		Key:     "b1:m1",
	}
	c2 := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "b2", Model: "m2"},
		Key:     "b2:m2",
	}
	c2Par := routing.AttemptCandidate{
		Primary:    routing.Primary{Backend: "b2", Model: "m2"},
		Key:        "b2:m2",
		IsParallel: true,
	}

	if !routing.CandidateSetsEqual([]routing.AttemptCandidate{c1, c2}, []routing.AttemptCandidate{c1, c2}) {
		t.Fatal("identical sets must be equal")
	}
	if routing.CandidateSetsEqual([]routing.AttemptCandidate{c1, c2}, []routing.AttemptCandidate{c2, c1}) {
		t.Fatal("sets with swapped order must NOT be equal")
	}
	if routing.CandidateSetsEqual([]routing.AttemptCandidate{c1}, []routing.AttemptCandidate{c1, c2}) {
		t.Fatal("sets with different length must NOT be equal")
	}
	if routing.CandidateSetsEqual([]routing.AttemptCandidate{c1, c2}, []routing.AttemptCandidate{c1, c2Par}) {
		t.Fatal("candidates with different IsParallel must NOT be equal")
	}
}

type testNativeResolver struct {
	mapping map[string]routing.ModelBinding
}

func (r testNativeResolver) ResolveModelBinding(backendID, model string) routing.ModelBinding {
	if b, ok := r.mapping[backendID+":"+model]; ok {
		return b
	}
	return routing.ModelBinding{Kind: routing.ModelBindingUnknown}
}

func TestComposeInitialCandidates(t *testing.T) {
	t.Parallel()
	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "^fast$", Replacement: "primary-be:fast-model"},
	})
	if err != nil {
		t.Fatalf("NewAliasResolver: %v", err)
	}

	execResolver := routing.BackendExecutionResolverFunc(func(backendID string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionInference, true
	})

	nativeRes := testNativeResolver{
		mapping: map[string]routing.ModelBinding{
			"primary-be:fast-model": {
				Kind:   routing.ModelBindingExactCanonical,
				Native: "fast-model-native-v1",
			},
		},
	}

	// 1. Successful composition with alias and native binding
	cands, sel, err := routing.ComposeInitialCandidates("fast", aliases, "default-be", execResolver, config.ExecutionCompositionSafe, nativeRes)
	if err != nil {
		t.Fatalf("ComposeInitialCandidates failed: %v", err)
	}
	if sel == nil {
		t.Fatal("expected non-nil selector")
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Primary.Backend != "primary-be" || cands[0].Primary.Model != "fast-model" || cands[0].Primary.NativeModel != "fast-model-native-v1" {
		t.Fatalf("unexpected candidate: %#v", cands[0])
	}

	// 2. Default backend applied to model-only
	cands2, _, err := routing.ComposeInitialCandidates("gpt-4o", nil, "default-be", execResolver, config.ExecutionCompositionSafe, nil)
	if err != nil {
		t.Fatalf("model-only ComposeInitialCandidates failed: %v", err)
	}
	if len(cands2) != 1 || cands2[0].Primary.Backend != "default-be" || cands2[0].Primary.Model != "gpt-4o" {
		t.Fatalf("unexpected candidate for default backend: %#v", cands2)
	}

	// 3. Unresolved model-only declines
	_, _, err = routing.ComposeInitialCandidates("gpt-4o", nil, "", execResolver, config.ExecutionCompositionSafe, nil)
	if !errors.Is(err, aipapi.ErrUnresolvedModelOnlySelector) {
		t.Fatalf("expected ErrUnresolvedModelOnlySelector, got %v", err)
	}

	// 4. Unsafe execution composition declines
	nonInferenceResolver := routing.BackendExecutionResolverFunc(func(backendID string) (aipsdk.BackendExecutionClass, bool) {
		return aipsdk.BackendExecutionAgentRuntime, true
	})
	_, _, err = routing.ComposeInitialCandidates("be1:m1 | be2:m2", nil, "", nonInferenceResolver, config.ExecutionCompositionSafe, nil)
	if !errors.Is(err, routing.ErrUnsafeExecutionComposition) {
		t.Fatalf("expected ErrUnsafeExecutionComposition, got %v", err)
	}

	// 5. Wrong-backend canonical declines
	wrongBackendRes := testNativeResolver{
		mapping: map[string]routing.ModelBinding{
			"be1:m1": {
				Kind: routing.ModelBindingWrongBackend,
			},
		},
	}
	_, _, err = routing.ComposeInitialCandidates("be1:m1", nil, "", execResolver, config.ExecutionCompositionSafe, wrongBackendRes)
	if !errors.Is(err, routing.ErrWrongBackendCanonical) {
		t.Fatalf("expected ErrWrongBackendCanonical, got %v", err)
	}
}
