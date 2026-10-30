package hooks_test

import (
	"context"
	"testing"

	corehooks "github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func TestApplyToolReactors_renameReclassifiesForNextReactor(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read"}
	renamer := &stubTool{
		id: "rename", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "bash"}, nil
		},
	}
	var observed aipapi.ToolEvent
	observer := &stubTool{
		id: "observe", order: 2,
		fn: func(_ context.Context, cur aipapi.ToolEvent, _ sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			observed = cur
			return sdk.ToolPass, aipapi.ToolEvent{}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{renamer, observer}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if observed.Category != aipapi.ToolCategoryOSCommand || !observed.MayMutateLocalFS {
		t.Fatalf("renamed event seen by next reactor = (%q,%v), want (os_command,true)", observed.Category, observed.MayMutateLocalFS)
	}
	if out.Event.Category != aipapi.ToolCategoryOSCommand || !out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want (os_command,true)", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_sameIDNamelessRewriteOfUnclassifiedCurIsUnknown(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1"}
	rewriter := &stubTool{
		id: "rw", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "y"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{rewriter}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != aipapi.ToolCategoryUnknown || !out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want (unknown,true) for unclassified same-ID cur", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_sameIDNamelessRewritePreservesClassification(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", Category: aipapi.ToolCategoryFileRead, MayMutateLocalFS: false}
	rewriter := &stubTool{
		id: "rw", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{Kind: aipapi.ToolEventArgsDelta, ToolCallID: "c1", ArgsDelta: "y"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{rewriter}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != aipapi.ToolCategoryFileRead || out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want preserved (file_read,false)", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_changedIDNamelessReplacementIsUnknown(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read", Category: aipapi.ToolCategoryFileRead, MayMutateLocalFS: false}
	replacer := &stubTool{
		id: "rep", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolReplace, aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c2"}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{replacer}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.ToolCallID != "c2" {
		t.Fatalf("ToolCallID=%q, want c2", out.Event.ToolCallID)
	}
	if out.Event.Category != aipapi.ToolCategoryUnknown || !out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want (unknown,true)", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_whitespaceOnlyNamePreservesClassification(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{
		Kind:             aipapi.ToolEventArgsDelta,
		ToolCallID:       "c1",
		ToolName:         "read",
		Category:         aipapi.ToolCategoryFileRead,
		MayMutateLocalFS: false,
	}
	rewriter := &stubTool{
		id: "rw", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{
				Kind:       aipapi.ToolEventArgsDelta,
				ToolCallID: "c1",
				ToolName:   "   ",
				ArgsDelta:  "y",
			}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{rewriter}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != aipapi.ToolCategoryFileRead || out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want preserved (file_read,false) for whitespace-only name", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}

func TestApplyToolReactors_reactorCannotOverrideDerivedFromName(t *testing.T) {
	t.Parallel()
	te := aipapi.ToolEvent{Kind: aipapi.ToolEventStarted, ToolCallID: "c1", ToolName: "read"}
	liar := &stubTool{
		id: "liar", order: 1,
		fn: func(context.Context, aipapi.ToolEvent, sdk.ToolMeta) (sdk.ToolDecision, aipapi.ToolEvent, error) {
			return sdk.ToolRewrite, aipapi.ToolEvent{
				Kind:             aipapi.ToolEventStarted,
				ToolCallID:       "c1",
				ToolName:         "bash",
				Category:         aipapi.ToolCategoryFileRead,
				MayMutateLocalFS: false,
			}, nil
		},
	}
	b := corehooks.New(corehooks.Config{ToolReactors: []sdk.ToolReactor{liar}})
	out := b.ApplyToolReactors(context.Background(), te, sdk.ToolMeta{})
	if !out.Emit || out.Err != nil {
		t.Fatalf("emit=%v err=%v", out.Emit, out.Err)
	}
	if out.Event.Category != aipapi.ToolCategoryOSCommand || !out.Event.MayMutateLocalFS {
		t.Fatalf("out = (%q,%v), want (os_command,true) derived from name", out.Event.Category, out.Event.MayMutateLocalFS)
	}
}
