package routeoverride

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// Service is the generation-bound command service. Persistence is process-owned;
// SelectorValidator is bound to the generation that constructed this instance.
type Service struct {
	store     Store
	validator SelectorValidator
	now       func() time.Time
}

// NewService constructs a command service. Store and validator are required.
func NewService(store Store, validator SelectorValidator, now func() time.Time) (*Service, error) {
	if store == nil {
		return nil, fmt.Errorf("routeoverride: nil store")
	}
	if validator == nil {
		return nil, fmt.Errorf("routeoverride: nil selector validator")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, validator: validator, now: now}, nil
}

func (s *Service) Get(ctx context.Context, aLegID string) (State, error) {
	if s == nil || s.store == nil {
		return State{}, fmt.Errorf("routeoverride: nil service")
	}
	id, err := normalizeALegID(aLegID)
	if err != nil {
		return State{}, err
	}
	st, err := s.store.Get(ctx, id)
	return st, mapStoreErr(err)
}

func (s *Service) Replace(ctx context.Context, aLegID, selector string) (State, error) {
	if s == nil || s.store == nil {
		return State{}, fmt.Errorf("routeoverride: nil service")
	}
	id, err := normalizeALegID(aLegID)
	if err != nil {
		return State{}, err
	}
	normalized, err := prepareSelector(selector)
	if err != nil {
		return State{}, err
	}
	if err := s.validator.ValidateSelector(ctx, normalized); err != nil {
		if errors.Is(err, ErrInvalidSelector) {
			return State{}, err
		}
		return State{}, fmt.Errorf("%w: %w", ErrInvalidSelector, err)
	}
	st, err := s.store.Replace(ctx, id, normalized, s.now())
	return st, mapStoreErr(err)
}

func (s *Service) Clear(ctx context.Context, aLegID string) (State, error) {
	if s == nil || s.store == nil {
		return State{}, fmt.Errorf("routeoverride: nil service")
	}
	id, err := normalizeALegID(aLegID)
	if err != nil {
		return State{}, err
	}
	st, err := s.store.Clear(ctx, id, s.now())
	return st, mapStoreErr(err)
}

func normalizeALegID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", ErrNotFound
	}
	return id, nil
}

func prepareSelector(raw string) (string, error) {
	normalized := NormalizeSelector(raw)
	if normalized == "" {
		return "", fmt.Errorf("%w: empty selector", ErrInvalidSelector)
	}
	if len(normalized) > aipapi.MaxRouteSelectorBytes {
		return "", fmt.Errorf("%w: selector exceeds %d bytes", ErrInvalidSelector, aipapi.MaxRouteSelectorBytes)
	}
	return normalized, nil
}

func mapStoreErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNotFound) ||
		errors.Is(err, ErrInvalidSelector) ||
		errors.Is(err, ErrRevisionExhausted) ||
		errors.Is(err, ErrUnavailable) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrUnavailable, err)
}
