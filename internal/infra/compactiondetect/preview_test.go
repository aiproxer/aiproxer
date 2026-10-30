package compactiondetect

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestPreviewRequestIsPureAndSharesCommittedAuthority(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	call := textCall("context checkpoint compaction", 0)
	meta := reqMeta("preview-request")

	preview := d.PreviewRequest(meta, call)
	if preview.Kind != PreviewStartCandidate || preview.RuleID != "codex.local_checkpoint.v1" {
		t.Fatalf("preview=%+v", preview)
	}
	if preview.TransactionID != "" || preview.BoundaryFingerprint != "" {
		t.Fatalf("uncommitted strict preview exposed an identity: %+v", preview)
	}
	d.mu.Lock()
	legCount := len(d.legs)
	d.mu.Unlock()
	if legCount != 0 {
		t.Fatalf("request preview mutated detector legs: %d", legCount)
	}
	if again := d.PreviewRequest(meta, call); again.Kind != preview.Kind || again.RuleID != preview.RuleID {
		t.Fatalf("repeated preview changed: first=%+v second=%+v", preview, again)
	}
	if events := d.RequestOpened(meta, call); len(events) != 1 || events[0].Phase != "started" {
		t.Fatalf("committed request events=%v", events)
	}
}

func TestPreviewResponseIsPureAndCommittedReleaseStillSeesEvent(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	meta := reqMeta("preview-response")
	call := textCall("context checkpoint compaction", 0)
	if events := d.RequestOpened(meta, call); len(events) != 1 {
		t.Fatalf("start events=%v", events)
	}
	release := aipapi.Event{Kind: aipapi.EventItem, Item: &aipapi.Item{
		Kind:    aipapi.ItemKindMessage,
		Role:    aipapi.RoleAssistant,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "context checkpoint compaction"}},
	}}
	preview := d.PreviewResponse(meta, release)
	if preview.Kind != PreviewCompletionCandidate || preview.RuleID != "codex.local_checkpoint.v1" {
		t.Fatalf("preview=%+v", preview)
	}
	d.mu.Lock()
	legCount := len(d.legs)
	active := d.legs[meta.ALegID]
	activeCompleted := active != nil && active.active != nil && active.active.completed
	lastSeen := time.Time{}
	releaseTrace := ""
	releaseText := ""
	if active != nil {
		lastSeen = active.lastSeen
		releaseTrace = active.releaseTextTrace
		releaseText = active.releaseText.String()
	}
	d.mu.Unlock()
	if legCount != 1 || active == nil || activeCompleted {
		t.Fatalf("response preview mutated detector state: legs=%d state=%+v", legCount, active)
	}
	d.mu.Lock()
	if got := d.legs[meta.ALegID]; got == nil || got.lastSeen != lastSeen || got.releaseTextTrace != releaseTrace || got.releaseText.String() != releaseText {
		d.mu.Unlock()
		t.Fatalf("response preview changed state snapshot: before=(%v,%q,%q) after=%+v", lastSeen, releaseTrace, releaseText, got)
	}
	d.mu.Unlock()
	if events := d.ResponseReleased(meta, release); len(events) != 1 || events[0].Phase != "completed" {
		t.Fatalf("committed response events=%v", events)
	}
	if events := d.ResponseReleased(meta, release); len(events) != 0 {
		t.Fatalf("duplicate committed response events=%v", events)
	}
}

func TestPreviewRequestHistoryBoundaryIsStableAndNonCommitting(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	d := New(Config{Now: func() time.Time { return now }})
	prior := itemCall(bigText(40000), bigText(8000), bigText(8000))
	current := itemCall(bigText(4000), bigText(8000), bigText(8000))
	if events := d.RequestOpened(reqMeta("history-prior"), prior); len(events) != 0 {
		t.Fatalf("unexpected prior events=%v", events)
	}
	meta := reqMeta("history-current")
	preview := d.PreviewRequest(meta, current)
	if preview.Kind != PreviewCompletionCandidate || preview.Evidence != "history_heuristic" || preview.BoundaryFingerprint == "" {
		t.Fatalf("preview=%+v", preview)
	}
	now = now.Add(time.Hour)
	if again := d.PreviewRequest(meta, current); again.BoundaryFingerprint != preview.BoundaryFingerprint {
		t.Fatalf("boundary fingerprint changed with clock only: first=%+v second=%+v", preview, again)
	}
	d.mu.Lock()
	active := d.legs[meta.ALegID]
	lastTrace := ""
	if active != nil {
		lastTrace = active.lastFP.TraceID
	}
	d.mu.Unlock()
	if active == nil || lastTrace != "history-prior" {
		t.Fatalf("preview changed last fingerprint: leg=%+v", active)
	}
	if events := d.RequestOpened(meta, current); len(events) != 1 || events[0].Phase != "completed" {
		t.Fatalf("committed history events=%v", events)
	}
}

func TestPreviewRequestNearMissDoesNotUseGenericCompactionWords(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	meta := reqMeta("preview-near-miss")
	for _, text := range []string{
		"please summarize the conversation",
		"compact this answer before continuing",
		"context checkpoint without compaction",
	} {
		preview := d.PreviewRequest(meta, textCall(text, 0))
		if preview.Kind != PreviewNone {
			t.Fatalf("text %q produced preview=%+v", text, preview)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.legs) != 0 {
		t.Fatalf("near-miss previews mutated detector state: %d legs", len(d.legs))
	}
}

func TestPreviewResponseCompletionOnlyIsPureBeforeAnyTransaction(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	meta := resMeta("preview-completion-only")
	ev := aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "<SYSTEM_NOTICE> Earlier context was compacted."}

	preview := d.PreviewResponse(meta, ev)
	if preview.Kind != PreviewCompletionCandidate || preview.RuleID != "cline.basic_compaction_post.v1" || preview.TransactionID == "" {
		t.Fatalf("preview=%+v", preview)
	}
	d.mu.Lock()
	if len(d.legs) != 0 {
		d.mu.Unlock()
		t.Fatalf("completion-only response preview created detector state: %d legs", len(d.legs))
	}
	d.mu.Unlock()
	if again := d.PreviewResponse(meta, ev); again != preview {
		t.Fatalf("repeated completion-only preview changed identity: first=%+v second=%+v", preview, again)
	}
	if events := d.ResponseReleased(meta, ev); len(events) != 1 || events[0].Phase != "completed" || events[0].TransactionID != preview.TransactionID {
		t.Fatalf("committed response=%v want preview transaction", events)
	}
}
