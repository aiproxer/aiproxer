package usageevidence

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// EconomicEvidenceDispositionVersionV1 is the additive durable carrier version
// for transport coverage metadata. It is intentionally separate from the
// provider observation payload and therefore cannot be interpreted as a
// billable measure.
const EconomicEvidenceDispositionVersionV1 = 1

const (
	EconomicEvidenceCoverageComplete    = "complete"
	EconomicEvidenceCoveragePartial     = "partial"
	EconomicEvidenceCoverageUnsupported = "unsupported"

	maxEconomicEvidenceDispositionIdentityBytes = metering.MaxSafeEvidenceBytes
	maxEconomicEvidenceDispositionHashBytes     = 128
)

// EconomicEvidenceCoverage is a durable transport disposition, not a meter
// unit or a charge. A partial/unsupported disposition must retain a bounded,
// safe reason so later rating/reconciliation cannot mistake it for complete
// provider evidence.
type EconomicEvidenceCoverage string

// EconomicEvidenceDisposition binds one coverage disposition to the immutable
// source observation it describes. ObservationHash is replay-stable and
// excludes receipt time, matching durable call-leg replay semantics; the
// observation's own Fingerprint is never rewritten.
type EconomicEvidenceDisposition struct {
	ObservationIdentity string                   `json:"observation_identity"`
	ObservationHash     string                   `json:"observation_hash"`
	Coverage            EconomicEvidenceCoverage `json:"coverage"`
	CoverageReason      string                   `json:"coverage_reason,omitempty"`
}

// NewEconomicEvidenceDisposition constructs a bounded durable disposition
// from a canonical observation without changing that observation.
func NewEconomicEvidenceDisposition(observation metering.Observation, coverage, reason string) (EconomicEvidenceDisposition, error) {
	canonical, err := observation.Canonical()
	if err != nil {
		return EconomicEvidenceDisposition{}, fmt.Errorf("%w: observation", ErrInvalidRecord)
	}
	coverage = strings.TrimSpace(coverage)
	if coverage == "" {
		coverage = EconomicEvidenceCoverageComplete
	}
	disposition := EconomicEvidenceDisposition{
		ObservationIdentity: canonical.IdentityKey(),
		ObservationHash:     evidenceReplayFingerprint(canonical),
		Coverage:            EconomicEvidenceCoverage(coverage),
		CoverageReason:      strings.TrimSpace(reason),
	}
	if err := disposition.validateForObservation(canonical); err != nil {
		return EconomicEvidenceDisposition{}, err
	}
	return disposition, nil
}

func (d EconomicEvidenceDisposition) validateForObservation(observation metering.Observation) error {
	if err := d.Validate(); err != nil {
		return err
	}
	if d.ObservationIdentity != observation.IdentityKey() || d.ObservationHash != evidenceReplayFingerprint(observation) {
		return fmt.Errorf("%w: economic evidence disposition does not match observation", ErrInvalidRecord)
	}
	return nil
}

// Validate checks the durable disposition without requiring the observation.
// CallLegUsageRecord validation additionally verifies its identity and hash
// against the persisted observation list.
func (d EconomicEvidenceDisposition) Validate() error {
	if !validEconomicIdentity(d.ObservationIdentity, maxEconomicEvidenceDispositionIdentityBytes) ||
		d.ObservationHash == "" || !validEconomicEvidenceText(d.ObservationHash, maxEconomicEvidenceDispositionHashBytes) {
		return fmt.Errorf("%w: invalid economic evidence disposition identity", ErrInvalidRecord)
	}
	return validateEconomicEvidenceCoverage(d.Coverage, d.CoverageReason)
}

func validEconomicIdentity(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		// IdentityKey uses NUL as an unambiguous component separator. Other
		// controls are not safe durable diagnostic text.
		if unicode.IsControl(r) && r != '\x00' {
			return false
		}
	}
	return true
}

func validateEconomicEvidenceCoverage(coverage EconomicEvidenceCoverage, reason string) error {
	if coverage != EconomicEvidenceCoverageComplete && coverage != EconomicEvidenceCoveragePartial && coverage != EconomicEvidenceCoverageUnsupported {
		return fmt.Errorf("%w: unsupported economic evidence coverage", ErrInvalidRecord)
	}
	if !validEconomicEvidenceText(reason, metering.MaxSafeEvidenceFieldBytes) {
		return fmt.Errorf("%w: invalid economic evidence coverage reason", ErrInvalidRecord)
	}
	if (coverage == EconomicEvidenceCoveragePartial || coverage == EconomicEvidenceCoverageUnsupported) && reason == "" {
		return fmt.Errorf("%w: incomplete economic evidence requires coverage reason", ErrInvalidRecord)
	}
	return nil
}

func validEconomicEvidenceText(value string, maxBytes int) bool {
	if value == "" {
		return true
	}
	if len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func canonicalEconomicEvidenceDispositions(in []EconomicEvidenceDisposition) []EconomicEvidenceDisposition {
	if len(in) == 0 {
		return nil
	}
	out := append([]EconomicEvidenceDisposition(nil), in...)
	slices.SortFunc(out, func(a, b EconomicEvidenceDisposition) int {
		if a.ObservationIdentity != b.ObservationIdentity {
			return strings.Compare(a.ObservationIdentity, b.ObservationIdentity)
		}
		if a.ObservationHash != b.ObservationHash {
			return strings.Compare(a.ObservationHash, b.ObservationHash)
		}
		if a.Coverage != b.Coverage {
			return strings.Compare(string(a.Coverage), string(b.Coverage))
		}
		return strings.Compare(a.CoverageReason, b.CoverageReason)
	})
	return out
}

func validateEconomicEvidenceDispositions(leg CallLegUsageRecord) error {
	if len(leg.EconomicDispositions) == 0 {
		return nil
	}
	observations := make(map[string]string, len(leg.Observations))
	for _, observation := range leg.Observations {
		observations[observation.IdentityKey()] = evidenceReplayFingerprint(observation)
	}
	seen := make(map[string]struct{}, len(leg.EconomicDispositions))
	for i, disposition := range leg.EconomicDispositions {
		if err := disposition.Validate(); err != nil {
			return fmt.Errorf("%w: economic evidence disposition %d", ErrInvalidRecord, i)
		}
		if _, exists := seen[disposition.ObservationIdentity]; exists {
			return fmt.Errorf("%w: duplicate economic evidence disposition", ErrInvalidRecord)
		}
		seen[disposition.ObservationIdentity] = struct{}{}
		hash, exists := observations[disposition.ObservationIdentity]
		if !exists || hash != disposition.ObservationHash {
			return fmt.Errorf("%w: economic evidence disposition does not match retained observation", ErrInvalidRecord)
		}
	}
	return nil
}
