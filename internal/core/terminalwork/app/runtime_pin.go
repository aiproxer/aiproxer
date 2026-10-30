package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

// retainProviderPin retains a provider pin from the request context and stamps
// Versions.RuntimeInstanceID + RuntimeGenerationID. When a retainer is bound,
// retain failure fails closed. Missing retainer is a no-op (legacy compatible).
func retainProviderPin(ctx context.Context, versions *terminalwork.BoundVersions) (genpin.Pin, error) {
	if versions == nil {
		return nil, nil
	}
	ret, ok := genpin.FromContext(ctx)
	if !ok || ret == nil {
		return nil, nil
	}
	pin, ok := ret.Retain(genpin.KindProvider)
	if !ok || pin == nil {
		return nil, fmt.Errorf("%w: runtime generation pin retain failed", ErrDurableIntentRejected)
	}
	if inst := ret.RuntimeInstanceID(); inst != "" {
		versions.RuntimeInstanceID = inst
	}
	if id := ret.RuntimeGenerationID(); id != "" {
		versions.RuntimeGenerationID = id
	}
	return pin, nil
}

// prepareRuntimeGenerationPin retains a provider pin and returns a one-shot
// commit helper for tests/helpers that still use the success/failure seam.
func prepareRuntimeGenerationPin(ctx context.Context, tracker *GenerationPinTracker, versions *terminalwork.BoundVersions) (commit func(workID string, success bool), err error) {
	noop := func(string, bool) {}
	pin, err := retainProviderPin(ctx, versions)
	if err != nil {
		return noop, err
	}
	if pin == nil {
		return noop, nil
	}
	var once sync.Once
	return func(workID string, success bool) {
		once.Do(func() {
			if !success {
				pin.Release()
				return
			}
			if tracker == nil || workID == "" || !tracker.Hold(workID, pin, nil) {
				pin.Release()
			}
		})
	}, nil
}
