package interleavedthinking

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state"
)

// Sentinel errors for the memo store contract.
var (
	// ErrMemoNotFound is returned when a memo reference does not exist under
	// the requested scope. Cross-scope access must surface this error so
	// session isolation is enforced by the store contract.
	ErrMemoNotFound = errors.New("interleavedthinking: memo not found")
	// ErrMemoTooLarge is returned when a put or update would store memo content
	// exceeding the configured byte limit.
	ErrMemoTooLarge = errors.New("interleavedthinking: memo too large")
	// ErrEmptyScope is returned when a mutating or lookup operation is called
	// with an empty scope. An empty scope cannot own a memo.
	ErrEmptyScope = errors.New("interleavedthinking: empty scope")
	// ErrEmptyMemoRef is returned when a mutating or lookup operation is called
	// with a memo reference whose key is empty. Lookup is keyed by ref.Key, so
	// an empty key cannot identify a memo.
	ErrEmptyMemoRef = errors.New("interleavedthinking: empty memo ref")
)

// Scope identifies the authoritative session or A-leg that owns a memo. Memos
type Scope string

// MemoRef aliases state.MemoRef for convenience.
type MemoRef = state.MemoRef

// MemoState aliases state.MemoState for convenience.
type MemoState = state.MemoState

// MemoStore is the bounded memo store contract keyed by memo reference under an
// authoritative scope.
type MemoStore interface {
	Put(ctx context.Context, scope Scope, st state.MemoState) (state.MemoRef, error)
	Get(ctx context.Context, scope Scope, ref state.MemoRef) (state.MemoState, bool, error)
	Update(ctx context.Context, scope Scope, ref state.MemoRef, st state.MemoState) (state.MemoRef, error)
	Delete(ctx context.Context, scope Scope, ref state.MemoRef) error
	LatestEntry(ctx context.Context, scope Scope) (state.MemoState, state.MemoRef, bool, error)
}

type memoEntry struct {
	state state.MemoState
	ref   state.MemoRef
}

// InMemoryMemoStore is a process-local, bounded MemoStore keyed by scope and
// memo reference. It is safe for concurrent use.
type InMemoryMemoStore struct {
	mu       sync.Mutex
	maxBytes int
	nextKey  int64
	byScope  map[Scope]map[string]*memoEntry
}

var _ MemoStore = (*InMemoryMemoStore)(nil)

// NewMemoStore returns an in-memory memo store that rejects memo content larger
// than maxBytes. A non-positive maxBytes disables size enforcement.
func NewMemoStore(maxBytes int) *InMemoryMemoStore {
	return &InMemoryMemoStore{
		maxBytes: maxBytes,
		byScope:  make(map[Scope]map[string]*memoEntry),
	}
}

func (s *InMemoryMemoStore) checkSize(body string) error {
	if s.maxBytes <= 0 {
		return nil
	}
	if len(body) > s.maxBytes {
		return fmt.Errorf("%w: %d > %d", ErrMemoTooLarge, len(body), s.maxBytes)
	}
	return nil
}

func (s *InMemoryMemoStore) scopeMap(scope Scope) map[string]*memoEntry {
	m, ok := s.byScope[scope]
	if !ok {
		m = make(map[string]*memoEntry)
		s.byScope[scope] = m
	}
	return m
}

func (s *InMemoryMemoStore) lookup(scope Scope, ref state.MemoRef) (*memoEntry, bool) {
	m, ok := s.byScope[scope]
	if !ok {
		return nil, false
	}
	e, ok := m[ref.Key]
	return e, ok
}

func validateScopeRef(scope Scope, ref state.MemoRef) error {
	if scope == "" {
		return ErrEmptyScope
	}
	if ref.Key == "" {
		return ErrEmptyMemoRef
	}
	return nil
}

// Put stores a new memo under scope, allocates a new reference key with Version=1,
// and returns the allocated reference.
func (s *InMemoryMemoStore) Put(ctx context.Context, scope Scope, st state.MemoState) (state.MemoRef, error) {
	if err := ctx.Err(); err != nil {
		return state.MemoRef{}, err
	}
	if scope == "" {
		return state.MemoRef{}, ErrEmptyScope
	}
	if err := s.checkSize(st.Memo); err != nil {
		return state.MemoRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextKey++
	ref := state.MemoRef{Key: fmt.Sprintf("memo-%d", s.nextKey), Version: 1}
	m := s.scopeMap(scope)
	m[ref.Key] = &memoEntry{state: st, ref: ref}
	return ref, nil
}

// Get returns the memo state stored under scope for ref.Key.
func (s *InMemoryMemoStore) Get(ctx context.Context, scope Scope, ref state.MemoRef) (state.MemoState, bool, error) {
	if err := ctx.Err(); err != nil {
		return state.MemoState{}, false, err
	}
	if err := validateScopeRef(scope, ref); err != nil {
		return state.MemoState{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookup(scope, ref)
	if !ok {
		return state.MemoState{}, false, nil
	}
	return e.state, true, nil
}

// Update replaces the memo state stored under scope for ref.Key, bumps Version,
// and returns the updated reference.
func (s *InMemoryMemoStore) Update(ctx context.Context, scope Scope, ref state.MemoRef, st state.MemoState) (state.MemoRef, error) {
	if err := ctx.Err(); err != nil {
		return state.MemoRef{}, err
	}
	if err := validateScopeRef(scope, ref); err != nil {
		return state.MemoRef{}, err
	}
	if err := s.checkSize(st.Memo); err != nil {
		return state.MemoRef{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookup(scope, ref)
	if !ok {
		return state.MemoRef{}, ErrMemoNotFound
	}
	nextRef := state.MemoRef{Key: ref.Key, Version: e.ref.Version + 1}
	e.state = st
	e.ref = nextRef
	return nextRef, nil
}

// Delete removes the memo stored under scope for ref.Key.
func (s *InMemoryMemoStore) Delete(ctx context.Context, scope Scope, ref state.MemoRef) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateScopeRef(scope, ref); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byScope[scope]
	if !ok {
		return nil
	}
	delete(m, ref.Key)
	return nil
}

// LatestEntry returns the most recent memo state and reference stored under scope, if any.
func (s *InMemoryMemoStore) LatestEntry(ctx context.Context, scope Scope) (state.MemoState, state.MemoRef, bool, error) {
	if err := ctx.Err(); err != nil {
		return state.MemoState{}, state.MemoRef{}, false, err
	}
	if scope == "" {
		return state.MemoState{}, state.MemoRef{}, false, ErrEmptyScope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byScope[scope]
	if !ok || len(m) == 0 {
		return state.MemoState{}, state.MemoRef{}, false, nil
	}
	var latest *memoEntry
	for _, e := range m {
		if latest == nil || e.ref.Version > latest.ref.Version {
			latest = e
		}
	}
	if latest == nil {
		return state.MemoState{}, state.MemoRef{}, false, nil
	}
	return latest.state, latest.ref, true, nil
}

// Latest returns the most recent memo state stored under scope, if any.
func (s *InMemoryMemoStore) Latest(ctx context.Context, scope Scope) (state.MemoState, bool, error) {
	st, _, ok, err := s.LatestEntry(ctx, scope)
	return st, ok, err
}
