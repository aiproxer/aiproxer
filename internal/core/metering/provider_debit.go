package metering

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/metering/aggregate"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

var (
	// ErrProviderDebitIncomplete is returned when no complete authoritative
	// request debit can be established (for example estimated/absent evidence,
	// an unresolved correction, or a pending supersession). The returned
	// snapshot retains the diagnostic aggregate but must not be posted or
	// converted to money by a caller.
	ErrProviderDebitIncomplete = errors.New("metering: provider debit incomplete")
	// ErrProviderDebitIdentityConflict is the typed reducer spelling of the
	// canonical replay conflict. A changed payload under one source revision is
	// never accepted as a second debit.
	ErrProviderDebitIdentityConflict = aggregate.ErrIdentityConflict
)

// ProviderDebitSnapshot is a deterministic nonmonetary reduction of typed
// provider unit debits. Measures retain the complete account/pool/window and
// request/BillingCallID/B-leg scope from aggregate.SnapshotV2. Reduction has
// no customer balance, currency, or journal-posting fields by design.
type ProviderDebitSnapshot struct {
	Measures     []aggregate.ReducedMeasure
	Observations []sdkmetering.Observation
	Complete     bool
	Replayed     int
}

// ReduceProviderDebits validates typed evidence, projects it to the canonical
// V2 observation journal and applies the existing replay/supersession reducer.
// A gauge, request association without a debit, estimate, or partial record
// therefore cannot enter this function as a payable quantity.
func ReduceProviderDebits(debits []sdkmetering.ProviderDebit) (ProviderDebitSnapshot, error) {
	if len(debits) == 0 {
		return ProviderDebitSnapshot{}, sdkmetering.ErrProviderDebitAbsent
	}
	observations := make([]sdkmetering.Observation, 0, len(debits))
	for i, debit := range debits {
		if err := debit.Validate(); err != nil {
			return ProviderDebitSnapshot{}, fmt.Errorf("%w: debit[%d]: %w", ErrProviderDebitIncomplete, i, err)
		}
		observation, err := debit.ToObservation()
		if err != nil {
			return ProviderDebitSnapshot{}, fmt.Errorf("%w: debit[%d] observation: %w", ErrProviderDebitIncomplete, i, err)
		}
		observations = append(observations, observation)
	}
	return ReduceProviderDebitObservations(observations)
}

// ReduceProviderDebitObservations reduces only canonical provider-debit
// observations read back from the durable V2 journal. Account-window
// observations and other provider evidence are rejected rather than inferred
// into this request-debit plane.
func ReduceProviderDebitObservations(observations []sdkmetering.Observation) (ProviderDebitSnapshot, error) {
	if len(observations) == 0 {
		return ProviderDebitSnapshot{}, sdkmetering.ErrProviderDebitAbsent
	}
	canonical := make([]sdkmetering.Observation, 0, len(observations))
	for i, observation := range observations {
		if _, err := sdkmetering.ProviderDebitFromObservation(observation); err != nil {
			return ProviderDebitSnapshot{}, fmt.Errorf("%w: observation[%d]: %w", ErrProviderDebitIncomplete, i, err)
		}
		normalized, err := observation.Canonical()
		if err != nil {
			return ProviderDebitSnapshot{}, fmt.Errorf("%w: observation[%d] canonicalization: %w", ErrProviderDebitIncomplete, i, err)
		}
		canonical = append(canonical, normalized)
	}
	if err := validateProviderDebitRevisionLinks(canonical); err != nil {
		return ProviderDebitSnapshot{}, fmt.Errorf("%w: revision links: %w", ErrProviderDebitIncomplete, err)
	}
	reduction, err := aggregate.ApplyObservations(canonical)
	if err != nil {
		if errors.Is(err, aggregate.ErrIdentityConflict) {
			return ProviderDebitSnapshot{}, fmt.Errorf("%w: %v", ErrProviderDebitIdentityConflict, err)
		}
		return ProviderDebitSnapshot{}, fmt.Errorf("%w: reduction: %v", ErrProviderDebitIncomplete, err)
	}
	snapshot := ProviderDebitSnapshot{
		Measures:     append([]aggregate.ReducedMeasure(nil), reduction.Measures...),
		Observations: append([]sdkmetering.Observation(nil), reduction.Observations...),
		Complete:     reduction.Complete,
		Replayed:     reduction.Replayed,
	}
	if !snapshot.Complete {
		return snapshot, fmt.Errorf("%w: unresolved provider debit evidence", ErrProviderDebitIncomplete)
	}
	return snapshot, nil
}

func validateProviderDebitRevisionLinks(observations []sdkmetering.Observation) error {
	type identity struct {
		store, id string
		revision  uint64
	}
	known := make(map[identity]sdkmetering.ProviderDebit, len(observations))
	for _, observation := range observations {
		debit, err := sdkmetering.ProviderDebitFromObservation(observation)
		if err != nil {
			return err
		}
		known[identity{store: observation.Subject.StoreID, id: observation.ID, revision: observation.Revision}] = debit
	}
	for _, observation := range observations {
		debit, err := sdkmetering.ProviderDebitFromObservation(observation)
		if err != nil {
			return err
		}
		for _, ref := range debit.Supersedes {
			prior, ok := known[identity{store: ref.StoreID, id: ref.ObservationID, revision: ref.Revision}]
			if !ok {
				continue
			}
			if !prior.Component.Equal(debit.Component) {
				return fmt.Errorf("provider debit correction component mismatch: %s supersedes %s", debit.Component.CanonicalKey(), prior.Component.CanonicalKey())
			}
		}
	}
	return nil
}
