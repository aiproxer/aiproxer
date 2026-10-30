package memorystore

import (
	"context"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
)

type Store struct {
	mu       sync.RWMutex
	bindings map[affinity.Key]affinity.Binding
}

func New() *Store {
	return &Store{bindings: map[affinity.Key]affinity.Binding{}}
}

func (s *Store) Get(ctx context.Context, key affinity.Key) (affinity.Binding, bool, error) {
	if err := ctx.Err(); err != nil {
		return affinity.Binding{}, false, err
	}
	if s == nil || !key.Valid() {
		return affinity.Binding{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.bindings[key]
	return b, ok, nil
}

func (s *Store) Set(ctx context.Context, binding affinity.Binding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || !binding.Key.Valid() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings == nil {
		s.bindings = map[affinity.Key]affinity.Binding{}
	}
	s.bindings[binding.Key] = binding
	return nil
}

func (s *Store) Delete(ctx context.Context, key affinity.Key) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || !key.Valid() {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.bindings, key)
	return nil
}

var _ affinity.Store = (*Store)(nil)
