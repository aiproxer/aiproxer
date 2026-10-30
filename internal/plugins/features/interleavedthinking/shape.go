package interleavedthinking

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/internal/plugins/features/interleavedthinking/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// ErrThinkerInstructionsMissing is returned when a thinker candidate is shaped
// without resolved thinker instructions.
var ErrThinkerInstructionsMissing = errors.New("interleavedthinking: thinker instructions required but missing")

// SessionSteeringGuidanceHeader is the plain-text header that introduces the
// steering block. It is used when persisting the thinker memo as a
// conversation-view steering overlay.
const SessionSteeringGuidanceHeader = "[Session Steering Guidance]"

// MemoInjectionModeConversationView identifies the memo injection mode in diagnostics.
const MemoInjectionModeConversationView = "conversation_view_overlay"

// MemoOutcome classifies executor memo shaping for bounded diagnostics.
type MemoOutcome string

const (
	MemoOutcomeNone           MemoOutcome = ""
	MemoOutcomeInjected       MemoOutcome = "injected"
	MemoOutcomeExpired        MemoOutcome = "expired"
	MemoOutcomeSkippedVisible MemoOutcome = "skipped_visible"
	MemoOutcomeSkippedMissing MemoOutcome = "skipped_missing"
	MemoOutcomeSkippedEmpty   MemoOutcome = "skipped_empty"
)

// PendingMemoUpdate is the memo-store mutation that should be committed once a
// shaped executor attempt becomes authoritative.
type PendingMemoUpdate struct {
	Ref   state.MemoRef
	State state.MemoState
}

// ShapeThinkerCall shapes a thinker candidate call by prepending thinker
// instructions to Instructions, clearing Tools and ToolChoice, and validating.
func ShapeThinkerCall(call aipapi.Call, instructions string) (aipapi.Call, error) {
	out := aipapi.CloneCall(call)
	inst := strings.TrimSpace(instructions)
	if inst == "" {
		return aipapi.Call{}, ErrThinkerInstructionsMissing
	}
	prepend := aipapi.Message{
		Role:  aipapi.RoleSystem,
		Parts: []aipapi.Part{aipapi.TextPart(inst)},
	}
	out.Instructions = append([]aipapi.Message{prepend}, out.Instructions...)
	out.Tools = nil
	out.ToolChoice = aipapi.ToolChoice{}
	if err := out.Validate(); err != nil {
		return aipapi.Call{}, fmt.Errorf("interleavedthinking: shaped thinker call invalid: %w", err)
	}
	return out, nil
}

// ShapeExecutorCall classifies the latest valid memo for an executor candidate,
// applies budget decrements, and validates the call.
func ShapeExecutorCall(
	ctx context.Context,
	call aipapi.Call,
	store MemoStore,
	scope Scope,
	ref *state.MemoRef,
	suppressVisibleMemo bool,
) (aipapi.Call, bool, *PendingMemoUpdate, MemoOutcome, error) {
	out := aipapi.CloneCall(call)
	if store == nil || ref == nil || ref.Key == "" {
		return out, false, nil, MemoOutcomeSkippedMissing, nil
	}
	if err := ctx.Err(); err != nil {
		return aipapi.Call{}, false, nil, MemoOutcomeNone, err
	}
	if scope == "" {
		return aipapi.Call{}, false, nil, MemoOutcomeNone, ErrEmptyScope
	}
	st, ok, err := store.Get(ctx, scope, *ref)
	if err != nil {
		return aipapi.Call{}, false, nil, MemoOutcomeNone, fmt.Errorf("interleavedthinking: memo lookup: %w", err)
	}
	if !ok {
		return out, false, nil, MemoOutcomeSkippedMissing, nil
	}
	memo := strings.TrimSpace(st.Memo)
	if memo == "" {
		return out, false, nil, MemoOutcomeSkippedEmpty, nil
	}
	if st.RegularTurnsRemaining <= 0 {
		return out, false, nil, MemoOutcomeExpired, nil
	}
	if st.VisibleToClient && suppressVisibleMemo {
		return out, false, nil, MemoOutcomeSkippedVisible, nil
	}
	st.InjectedCount++
	st.RegularTurnsRemaining--
	if st.RegularTurnsRemaining < 0 {
		st.RegularTurnsRemaining = 0
	}
	pending := &PendingMemoUpdate{Ref: *ref, State: st}
	if err := out.Validate(); err != nil {
		return aipapi.Call{}, false, nil, MemoOutcomeNone, fmt.Errorf("interleavedthinking: shaped executor call invalid: %w", err)
	}
	return out, true, pending, MemoOutcomeInjected, nil
}
