package compactioncontinuity

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// The compaction-continuity feature is the authority for the `compaction`
// structural cause: the continuity projection it appends to a request is a
// change to the cached prefix, so the bounded label is recorded on the request
// exactly when the projection is committed. The prompt-cache collapse
// diagnostic can then attribute the collapse the projection caused.

func TestBeforeRequest_recordsCompactionCauseWithAppliedProjection(t *testing.T) {
	t.Parallel()
	plugin, _, background := openFixture(t)
	call := openCall()
	err := plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "preview-boundary-cause",
	}, openMeta(), compaction.Services{BackgroundAux: background})
	if err != nil {
		t.Fatalf("BeforeRequest: %v", err)
	}
	if call.StructuralCause != CauseCompaction {
		t.Fatalf("structural cause = %q, want %q", call.StructuralCause, CauseCompaction)
	}
	if len(call.StructuralCause) > aipapi.MaxStructuralCauseBytes {
		t.Fatalf("cause exceeds the bounded label size: %q", call.StructuralCause)
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("recorded cause invalidates the call: %v", err)
	}
}

// TestBeforeRequest_committedProjectionKeepsTheFirstCause pins the first-label
// rule: the earliest change to the cached prefix keeps the label, so a later
// committed projection never replaces it.
func TestBeforeRequest_committedProjectionKeepsTheFirstCause(t *testing.T) {
	t.Parallel()
	plugin, _, background := openFixture(t)
	call := openCall()
	call.StructuralCause = "system-prompt"
	err := plugin.BeforeRequest(context.Background(), &call, compaction.RequestPreview{
		Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "preview-boundary-first-wins",
	}, openMeta(), compaction.Services{BackgroundAux: background})
	if err != nil {
		t.Fatalf("BeforeRequest: %v", err)
	}
	if call.StructuralCause != "system-prompt" {
		t.Fatalf("compaction replaced an earlier cause: %q", call.StructuralCause)
	}
}

// TestBeforeRequest_noOpReapplicationPublishesNothing proves a re-application
// of the same call-local lifecycle publishes no cause: it applies no
// projection, so it must record nothing. Clearing the label the first
// application recorded makes the no-op visible.
func TestBeforeRequest_noOpReapplicationPublishesNothing(t *testing.T) {
	t.Parallel()
	plugin, _, background := openFixture(t)
	call := openCall()
	preview := compaction.RequestPreview{Kind: compaction.PreviewCompletionCandidate, BoundaryFingerprint: "preview-boundary-noop"}
	meta := openMeta()
	if err := plugin.BeforeRequest(context.Background(), &call, preview, meta, compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("BeforeRequest: %v", err)
	}
	if call.StructuralCause != CauseCompaction {
		t.Fatalf("first application recorded %q, want %q", call.StructuralCause, CauseCompaction)
	}
	call.StructuralCause = ""
	if err := plugin.BeforeRequest(context.Background(), &call, preview, meta, compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatalf("BeforeRequest retry: %v", err)
	}
	if call.StructuralCause != "" {
		t.Fatalf("no-op pass published a cause: %q", call.StructuralCause)
	}
}
