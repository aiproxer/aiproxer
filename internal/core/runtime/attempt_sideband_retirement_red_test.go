package runtime

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestAttemptSlotSwapRetainsDetachedAttemptOwnership(t *testing.T) {
	oldCandidate := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "old-backend", Model: "old-model"},
		Key:     "old-backend:old-model",
	}
	newCandidate := routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "new-backend", Model: "new-model"},
		Key:     "new-backend:new-model",
	}
	oldAccounting := newAttemptAccountingTracker(time.Unix(101, 0))
	oldAccounting.observeUsage(aipapi.Event{Kind: aipapi.EventUsageDelta, OutputTokens: 7})
	oldToolFinal := &toolCallAssembler{
		active:      map[string]*toolCallBuffer{"old-active": {id: "old-active"}},
		passThrough: map[string]struct{}{"old-pass": {}},
		completed:   map[string]struct{}{"old-complete": {}},
		drain:       []aipapi.Event{{Kind: aipapi.EventToolCallFinished, ToolCallID: "old-drain"}},
	}
	old := newAttemptSession(attemptSessionInput{
		cand:       oldCandidate,
		accounting: oldAccounting,
		toolFinal:  oldToolFinal,
	})
	old.internalUsageKeys = map[string]struct{}{"old-sideband": {}}
	oldTerminal := old.terminal
	oldAccountingSnapshot := old.accounting.snapshot()

	var slot attemptSlot
	testInstallSlot(&slot, old)
	retained := slot.snapshot()
	newAttempt := newAttemptSession(attemptSessionInput{cand: newCandidate})
	ready := &readyAttempt{session: newAttempt, state: readyStatePrepared}
	if got, published := slot.swapIfOpen(ready); !published || got != retained {
		t.Fatalf("swap returned %p, want retained old attempt %p", got, retained)
	}

	if got := slot.snapshot(); got != newAttempt {
		t.Fatalf("slot exposes %p, want replacement %p", got, newAttempt)
	}
	if !reflect.DeepEqual(retained.cand, oldCandidate) {
		t.Fatalf("retained candidate = %+v, want %+v", retained.cand, oldCandidate)
	}
	if retained.terminal != oldTerminal || retained.terminal.Owner() == nil {
		t.Fatal("retained attempt no longer owns its original terminal")
	}
	if !reflect.DeepEqual(retained.accounting.snapshot(), oldAccountingSnapshot) {
		t.Fatalf("retained accounting changed: got %+v want %+v", retained.accounting.snapshot(), oldAccountingSnapshot)
	}
	if _, ok := retained.internalUsageKeys["old-sideband"]; !ok {
		t.Fatal("retained attempt lost its usage dedupe key")
	}
	if retained.toolFinal != oldToolFinal || len(retained.toolFinal.active) != 1 || len(retained.toolFinal.passThrough) != 1 || len(retained.toolFinal.completed) != 1 || len(retained.toolFinal.drain) != 1 {
		t.Fatal("retained attempt no longer owns unchanged tool-finalizer state")
	}
}

func TestAttemptSidebandUsageRetainsSourceAttemptOwnershipAcrossSwap(t *testing.T) {
	const dedupeKey = "retired-old-sideband"
	oldSource := &usageSidebandStream{evidence: []aipapi.Event{{
		Kind:          aipapi.EventUsageDelta,
		OutputTokens:  11,
		Accounting:    aipapi.UsageAccountingMetadata{DedupeKey: dedupeKey},
		UsagePresence: aipapi.UsagePresence{OutputTokens: true},
	}}}
	old := newAttemptSession(attemptSessionInput{
		inner:      oldSource,
		cand:       routing.AttemptCandidate{Primary: routing.Primary{Backend: "old-backend", Model: "old-model"}, Key: "old-backend:old-model"},
		accounting: newAttemptAccountingTracker(time.Unix(202, 0)),
	})
	newAttempt := newAttemptSession(attemptSessionInput{
		cand:       routing.AttemptCandidate{Primary: routing.Primary{Backend: "new-backend", Model: "new-model"}, Key: "new-backend:new-model"},
		accounting: newAttemptAccountingTracker(time.Unix(303, 0)),
	})
	var slot attemptSlot
	testInstallSlot(&slot, old)
	retained := slot.snapshot()
	oldInner := retained.loadInner()
	if oldInner != oldSource {
		t.Fatalf("retained source = %T, want old source", oldInner)
	}
	ready := &readyAttempt{session: newAttempt, state: readyStatePrepared}
	if got, published := slot.swapIfOpen(ready); !published || got != retained {
		t.Fatalf("swap returned %p, want retained old attempt %p", got, retained)
	}

	// Keep consuming the retained old source after the slot publishes its replacement.
	// The helper must bind evidence to the source's attempt, not re-snapshot the slot.
	rs := &retryRecvStream{responsePipeline: newResponsePipeline()}
	testInstallSlot(&rs.attempt, newAttempt)
	rs.responsePipeline.consumeBackendUsageEvidenceForAttempt(context.Background(), rs.facts, retained, oldInner)

	if !retained.accounting.usageObserved {
		t.Fatal("retained old attempt did not record sideband usage")
	}
	if _, ok := retained.internalUsageKeys[dedupeKey]; !ok {
		t.Fatalf("retained old attempt missing dedupe key %q", dedupeKey)
	}
	if newAttempt.accounting.usageObserved {
		t.Fatal("replacement attempt incorrectly recorded retained old sideband usage")
	}
	if _, ok := newAttempt.internalUsageKeys[dedupeKey]; ok {
		t.Fatalf("replacement attempt incorrectly retained dedupe key %q", dedupeKey)
	}
}
