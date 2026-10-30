package metrics

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// promptIdentity returns a deterministic non-zero instruction identity for
// tests. The diagnostic only compares opaque values, so the payload is
// irrelevant to the claims below.
func promptIdentity(fill byte) [32]byte {
	var digest [32]byte
	for i := range digest {
		digest[i] = fill
	}
	return digest
}

// systemPromptSample builds one usage sample that carries the leg's
// content-free instruction identity. A zero identity means the leg published
// no observation.
func systemPromptSample(session, traceID, aLegID, leg string, seq int, warm bool, identity [32]byte) usage.Event {
	ev := collapseSample(session, traceID, aLegID, leg, seq, warm, "")
	ev.SystemPromptDigest = identity
	// The detector orders requests by process-owned launch ordinal. Tests use
	// the sample's sequence as that ordinal; explicit late-completion cases
	// override it to model an older request finishing after a newer one.
	ev.PromptCacheLaunchSeq = uint64(seq)
	return ev
}

// TestPromptCacheCollapse_SystemPromptIdentity_CrossTurnChange proves the
// authority this slice adds: when the provider-bound instruction prefix changes
// between turns, the diagnostic attributes the episode to system-prompt at the
// leg that first carried the new prefix, without a per-request publication.
func TestPromptCacheCollapse_SystemPromptIdentity_CrossTurnChange(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	before, after := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, before))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+1), fmt.Sprintf("a-leg-%d", i+1), fmt.Sprintf("leg-%d", i+1), i+2, false, after))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseSystemPrompt)+`"`) {
		t.Fatalf("cross-turn instruction change was not attributed in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_UnchangedStaysUnexplained proves
// the unchanged-identity negative: stable instructions are not a cause, so an
// unexplained collapse is never mislabelled system-prompt.
func TestPromptCacheCollapse_SystemPromptIdentity_UnchangedStaysUnexplained(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	stable := promptIdentity(0x11)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, stable))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+1), fmt.Sprintf("a-leg-%d", i+1), fmt.Sprintf("leg-%d", i+1), i+2, false, stable))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("stable instructions must stay unexplained in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_MissingObservationDoesNotFabricate
// proves a leg without an identity is not itself a change: a path that carries
// no canonical call must not make stable instructions look like a cross-turn
// system-prompt change.
func TestPromptCacheCollapse_SystemPromptIdentity_MissingObservationDoesNotFabricate(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	stable := promptIdentity(0x11)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, stable))
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-gap", "a-leg-gap", "leg-gap", 2, false, [32]byte{}))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+1), fmt.Sprintf("a-leg-%d", i+1), fmt.Sprintf("leg-%d", i+1), i+3, false, stable))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a missing observation must not fabricate a change in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_MissingObservationKeepsBaseline
// proves a leg without an identity neither fabricates nor resets a change: the
// comparison resumes against the last observed prefix, so a path that carries
// no canonical call cannot hide a real instruction change.
func TestPromptCacheCollapse_SystemPromptIdentity_MissingObservationKeepsBaseline(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	before, after := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, before))
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-gap", "a-leg-gap", "leg-gap", 2, false, [32]byte{}))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+1), fmt.Sprintf("a-leg-%d", i+1), fmt.Sprintf("leg-%d", i+1), i+3, false, after))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseSystemPrompt)+`"`) {
		t.Fatalf("a missing observation must not reset the comparison baseline in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_HealthySampleClearsNote proves
// the pinned clearing rule still holds for the identity-derived note: a healthy
// sample with the new prefix proves the change stale, so the later cold run is
// unexplained.
func TestPromptCacheCollapse_SystemPromptIdentity_HealthySampleClearsNote(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	before, after := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, before))
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, after))
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-2", "a-leg-2", "leg-2", 3, true, after))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+3), fmt.Sprintf("a-leg-%d", i+3), fmt.Sprintf("leg-%d", i+3), i+4, false, after))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a healthy sample must clear the identity-derived note in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_ExplicitPublicationWins proves
// the precedence between the two system-prompt sources on one leg: a cause the
// request explicitly published names a change the proxy made to that request,
// so it wins over the identity-derived note for the same sample.
func TestPromptCacheCollapse_SystemPromptIdentity_ExplicitPublicationWins(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	before, after := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, before))
	now = now.Add(time.Second)
	explicit := systemPromptSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, after)
	explicit.StructuralCause = string(CollapseCauseCompaction)
	observeCache(t, d, explicit)
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, after))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseCompaction)+`"`) {
		t.Fatalf("an explicit request publication must win over the identity-derived note in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_LateOlderLegDoesNotFabricate
// proves attribution is anchored to the healthy sample and the triggering leg,
// not to arrival order: a slow older request that completes after a newer
// healthy sample carries the old prefix and must not manufacture a
// system-prompt cause for an episode triggered by the new prefix.
func TestPromptCacheCollapse_SystemPromptIdentity_LateOlderLegDoesNotFabricate(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	oldIdentity, newIdentity := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-late", "a-leg-late", "leg-late", 1, true, newIdentity))
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-old", "a-leg-old", "leg-old", 0, false, oldIdentity))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i), fmt.Sprintf("a-leg-%d", i), fmt.Sprintf("leg-%d", i), i+2, false, newIdentity))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a late older leg must not fabricate an instruction change in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_LateOlderHealthyDoesNotFabricate
// proves a late older healthy leg cannot replace the healthy instruction
// baseline of a newer one: usage observations carry no request-start order, so
// two differing healthy identities in one era are ambiguous and the episode
// stays unexplained rather than attributing a change that did not cause it.
func TestPromptCacheCollapse_SystemPromptIdentity_LateOlderHealthyDoesNotFabricate(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	newIdentity, olderIdentity := promptIdentity(0x22), promptIdentity(0x11)
	// The newer request established the healthy baseline.
	observeCache(t, d, systemPromptSample("session", "trace-new", "a-leg-new", "leg-new", 2, true, newIdentity))
	// An older request completes late and is also healthy; its identity
	// differs from the established baseline.
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-old", "a-leg-old", "leg-old", 1, true, olderIdentity))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("leg-cold-%d", i), i+3, false, newIdentity))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a late older healthy leg must not fabricate an instruction change in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_RevertedChangeDoesNotFabricate
// proves a temporary change that reverted before the trigger is not attributed:
// the triggering leg carries the healthy instruction prefix, so the collapse is
// unexplained even though an intervening cold leg carried a different prefix.
func TestPromptCacheCollapse_SystemPromptIdentity_RevertedChangeDoesNotFabricate(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	base, temporary := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, base))
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-1", "a-leg-1", "leg-1", 2, false, temporary))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+2), fmt.Sprintf("a-leg-%d", i+2), fmt.Sprintf("leg-%d", i+2), i+3, false, base))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a reverted temporary change must not be attributed in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_MissingHealthyIdentity stays
// conservative when the healthy sample carried no identity: without a baseline
// the episode is unexplained rather than attributed from incomplete evidence.
func TestPromptCacheCollapse_SystemPromptIdentity_MissingHealthyIdentity(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	changed := promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, [32]byte{}))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+1), fmt.Sprintf("a-leg-%d", i+1), fmt.Sprintf("leg-%d", i+1), i+2, false, changed))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a missing healthy identity must stay unexplained in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_MissingTriggerIdentity stays
// conservative when the triggering leg carried no identity: the comparison is
// impossible, so the episode is unexplained rather than attributed.
func TestPromptCacheCollapse_SystemPromptIdentity_MissingTriggerIdentity(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	base, changed := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, base))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+1), fmt.Sprintf("a-leg-%d", i+1), fmt.Sprintf("leg-%d", i+1), i+2, false, changed))
	}
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-trigger", "a-leg-trigger", "leg-trigger", 6, false, [32]byte{}))
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a missing trigger identity must stay unexplained in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_BeatsConfigGeneration proves the
// pinned precedence is unchanged: a demonstrated instruction change still
// outranks a configuration generation boundary.
func TestPromptCacheCollapse_SystemPromptIdentity_BeatsConfigGeneration(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	before, after := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-0", "a-leg-0", "leg-0", 1, true, before))
	d.NoteConfigGeneration()
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-%d", i+1), fmt.Sprintf("a-leg-%d", i+1), fmt.Sprintf("leg-%d", i+1), i+2, false, after))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"cause":"`+string(CollapseCauseSystemPrompt)+`"`) {
		t.Fatalf("a published identity cause must outrank the generation boundary in %s", text)
	}
	if strings.Contains(text, `"cause":"`+string(CollapseCauseConfigGeneration)+`"`) {
		t.Fatalf("the generation boundary must not win over a published cause in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_LateOlderHealthyAfterColdDoesNotFabricate
// proves a cold observation between two healthy samples does not establish
// launch order. A newer healthy request, then cold samples, then an older
// request completing late as healthy, must not hand the baseline to the older
// identity: the following cold run belongs to the newer instructions, so
// attributing it to a system-prompt change would fabricate a cause.
func TestPromptCacheCollapse_SystemPromptIdentity_LateOlderHealthyAfterColdDoesNotFabricate(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	newIdentity, olderIdentity := promptIdentity(0x22), promptIdentity(0x11)
	// Arrival 1: the newer request established the healthy baseline.
	observeCache(t, d, systemPromptSample("session", "trace-new", "a-leg-new", "leg-new", 2, true, newIdentity))
	// Arrival 2: a cold sample of the same newer instructions.
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-cold-new", "a-leg-cold-new", "leg-cold-new", 3, false, newIdentity))
	// Arrival 3: an older request completes late and is healthy. Its identity
	// differs, and the intervening cold sample proves nothing about order.
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-old", "a-leg-old", "leg-old", 1, true, olderIdentity))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("leg-cold-%d", i), i+4, false, newIdentity))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a late older healthy leg after a cold sample must not fabricate an instruction change in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_LateOlderHealthyAfterTripDoesNotRestoreBaseline
// proves a trip does not serialize later completions: an older request can
// still complete healthy after the episode, and adopting its identity would
// fabricate a change for the next cold run. Once healthy identities conflict
// without launch chronology, the key keeps no comparison baseline until its
// state rotates or is evicted.
func TestPromptCacheCollapse_SystemPromptIdentity_LateOlderHealthyAfterTripDoesNotRestoreBaseline(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	newIdentity, olderIdentity := promptIdentity(0x22), promptIdentity(0x11)
	observeCache(t, d, systemPromptSample("session", "trace-new", "a-leg-new", "leg-new", 2, true, newIdentity))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-1-%d", i), fmt.Sprintf("a-leg-cold-1-%d", i), fmt.Sprintf("leg-cold-1-%d", i), i+3, false, newIdentity))
	}
	// The first episode trips unexplained (stable identity). An older request
	// now completes late and healthy.
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-old", "a-leg-old", "leg-old", 1, true, olderIdentity))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-2-%d", i), fmt.Sprintf("a-leg-cold-2-%d", i), fmt.Sprintf("leg-cold-2-%d", i), i+9, false, newIdentity))
	}
	if got := collapseCount(t, reg); got != 2 {
		t.Fatalf("trips=%v want 2", got)
	}
	if text := logs.String(); strings.Contains(text, `"cause":"`+string(CollapseCauseSystemPrompt)+`"`) {
		t.Fatalf("a late older healthy leg after a trip must not fabricate an instruction change in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_LateOlderColdTriggerDoesNotFabricate
// proves the triggering leg itself is ordered against the healthy reference. A
// request that started before the healthy reference but completes late as the
// fifth cold sample cannot establish a cross-turn instruction change.
func TestPromptCacheCollapse_SystemPromptIdentity_LateOlderColdTriggerDoesNotFabricate(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	newIdentity, olderIdentity := promptIdentity(0x22), promptIdentity(0x11)
	observeCache(t, d, systemPromptSample("session", "trace-new", "a-leg-new", "leg-new", 2, true, newIdentity))
	for i := range 4 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("leg-cold-%d", i), i+3, false, newIdentity))
	}
	// The older request completes last and is the fifth cold observation that
	// trips the episode.
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-old", "a-leg-old", "leg-old", 1, false, olderIdentity))
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("an older cold trigger must not fabricate an instruction change in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_MissingHealthyIdentityKeepsOrdering
// proves a healthy sample without an identity still advances the launch order.
// Otherwise a later completion from a request older than that sample could
// become the reference and fabricate a change for the next cold run.
func TestPromptCacheCollapse_SystemPromptIdentity_MissingHealthyIdentityKeepsOrdering(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	newIdentity, olderIdentity := promptIdentity(0x22), promptIdentity(0x11)
	// Healthy B establishes the reference at launch 2.
	observeCache(t, d, systemPromptSample("session", "trace-new", "a-leg-new", "leg-new", 2, true, newIdentity))
	// A newer healthy request (launch 4) carries no identity. It must still
	// advance the ordering so launch 3 cannot become the baseline.
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-missing", "a-leg-missing", "leg-missing", 4, true, [32]byte{}))
	// The delayed older healthy request (launch 3) arrives after launch 4.
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-old", "a-leg-old", "leg-old", 3, true, olderIdentity))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("leg-cold-%d", i), i+5, false, newIdentity))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("a missing healthy identity must keep launch ordering in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_UnsequencedHealthyDoesNotInheritOrdinal
// proves an unnumbered healthy observation can confirm a numbered reference but
// can never supply its identity. Otherwise the retained ordinal would describe a
// different observation and a later trigger could be attributed from a
// chronology that never existed.
func TestPromptCacheCollapse_SystemPromptIdentity_UnsequencedHealthyDoesNotInheritOrdinal(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	identityA, identityB := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-a", "a-leg-a", "leg-a", 10, true, identityA))
	now = now.Add(time.Second)
	// A newer healthy request carries no identity; it advances the watermark.
	observeCache(t, d, systemPromptSample("session", "trace-missing", "a-leg-missing", "leg-missing", 11, true, [32]byte{}))
	now = now.Add(time.Second)
	// An unsequenced healthy observation arrives with a different identity. It
	// must not become the numbered reference's identity.
	observeCache(t, d, systemPromptSample("session", "trace-unsequenced", "a-leg-unsequenced", "leg-unsequenced", 0, true, identityB))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("leg-cold-%d", i), i+12, false, identityA))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("an unsequenced healthy identity must not inherit a numbered ordinal in %s", text)
	}
}

// TestPromptCacheCollapse_SystemPromptIdentity_SameRequestAmbiguityStaysLatched
// proves a conflicting identity from one request's legs is not rearmed by a
// third leg of that same request. Only a strictly newer healthy request may
// establish a fresh reference.
func TestPromptCacheCollapse_SystemPromptIdentity_SameRequestAmbiguityStaysLatched(t *testing.T) {
	var logs bytes.Buffer
	now := time.Unix(1000, 0)
	d, reg := collapseCauseDetector(t, &logs, &now, PromptCacheCollapseSettings{})

	identityA, identityB := promptIdentity(0x11), promptIdentity(0x22)
	observeCache(t, d, systemPromptSample("session", "trace-1", "a-leg-1", "leg-1", 10, true, identityA))
	now = now.Add(time.Second)
	observeCache(t, d, systemPromptSample("session", "trace-2", "a-leg-2", "leg-2", 10, true, identityB))
	now = now.Add(time.Second)
	// The third leg repeats the first identity but shares the ambiguous ordinal.
	observeCache(t, d, systemPromptSample("session", "trace-3", "a-leg-3", "leg-3", 10, true, identityA))
	for i := range 5 {
		now = now.Add(time.Second)
		observeCache(t, d, systemPromptSample("session", fmt.Sprintf("trace-cold-%d", i), fmt.Sprintf("a-leg-cold-%d", i), fmt.Sprintf("leg-cold-%d", i), i+11, false, identityB))
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"cause":"`+string(CollapseCauseUnexplained)+`"`) {
		t.Fatalf("same-request ambiguity must stay latched in %s", text)
	}
}
