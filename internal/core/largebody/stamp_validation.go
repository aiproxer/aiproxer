package largebody

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
)

// ErrStampDisagreement is returned when live execution facts disagree with the
// bound AssessmentStamp (Requirements 6.6, 6.7, 8.5; Task 11.8).
// This is an internal invariant failure, never a fallback to canonical.
var ErrStampDisagreement = errors.New("largebody: assessment stamp disagreement (invariant failure)")

// StampInvariantError details which specific field of the AssessmentStamp disagreed
// with live execution facts. It wraps ErrStampDisagreement so errors.Is(err, ErrStampDisagreement)
// evaluates to true, signaling a terminal invariant failure (Requirement 6.7).
type StampInvariantError struct {
	Field      string
	BoundValue string
	LiveValue  string
}

// Error formats the disagreement for telemetry and invariant logging.
func (e *StampInvariantError) Error() string {
	return fmt.Sprintf("largebody: assessment stamp disagreement on %s: bound %q vs live %q (invariant failure)", e.Field, e.BoundValue, e.LiveValue)
}

// Unwrap returns ErrStampDisagreement for error classification.
func (e *StampInvariantError) Unwrap() error {
	return ErrStampDisagreement
}

// IsInvariantFailure reports whether this error represents an internal invariant failure
// that must never trigger canonical fallback (Requirement 6.6, 6.7).
func (e *StampInvariantError) IsInvariantFailure() bool {
	return true
}

// LiveExecutionFacts carries the live execution-time facts that must agree
// with the bound AssessmentStamp before wire execution can begin (Requirements 6.7, 8.5; Task 11.8).
type LiveExecutionFacts struct {
	GenerationID              string
	ProfileID                 string
	Source                    SourceDigest
	BodyBytes                 int64
	Mode                      BodyMode
	Rewrite                   RewriteSemantics
	CandidateDomainGeneration string
}

// Validate enforces bounds under the semantic-fact budget.
func (f LiveExecutionFacts) Validate(maxFactBytes int64) error {
	if err := checkBudget(maxFactBytes); err != nil {
		return err
	}
	if strings.TrimSpace(f.GenerationID) == "" {
		return fmt.Errorf("largebody: live execution generation must not be empty")
	}
	if strings.TrimSpace(f.ProfileID) == "" {
		return fmt.Errorf("largebody: live execution profile must not be empty")
	}
	if f.Source.IsZero() {
		return fmt.Errorf("largebody: live execution source digest must not be zero")
	}
	if f.BodyBytes <= 0 {
		return fmt.Errorf("largebody: live execution body bytes must be > 0, got %d", f.BodyBytes)
	}
	if err := f.Mode.Validate(); err != nil {
		return err
	}
	if err := f.Rewrite.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(f.CandidateDomainGeneration) == "" {
		return fmt.Errorf("largebody: live execution candidate/domain proof generation must not be empty")
	}
	if int64(len(f.GenerationID)) > maxFactBytes || int64(len(f.ProfileID)) > maxFactBytes || int64(len(f.CandidateDomainGeneration)) > maxFactBytes {
		return fmt.Errorf("largebody: live execution facts exceed budget %d", maxFactBytes)
	}
	return nil
}

// ValidateAssessmentStamp revalidates the bound AssessmentStamp against live execution facts
// (Requirements 6.6, 6.7, 8.5; Task 11.8).
// Any disagreement returns a *StampInvariantError wrapping ErrStampDisagreement.
// Execute disagreement is an invariant failure, NEVER a fallback to canonical.
func ValidateAssessmentStamp(stamp AssessmentStamp, live LiveExecutionFacts) error {
	if stamp.IsZero() {
		return &StampInvariantError{
			Field:      "stamp",
			BoundValue: "zero",
			LiveValue:  "non-zero",
		}
	}

	// 1. Generation identity
	if stamp.GenerationID() != live.GenerationID {
		return &StampInvariantError{
			Field:      "generation",
			BoundValue: stamp.GenerationID(),
			LiveValue:  live.GenerationID,
		}
	}

	// 2. Profile identity
	if stamp.ProfileID() != live.ProfileID {
		return &StampInvariantError{
			Field:      "profile",
			BoundValue: stamp.ProfileID(),
			LiveValue:  live.ProfileID,
		}
	}

	// 3. Source digest
	if stamp.SourceDigest() != live.Source {
		return &StampInvariantError{
			Field:      "source",
			BoundValue: stamp.SourceDigest().String(),
			LiveValue:  live.Source.String(),
		}
	}

	// 4. Source size
	if stamp.BodyBytes() != live.BodyBytes {
		return &StampInvariantError{
			Field:      "body size",
			BoundValue: fmt.Sprintf("%d", stamp.BodyBytes()),
			LiveValue:  fmt.Sprintf("%d", live.BodyBytes),
		}
	}

	// 5. Body mode
	if stamp.BodyMode() != live.Mode {
		return &StampInvariantError{
			Field:      "body mode",
			BoundValue: string(stamp.BodyMode()),
			LiveValue:  string(live.Mode),
		}
	}

	// 6. Rewrite contract
	if stamp.Rewrite() != live.Rewrite {
		return &StampInvariantError{
			Field:      "rewrite",
			BoundValue: stamp.Rewrite().String(),
			LiveValue:  live.Rewrite.String(),
		}
	}

	// 7. Candidate/domain proof generation
	if stamp.CandidateDomainGeneration() != live.CandidateDomainGeneration {
		return &StampInvariantError{
			Field:      "candidate/domain proof generation",
			BoundValue: stamp.CandidateDomainGeneration(),
			LiveValue:  live.CandidateDomainGeneration,
		}
	}

	return nil
}

// Revalidate checks the stamp against live execution facts (Requirement 6.7, Task 11.8).
// Any disagreement is an invariant failure, never a fallback.
func (s AssessmentStamp) Revalidate(live LiveExecutionFacts) error {
	return ValidateAssessmentStamp(s, live)
}

// ValidateExecuteLargeBody validates an accepted Assessment, its bound stamp, and source
// against live execution facts before wire execution proceeds (Requirements 6.6, 6.7; Task 11.8).
func ValidateExecuteLargeBody(accepted Assessment, src Source, live LiveExecutionFacts) error {
	if !accepted.Accepted() {
		return &StampInvariantError{
			Field:      "decision",
			BoundValue: accepted.Decision.String(),
			LiveValue:  AssessmentDecisionAccept.String(),
		}
	}
	if accepted.Stamp.IsZero() {
		return &StampInvariantError{
			Field:      "stamp",
			BoundValue: "zero",
			LiveValue:  "non-zero",
		}
	}
	if src != nil {
		if digester, ok := src.(interface{ Digest() SourceDigest }); ok {
			if digester.Digest() != accepted.Stamp.SourceDigest() {
				return &StampInvariantError{
					Field:      "source",
					BoundValue: accepted.Stamp.SourceDigest().String(),
					LiveValue:  digester.Digest().String(),
				}
			}
		} else if digester, ok := src.(interface{ SourceDigest() SourceDigest }); ok {
			if digester.SourceDigest() != accepted.Stamp.SourceDigest() {
				return &StampInvariantError{
					Field:      "source",
					BoundValue: accepted.Stamp.SourceDigest().String(),
					LiveValue:  digester.SourceDigest().String(),
				}
			}
		}
		if src.Size() != accepted.Stamp.BodyBytes() {
			return &StampInvariantError{
				Field:      "body size",
				BoundValue: fmt.Sprintf("%d", accepted.Stamp.BodyBytes()),
				LiveValue:  fmt.Sprintf("%d", src.Size()),
			}
		}
	}
	if boundComp := accepted.Stamp.CompactionDigest(); boundComp != [32]byte{} {
		if liveComp := compactionfacts.Digest(accepted.CompactionFacts, accepted.CompactionComplete); boundComp != liveComp {
			return &StampInvariantError{
				Field:      "compaction facts",
				BoundValue: hex.EncodeToString(boundComp[:]),
				LiveValue:  hex.EncodeToString(liveComp[:]),
			}
		}
	}
	if boundCaps := accepted.Stamp.RequiredCapabilitiesDigest(); boundCaps != [32]byte{} || len(accepted.Stamp.RequiredCapabilities()) > 0 {
		if liveCaps := CapabilitiesDigest(accepted.WireRequest.RequiredCapabilities); boundCaps != liveCaps {
			return &StampInvariantError{
				Field:      "required capabilities",
				BoundValue: hex.EncodeToString(boundCaps[:]),
				LiveValue:  hex.EncodeToString(liveCaps[:]),
			}
		}
		if liveDomainCaps := CapabilitiesDigest(accepted.WireDomain.RequiredCapabilities); boundCaps != liveDomainCaps {
			return &StampInvariantError{
				Field:      "wire domain required capabilities",
				BoundValue: hex.EncodeToString(boundCaps[:]),
				LiveValue:  hex.EncodeToString(liveDomainCaps[:]),
			}
		}
	}
	return ValidateAssessmentStamp(accepted.Stamp, live)
}
