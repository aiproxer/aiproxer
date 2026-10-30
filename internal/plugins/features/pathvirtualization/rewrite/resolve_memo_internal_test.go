package rewrite

// Spec: b-leg-path-virtualization Task 12.1, requirements.md 9.3 ("audit/rewrite
// processing shall be bounded in CPU and memory by canonical payload limits and
// feature-specific selector/profile limits") and 9.4.
//
// The per-pass resolution memo removes a per-occurrence amplification, and it introduces
// the only memory this walk ever holds that grows with its input: one entry per distinct
// tool name the walk resolves. That number has to be bounded, because a call's tool names
// are model-emitted and therefore attacker-chosen, and because the legacy message authority
// can carry far more parts than the item authority's occurrence cap.
//
// Two facts are pinned here rather than argued in a comment:
//
//	THE MEMO STOPS AT ITS BOUND. A walk over more distinct names than the bound holds
//	    resolves every name, and the surplus resolutions are simply not cached. Dropping
//	    the cache changes no answer - it only costs the repeated work the memo exists to
//	    avoid - so the bound is a memory guarantee rather than a correctness one.
//
//	THE SHARED REWRITER CARRIES NO MEMO. One Rewriter serves every request of a
//	    generation, so a memo held on it would be a cross-request store of derived policy
//	    answers. Reflection reads the struct rather than trusting a comment, so a field
//	    added later fails this test instead of quietly becoming one.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// countingInference is a stub inference step that records every consultation and returns
// no selector. The bound being pinned is about how many DISTINCT names a walk caches, so
// what the step answers is irrelevant - and returning nothing keeps the walk cheap enough
// to build a fixture at the bound's scale.
type countingInference struct {
	calls int
}

// InferArgumentSelectors implements pathvirtualization.ArgumentInference.
func (c *countingInference) InferArgumentSelectors([]byte) pathvirtualization.SelectorSet {
	c.calls++
	return nil
}

// memoBoundPartsPerMessage is how many tool-call surfaces one fixture message carries. It
// is under the canonical per-message part cap, and more than one message is therefore
// needed to reach the memo's bound.
//
// The LEGACY message authority is the right fixture here rather than the item authority,
// and the reason is the point being pinned: the item authority's own occurrence cap IS the
// memo's bound, so a call built on it can never exceed it, while the legacy authority's
// surface count is a product of two envelope limits and is strictly larger.
const memoBoundPartsPerMessage = 1366

// memoBoundFixture builds one canonical call carrying surfaces tool-call surfaces whose
// names are all distinct and none of which the call declares, so every resolution reaches
// the inference step with no declared schema.
func memoBoundFixture(t *testing.T, surfaces int) *aipapi.Call {
	t.Helper()
	var messages []aipapi.Message
	index := 0
	for index < surfaces {
		count := min(surfaces-index, memoBoundPartsPerMessage)
		parts := make([]aipapi.Part, 0, count)
		for range count {
			parts = append(parts, aipapi.Part{
				Kind:       aipapi.PartJSON,
				ToolCallID: fmt.Sprintf("call_%05d", index),
				ToolName:   fmt.Sprintf("synthetic_unclaimed_%05d", index),
				Content:    json.RawMessage(`{"file_path":"plain content"}`),
			})
			index++
		}
		messages = append(messages, aipapi.Message{Role: aipapi.RoleAssistant, Parts: parts})
	}
	call := &aipapi.Call{Messages: messages}
	if err := call.Validate(); err != nil {
		t.Fatalf("the bound fixture must be canonical: %v", err)
	}
	return call
}

// memoBoundRoot is a synthetic deep POSIX root whose derived alias is strictly shorter,
// so the mapping is ACTIVE (requirement 1.4) and the walk reaches every surface rather
// than recording one mapping-inactive skip.
const memoBoundRoot = "/synthetic/build-agent/workspaces/aiproxer-example-project/monorepo" +
	"/services/interactive-proxy/.worktrees/b-leg-path-virtualization-memo-bound-fixture"

// memoBoundMapping derives the fixture mapping and fails unless it is active.
func memoBoundMapping(t *testing.T) pathvirtualization.Mapping {
	t.Helper()
	mapping, reason := pathvirtualization.DeriveMapping(memoBoundRoot)
	if reason != pathvirtualization.SkipReasonNone || mapping.VirtualRoot == "" {
		t.Fatalf("the fixture root must derive an active alias: reason %v", reason)
	}
	return mapping
}

// TestTheResolutionMemoNeverExceedsItsBound pins the memory guarantee.
func TestTheResolutionMemoNeverExceedsItsBound(t *testing.T) {
	t.Parallel()
	// Two names past the bound, the second of them repeated, so the surplus resolution is
	// observable: a memo that kept growing would answer the repeat from its own cache.
	const surplus = 2

	step := &countingInference{}
	resolver, reject := pathvirtualization.NewResolver(nil, nil, step)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new resolver: reject %v", reject)
	}
	rewriter := New(memoBoundMapping(t), resolver)

	walk := &callWalker{rewriter: rewriter, in: memoBoundFixture(t, maxMemoizedResolutions+surplus)}
	walk.run()
	if walk.err != nil {
		t.Fatalf("the walk failed: %v", walk.err)
	}

	if got := len(walk.resolved); got > maxMemoizedResolutions {
		t.Fatalf("the memo holds %d entries, over its bound of %d", got, maxMemoizedResolutions)
	}
	if got, want := step.calls, maxMemoizedResolutions+surplus; got != want {
		t.Fatalf("the inference step was consulted %d times over %d distinct names, want %d: "+
			"every name must still be resolved, and only the cached ones may be reused",
			got, maxMemoizedResolutions+surplus, want)
	}
}

// TestASmallCallNeverReachesTheBound is the other direction: the memo is not a fixed-size
// allocation on every walk. A call with one tool name must cache exactly that one name, so
// the hot path pays for what it uses rather than for the bound.
func TestASmallCallNeverReachesTheBound(t *testing.T) {
	t.Parallel()
	step := &countingInference{}
	resolver, reject := pathvirtualization.NewResolver(nil, nil, step)
	if reject != pathvirtualization.SelectorRejectNone {
		t.Fatalf("new resolver: reject %v", reject)
	}
	rewriter := New(memoBoundMapping(t), resolver)

	walk := &callWalker{rewriter: rewriter, in: memoBoundFixture(t, 1)}
	walk.run()
	if walk.err != nil {
		t.Fatalf("the walk failed: %v", walk.err)
	}
	if got := len(walk.resolved); got != 1 {
		t.Fatalf("the memo holds %d entries for one distinct tool name, want 1", got)
	}
}

// TestTheSharedRewriterCarriesNoResolutionMemo keeps the memo off the generation-shared
// value.
//
// Reflection reads the struct rather than trusting a comment: a map field added to
// Rewriter later - by this package or by another - fails here.
func TestTheSharedRewriterCarriesNoResolutionMemo(t *testing.T) {
	t.Parallel()
	rewriter := reflect.TypeFor[Rewriter]()
	for field := range rewriter.Fields() {
		if field.Type.Kind() == reflect.Map {
			t.Errorf("Rewriter.%s is a %s; the memo must live on one walk, not on the rewriter "+
				"every request of a generation shares", field.Name, field.Type.Kind())
		}
	}
}
