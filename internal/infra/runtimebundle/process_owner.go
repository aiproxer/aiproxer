package runtimebundle

import (
	"context"
	"fmt"
)

// processResourceOwner is a construction-only, append-only facade over the
// authoritative ProcessServices closer set (req 2.5, 2.7).
type processResourceOwner struct {
	register func(func() error)
}

// Own appends a process release into the authoritative closer set. A nil release
// is ignored, since registering one would panic the closer set at shutdown.
func (o *processResourceOwner) Own(release func() error) {
	if o == nil || release == nil {
		return
	}
	o.register(release)
}

// acquireOwnedProcess atomically acquires a value and registers its non-nil
// release before the value escapes (req 2.1, 2.2).
func acquireOwnedProcess[T any](ctx context.Context, owner *processResourceOwner, acquire func(context.Context) (T, func() error, error)) (T, error) {
	var zero T
	if owner == nil {
		return zero, fmt.Errorf("runtimebundle: nil process owner")
	}
	value, release, err := acquire(ctx)
	if err != nil {
		return zero, err
	}
	if release == nil {
		return zero, fmt.Errorf("runtimebundle: owned acquisition returned nil release")
	}
	owner.Own(release)
	return value, nil
}
