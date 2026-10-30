package keepwarm

import (
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

var ErrManagerNotRegistered = errors.New("keepwarm: manager not registered")

type SessionPolicy struct {
	ALegID    string
	Disabled  bool
	Revision  uint64
	UpdatedAt time.Time
}

type PolicyStore struct {
	mu      sync.Mutex
	max     int
	nextRev uint64
	states  map[string]SessionPolicy
}

func NewPolicyStore(maxEntries int) (*PolicyStore, error) {
	if maxEntries <= 0 {
		return nil, ErrInvalidConfig
	}
	return &PolicyStore{max: maxEntries, states: make(map[string]SessionPolicy)}, nil
}

func (s *PolicyStore) Disable(aLegID string, now time.Time) (SessionPolicy, error) {
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" || len(aLegID) > promptcache.MaxLegIDBytes {
		return SessionPolicy{}, ErrInvalidConfig
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[aLegID]; !ok && len(s.states) >= s.max {
		return SessionPolicy{}, ErrPolicyCapacity
	}
	s.nextRev++
	state := SessionPolicy{ALegID: aLegID, Disabled: true, Revision: s.nextRev, UpdatedAt: now}
	s.states[aLegID] = state
	return state, nil
}

func (s *PolicyStore) Clear(aLegID string) error {
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" || len(aLegID) > promptcache.MaxLegIDBytes {
		return ErrInvalidConfig
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.states[aLegID]; !ok {
		return ErrPolicyNotFound
	}
	delete(s.states, aLegID)
	return nil
}

func (s *PolicyStore) Get(aLegID string) (SessionPolicy, bool) {
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" || len(aLegID) > promptcache.MaxLegIDBytes {
		return SessionPolicy{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.states[aLegID]
	return state, ok
}

func (s *PolicyStore) Forget(aLegID string) {
	aLegID = strings.TrimSpace(aLegID)
	if aLegID == "" || len(aLegID) > promptcache.MaxLegIDBytes {
		return
	}
	s.mu.Lock()
	delete(s.states, aLegID)
	s.mu.Unlock()
}

func (s *PolicyStore) Len() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.states) }

// DisabledALegIDs returns a bounded snapshot used when a new generation
// registers. Provider state and manager references never enter this store.
func (s *PolicyStore) DisabledALegIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.states))
	for id, state := range s.states {
		if state.Disabled {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Invalidator is deliberately narrower than Manager so the process registry
// cannot retain provider handles, controllers, or backend generations.
type Invalidator interface{ BeginForegroundTurn(string) }

// SessionDisabler is an optional extension used by generation managers that
// can retain the process policy decision. The registry still accepts plain
// Invalidators for compatibility with minimal/test managers.
type SessionDisabler interface {
	Invalidator
	SetSessionDisabled(string, bool)
}

type ManagerRegistry struct {
	mu       sync.Mutex
	next     uint64
	managers map[uint64]Invalidator
}

func NewManagerRegistry() *ManagerRegistry {
	return &ManagerRegistry{managers: make(map[uint64]Invalidator)}
}

func (r *ManagerRegistry) Register(manager Invalidator) (uint64, error) {
	if manager == nil {
		return 0, ErrManagerNotRegistered
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	r.managers[r.next] = manager
	return r.next, nil
}

func (r *ManagerRegistry) Unregister(id uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.managers[id]; !ok {
		return ErrManagerNotRegistered
	}
	delete(r.managers, id)
	return nil
}

func (r *ManagerRegistry) Disable(aLegID string) {
	r.mu.Lock()
	managers := make([]Invalidator, 0, len(r.managers))
	for _, m := range r.managers {
		managers = append(managers, m)
	}
	r.mu.Unlock()
	for _, m := range managers {
		if disabler, ok := m.(SessionDisabler); ok {
			disabler.SetSessionDisabled(aLegID, true)
			continue
		}
		m.BeginForegroundTurn(aLegID)
	}
}

// Clear restores inheritance on every live generation manager. Managers that
// can retain the process policy decision clear their shadow so future arms are
// allowed again; plain invalidators never retained the decision.
func (r *ManagerRegistry) Clear(aLegID string) {
	r.mu.Lock()
	managers := make([]Invalidator, 0, len(r.managers))
	for _, m := range r.managers {
		managers = append(managers, m)
	}
	r.mu.Unlock()
	for _, m := range managers {
		if disabler, ok := m.(SessionDisabler); ok {
			disabler.SetSessionDisabled(aLegID, false)
		}
	}
}

func (r *ManagerRegistry) Len() int { r.mu.Lock(); defer r.mu.Unlock(); return len(r.managers) }

func (s *PolicyStore) DisableAndBroadcast(registry *ManagerRegistry, aLegID string, now time.Time) (SessionPolicy, error) {
	state, err := s.Disable(aLegID, now)
	if err != nil {
		return SessionPolicy{}, err
	}
	if registry != nil {
		registry.Disable(aLegID)
	}
	return state, nil
}

// ClearAndBroadcast mirrors [PolicyStore.DisableAndBroadcast]: the store is
// mutated first, and only a successful clear restores inheritance on the live
// generation managers.
func (s *PolicyStore) ClearAndBroadcast(registry *ManagerRegistry, aLegID string) error {
	if err := s.Clear(aLegID); err != nil {
		return err
	}
	if registry != nil {
		registry.Clear(aLegID)
	}
	return nil
}
