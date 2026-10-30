package policydecision

import "fmt"

// ValidateRecord validates a policy decision record against the legality
// descriptors (requirements 1.5, 3.6, 4.4, 6.6). It rejects unknown stages,
// OutcomeUnknown, unknown effects, and illegal outcome/effect pairs as
// malformed policy decisions. It does not merge or order records: deterministic
// application order remains with the stage runners.
//
// Stage identifiers may carry surrounding whitespace that normalization would
// trim; callers that want whitespace-tolerant validation must trim the stage
// before calling ValidateRecord.
func ValidateRecord(record Record) error {
	if !IsLegalStageID(record.Stage) {
		return fmt.Errorf("malformed policy decision: unknown stage %q", record.Stage)
	}
	if record.Outcome == OutcomeUnknown || !record.Outcome.IsKnown() {
		return fmt.Errorf("malformed policy decision: unknown outcome %q at stage %q", record.Outcome, record.Stage)
	}
	if !record.Effect.IsKnown() {
		return fmt.Errorf("malformed policy decision: unknown effect %q at stage %q", record.Effect, record.Stage)
	}
	if !IsLegalPair(record.Stage, record.Outcome, record.Effect) {
		return fmt.Errorf("malformed policy decision: illegal pair outcome=%q effect=%q at stage %q",
			record.Outcome, record.Effect, record.Stage)
	}
	return nil
}
