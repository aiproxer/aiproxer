package state

import (
	"context"
	"time"
)

// Scope selects the persistence boundary for a key (design §8).
type Scope string

const (
	ScopeRequest   Scope = "request"
	ScopeSession   Scope = "session"
	ScopePrincipal Scope = "principal"
	ScopeGlobal    Scope = "global"
)

// Store is the narrow plugin-facing state API (design §8).
type Store interface {
	Get(ctx context.Context, scope Scope, ns, key string, out any) (found bool, err error)
	Put(ctx context.Context, scope Scope, ns, key string, value any, ttl time.Duration) error
	Delete(ctx context.Context, scope Scope, ns, key string) error
	InspectTTL(ctx context.Context, scope Scope, ns, key string) (ttl time.Duration, found bool, err error)
}

// DisabledStore is a placeholder binding that rejects all mutating use until core wiring exists.
type DisabledStore struct{}

func (DisabledStore) Get(context.Context, Scope, string, string, any) (bool, error) {
	return false, ErrNotConfigured
}

func (DisabledStore) Put(context.Context, Scope, string, string, any, time.Duration) error {
	return ErrNotConfigured
}

func (DisabledStore) Delete(context.Context, Scope, string, string) error {
	return ErrNotConfigured
}

func (DisabledStore) InspectTTL(context.Context, Scope, string, string) (time.Duration, bool, error) {
	return 0, false, ErrNotConfigured
}
