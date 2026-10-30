package continuation

import (
	"context"
	"errors"
)

// Store reserves proxy response IDs, stores terminal records, and performs scoped lookup.
type Store interface {
	Reserve(ctx context.Context, scope Scope, policy StoragePolicy) (ResponseID, error)
	PutTerminal(ctx context.Context, record ContinuationRecord) error
	Get(ctx context.Context, scope Scope, id ResponseID) (ContinuationRecord, error)
	Delete(ctx context.Context, scope Scope, id ResponseID) error
}

// Lookup performs a scoped get and maps every miss to ErrPreviousResponseNotFound.
func Lookup(ctx context.Context, store Store, scope Scope, id ResponseID) (ContinuationRecord, error) {
	if ctx == nil {
		return ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	if store == nil {
		return ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	if scope.IsZero() {
		return ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	if err := id.Validate(); err != nil {
		return ContinuationRecord{}, ErrPreviousResponseNotFound
	}
	rec, err := store.Get(ctx, scope, id)
	if err != nil {
		if errorsIsPreviousNotFound(err) {
			return ContinuationRecord{}, ErrPreviousResponseNotFound
		}
		return ContinuationRecord{}, err
	}
	return rec, nil
}

func errorsIsPreviousNotFound(err error) bool {
	return errors.Is(err, ErrPreviousResponseNotFound) ||
		errors.Is(err, ErrRecordNotReady) ||
		errors.Is(err, ErrIncompleteNotEligible) ||
		errors.Is(err, ErrRecordNotEligible) ||
		errors.Is(err, ErrChainDepthExceeded) ||
		errors.Is(err, ErrCycleDetected) ||
		errors.Is(err, ErrLineageMismatch) ||
		errors.Is(err, ErrMaterializedSizeExceeded) ||
		errors.Is(err, ErrMaterializedItemsExceeded) ||
		errors.Is(err, ErrStoreClosed)
}
