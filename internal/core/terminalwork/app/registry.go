package app

import (
	"fmt"
	"reflect"
	"strings"
	"sync"

	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Registry resolves stable provider IDs to effect providers (design D4, D9).
type Registry struct {
	mu        sync.RWMutex
	byID      map[string]EffectProvider
	kindIndex map[string]map[sdk.WorkKind]struct{}
}

// NewRegistry returns an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:      make(map[string]EffectProvider),
		kindIndex: make(map[string]map[sdk.WorkKind]struct{}),
	}
}

// Register validates and indexes a provider. Duplicate IDs and typed nils fail.
func (r *Registry) Register(p EffectProvider) error {
	if r == nil {
		return fmt.Errorf("%w: nil registry", ErrNilProvider)
	}
	if isNilProvider(p) {
		return ErrNilProvider
	}
	id := strings.TrimSpace(p.ProviderID())
	if id == "" {
		return fmt.Errorf("%w: empty provider id", ErrNilProvider)
	}
	if strings.TrimSpace(p.Version()) == "" {
		return fmt.Errorf("%w: empty version for %q", ErrMalformedProvider, id)
	}
	kinds := p.SupportedKinds()
	if len(kinds) == 0 {
		return fmt.Errorf("%w: no supported kinds for %q", ErrUnsupportedKind, id)
	}
	kindSet := make(map[sdk.WorkKind]struct{}, len(kinds))
	for _, k := range kinds {
		if err := k.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrUnsupportedKind, err)
		}
		kindSet[k] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byID[id]; exists {
		return fmt.Errorf("%w: %s", ErrDuplicateProvider, id)
	}
	r.byID[id] = p
	r.kindIndex[id] = kindSet
	return nil
}

// Resolve returns the provider for id when it supports kind.
func (r *Registry) Resolve(providerID string, kind sdk.WorkKind) (EffectProvider, error) {
	if r == nil {
		return nil, ErrMissingProvider
	}
	id := strings.TrimSpace(providerID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrMissingProvider, id)
	}
	if _, ok := r.kindIndex[id][kind]; !ok {
		return nil, fmt.Errorf("%w: %s does not support %s", ErrUnsupportedKind, id, kind)
	}
	return p, nil
}

// Has reports whether providerID is registered.
func (r *Registry) Has(providerID string) bool {
	if r == nil {
		return false
	}
	id := strings.TrimSpace(providerID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byID[id]
	return ok
}

// ProviderIDs returns registered IDs (unsorted).
func (r *Registry) ProviderIDs() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byID))
	for id := range r.byID {
		out = append(out, id)
	}
	return out
}

func isNilProvider(p EffectProvider) bool {
	if p == nil {
		return true
	}
	v := reflect.ValueOf(p)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}
