package runtime

import (
	"context"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// cleanEmptyTurnTerminal reports whether a response_finished event carries a
// clean terminal taxonomy: no error, no truncation, and no refusal.
//
// ResponseStatus is authoritative when set ("completed" or "incomplete"). An
// empty status leaves the classification to the finish reason, and an unknown
// or refusal/truncation lexeme is not clean: bounded allowlist, never a
// "anything but the known bad values" rule. The canonical dialect leaves a
// clean stop's FinishReason empty (adapters map "stop", "end_turn",
// "tool_use", and empty vendor reasons to the empty canonical value), so an
// empty reason is clean here; the legacy fail-closed inference used by
// completion recorders is deliberately not applied to this diagnostic.
func cleanEmptyTurnTerminal(finish aipapi.Event) bool {
	if finish.ResponseStatus != "" {
		return finish.ResponseStatus == "completed"
	}
	switch strings.ToLower(strings.TrimSpace(finish.FinishReason)) {
	case "", "stop", "end_turn", "stop_sequence", "completed":
		return true
	default:
		return false
	}
}

// producedVisibleOutput reports whether an attempt produced client-visible
// output so far, whether or not it was ever released: text, decision results,
// assistant media refs, or tool-call lifecycle. Reasoning-only output is
// deliberately not visible for this signal.
func producedVisibleOutput(attempt *attemptSession) bool {
	if attempt == nil {
		return false
	}
	return attempt.producedVisible || len(attempt.toolOutputCallIDs) > 0
}

// emptyVisibleTurn reports whether a surviving attempt's accepted clean finish
// is the degenerate empty visible turn operators need a signal for: a clean
// terminal taxonomy and no produced visible output. Reasoning-only turns count
// as empty; truncated or refusal terminals never count. It is read-only and
// changes no terminal, routing, or billing behavior.
func emptyVisibleTurn(attempt *attemptSession, finish aipapi.Event) bool {
	if attempt == nil {
		return false
	}
	return !producedVisibleOutput(attempt) && cleanEmptyTurnTerminal(finish)
}

// observeEmptyVisibleTurn records the degenerate-turn signal for one clean
// terminal winner: a bounded per-backend counter increment through the
// injected metrics seam and one content-safe decision log carrying only
// lineage and the operator-configured backend id. It never mutates the turn.
func (t *turnTerminal) observeEmptyVisibleTurn(ctx context.Context, request requestTerminalFacts, attempt *attemptSession, finish aipapi.Event) {
	if t == nil || attempt == nil {
		return
	}
	if !emptyVisibleTurn(attempt, finish) {
		return
	}
	backend := strings.TrimSpace(attempt.cand.Primary.Backend)
	if t.recordEmptyVisibleTurn != nil {
		t.recordEmptyVisibleTurn(backend)
	}
	diag.LogDecision(ctx, t.log, "empty_visible_turn", diag.AttrOpts{CallID: request.traceID, BLegID: attempt.bleg.BLegID},
		slog.String("backend", backend),
	)
}
