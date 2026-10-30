package metrics

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

func collapseCauseDetector(t *testing.T, logs *bytes.Buffer, now *time.Time, cfg PromptCacheCollapseSettings) (*PromptCacheCollapse, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	logger := slog.New(slog.DiscardHandler)
	if logs != nil {
		logger = slog.New(slog.NewJSONHandler(logs, nil))
	}
	return NewPromptCacheCollapseWithSettings(RegisterPromptCacheCollapseProm(reg), logger, func() time.Time { return *now }, cfg), reg
}

// collapseSample builds one usage sample. Every leg belongs to its own
// request, as a real request produces exactly one B-leg; the legs of one
// request share its trace identifier. The structural cause is the proxy-owned
// label the request carried, carried by the leg's own sample.
func collapseSample(session, traceID, aLegID, leg string, seq int, warm bool, cause string) usage.Event {
	ev := usage.Event{
		TraceID:     traceID,
		ALegID:      aLegID,
		SessionID:   session,
		BackendID:   "backend",
		Model:       "model",
		BLegID:      leg,
		AttemptSeq:  seq,
		InputTokens: 8000,

		StructuralCause: cause,
	}
	if warm {
		ev.CacheReadTokens = 8000
	}
	return ev
}

func TestPromptCacheCollapse_StructuralCause(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	// A healthy epoch on the key, then the request that carried the system-prompt change.
	observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
	now = now.Add(time.Second)
	observeCache(t, d, collapseSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, string(CollapseCauseSystemPrompt)))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, ""))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"cause":"`+string(CollapseCauseSystemPrompt)+`"`) {
		t.Fatalf("missing cause in %s", text)
	}
}

func TestPromptCacheCollapse_StructuralCause_UnknownLabelIgnored(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	for _, label := range []string{"", "not-a-cause", string(CollapseCauseUnexplained), string(CollapseCauseConfigGeneration)} {
		observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, label))
		for i := range 4 {
			now = now.Add(time.Second)
			observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, ""))
		}
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); strings.Contains(text, string(CollapseCauseSystemPrompt)) {
		t.Fatalf("unknown label published a cause in %s", text)
	}
}

// TestPromptCacheCollapse_StructuralCause_OtherRequestNotAttributed proves the
// cause is bound to the request that carried it: a leg whose own sample
// carries no cause never inherits another leg's cause.
func TestPromptCacheCollapse_StructuralCause_OtherRequestNotAttributed(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
	now = now.Add(time.Second)
	// A resumed turn that shares the A-leg with an earlier turn but carried
	// no structural change of its own.
	observeCache(t, d, collapseSample("session", "trace-1", "a-leg-0", "leg-1", 2, false, ""))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+2), "a-leg-0", fmt.Sprintf("leg-%d", i+2), i+3, false, ""))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); strings.Contains(text, string(CollapseCauseSystemPrompt)) {
		t.Fatalf("cause leaked to another request in %s", text)
	}
}

// TestPromptCacheCollapse_StructuralCause_ClearedByLaterHealthySample proves
// the healthy-epoch rule still holds: a healthy sample clears the cause.
func TestPromptCacheCollapse_StructuralCause_ClearedByLaterHealthySample(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
	now = now.Add(time.Second)
	observeCache(t, d, collapseSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, string(CollapseCauseSystemPrompt)))
	now = now.Add(time.Second)
	observeCache(t, d, collapseSample("session", "trace-2", "a-leg-2", "leg-2", 3, true, ""))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+3), fmt.Sprintf("a-leg-%d", i+3), fmt.Sprintf("leg-%d", i+3), i+4, false, ""))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); strings.Contains(text, string(CollapseCauseSystemPrompt)) {
		t.Fatalf("stale cause survived a healthy sample in %s", text)
	}
}

// TestPromptCacheCollapse_StructuralCause_DuplicateSampleDoesNotReviveCause
// proves the deduplication boundary is authoritative for state mutation: a
// repeated usage delta for a leg that already reported healthy usage must not
// revive the cause that the healthy sample cleared.
func TestPromptCacheCollapse_StructuralCause_DuplicateSampleDoesNotReviveCause(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
	now = now.Add(time.Second)
	// The eliding leg reports healthy usage: the cause is applied and then
	// cleared by the healthy branch.
	changed := collapseSample("session", "trace-1", "a-leg-1", "leg-1", 2, true, string(CollapseCauseSystemPrompt))
	observeCache(t, d, changed)
	// A second usage delta for the same leg, still carrying the cause.
	now = now.Add(time.Second)
	duplicate := changed
	observeCache(t, d, duplicate)
	// Five unrelated cold legs trip the episode; the cause must stay cleared.
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, ""))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); strings.Contains(text, string(CollapseCauseSystemPrompt)) {
		t.Fatalf("a duplicate usage delta revived a stale cause in %s", text)
	}
}

// TestPromptCacheCollapse_StructuralCause_EveryCandidateKey proves every
// candidate key of the request is attributed: the cause rides each candidate
// leg's own sample.
func TestPromptCacheCollapse_StructuralCause_EveryCandidateKey(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	// A healthy epoch on every candidate key.
	for i := range 64 {
		now = now.Add(time.Second)
		ev := collapseSample("session", fmt.Sprintf("trace-warm-%d", i), fmt.Sprintf("a-leg-warm-%d", i), "warm", 1, true, "")
		ev.Model = fmt.Sprintf("model-%d", i)
		observeCache(t, d, ev)
	}
	// The request fans out to all of them and each leg carries the cause.
	for i := range 64 {
		now = now.Add(time.Second)
		ev := collapseSample("session", "trace-1", "a-leg-1", fmt.Sprintf("leg-%d", i), i+2, false, string(CollapseCauseSystemPrompt))
		ev.Model = fmt.Sprintf("model-%d", i)
		observeCache(t, d, ev)
	}
	// The last candidate key keeps running cold.
	for i := range 4 {
		now = now.Add(time.Second)
		ev := collapseSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("cold-%d", i), 100+i, false, "")
		ev.Model = "model-63"
		observeCache(t, d, ev)
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseSystemPrompt)+`"`) {
		t.Fatalf("missing cause in %s", text)
	}
}

// TestPromptCacheCollapse_StructuralCause_PublishedCauseOutranksDerived pins
// the attribution precedence through the carried-cause path: a published cause
// wins over a configuration generation, which wins over the keep-warm idle
// horizon. The derived order is exercised by the generation/gap tests.
func TestPromptCacheCollapse_StructuralCause_PublishedCauseOutranksDerived(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
	// Both derived causes would win on their own: a reloaded generation and a
	// gap beyond the keep-warm idle horizon.
	d.NoteConfigGeneration()
	now = now.Add(61 * time.Minute)
	observeCache(t, d, collapseSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, string(CollapseCauseCompaction)))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, ""))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"compaction"`) {
		t.Fatalf("published cause did not win in %s", text)
	}
}

func TestPromptCacheCollapse_StructuralCauseVocabulary(t *testing.T) {
	// Every cause with an authority is publishable through both entry points,
	// so the vocabulary stays closed in one place.
	for _, cause := range []CollapseCause{
		CollapseCauseAffinitySwitch, CollapseCauseCredentialSwitch, CollapseCauseCompaction,
		CollapseCauseSystemPrompt,
	} {
		if !publishableCollapseCause(cause) {
			t.Fatalf("%q must be publishable", cause)
		}
	}
	// Derived causes are never published. Keep-warm expiry is derived from the
	// keep-warm idle horizon and must keep losing to a configuration generation
	// and to a published cause, so no authority may publish it explicitly.
	for _, cause := range []CollapseCause{
		CollapseCauseUnexplained, CollapseCauseKeepWarmExpiry, CollapseCauseConfigGeneration, CollapseCause("hostile"),
	} {
		if publishableCollapseCause(cause) {
			t.Fatalf("%q must not be publishable", cause)
		}
	}
}

// TestPromptCacheCollapse_StructuralCause_DerivedKeepWarmNotPublished proves a
// sample that carries the derived keep-warm label cannot publish it: the label
// is derived from the keep-warm idle horizon, and its win conditions are fixed
// below a configuration generation and every published cause.
func TestPromptCacheCollapse_StructuralCause_DerivedKeepWarmNotPublished(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
	now = now.Add(time.Second)
	observeCache(t, d, collapseSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, string(CollapseCauseKeepWarmExpiry)))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, ""))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"unexplained"`) {
		t.Fatalf("derived keep-warm label was published in %s", text)
	}
}

// TestPromptCacheCollapse_StructuralCause_RepeatedIdenticalContent proves that
// two distinct requests with identical content — the same trace identifier,
// distinct B-legs — each apply their own cause. The trace identifier is not a
// request identity: production derives it from the call content and callers
// may reuse it, so a second request that legitimately changed the system prompt
// must not lose its cause to the first request's publication.
func TestPromptCacheCollapse_StructuralCause_RepeatedIdenticalContent(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
	// The first request carries the system-prompt change; its leg reports healthy usage, so
	// the cause is applied and then cleared as stale.
	now = now.Add(time.Second)
	observeCache(t, d, collapseSample("session", "trace-shared", "a-leg-1", "leg-a", 2, true, string(CollapseCauseSystemPrompt)))
	// The second request has identical content, so it carries the same trace
	// identifier, but it is a different request with its own B-leg, and it
	// made its own system-prompt change.
	now = now.Add(time.Second)
	observeCache(t, d, collapseSample("session", "trace-shared", "a-leg-2", "leg-b", 3, false, string(CollapseCauseSystemPrompt)))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("leg-cold-%d", i), i+4, false, ""))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"cause":"`+string(CollapseCauseSystemPrompt)+`"`) {
		t.Fatalf("the second request's own cause was lost in %s", text)
	}
}

// TestPromptCacheCollapse_StructuralCause_SwitchLabels proves the diagnostic
// attributes an episode to the two switch labels once the runtime authority
// publishes them on a request. The switch identity itself never reaches the
// diagnostic: only the fixed label rides the leg's usage sample.
func TestPromptCacheCollapse_StructuralCause_SwitchLabels(t *testing.T) {
	for _, cause := range []CollapseCause{CollapseCauseAffinitySwitch, CollapseCauseCredentialSwitch} {
		t.Run(string(cause), func(t *testing.T) {
			var logs bytes.Buffer
			now := time.Unix(1000, 0)
			d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})
			observeCache(t, d, collapseSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, ""))
			now = now.Add(time.Second)
			observeCache(t, d, collapseSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, string(cause)))
			for i := range 4 {
				now = now.Add(time.Second)
				observeCache(t, d, collapseSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, ""))
			}
			if got := collapseCount(t, reg); got != 1 {
				t.Fatalf("trips=%v want 1", got)
			}
			if text := logs.String(); !strings.Contains(text, `"cause":"`+string(cause)+`"`) {
				t.Fatalf("missing %s cause in %s", cause, text)
			}
		})
	}
}
