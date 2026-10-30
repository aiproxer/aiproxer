package terminal

import "fmt"

// OutcomeCode classifies the published terminal result without carrying
// provider-specific wire shapes.
type OutcomeCode string

const (
	OutcomeCodeSuccess        OutcomeCode = "success"
	OutcomeCodePartial        OutcomeCode = "partial"
	OutcomeCodeCancelled      OutcomeCode = "cancelled"
	OutcomeCodeError          OutcomeCode = "error"
	OutcomeCodeTimeout        OutcomeCode = "timeout"
	OutcomeCodeReplaced       OutcomeCode = "replaced"
	OutcomeCodeParallelLoser  OutcomeCode = "parallel_loser"
	OutcomeCodeDenied         OutcomeCode = "denied"
	OutcomeCodeEncoderFailure OutcomeCode = "encoder_failure"
	OutcomeCodePanic          OutcomeCode = "panic"
	OutcomeCodeEOF            OutcomeCode = "eof"
	OutcomeCodeSwallowed      OutcomeCode = "swallowed"
	OutcomeCodeBackendOpen    OutcomeCode = "backend_open_failure"
	OutcomeCodeFailed         OutcomeCode = "failed"
)

// IsKnown reports whether c is a documented outcome code.
func (c OutcomeCode) IsKnown() bool {
	switch c {
	case OutcomeCodeSuccess, OutcomeCodePartial, OutcomeCodeCancelled, OutcomeCodeError,
		OutcomeCodeTimeout, OutcomeCodeReplaced, OutcomeCodeParallelLoser, OutcomeCodeDenied,
		OutcomeCodeEncoderFailure, OutcomeCodePanic, OutcomeCodeEOF, OutcomeCodeSwallowed,
		OutcomeCodeBackendOpen, OutcomeCodeFailed:
		return true
	}
	return false
}

// Validate returns an error when c is not a known outcome code.
func (c OutcomeCode) Validate() error {
	if !c.IsKnown() {
		return fmt.Errorf("%w: unknown outcome code %q", ErrInvalid, c)
	}
	return nil
}

// OutcomeCodeFor maps a terminal command to its canonical outcome code.
func OutcomeCodeFor(cmd Command) OutcomeCode {
	switch cmd {
	case CommandNormalFinish:
		return OutcomeCodeSuccess
	case CommandPartialError:
		return OutcomeCodePartial
	case CommandCancel:
		return OutcomeCodeCancelled
	case CommandClose:
		return OutcomeCodeCancelled
	case CommandTimeout:
		return OutcomeCodeTimeout
	case CommandGateReplacement:
		return OutcomeCodeReplaced
	case CommandParallelLoser:
		return OutcomeCodeParallelLoser
	case CommandFrontendEncoderFailure:
		return OutcomeCodeEncoderFailure
	case CommandPreBackendDenial:
		return OutcomeCodeDenied
	case CommandPanic:
		return OutcomeCodePanic
	case CommandEOF:
		return OutcomeCodeEOF
	case CommandSwallowedAttempt:
		return OutcomeCodeSwallowed
	case CommandBackendOpenFailure:
		return OutcomeCodeBackendOpen
	default:
		return OutcomeCodeFailed
	}
}
