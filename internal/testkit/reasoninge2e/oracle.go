package reasoninge2e

import (
	"bytes"
	"fmt"
)

// Check compares a backend-bound request observation against the precomputed plan.
// Errors include seed, turn id, and mode, and describe structural mismatches only.
// Streaming on PlannedTurn / AssistantTurn is plan metadata only; this oracle does
// not validate stream vs non-stream wire shape on BackendTurnObservation.
func Check(plan Plan, obs BackendRequestObservation) error {
	want := plan.Turns()
	if len(obs.AssistantTurns) != len(want) {
		return fmt.Errorf(
			"reasoninge2e oracle: seed=%d policy=%s structural mismatch: turn_count got=%d want=%d",
			plan.Seed, plan.Policy.String(), len(obs.AssistantTurns), len(want),
		)
	}
	for i := range want {
		if err := checkTurn(plan.Seed, want[i], obs.AssistantTurns[i]); err != nil {
			return err
		}
	}
	return nil
}

func checkTurn(seed uint64, want PlannedTurn, got BackendTurnObservation) error {
	prefix := fmt.Sprintf("reasoninge2e oracle: seed=%d turn=%s mode=%s", seed, want.ID, want.Mode)
	if got.TurnID != want.ID {
		return fmt.Errorf("%s structural mismatch: turn_id", prefix)
	}
	if got.VisibleText != want.ExpectedBackend.VisibleText {
		return fmt.Errorf("%s structural mismatch: visible_text", prefix)
	}
	if err := checkTool(prefix, want.ExpectedBackend.Tool, got.Tool); err != nil {
		return err
	}
	return checkReasoning(prefix, want.Mode, want.ExpectedBackend.Reasoning, got.Reasoning)
}

func checkTool(prefix string, want, got *ToolExchange) error {
	switch {
	case want == nil && got == nil:
		return nil
	case want == nil || got == nil:
		return fmt.Errorf("%s structural mismatch: tool_presence", prefix)
	case want.ID != got.ID:
		return fmt.Errorf("%s structural mismatch: tool_id", prefix)
	case want.Name != got.Name:
		return fmt.Errorf("%s structural mismatch: tool_name", prefix)
	case want.Arguments != got.Arguments:
		return fmt.Errorf("%s structural mismatch: tool_arguments", prefix)
	case want.Result != got.Result:
		return fmt.Errorf("%s structural mismatch: tool_result", prefix)
	default:
		return nil
	}
}

func checkReasoning(prefix string, mode RetentionMode, want, got []ReasoningBlock) error {
	switch mode {
	case ModeNone:
		if len(got) != 0 {
			return fmt.Errorf("%s structural mismatch: unexpected_reasoning_insertion", prefix)
		}
		return nil
	case ModePreserved:
		if len(got) != len(want) {
			if len(got) > len(want) {
				return fmt.Errorf("%s structural mismatch: reasoning_duplication count_got=%d count_want=%d", prefix, len(got), len(want))
			}
			return fmt.Errorf("%s structural mismatch: reasoning_count got=%d want=%d", prefix, len(got), len(want))
		}
	case ModeDropped:
		if len(got) != len(want) {
			if len(got) > len(want) {
				return fmt.Errorf("%s structural mismatch: reasoning_duplication count_got=%d count_want=%d", prefix, len(got), len(want))
			}
			return fmt.Errorf("%s structural mismatch: restoration_incomplete count_got=%d count_want=%d", prefix, len(got), len(want))
		}
	case ModeConflict:
		if len(got) != len(want) {
			return fmt.Errorf("%s structural mismatch: conflict_rewrite count_got=%d count_want=%d", prefix, len(got), len(want))
		}
	default:
		return fmt.Errorf("%s structural mismatch: unknown_mode", prefix)
	}
	for i := range want {
		if !blocksEqualStructural(want[i], got[i]) {
			switch mode {
			case ModeDropped:
				return fmt.Errorf("%s structural mismatch: restoration_content block=%d", prefix, i)
			case ModeConflict:
				return fmt.Errorf("%s structural mismatch: conflict_untouched block=%d", prefix, i)
			case ModePreserved:
				return fmt.Errorf("%s structural mismatch: preserved_content block=%d", prefix, i)
			default:
				return fmt.Errorf("%s structural mismatch: reasoning_block block=%d", prefix, i)
			}
		}
	}
	return nil
}

func blocksEqualStructural(a, b ReasoningBlock) bool {
	if a.Dialect != b.Dialect || a.Text != b.Text || a.Signature != b.Signature {
		return false
	}
	return bytes.Equal(a.Opaque, b.Opaque)
}
