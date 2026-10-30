package runtime

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/metering/plane"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// customerEvidenceAccumulator records client-visible canonical content released
// after response hooks and completion-gate resolution, immediately before the
// frontend encoder (design D1/D8). It never observes provider UsageDelta scopes
// or provider money. Released text is the StreamUsage / CountOutput OutputText
// source; reasoning and tool-arg deltas are retained as non-usage events for the
// same counting seam (CountOutput may ignore them — preserve that behavior).
type customerEvidenceAccumulator struct {
	mu sync.Mutex
	// content retains every released content delta in order. The concatenated
	// text, reasoning, and tool-arg views are derived from it on demand: an
	// append-only builder that grows one streamed delta at a time re-copies the
	// whole accumulated payload at every capacity step, so a multi-megabyte
	// response allocates several times its own size before settlement.
	content   []releasedContent
	text      strings.Builder
	reasoning strings.Builder
	toolArgs  strings.Builder
	// joined reports whether those builders match content; textLen is the number
	// of observed text bytes. ObserveReleased and resetContent invalidate both.
	joined  bool
	textLen int
	events  int
	settled atomic.Bool
	// boundary is the byte length of text already attributed to prior
	// attempts of the logical response when the latest continuation was
	// published. Progress detection observes only the text released since
	// this boundary, so identical answers on successive attempts compare
	// equal instead of looking like fresh evidence. Delivery, accounting,
	// and history keep using the cumulative text.
	boundary int
}

func newCustomerEvidenceAccumulator() *customerEvidenceAccumulator {
	return &customerEvidenceAccumulator{}
}

// ObserveReleased records one post-hook/post-gate event about to be released to
// the client. Non-content kinds (usage, keepalive, warning, finish) are ignored.
func (a *customerEvidenceAccumulator) ObserveReleased(ev aipapi.Event) {
	if a == nil {
		return
	}
	switch ev.Kind {
	case aipapi.EventTextDelta, aipapi.EventReasoningDelta, aipapi.EventToolCallArgsDelta:
	default:
		return
	}
	a.mu.Lock()
	a.content = append(a.content, releasedContent{kind: ev.Kind, delta: ev.Delta})
	if ev.Kind == aipapi.EventTextDelta {
		a.textLen += len(ev.Delta)
	}
	a.joined = false
	a.events++
	a.mu.Unlock()
}

// joinLocked rebuilds the concatenated views from the retained deltas once per
// batch of observations. Each builder is grown to its exact final size first, so
// the whole payload is copied once per kind instead of once per capacity step.
func (a *customerEvidenceAccumulator) joinLocked() {
	if a.joined {
		return
	}
	var textBytes, reasoningBytes, toolArgsBytes int
	for _, c := range a.content {
		switch c.kind {
		case aipapi.EventTextDelta:
			textBytes += len(c.delta)
		case aipapi.EventReasoningDelta:
			reasoningBytes += len(c.delta)
		case aipapi.EventToolCallArgsDelta:
			toolArgsBytes += len(c.delta)
		}
	}
	a.text.Reset()
	a.reasoning.Reset()
	a.toolArgs.Reset()
	a.text.Grow(textBytes)
	a.reasoning.Grow(reasoningBytes)
	a.toolArgs.Grow(toolArgsBytes)
	for _, c := range a.content {
		switch c.kind {
		case aipapi.EventTextDelta:
			a.text.WriteString(c.delta)
		case aipapi.EventReasoningDelta:
			a.reasoning.WriteString(c.delta)
		case aipapi.EventToolCallArgsDelta:
			a.toolArgs.WriteString(c.delta)
		}
	}
	a.joined = true
}

// releasedContent is the compact retention of one released content event. Only
// the kind and the delta are observable through [customerEvidenceAccumulator.contentEvents],
// while [aipapi.Event] is over 600 bytes, so retaining the canonical struct for
// every streamed delta costs an order of magnitude more than the evidence it
// carries on the per-event release path.
type releasedContent struct {
	kind  aipapi.EventKind
	delta string
}

// Snapshot returns released text and content-event count for settlement.
func (a *customerEvidenceAccumulator) Snapshot() (text, reasoning, toolArgs string, events int) {
	if a == nil {
		return "", "", "", 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.joinLocked()
	return a.text.String(), a.reasoning.String(), a.toolArgs.String(), a.events
}

// resetContent clears released buffers on B-leg replacement (mirrors visibleText.Reset).
func (a *customerEvidenceAccumulator) resetContent() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.text.Reset()
	a.reasoning.Reset()
	a.toolArgs.Reset()
	a.content = nil
	a.joined = false
	a.textLen = 0
	a.events = 0
	a.boundary = 0
	a.mu.Unlock()
}

// markAttemptBoundary snapshots the current text length as the start of the
// latest continuation's attempt. The newly published attempt has released
// nothing yet, so everything accumulated so far belongs to prior attempts.
// Every continuation re-snapshots. A nil accumulator is a no-op.
func (a *customerEvidenceAccumulator) markAttemptBoundary() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.boundary = a.textLen
	a.mu.Unlock()
}

// attemptText reports the text released since the latest attempt boundary, for
// progress detection only. The buffer is append-only between a boundary
// snapshot and the next reset, so the boundary is always a prefix of the
// current text; each appended delta is a complete string, so the cut is always
// on a character boundary. A boundary that is not a usable prefix falls back
// to the cumulative text.
func (a *customerEvidenceAccumulator) attemptText() string {
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.joinLocked()
	full := a.text.String()
	if a.boundary > 0 && a.boundary <= len(full) {
		return full[a.boundary:]
	}
	return full
}

// contentEvents returns a copy of released content events for StreamUsage.Reconstruct.
// The canonical events are rebuilt from the compact retention: only kind and delta
// were ever observable through this seam.
func (a *customerEvidenceAccumulator) contentEvents() []aipapi.Event {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.content) == 0 {
		return nil
	}
	out := make([]aipapi.Event, len(a.content))
	for i, c := range a.content {
		out[i] = aipapi.Event{Kind: c.kind, Delta: c.delta}
	}
	return out
}

// MarkSettled records that customer authority settlement consumed this snapshot.
// Returns false when settlement already ran (once-only).
func (a *customerEvidenceAccumulator) MarkSettled() bool {
	if a == nil {
		return true
	}
	return a.settled.CompareAndSwap(false, true)
}

func (a *customerEvidenceAccumulator) unmarkSettled() {
	if a == nil {
		return
	}
	a.settled.Store(false)
}

// customerPlaneUsageEvent projects usage evidence safe for customer FE egress /
// settlement: provider_billable scopes and all provider money are removed.
func customerPlaneUsageEvent(ev aipapi.Event) aipapi.Event {
	return plane.CustomerPlaneUsageEvent(ev)
}
