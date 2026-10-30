// Package sessionpolicy owns the bounded process policy used by the
// terminal-decision feature. The store is deliberately small: it keeps only
// safe scope identity and actor tri-state values, and has no expiry or
// eviction policy.
package sessionpolicy

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	// ErrCapacity means that a write would create a new key beyond MaxKeys.
	ErrCapacity = errors.New("terminal decision policy: capacity reached")
	// ErrBounds means that a key or stored actor value exceeds its configured bound.
	ErrBounds = errors.New("terminal decision policy: bounds exceeded")
	// ErrUnauthorized means that the supplied authority does not own the key.
	ErrUnauthorized = errors.New("terminal decision policy: unauthorized scope")
	// ErrClosed means that the process-owned store has been closed.
	ErrClosed = errors.New("terminal decision policy: store closed")
)

const (
	defaultMaxKeys       = 1024
	defaultMaxKeyBytes   = 256
	defaultMaxValueBytes = 128
)

// Config bounds the in-process policy store. Values remain until explicitly
// changed or the owning process restarts; Config intentionally has no TTL or
// eviction fields.
type Config struct {
	MaxKeys       int
	MaxKeyBytes   int
	MaxValueBytes int
}

func (c Config) normalized() Config {
	if c.MaxKeys <= 0 {
		c.MaxKeys = defaultMaxKeys
	}
	if c.MaxKeyBytes <= 0 {
		c.MaxKeyBytes = defaultMaxKeyBytes
	}
	if c.MaxValueBytes <= 0 {
		c.MaxValueBytes = defaultMaxValueBytes
	}
	return c
}

// Key is the only identity retained by the policy store. It contains no
// credentials, tokens, secrets, or request content.
type Key struct {
	SecureSessionIncarnation string
	ALegID                   string
	FeatureID                string
}

// Authority is the already-authenticated scope supplied by the caller. The
// store checks it against Key; it does not perform authentication itself.
type Authority struct {
	SecureSessionIncarnation string
	ALegID                   string
	Authorized               bool
}

// Actor identifies the independently writable policy value.
type Actor string

const (
	ActorClient   Actor = "client"
	ActorOperator Actor = "operator"
)

// TriState represents an actor override. Unset delegates to the other actor
// or the generation default.
type TriState string

const (
	TriStateUnset    TriState = "unset"
	TriStateEnabled  TriState = "enabled"
	TriStateDisabled TriState = "disabled"
)

func (s TriState) valid() bool {
	return s == TriStateUnset || s == TriStateEnabled || s == TriStateDisabled
}

// Snapshot is an immutable admission-time view of one policy key.
type Snapshot struct {
	ClientState      TriState
	OperatorState    TriState
	EffectiveEnabled bool
	Revision         uint64
}

type entry struct {
	client   TriState
	operator TriState
	revision uint64
}

// Store is a process-owned, bounded policy map. Its mutex serializes key
// writes and makes each returned Snapshot a coherent linearization point.
type Store struct {
	mu       sync.RWMutex
	config   Config
	closed   bool
	revision uint64
	entries  map[Key]entry
}

// NewStore constructs an empty process policy store.
func NewStore(config Config) *Store {
	config = config.normalized()
	return &Store{
		config:  config,
		entries: make(map[Key]entry),
	}
}

// Set atomically replaces one actor's tri-state value and returns the new
// coherent snapshot. Every accepted write advances the store revision,
// including an explicit unset on a new key, so the key boundary is observable.
func (s *Store) Set(ctx context.Context, authority Authority, key Key, actor Actor, state TriState) (Snapshot, error) {
	if err := contextErr(ctx); err != nil {
		return Snapshot{}, err
	}
	if s == nil {
		return Snapshot{}, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return Snapshot{}, ErrClosed
	}
	if err := validateAuthority(authority, key); err != nil {
		return Snapshot{}, err
	}
	if !validKey(key, s.config.MaxKeyBytes) || !state.valid() || !validActor(actor) {
		return Snapshot{}, ErrBounds
	}
	if valueBytes(actor, state) > s.config.MaxValueBytes {
		return Snapshot{}, ErrBounds
	}

	current, exists := s.entries[key]
	if !exists && len(s.entries) >= s.config.MaxKeys {
		return Snapshot{}, ErrCapacity
	}
	switch actor {
	case ActorClient:
		current.client = state
	case ActorOperator:
		current.operator = state
	default:
		return Snapshot{}, ErrBounds
	}
	s.revision++
	current.revision = s.revision
	s.entries[key] = current
	return snapshotOf(current, current.revision, false), nil
}

// Snapshot returns one coherent admission-time view. It never creates a key,
// so reads cannot consume bounded capacity.
func (s *Store) Snapshot(ctx context.Context, authority Authority, key Key, generationDefault bool) (Snapshot, error) {
	if err := contextErr(ctx); err != nil {
		return Snapshot{}, err
	}
	if s == nil {
		return Snapshot{}, ErrClosed
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return Snapshot{}, ErrClosed
	}
	if err := validateAuthority(authority, key); err != nil {
		return Snapshot{}, err
	}
	if !validKey(key, s.config.MaxKeyBytes) {
		return Snapshot{}, ErrBounds
	}
	current := s.entries[key]
	return snapshotOf(current, current.revision, generationDefault), nil
}

// Close prevents all later writes and is idempotent. There are no background
// goroutines or external resources owned by this store.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		s.entries = nil
	}
	s.mu.Unlock()
	return nil
}

func snapshotOf(e entry, revision uint64, generationDefault bool) Snapshot {
	if e.client == "" {
		e.client = TriStateUnset
	}
	if e.operator == "" {
		e.operator = TriStateUnset
	}
	return Snapshot{
		ClientState:      e.client,
		OperatorState:    e.operator,
		EffectiveEnabled: effectiveEnabled(e.client, e.operator, generationDefault),
		Revision:         revision,
	}
}

func effectiveEnabled(client, operator TriState, generationDefault bool) bool {
	if client == TriStateDisabled || operator == TriStateDisabled {
		return false
	}
	if client == TriStateEnabled || operator == TriStateEnabled {
		return true
	}
	return generationDefault
}

func validateAuthority(authority Authority, key Key) error {
	if !authority.Authorized || authority.SecureSessionIncarnation != key.SecureSessionIncarnation || authority.ALegID != key.ALegID {
		return ErrUnauthorized
	}
	return nil
}

func validKey(key Key, maxBytes int) bool {
	if key.SecureSessionIncarnation == "" || key.ALegID == "" || key.FeatureID == "" {
		return false
	}
	return len(key.SecureSessionIncarnation)+len(key.ALegID)+len(key.FeatureID) <= maxBytes
}

func validActor(actor Actor) bool {
	return actor == ActorClient || actor == ActorOperator
}

func valueBytes(actor Actor, state TriState) int {
	return len(actor) + len(state)
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("terminal decision policy: nil context")
	}
	return ctx.Err()
}
