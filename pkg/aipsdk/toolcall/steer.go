package toolcall

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// ErrInvalidSteer is the parent of every [SteerRequest.Validate] failure. A
// request that does not validate makes the whole result unusable, so callers
// classify with errors.Is instead of parsing messages.
var ErrInvalidSteer = errors.New("toolcall: invalid steer request")

// ActionSteer asks core to withhold this completed call and continue the turn
// once, hidden, with Steer.Instruction. Core may decline; the call then passes.
const ActionSteer Action = ActionReject + 1

const (
	// MaxSteerInstructionBytes bounds one steer instruction in bytes.
	MaxSteerInstructionBytes = 4096

	// MinSteerTurnBudget and MaxSteerTurnBudget bound the corrections allowed
	// per client turn.
	MinSteerTurnBudget = 1
	MaxSteerTurnBudget = 3

	// ReasonUnknownToolName classifies a steer request caused by a completed
	// call naming a tool the model was never offered.
	ReasonUnknownToolName = "unknown_tool_name"
)

// SteerRequest is the additive [ActionSteer] payload: the hidden instruction a
// finalizer asks core to send, plus the bounds core honors when it decides
// whether the call may be corrected.
type SteerRequest struct {
	// Instruction is the hidden correction message. It is required, must be
	// valid UTF-8, and must not exceed [MaxSteerInstructionBytes].
	Instruction string

	// TurnBudget is the number of corrections allowed per client turn, in
	// [MinSteerTurnBudget, MaxSteerTurnBudget].
	TurnBudget uint8

	// AllowAfterReasoning permits the correction although this attempt already
	// produced reasoning output.
	AllowAfterReasoning bool
}

// Validate reports whether the request is complete and within its bounds.
//
// Every failure wraps [ErrInvalidSteer]. The zero request is invalid, so an
// unusable request can never steer silently: [Result.UsableSteer] reports it
// unusable and core takes its pre-existing unusable-result fallback.
func (r SteerRequest) Validate() error {
	if r.Instruction == "" {
		return fmt.Errorf("%w: instruction is empty", ErrInvalidSteer)
	}
	if len(r.Instruction) > MaxSteerInstructionBytes {
		return fmt.Errorf("%w: instruction is %d bytes, over the %d-byte limit",
			ErrInvalidSteer, len(r.Instruction), MaxSteerInstructionBytes)
	}
	if !utf8.ValidString(r.Instruction) {
		return fmt.Errorf("%w: instruction is not valid UTF-8", ErrInvalidSteer)
	}
	if r.TurnBudget < MinSteerTurnBudget || r.TurnBudget > MaxSteerTurnBudget {
		return fmt.Errorf("%w: turn budget %d outside [%d, %d]",
			ErrInvalidSteer, r.TurnBudget, MinSteerTurnBudget, MaxSteerTurnBudget)
	}
	return nil
}

// UsableSteer returns the steer request core may act on, and whether this
// result carries one.
//
// A steer is usable only when the action is [ActionSteer], the request
// validates, and the result carries neither a rewritten tool name nor
// rewritten arguments: the outcome asks core to withhold the call and continue
// the turn, so it can never also release a rewritten call.
//
// This method reports only whether a usable steer is present; it is not the
// whole usability rule for a result. A non-nil [Result.Steer] on any action
// other than [ActionSteer] makes the whole result unusable, and core takes its
// pre-existing unusable-result fallback - exactly as for an [ActionSteer] whose
// request does not validate. Callers must therefore treat
// r.Action != ActionSteer && r.Steer != nil as unusable in addition to the
// false this method returns.
func (r Result) UsableSteer() (*SteerRequest, bool) {
	if r.Action != ActionSteer || r.Steer == nil || r.ToolName != "" || r.ArgsJSON != nil {
		return nil, false
	}
	if err := r.Steer.Validate(); err != nil {
		return nil, false
	}
	return r.Steer, true
}
