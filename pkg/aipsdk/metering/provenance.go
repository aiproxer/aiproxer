package metering

import "fmt"

// legacyV1ObservationTuple reports whether an observation has the versioned
// marker and scope emitted by the historical V1 lift. It is deliberately
// limited to V1's local/provider origins and subject union; statement records
// were never representable by the V1 Fact DTO.
func legacyV1ObservationTuple(o Observation) bool {
	if o.Version != ObservationVersionV2 ||
		o.MappingRef != LegacyV1MappingRef ||
		o.Acquisition != LegacyV1MappingRef ||
		o.Subject.StoreID != LegacyV1StoreID ||
		o.Correlation.StoreID != LegacyV1StoreID {
		return false
	}
	if o.Origin != OriginLocal && o.Origin != OriginProvider {
		return false
	}
	switch o.Subject.Kind {
	case SubjectALeg, SubjectRequest, SubjectBLeg, SubjectBillingCall, SubjectSubmission:
		return true
	default:
		return false
	}
}

// legacyV1TrustedTuple requires both the private authority bit and the
// complete historical tuple. Generic JSON decoding cannot set the bit, and
// mutating a trusted value's exported scope invalidates the tuple.
func legacyV1TrustedTuple(o Observation) bool {
	return o.legacyV1Trusted && legacyV1ObservationTuple(o)
}

// legacyProviderRequestTuple reports whether an observation has exactly the
// marker and scope used by the historical V1 provider/request representation.
// The tuple is checked on every validation; the private bit alone is never an
// authority grant because callers may mutate exported fields after a lift.
func legacyProviderRequestTuple(o Observation) bool {
	if !legacyV1ObservationTuple(o) || o.Origin != OriginProvider {
		return false
	}
	switch o.Subject.Kind {
	case SubjectALeg, SubjectRequest, SubjectBillingCall, SubjectSubmission:
		return true
	default:
		return false
	}
}

func legacyProviderRequestAllowed(o Observation) bool {
	return o.legacyProviderRequest && legacyProviderRequestTuple(o)
}

func validateObservationOwnership(o Observation, allowLegacyProviderRequest bool) error {
	if o.Correlation.AttemptSeq != 0 && o.Correlation.BLegID == "" {
		return fmt.Errorf("%w: AttemptSeq requires BLegID ownership", ErrInvalidObservation)
	}
	if o.Correlation.AttemptID != "" && o.Correlation.BLegID == "" {
		return fmt.Errorf("%w: AttemptID requires BLegID ownership", ErrInvalidObservation)
	}

	switch o.Subject.Kind {
	case SubjectBLeg, SubjectProviderCharge, SubjectProviderDebit:
		if o.Subject.BLegID == "" || o.Correlation.BLegID == "" {
			return fmt.Errorf("%w: request-scoped evidence requires BLegID on subject and correlation", ErrInvalidObservation)
		}
	case SubjectAccountWindow, SubjectResource:
		if o.Correlation.BLegID != "" || o.Correlation.AttemptID != "" || o.Correlation.AttemptSeq != 0 {
			return fmt.Errorf("%w: non-request subject cannot carry B-leg attempt lineage", ErrInvalidObservation)
		}
	}

	// A historical V1 fact can only carry request-level provider attribution;
	// retain that already-published compatibility shape under its explicit
	// legacy mapping. New V2 provider evidence remains B-leg-owned.
	if o.Origin == OriginProvider && !allowLegacyProviderRequest {
		switch o.Subject.Kind {
		case SubjectALeg, SubjectRequest, SubjectBillingCall, SubjectSubmission:
			return fmt.Errorf("%w: provider request-scoped evidence requires a concrete BLegID", ErrInvalidObservation)
		}
	}
	return nil
}

func validateObservationSubjectSemantics(o Observation) error {
	switch o.Subject.Kind {
	case SubjectAccountWindow:
		if o.Semantics != SemanticsGauge {
			return fmt.Errorf("%w: account-window utilization must use gauge semantics", ErrInvalidObservation)
		}
		if len(o.Measures) == 0 || len(o.Charges) != 0 {
			return fmt.Errorf("%w: account-window utilization requires measures and cannot carry charges", ErrInvalidObservation)
		}
		for i, measure := range o.Measures {
			if measure.Key.Direction != DirectionNone {
				return fmt.Errorf("%w: account-window measure[%d] must be nondirectional", ErrInvalidObservation, i)
			}
		}
	case SubjectProviderDebit:
		if o.Origin != OriginProvider {
			return fmt.Errorf("%w: provider debit requires provider origin", ErrInvalidObservation)
		}
		if o.Authority != AuthorityObservedClaim {
			return fmt.Errorf("%w: provider debit requires observed provider authority", ErrInvalidObservation)
		}
		if o.Semantics == SemanticsGauge {
			return fmt.Errorf("%w: provider debit cannot use gauge semantics", ErrInvalidObservation)
		}
		if len(o.Measures) != 1 || len(o.Charges) != 0 {
			return fmt.Errorf("%w: provider debit requires exactly one measure and no charges", ErrInvalidObservation)
		}
		if o.Measures[0].Key.Direction != DirectionNone {
			return fmt.Errorf("%w: provider debit measure must be nondirectional", ErrInvalidObservation)
		}
		if o.Measures[0].Key.Unit == UnitPercent {
			return fmt.Errorf("%w: provider debit cannot use percent gauges", ErrInvalidObservation)
		}
		if o.Measures[0].Quality != QualityObserved {
			return fmt.Errorf("%w: provider debit requires observed measure quality", ErrInvalidObservation)
		}
	case SubjectResource:
		for i, measure := range o.Measures {
			if measure.Key.Direction != DirectionNone {
				return fmt.Errorf("%w: resource measure[%d] must be nondirectional", ErrInvalidObservation, i)
			}
		}
		for i, charge := range o.Charges {
			if charge.Component != nil && charge.Component.Direction != DirectionNone {
				return fmt.Errorf("%w: resource charge[%d] must be nondirectional", ErrInvalidObservation, i)
			}
		}
	}
	return nil
}
