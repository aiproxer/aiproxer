package modelcatalog

import (
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/routing"
)

// OverrideSet holds administrator facts keyed by backend:model pair or model name only.
// Pair keys use [routing.Primary] backend and model with surrounding space trimmed; query params are ignored.
type OverrideSet struct {
	Pair  map[string]ModelFacts
	Model map[string]ModelFacts
}

// OverrideResolver applies pair-then-model override precedence (design §OverrideResolver).
type OverrideResolver interface {
	Resolve(candidate routing.AttemptCandidate) (ModelFacts, bool)
}

type overrideResolver struct {
	set OverrideSet
}

var _ OverrideResolver = (*overrideResolver)(nil)

// NewOverrideResolver returns a resolver for the given set (nil maps are treated as empty).
func NewOverrideResolver(set OverrideSet) OverrideResolver {
	if set.Pair == nil {
		set.Pair = map[string]ModelFacts{}
	}
	if set.Model == nil {
		set.Model = map[string]ModelFacts{}
	}
	return &overrideResolver{set: set}
}

// Resolve implements [OverrideResolver].
func (o *overrideResolver) Resolve(candidate routing.AttemptCandidate) (ModelFacts, bool) {
	if key := pairOverrideLookupKey(candidate.Primary); key != "" {
		if f, ok := o.set.Pair[key]; ok {
			return f, true
		}
	}
	model := strings.TrimSpace(candidate.Primary.Model)
	if model != "" {
		if f, ok := o.set.Model[model]; ok {
			return f, true
		}
	}
	return ModelFacts{}, false
}

func pairOverrideLookupKey(p routing.Primary) string {
	b := strings.TrimSpace(p.Backend)
	if b == "" {
		return ""
	}
	m := strings.TrimSpace(p.Model)
	if m == "" {
		return ""
	}
	return b + ":" + m
}
