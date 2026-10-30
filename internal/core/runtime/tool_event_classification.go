package runtime

import (
	"strings"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// toolEventClassification is the remembered derived metadata for one source ToolCallID.
type toolEventClassification struct {
	category  aipapi.ToolCategory
	mayMutate bool
}

// toolEventClassificationState correlates a tool call's derived classification by its
// source ToolCallID within one retryRecvStream. Event processing is owned by the
// single goroutine that drives Recv (retryRecvStream is not multi-Recv-safe), while
// terminal cleanup may run concurrently from Close; the mutex protects that cleanup
// boundary. The map is allocated lazily on first use.
type toolEventClassificationState struct {
	mu       sync.Mutex
	byCallID map[string]toolEventClassification
}

// enrich attaches classification to te before it reaches tool policies/reactors:
// a non-empty name is classified and remembered; a name-less fragment inherits the
// remembered classification for its ID; otherwise it receives the conservative
// unknown/true fallback.
func (st *toolEventClassificationState) enrich(te *aipapi.ToolEvent) {
	if te == nil {
		return
	}
	if strings.TrimSpace(te.ToolName) != "" {
		te.Category, te.MayMutateLocalFS = aipapi.ClassifyToolName(te.ToolName)
		st.remember(te.ToolCallID, te.Category, te.MayMutateLocalFS)
		return
	}
	if cls, ok := st.lookup(te.ToolCallID); ok {
		te.Category = cls.category
		te.MayMutateLocalFS = cls.mayMutate
		return
	}
	te.Category = aipapi.ToolCategoryUnknown
	te.MayMutateLocalFS = true
}

// rememberEffective records the post-reactor effective classification (already
// reconciled by the hook bus) under the source lifecycle ID so later name-less
// fragments inherit it.
func (st *toolEventClassificationState) rememberEffective(sourceID string, te aipapi.ToolEvent) {
	st.remember(sourceID, te.Category, te.MayMutateLocalFS)
}

// observeFinalName refreshes the source lifecycle from a final non-empty tool name
// observed after response-part hooks, so a later name-less fragment inherits the
// classification of the last emitted name.
func (st *toolEventClassificationState) observeFinalName(sourceID string, ev aipapi.Event) {
	te, ok := aipapi.ToolEventFromEvent(ev)
	if !ok || strings.TrimSpace(te.ToolName) == "" {
		return
	}
	st.remember(sourceID, te.Category, te.MayMutateLocalFS)
}

func (st *toolEventClassificationState) remember(id string, cat aipapi.ToolCategory, mut bool) {
	if st == nil || id == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.byCallID == nil {
		st.byCallID = make(map[string]toolEventClassification)
	}
	st.byCallID[id] = toolEventClassification{category: cat, mayMutate: mut}
}

func (st *toolEventClassificationState) lookup(id string) (toolEventClassification, bool) {
	if st == nil || id == "" {
		return toolEventClassification{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	cls, ok := st.byCallID[id]
	return cls, ok
}

// forget removes one source ID's remembered classification (e.g. after a finished
// lifecycle completes processing) so a later reuse of that ID cannot inherit stale state.
func (st *toolEventClassificationState) forget(id string) {
	if st == nil || id == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.byCallID, id)
}

// clear discards all remembered classification state (e.g. when the owning stream
// resets, is replaced, or terminates).
func (st *toolEventClassificationState) clear() {
	if st == nil {
		return
	}
	st.mu.Lock()
	st.byCallID = nil
	st.mu.Unlock()
}
