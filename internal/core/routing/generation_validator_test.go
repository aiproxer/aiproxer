package routing_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func TestGenerationSelectorValidator_ValidateSelector(t *testing.T) {
	t.Parallel()

	execResolver := routing.BackendExecutionResolverFunc(func(id string) (aipsdk.BackendExecutionClass, bool) {
		switch id {
		case "inf-1", "inf-2":
			return aipsdk.BackendExecutionInference, true
		case "agent-1":
			return aipsdk.BackendExecutionAgentRuntime, true
		default:
			return aipsdk.BackendExecutionUnknown, false
		}
	})

	aliases, err := routing.NewAliasResolver([]routing.ModelAliasRule{
		{Pattern: "^fast$", Replacement: "inf-1:gpt-4o-mini"},
	})
	if err != nil {
		t.Fatalf("alias resolver: %v", err)
	}

	known := map[string]struct{}{
		"inf-1":   {},
		"inf-2":   {},
		"agent-1": {},
	}

	v := routing.NewGenerationSelectorValidator(
		aliases,
		"inf-1",
		known,
		execResolver,
		config.ExecutionCompositionSafe,
	)

	ctx := context.Background()

	// 1. Direct primary to inference backend passes
	if err := v.ValidateSelector(ctx, "inf-1:gpt-4o"); err != nil {
		t.Fatalf("expected direct primary to pass, got: %v", err)
	}

	// 2. Direct primary to agent backend passes (direct primary always permitted)
	if err := v.ValidateSelector(ctx, "agent-1:gpt-4o"); err != nil {
		t.Fatalf("expected direct primary to agent to pass, got: %v", err)
	}

	// 3. Composite of inference backends passes
	if err := v.ValidateSelector(ctx, "inf-1:m1 | inf-2:m2"); err != nil {
		t.Fatalf("expected inference composite to pass, got: %v", err)
	}

	// 4. Composite with non-inference backend fails under safe policy
	if err := v.ValidateSelector(ctx, "inf-1:m1 | agent-1:m2"); !errors.Is(err, routing.ErrUnsafeExecutionComposition) {
		t.Fatalf("expected ErrUnsafeExecutionComposition, got: %v", err)
	}

	// 5. Unknown backend fails
	if err := v.ValidateSelector(ctx, "unknown-backend:m1"); !errors.Is(err, routing.ErrUnknownBackend) {
		t.Fatalf("expected ErrUnknownBackend, got: %v", err)
	}

	// 6. Alias expands and validates
	if err := v.ValidateSelector(ctx, "fast"); err != nil {
		t.Fatalf("expected alias 'fast' to pass, got: %v", err)
	}

	// 7. Model-only uses DefaultBackend
	if err := v.ValidateSelector(ctx, "some-model"); err != nil {
		t.Fatalf("expected model-only selector to use DefaultBackend and pass, got: %v", err)
	}
}

func TestGenerationSelectorValidator_NilValidatorFailsClosed(t *testing.T) {
	t.Parallel()

	// A nil validator cannot prove any selector legal: fail closed, never accept-all.
	var nilV *routing.GenerationSelectorValidator
	if err := nilV.ValidateSelector(context.Background(), "be:model"); err == nil {
		t.Fatal("expected error for nil validator, got nil")
	}
}

func TestGenerationSelectorValidator_LegalCandidateBackends(t *testing.T) {
	t.Parallel()

	// Nil validator
	var nilV *routing.GenerationSelectorValidator
	if nilV.LegalCandidateBackends() != nil {
		t.Fatal("expected nil for nil validator")
	}

	// Nil KnownBackends
	vNilKnown := &routing.GenerationSelectorValidator{KnownBackends: nil}
	if vNilKnown.LegalCandidateBackends() != nil {
		t.Fatal("expected nil for nil KnownBackends")
	}

	// Populated KnownBackends returns sorted slice
	vPopulated := &routing.GenerationSelectorValidator{
		KnownBackends: map[string]struct{}{
			"zebra": {},
			"apple": {},
			"mango": {},
		},
	}
	got := vPopulated.LegalCandidateBackends()
	want := []string{"apple", "mango", "zebra"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LegalCandidateBackends() = %v, want %v", got, want)
	}
}
