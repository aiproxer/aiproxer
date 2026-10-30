package journalstore

import (
	"context"
	"fmt"

	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	sdkmetering "github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// NewObservationSink adapts the V2 observation journal to the SDK capture
// ports. The returned adapter implements both ObservationSink for source
// compatibility and AtomicObservationSink for retry-safe checkpoint batches.
// DurableStore already has a legacy Fact Append method, so the adapter keeps
// the two append contracts distinct.
func NewObservationSink(store *DurableStore) sdkmetering.ObservationSink {
	if store == nil {
		return nil
	}
	return durableObservationSink{store: store}
}

// NewObservationSinkWithOutbox adapts the V2 observation journal and enables
// the atomic durable economic trigger outbox. It is deliberately a separate
// constructor so disabled or non-economic metering remains a no-op with no
// relay rows.
func NewObservationSinkWithOutbox(store *DurableStore) coremetering.EconomicObservationSink {
	if store == nil {
		return nil
	}
	return durableEconomicObservationSink{store: store}
}

type durableObservationSink struct {
	store *DurableStore
}

type durableEconomicObservationSink struct {
	store *DurableStore
}

var (
	_ sdkmetering.ObservationSink            = durableObservationSink{}
	_ sdkmetering.AtomicObservationSink      = durableObservationSink{}
	_ sdkmetering.ObservationJournalIdentity = durableObservationSink{}
	_ coremetering.EconomicObservationSink   = durableEconomicObservationSink{}
	_ sdkmetering.AtomicObservationSink      = durableEconomicObservationSink{}
	_ sdkmetering.ObservationJournalIdentity = durableEconomicObservationSink{}
)

// ObservationJournalIdentity reports the identity of the journal this sink
// writes to. The sink is a wrapper over one journal, so it reports that
// journal's identity unchanged: a recorder built from the same journal and
// this sink are provably one journal.
func (s durableObservationSink) ObservationJournalIdentity() sdkmetering.ObservationJournalID {
	if s.store == nil {
		return ""
	}
	return s.store.ObservationJournalIdentity()
}

// ObservationJournalIdentity reports the identity of the journal this sink
// writes to. The sink is a wrapper over one journal, so it reports that
// journal's identity unchanged: a recorder built from the same journal and
// this sink are provably one journal.
func (s durableEconomicObservationSink) ObservationJournalIdentity() sdkmetering.ObservationJournalID {
	if s.store == nil {
		return ""
	}
	return s.store.ObservationJournalIdentity()
}

func (s durableObservationSink) Append(ctx context.Context, observation sdkmetering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil observation sink store")
	}
	return s.store.AppendObservation(ctx, observation)
}

func (s durableObservationSink) AppendObservations(ctx context.Context, observations []sdkmetering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil observation sink store")
	}
	return s.store.AppendObservations(ctx, observations)
}

func (s durableEconomicObservationSink) Append(ctx context.Context, observation sdkmetering.Observation) error {
	return s.AppendEconomicObservationWithOutbox(ctx, observation)
}

func (s durableEconomicObservationSink) AppendObservations(ctx context.Context, observations []sdkmetering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil observation sink store")
	}
	return s.store.AppendObservationsWithOutbox(ctx, observations)
}

// AppendEconomicObservationWithOutbox is the only late-economics capability.
// The journal row and the economic-work trigger are committed by the existing
// AppendObservationsWithOutbox transaction, including its exact replay,
// ambiguity and recovery behavior.
func (s durableEconomicObservationSink) AppendEconomicObservationWithOutbox(ctx context.Context, observation sdkmetering.Observation) error {
	if s.store == nil {
		return fmt.Errorf("metering/journalstore: nil economic observation sink store")
	}
	return s.store.AppendObservationsWithOutbox(ctx, []sdkmetering.Observation{observation})
}
