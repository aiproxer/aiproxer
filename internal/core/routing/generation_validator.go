package routing

import (
	"context"
	"fmt"
	"sort"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

// GenerationSelectorValidator validates route selectors against the current generation's
// aliases, default backend, known backends, and execution composition policy (Requirements 7, 8).
// It implements routeoverride.SelectorValidator.
type GenerationSelectorValidator struct {
	Aliases                    *AliasResolver
	DefaultBackend             string
	KnownBackends              map[string]struct{}
	BackendExecutionResolver   BackendExecutionResolver
	ExecutionCompositionPolicy config.ExecutionCompositionPolicy
}

// NewGenerationSelectorValidator constructs a GenerationSelectorValidator.
func NewGenerationSelectorValidator(
	aliases *AliasResolver,
	defaultBackend string,
	knownBackends map[string]struct{},
	execResolver BackendExecutionResolver,
	policy config.ExecutionCompositionPolicy,
) *GenerationSelectorValidator {
	return &GenerationSelectorValidator{
		Aliases:                    aliases,
		DefaultBackend:             defaultBackend,
		KnownBackends:              knownBackends,
		BackendExecutionResolver:   execResolver,
		ExecutionCompositionPolicy: policy,
	}
}

// ValidateSelector compiles raw, verifies all referenced backends are known,
// and enforces the execution composition policy.
// A nil validator fails closed: it cannot prove any selector legal.
func (v *GenerationSelectorValidator) ValidateSelector(_ context.Context, raw string) error {
	if v == nil {
		return fmt.Errorf("routing: generation selector validator is not configured")
	}
	sel, err := CompileSelector(raw, v.Aliases, v.DefaultBackend)
	if err != nil {
		return err
	}
	if err := RejectUnknownBackends(sel, v.KnownBackends); err != nil {
		return err
	}
	return ValidateExecutionComposition(sel, v.BackendExecutionResolver, v.ExecutionCompositionPolicy)
}

// LegalCandidateBackends extracts all backend IDs that can legally be selected
// under this generation's validator.
// If KnownBackends is nil, returns nil (unbounded/unknown backends).
// If KnownBackends is non-nil, returns a sorted slice of all known backend IDs.
func (v *GenerationSelectorValidator) LegalCandidateBackends() []string {
	if v == nil || v.KnownBackends == nil {
		return nil
	}
	backends := make([]string, 0, len(v.KnownBackends))
	for id := range v.KnownBackends {
		backends = append(backends, id)
	}
	sort.Strings(backends)
	return backends
}

// HasRequestSizeConstraints reports whether the compiled and validated selector
// contains any context min/max request-size filters (Requirements 7, 8, 19).
// A nil validator fails closed and returns an error.
func (v *GenerationSelectorValidator) HasRequestSizeConstraints(raw string) (bool, error) {
	if v == nil {
		return false, fmt.Errorf("routing: generation selector validator is not configured")
	}
	sel, err := CompileSelector(raw, v.Aliases, v.DefaultBackend)
	if err != nil {
		return false, err
	}
	if err := RejectUnknownBackends(sel, v.KnownBackends); err != nil {
		return false, err
	}
	if err := ValidateExecutionComposition(sel, v.BackendExecutionResolver, v.ExecutionCompositionPolicy); err != nil {
		return false, err
	}
	return SelectorHasRequestSizeConstraints(sel), nil
}
