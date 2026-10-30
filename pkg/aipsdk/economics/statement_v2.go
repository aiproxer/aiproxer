package economics

import (
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// StatementLineOutcome records whether a normalized line was linked to an
// included observation charge. Unmatched is an explicit, reviewable outcome;
// it does not imply a missing B-leg attribution.
type StatementLineOutcome string

const (
	StatementLineMatched   StatementLineOutcome = "matched"
	StatementLineUnmatched StatementLineOutcome = "unmatched"
)

func (o StatementLineOutcome) IsKnown() bool {
	return o == StatementLineMatched || o == StatementLineUnmatched
}

func isZeroObservationRef(ref metering.ObservationRef) bool {
	return ref.StoreID == "" && ref.ObservationID == "" && ref.Revision == 0 && ref.PayloadHash == ""
}

type statementObservationKey struct {
	store       string
	observation string
	revision    uint64
}

func statementObservationKeyOf(ref metering.ObservationRef) statementObservationKey {
	return statementObservationKey{store: ref.StoreID, observation: ref.ObservationID, revision: ref.Revision}
}

func statementObservationRef(observation metering.Observation) metering.ObservationRef {
	return metering.ObservationRef{
		StoreID:       observation.Subject.StoreID,
		ObservationID: observation.ID,
		Revision:      observation.Revision,
		PayloadHash:   observation.Fingerprint(),
	}
}

func validateStatementBatchLinkage(batch StatementBatch) error {
	if batch.Subject.ProviderAccountKey != "" && batch.Subject.ProviderAccountKey != batch.ProviderAccountKey {
		return errors.New("economics: statement subject provider account mismatch")
	}
	if batch.Subject.StatementID != "" && batch.Subject.StatementID != batch.StatementID {
		return errors.New("economics: statement subject statement ID mismatch")
	}
	if batch.Subject.PeriodID != "" && batch.Subject.PeriodID != batch.PeriodID {
		return errors.New("economics: statement subject period mismatch")
	}

	observations := make(map[statementObservationKey]metering.Observation, len(batch.Observations))
	for i, observation := range batch.Observations {
		key := statementObservationKeyOf(statementObservationRef(observation))
		if prior, exists := observations[key]; exists {
			priorRef := statementObservationRef(prior)
			currentRef := statementObservationRef(observation)
			if !priorRef.Equal(currentRef) {
				return fmt.Errorf("economics: conflicting statement observation revision %q", observation.ID)
			}
			return fmt.Errorf("economics: duplicate statement observation %q", observation.ID)
		}
		if observation.Subject.ProviderAccountKey != "" && observation.Subject.ProviderAccountKey != batch.ProviderAccountKey {
			return fmt.Errorf("economics: statement observation %d provider account mismatch", i)
		}
		if observation.Correlation.ProviderAccountKey != "" && observation.Correlation.ProviderAccountKey != batch.ProviderAccountKey {
			return fmt.Errorf("economics: statement observation %d provider account mismatch", i)
		}
		if observation.Subject.StatementID != "" && observation.Subject.StatementID != batch.StatementID {
			return fmt.Errorf("economics: statement observation %d statement ID mismatch", i)
		}
		if observation.Subject.PeriodID != "" && observation.Subject.PeriodID != batch.PeriodID {
			return fmt.Errorf("economics: statement observation %d period mismatch", i)
		}
		if observation.Correlation.PeriodID != "" && observation.Correlation.PeriodID != batch.PeriodID {
			return fmt.Errorf("economics: statement observation %d period mismatch", i)
		}
		observations[key] = observation
	}

	linked := make(map[string]struct{}, len(batch.Lines))
	for i, line := range batch.Lines {
		if line.Subject.ProviderAccountKey != batch.ProviderAccountKey {
			return fmt.Errorf("economics: statement line %d provider account mismatch", i)
		}
		if line.Subject.StatementID != batch.StatementID {
			return fmt.Errorf("economics: statement line %d statement ID mismatch", i)
		}
		if line.Subject.StatementLineID != line.ID {
			return fmt.Errorf("economics: statement line %d subject line ID mismatch", i)
		}
		if line.Subject.PeriodID != batch.PeriodID {
			return fmt.Errorf("economics: statement line %d period mismatch", i)
		}
		if line.Outcome == StatementLineUnmatched {
			continue
		}

		key := statementObservationKeyOf(line.Observation)
		observation, exists := observations[key]
		if !exists {
			return fmt.Errorf("economics: statement line %d observation is not included exactly", i)
		}
		expected := statementObservationRef(observation)
		if !expected.Equal(line.Observation) {
			return fmt.Errorf("economics: statement line %d observation hash/revision does not match included observation", i)
		}
		if observation.Subject.Kind != metering.SubjectStatementLine {
			return fmt.Errorf("economics: statement line %d included observation must have statement-line subject", i)
		}
		if observation.Subject.ProviderAccountKey != batch.ProviderAccountKey || observation.Subject.StatementID != batch.StatementID || observation.Subject.PeriodID != batch.PeriodID {
			return fmt.Errorf("economics: statement line %d included observation statement identity mismatch", i)
		}
		if observation.Subject.StatementLineID != line.ID {
			return fmt.Errorf("economics: statement line %d included observation line ID mismatch", i)
		}
		chargeFound := false
		for _, charge := range observation.Charges {
			if charge.ChargeItemID == line.ChargeItemID {
				chargeFound = true
				break
			}
		}
		if !chargeFound {
			return fmt.Errorf("economics: statement line %d charge is not present in included observation", i)
		}
		linkKey := fmt.Sprintf("%s\x00%s\x00%d\x00%s", line.Observation.StoreID, line.Observation.ObservationID, line.Observation.Revision, line.ChargeItemID)
		if _, duplicate := linked[linkKey]; duplicate {
			return fmt.Errorf("economics: duplicate statement observation/charge linkage for line %q", line.ID)
		}
		linked[linkKey] = struct{}{}
	}
	return nil
}
