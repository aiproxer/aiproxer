package billing

import (
	"context"
	"errors"
	"fmt"
)

const (
	// MaxOwnedResources bounds binding-owned resources per lifecycle.
	MaxOwnedResources = 64
	// MaxBorrowedRefs bounds declared borrowed handles per lifecycle.
	MaxBorrowedRefs = 64
)

// ErrInvalidLifecycle identifies a malformed lifecycle declaration.
var ErrInvalidLifecycle = errors.New("billing: invalid lifecycle")

// OwnedResource is one binding-owned resource registration. Owned resources
// start and close exactly once under host cleanup; a start failure unwinds
// already-started owned resources through the same host cleanup path.
type OwnedResource struct {
	ID    string
	Start func(ctx context.Context) error
	Close func(ctx context.Context) error
}

// Validate requires resource identity and both start and close callbacks.
func (r OwnedResource) Validate() error {
	if err := validateRef("owned resource id", r.ID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLifecycle, err)
	}
	if r.Start == nil {
		return fmt.Errorf("%w: owned resource %q requires a start callback", ErrInvalidLifecycle, r.ID)
	}
	if r.Close == nil {
		return fmt.Errorf("%w: owned resource %q requires a close callback", ErrInvalidLifecycle, r.ID)
	}
	return nil
}

// BorrowedRef declares one borrowed store or resource handle. Borrowed
// handles are never implicitly closed: the type offers no close entry point,
// so host cleanup cannot dispose a store it does not own.
type BorrowedRef struct {
	ID   string
	Kind string
}

// Validate requires borrowed identity with an optional bounded kind label.
func (r BorrowedRef) Validate() error {
	if err := validateRef("borrowed ref id", r.ID, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLifecycle, err)
	}
	if err := validateOptionalRef("borrowed ref kind", r.Kind, MaxIdentityBytes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLifecycle, err)
	}
	return nil
}

// Lifecycle is the explicit owned-resource registration for one binding.
// Owned resources are started and closed once by the host; borrowed refs are
// declared for attribution and are never closed by host cleanup.
type Lifecycle struct {
	Owned    []OwnedResource
	Borrowed []BorrowedRef
}

// Validate checks owned/borrowed declarations, duplicate identity, and
// owned/borrowed collisions. An empty lifecycle is valid: the binding simply
// owns no process resources.
func (l Lifecycle) Validate() error {
	if len(l.Owned) > MaxOwnedResources {
		return fmt.Errorf("%w: owned resource bound exceeded", ErrInvalidLifecycle)
	}
	if len(l.Borrowed) > MaxBorrowedRefs {
		return fmt.Errorf("%w: borrowed ref bound exceeded", ErrInvalidLifecycle)
	}
	seen := make(map[string]struct{}, len(l.Owned)+len(l.Borrowed))
	for i, resource := range l.Owned {
		if err := resource.Validate(); err != nil {
			return fmt.Errorf("%w: owned %d: %v", ErrInvalidLifecycle, i, err)
		}
		if _, ok := seen[resource.ID]; ok {
			return fmt.Errorf("%w: duplicate resource id %q", ErrInvalidLifecycle, resource.ID)
		}
		seen[resource.ID] = struct{}{}
	}
	for i, ref := range l.Borrowed {
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("%w: borrowed %d: %v", ErrInvalidLifecycle, i, err)
		}
		if _, ok := seen[ref.ID]; ok {
			return fmt.Errorf("%w: borrowed id %q collides with an owned resource", ErrInvalidLifecycle, ref.ID)
		}
		seen[ref.ID] = struct{}{}
	}
	return nil
}

// Clone returns an independent copy of the lifecycle. Callbacks are shared by
// value; slices are never aliased.
func (l Lifecycle) Clone() Lifecycle {
	out := l
	out.Owned = append([]OwnedResource(nil), l.Owned...)
	out.Borrowed = append([]BorrowedRef(nil), l.Borrowed...)
	return out
}
