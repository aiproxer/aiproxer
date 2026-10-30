package terminal

import "fmt"

// Command identifies a competing terminalization path (requirements 7.1, 13.4).
type Command string

const (
	CommandNormalFinish           Command = "normal_finish"
	CommandPartialError           Command = "partial_error"
	CommandCancel                 Command = "cancel"
	CommandClose                  Command = "close"
	CommandTimeout                Command = "timeout"
	CommandGateReplacement        Command = "gate_replacement"
	CommandParallelLoser          Command = "parallel_loser"
	CommandFrontendEncoderFailure Command = "frontend_encoder_failure"
	CommandPreBackendDenial       Command = "pre_backend_denial"
	CommandPanic                  Command = "panic"
	CommandEOF                    Command = "eof"
	CommandSwallowedAttempt       Command = "swallowed_attempt"
	CommandBackendOpenFailure     Command = "backend_open_failure"
)

// AllCommands returns every documented terminal command in stable order.
func AllCommands() []Command {
	return []Command{
		CommandNormalFinish,
		CommandPartialError,
		CommandCancel,
		CommandClose,
		CommandTimeout,
		CommandGateReplacement,
		CommandParallelLoser,
		CommandFrontendEncoderFailure,
		CommandPreBackendDenial,
		CommandPanic,
		CommandEOF,
		CommandSwallowedAttempt,
		CommandBackendOpenFailure,
	}
}

// IsKnown reports whether c is a documented terminal command.
func (c Command) IsKnown() bool {
	switch c {
	case CommandNormalFinish, CommandPartialError, CommandCancel, CommandClose,
		CommandTimeout, CommandGateReplacement, CommandParallelLoser,
		CommandFrontendEncoderFailure, CommandPreBackendDenial, CommandPanic,
		CommandEOF, CommandSwallowedAttempt, CommandBackendOpenFailure:
		return true
	}
	return false
}

// Validate returns an error when c is not a known terminal command.
func (c Command) Validate() error {
	if !c.IsKnown() {
		return fmt.Errorf("%w: unknown command %q", ErrInvalid, c)
	}
	return nil
}

// IsRetryOrReplacement reports whether c would retry or replace an in-flight
// stream after visible output may already be committed (design D13).
func (c Command) IsRetryOrReplacement() bool {
	return c == CommandGateReplacement
}
