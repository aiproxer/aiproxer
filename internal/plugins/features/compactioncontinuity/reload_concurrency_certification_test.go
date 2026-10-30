package compactioncontinuity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

func TestReloadConcurrencyCertification_NewGenerationOnlyUsesNewExtractorPolicy(t *testing.T) {
	t.Parallel()
	oldConfig := openConfig(t)
	oldParent := &openParentFake{branch: ParentBranch{
		Binding: "sha256:" + strings.Repeat("b", 64), TraceID: "old-trace", ALegID: "old-a", BLegID: "old-b",
	}}
	oldBackground := &openBackgroundFake{}
	oldPlugin, err := New(oldConfig, oldParent)
	if err != nil {
		t.Fatal(err)
	}

	// Mutating the source value after construction simulates a published
	// candidate being replaced. The old plugin must keep its immutable snapshot.
	oldConfig.Extractor.Route = "mutated-after-publication"
	oldConfig.Extractor.Timeout = 3 * time.Second
	if err := oldPlugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: oldBackground}); err != nil {
		t.Fatal(err)
	}
	if len(oldBackground.submits) != 1 {
		t.Fatalf("old-generation submissions=%d want 1", len(oldBackground.submits))
	}
	if got := oldBackground.submits[0].Call.Route.Selector; got != "extractor/model" {
		t.Fatalf("old job route=%q changed after reload source mutation", got)
	}
	if got := oldBackground.options[0].Timeout; got != 137*time.Millisecond {
		t.Fatalf("old job timeout=%v changed after reload source mutation", got)
	}

	newConfig := oldPlugin.cfg
	newConfig.Extractor.Route = "reloaded/extractor"
	newConfig.Extractor.Timeout = 211 * time.Millisecond
	newParent := &openParentFake{branch: ParentBranch{
		Binding: "sha256:" + strings.Repeat("c", 64), TraceID: "new-trace", ALegID: "new-a", BLegID: "new-b",
	}}
	newBackground := &openBackgroundFake{}
	newPlugin, err := New(newConfig, newParent)
	if err != nil {
		t.Fatal(err)
	}
	if err := newPlugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: newBackground}); err != nil {
		t.Fatal(err)
	}
	if len(newBackground.submits) != 1 {
		t.Fatalf("new-generation submissions=%d want 1", len(newBackground.submits))
	}
	if got := newBackground.submits[0].Call.Route.Selector; got != "reloaded/extractor" {
		t.Fatalf("new job route=%q want reloaded/extractor", got)
	}
	if got := newBackground.options[0].Timeout; got != 211*time.Millisecond {
		t.Fatalf("new job timeout=%v want 211ms", got)
	}
}

func TestReloadConcurrencyCertification_DisabledGenerationSubmitsNoNewJob(t *testing.T) {
	t.Parallel()
	base := openConfig(t)
	base.Extractor.Enabled = false
	parent := &openParentFake{branch: ParentBranch{
		Binding: "sha256:" + strings.Repeat("d", 64), TraceID: "disabled-trace", ALegID: "disabled-a", BLegID: "disabled-b",
	}}
	background := &openBackgroundFake{}
	plugin, err := New(base, parent)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugin.RequestOpened(context.Background(), openCall(), openEvent(), openMeta(), compaction.Services{BackgroundAux: background}); err != nil {
		t.Fatal(err)
	}
	if len(background.submits) != 0 {
		t.Fatalf("disabled generation submitted %d jobs", len(background.submits))
	}
	if len(parent.order) != 0 {
		t.Fatalf("disabled generation touched continuity state: %v", parent.order)
	}
}
