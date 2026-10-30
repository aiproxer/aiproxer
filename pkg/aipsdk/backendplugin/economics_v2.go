package backendplugin

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

const (
	// DefaultMaxEconomicObservationBytes bounds one canonical V2 observation
	// before it crosses the executable connector boundary.
	DefaultMaxEconomicObservationBytes = uint64(64 << 10)
	// DefaultMaxEconomicEvidencePerAttempt bounds retained sideband entries in a
	// single attempt. It is deliberately no larger than the V1 pending bound.
	DefaultMaxEconomicEvidencePerAttempt = uint64(1024)
)

// EvidenceCoverage records whether a connector transported the complete V2
// evidence contract or an explicitly reduced compatibility projection.
type EvidenceCoverage string

const (
	EvidenceCoverageComplete    EvidenceCoverage = "complete"
	EvidenceCoveragePartial     EvidenceCoverage = "partial"
	EvidenceCoverageUnsupported EvidenceCoverage = "unsupported"
)

// AccountingEvidenceV2 is host-only provider evidence. Observation is the
// canonical metering DTO; this wrapper adds a bounded capability disposition so
// a V1 bridge cannot be mistaken for full-fidelity V2 support.
type AccountingEvidenceV2 struct {
	Observation    metering.Observation
	Coverage       EvidenceCoverage
	CoverageReason string
}

// EconomicObservation is a source-compatible name for the canonical V2 DTO.
// There is intentionally no second observation model in backendplugin.
type EconomicObservation = metering.Observation

// EconomicEvidenceV2 is an additive alias used by connector authors.
type EconomicEvidenceV2 = AccountingEvidenceV2

var (
	// ErrAccountingEvidenceV2Unsupported is returned when V2 evidence is used
	// without an explicit negotiated capability.
	ErrAccountingEvidenceV2Unsupported = errors.New("backendplugin: accounting evidence V2 unsupported")
	// ErrAccountingEvidenceV2Conflict identifies two payloads with one source
	// identity but different immutable content.
	ErrAccountingEvidenceV2Conflict = errors.New("backendplugin: conflicting accounting evidence V2")
)

// Validate checks the wrapper and delegates economic identity, decimal,
// component, coverage and safe-lexeme rules to metering.Observation.
func (e AccountingEvidenceV2) Validate() error {
	if err := e.Observation.Validate(); err != nil {
		// The metering validator intentionally includes offending values for
		// local diagnostics. Connector errors cross a process boundary, so keep
		// this public ABI error structural and secret-safe.
		return fmt.Errorf("%w: economic observation", ErrInvalidFrame)
	}
	if e.Coverage != "" && e.Coverage != EvidenceCoverageComplete && e.Coverage != EvidenceCoveragePartial && e.Coverage != EvidenceCoverageUnsupported {
		return fmt.Errorf("%w: coverage disposition", ErrInvalidFrame)
	}
	coverage := e.Coverage
	if coverage == "" {
		coverage = EvidenceCoverageComplete
	}
	if coverage != EvidenceCoverageComplete && strings.TrimSpace(e.CoverageReason) == "" {
		return fmt.Errorf("%w: coverage reason", ErrInvalidFrame)
	}
	if !validEconomicDiagnostic(e.CoverageReason) {
		return fmt.Errorf("%w: coverage reason", ErrInvalidFrame)
	}
	if b, err := e.Observation.CanonicalJSON(); err != nil {
		return fmt.Errorf("%w: economic observation", ErrInvalidFrame)
	} else if err := ValidateSize(uint64(len(b)), DefaultMaxEconomicObservationBytes); err != nil {
		return fmt.Errorf("%w: economic observation", err)
	}
	return nil
}

func validEconomicDiagnostic(value string) bool {
	return validEconomicText(value, int(DefaultMaxDiagnosticBytes))
}

func validEconomicText(value string, maxBytes int) bool {
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

// AccountingEvidenceV2Negotiated reports the exact feature/minor pair needed
// for lossless V2 transport.
func AccountingEvidenceV2Negotiated(neg Negotiation) bool {
	return neg.Compatible && neg.NegotiatedMinor >= ProtocolMinorAccountingEvidenceV2 && containsFeature(neg.EnabledFeatures, FeatureAccountingEvidenceV2)
}

// EconomicsV2Negotiated is the shorter spelling used by host policy callers.
func EconomicsV2Negotiated(neg Negotiation) bool { return AccountingEvidenceV2Negotiated(neg) }

// EconomicEvidenceV2Negotiated is an alias for compatibility with neutral
// connector terminology.
func EconomicEvidenceV2Negotiated(neg Negotiation) bool { return AccountingEvidenceV2Negotiated(neg) }

// HostBoundEconomicEvidenceV2Negotiated reports whether the peer opted into
// the host-bound economic evidence contract. Only such a peer's provider claim
// is bound into the host namespace; for every other peer the host performs no
// identity rewrite and records an explicit capture loss instead.
func HostBoundEconomicEvidenceV2Negotiated(neg Negotiation) bool {
	return neg.Compatible &&
		neg.NegotiatedMinor >= ProtocolMinorHostBoundEconomicEvidenceV2 &&
		containsFeature(neg.EnabledFeatures, FeatureHostBoundEconomicEvidenceV2)
}

// RequireAccountingEvidenceV2 fails before provider execution when a strict
// offer requires the V2 evidence contract but the negotiated peer lacks it.
func RequireAccountingEvidenceV2(neg Negotiation) error {
	if !AccountingEvidenceV2Negotiated(neg) {
		return ErrAccountingEvidenceV2Unsupported
	}
	return nil
}

func containsFeature(features []string, want string) bool {
	for _, feature := range features {
		if strings.TrimSpace(feature) == want {
			return true
		}
	}
	return false
}
