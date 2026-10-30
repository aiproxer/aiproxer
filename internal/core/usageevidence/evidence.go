package usageevidence

import (
	"fmt"
	"slices"
	"sort"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// cloneAndCanonicalizeObservations gives a sealed record ownership of every
// nested V2 value. Observation.Canonical also normalizes unordered component
// and coverage sets, making replay fingerprints stable across storage paths.
// Callers must validate before invoking this helper.
func cloneAndCanonicalizeObservations(in []metering.Observation) []metering.Observation {
	if len(in) == 0 {
		return nil
	}
	out := make([]metering.Observation, 0, len(in))
	for _, original := range in {
		canonical, err := original.Canonical()
		if err != nil {
			// validateCallLegObservations has already rejected this path. Keep a
			// defensive clone here so the helper never aliases caller memory if a
			// future caller uses it without the validator.
			canonical = original.Clone()
		}
		out = append(out, canonical)
	}
	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.IdentityKey() != right.IdentityKey() {
			return left.IdentityKey() < right.IdentityKey()
		}
		return left.Fingerprint() < right.Fingerprint()
	})
	return out
}

func canonicalObservations(in []metering.Observation) []metering.Observation {
	return cloneAndCanonicalizeObservations(in)
}

func canonicalObservationRefs(in []metering.ObservationRef) []metering.ObservationRef {
	if len(in) == 0 {
		return nil
	}
	out := append([]metering.ObservationRef(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		left, right := out[i], out[j]
		if left.StoreID != right.StoreID {
			return left.StoreID < right.StoreID
		}
		if left.ObservationID != right.ObservationID {
			return left.ObservationID < right.ObservationID
		}
		if left.Revision != right.Revision {
			return left.Revision < right.Revision
		}
		return left.PayloadHash < right.PayloadHash
	})
	return out
}

func canonicalEvidenceConflicts(in []EvidenceConflict) []EvidenceConflict {
	if len(in) == 0 {
		return nil
	}
	out := append([]EvidenceConflict(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Identity != out[j].Identity {
			return out[i].Identity < out[j].Identity
		}
		if out[i].ExistingHash != out[j].ExistingHash {
			return out[i].ExistingHash < out[j].ExistingHash
		}
		if out[i].IncomingHash != out[j].IncomingHash {
			return out[i].IncomingHash < out[j].IncomingHash
		}
		if out[i].ExistingCoverage != out[j].ExistingCoverage {
			return out[i].ExistingCoverage < out[j].ExistingCoverage
		}
		if out[i].ExistingCoverageReason != out[j].ExistingCoverageReason {
			return out[i].ExistingCoverageReason < out[j].ExistingCoverageReason
		}
		if out[i].IncomingCoverage != out[j].IncomingCoverage {
			return out[i].IncomingCoverage < out[j].IncomingCoverage
		}
		return out[i].IncomingCoverageReason < out[j].IncomingCoverageReason
	})
	return out
}

// validateCallLegObservations wraps each public metering observation cause
// with %w. The record keeps its own ErrInvalidRecord classification, and the
// failing SDK sentinel inside the observation stays classifiable instead of
// collapsing into rendered text.
func validateCallLegObservations(leg CallLegUsageRecord) error {
	if len(leg.Observations) == 0 {
		return nil
	}
	seen := make(map[string]string, len(leg.Observations))
	for i, observation := range leg.Observations {
		if err := observation.Validate(); err != nil {
			return fmt.Errorf("%w: observation %d: %w", ErrInvalidRecord, i, err)
		}
		// A CallLegUsageRecord is a B-leg closure. Resource/account-window and
		// statement subjects stay native and must be allocated explicitly by a
		// later economic owner; attaching one here would synthesize B-leg
		// ownership and make a durable record ambiguous.
		if observation.Subject.Kind != metering.SubjectBLeg && observation.Subject.Kind != metering.SubjectProviderCharge && observation.Subject.Kind != metering.SubjectProviderDebit {
			return fmt.Errorf("%w: observation %d subject %q is not B-leg rooted", ErrInvalidRecord, i, observation.Subject.Kind)
		}
		if observation.Subject.BLegID != leg.BLegID {
			return fmt.Errorf("%w: observation %d B-leg %q does not match record %q", ErrInvalidRecord, i, observation.Subject.BLegID, leg.BLegID)
		}
		if observation.Correlation.BLegID != "" && observation.Correlation.BLegID != leg.BLegID {
			return fmt.Errorf("%w: observation %d correlation B-leg %q does not match record %q", ErrInvalidRecord, i, observation.Correlation.BLegID, leg.BLegID)
		}
		if observation.Subject.ALegID != "" && observation.Subject.ALegID != leg.ALegID {
			return fmt.Errorf("%w: observation %d A-leg does not match record", ErrInvalidRecord, i)
		}
		if observation.Correlation.ALegID != "" && observation.Correlation.ALegID != leg.ALegID {
			return fmt.Errorf("%w: observation %d correlation A-leg does not match record", ErrInvalidRecord, i)
		}
		callID := leg.CallID.String()
		if observation.Subject.BillingCallID != "" && observation.Subject.BillingCallID != callID {
			return fmt.Errorf("%w: observation %d BillingCallID does not match record", ErrInvalidRecord, i)
		}
		if observation.Correlation.BillingCallID != "" && observation.Correlation.BillingCallID != callID {
			return fmt.Errorf("%w: observation %d correlation BillingCallID does not match record", ErrInvalidRecord, i)
		}
		if leg.AttemptSeq > 0 {
			if observation.Subject.AttemptSeq != 0 && observation.Subject.AttemptSeq != uint64(leg.AttemptSeq) {
				return fmt.Errorf("%w: observation %d attempt sequence does not match record", ErrInvalidRecord, i)
			}
			if observation.Correlation.AttemptSeq != 0 && observation.Correlation.AttemptSeq != uint64(leg.AttemptSeq) {
				return fmt.Errorf("%w: observation %d correlation attempt sequence does not match record", ErrInvalidRecord, i)
			}
		}
		identity := observation.IdentityKey()
		hash := evidenceReplayFingerprint(observation)
		if prior, exists := seen[identity]; exists {
			if prior != hash {
				return fmt.Errorf("%w: observation %d conflicts with source identity %q", ErrInvalidRecord, i, identity)
			}
			return fmt.Errorf("%w: duplicate observation source identity %q", ErrInvalidRecord, identity)
		}
		seen[identity] = hash
	}
	return nil
}

// evidenceReplayFingerprint delegates to the SDK replay preimage. That
// preimage excludes receipt metadata and normalizes the approved Subject /
// Correlation carrier placement while retaining every other source field.
func evidenceReplayFingerprint(observation metering.Observation) string {
	hash, err := observation.ReplayFingerprint()
	if err != nil {
		return ""
	}
	return hash
}

// ObservationEvidenceHash returns the replay-stable source hash used by the
// durable economic disposition carrier. It does not alter the observation or
// its provider-owned fingerprint.
func ObservationEvidenceHash(observation metering.Observation) string {
	return evidenceReplayFingerprint(observation)
}

// Clone returns a deep, caller-owned copy suitable for a terminal snapshot or
// a durable handoff. It is intentionally additive to the existing immutable
// record contract and does not expose request payloads.
func (l CallLegUsageRecord) Clone() CallLegUsageRecord {
	out := l
	if l.Observations != nil {
		out.Observations = make([]metering.Observation, len(l.Observations))
		for i, observation := range l.Observations {
			out.Observations[i] = observation.Clone()
		}
	}
	out.ObservationRefs = append([]metering.ObservationRef(nil), l.ObservationRefs...)
	out.EvidenceConflicts = append([]EvidenceConflict(nil), l.EvidenceConflicts...)
	out.EconomicDispositions = append([]EconomicEvidenceDisposition(nil), l.EconomicDispositions...)
	return out
}

// V2Observations returns a bounded deep snapshot. V1 callers should continue
// using Evidence, which is a compatibility projection and not an independent
// authority.
func (l CallLegUsageRecord) V2Observations() []metering.Observation {
	if len(l.Observations) == 0 {
		return nil
	}
	out := make([]metering.Observation, len(l.Observations))
	for i, observation := range l.Observations {
		out[i] = observation.Clone()
	}
	return out
}

// V2ObservationRefs returns a deep slice snapshot of immutable references.
func (l CallLegUsageRecord) V2ObservationRefs() []metering.ObservationRef {
	return slices.Clone(l.ObservationRefs)
}

// HasV2Evidence reports whether the record carries an additive V2 envelope.
func (l CallLegUsageRecord) HasV2Evidence() bool {
	return l.EvidenceVersion >= EvidenceFormatVersionV2 && (len(l.Observations) != 0 || len(l.ObservationRefs) != 0 || len(l.EvidenceConflicts) != 0 || len(l.EconomicDispositions) != 0)
}
