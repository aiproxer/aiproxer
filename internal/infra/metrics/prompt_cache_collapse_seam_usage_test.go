package metrics

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// driveSeamCollapseViaUsage feeds a warm leg followed by five cold legs with
// the digest vectors riding the legs' own usage samples (the production
// publisher path for issue #1367), instead of the direct ObserveSeamDigests
// entry point. healthy is the warm leg's vector; a nil healthy leaves the
// healthy reference unknown, so any collapse trips unattributed.
func driveSeamCollapseViaUsage(t *testing.T, d *PromptCacheCollapse, session string, healthy [][32]byte, digests map[string][][32]byte) {
	t.Helper()
	warm := seamColdLeg(session, "healthy")
	warm.CacheReadTokens = 8000
	warm.SeamDigests = healthy
	if err := d.OnUsage(t.Context(), warm); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		leg := fmt.Sprint("cold-", i)
		ev := seamColdLeg(session, leg)
		if vec, ok := digests[leg]; ok {
			ev.SeamDigests = vec
		}
		if err := d.OnUsage(t.Context(), ev); err != nil {
			t.Fatal(err)
		}
	}
}

// TestPromptCacheCollapse_SeamViaUsageAttribution proves the production path:
// digests riding the leg's own usage sample report the first divergent item
// index where the cold era differs from the last healthy baseline, with no
// content in the diagnostic.
func TestPromptCacheCollapse_SeamViaUsageAttribution(t *testing.T) {
	base := []string{"sys", "history-1", "history-2", "user"}
	for _, tc := range []struct {
		name string
		last []string
		want int
	}{
		{"mid-sequence edit", []string{"sys", "history-1", "EDITED", "user"}, 2},
		{"appended message", []string{"sys", "history-1", "history-2", "user", "followup"}, 4},
		{"identical prompts", []string{"sys", "history-1", "history-2", "user"}, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			d, reg := newSeamDetector(&logs, nil)
			feeds := map[string][][32]byte{}
			for i := range 4 {
				feeds[fmt.Sprint("cold-", i)] = seamVector(base...)
			}
			feeds["cold-4"] = seamVector(tc.last...)
			driveSeamCollapseViaUsage(t, d, "seam-session", seamVector(base...), feeds)
			if got := collapseCount(t, reg); got != 1 {
				t.Fatalf("trips=%v want 1", got)
			}
			text := logs.String()
			if tc.want < 0 {
				if strings.Contains(text, "seam_item_index") {
					t.Fatalf("unexpected seam attribution in %s", text)
				}
				return
			}
			if !strings.Contains(text, fmt.Sprintf(`"seam_item_index":%d`, tc.want)) {
				t.Fatalf("missing seam_item_index %d in %s", tc.want, text)
			}
		})
	}
}

// TestPromptCacheCollapse_SeamViaUsagePersistentDivergence proves a
// persistent prompt change stays visible: five identical modified cold legs
// still report where the cold era first differs from the healthy baseline,
// instead of comparing two identical cold legs to each other.
func TestPromptCacheCollapse_SeamViaUsagePersistentDivergence(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	mutated := seamVector("sys", "ELIDED-tool-output", "user")
	feeds := map[string][][32]byte{}
	for i := range 5 {
		feeds[fmt.Sprint("cold-", i)] = mutated
	}
	driveSeamCollapseViaUsage(t, d, "seam-session", seamVector("sys", "history-1", "user"), feeds)
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("missing persistent-divergence seam 1 in %s", text)
	}
}

// TestPromptCacheCollapse_SeamViaUsageMissingFeedGapUnknown proves missing
// digest evidence invalidates the comparison: cold legs without digests
// (for example the wire/large-body path) advance the episode but cannot
// attribute it, so a collapse whose triggering leg fed no digests reports
// unknown instead of a stale index from unrelated earlier legs.
func TestPromptCacheCollapse_SeamViaUsageMissingFeedGapUnknown(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	driveSeamCollapseViaUsage(t, d, "seam-session", seamVector("a", "b"), map[string][][32]byte{
		"cold-0": seamVector("a", "b"),
		"cold-1": seamVector("a", "MUTATED"),
		// cold-2..cold-4 feed nothing: the triggering leg carries no vector.
	})
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); strings.Contains(text, "seam_item_index") {
		t.Fatalf("stale index from a digest gap in %s", text)
	}
}

// TestPromptCacheCollapse_SeamDirectOnlyHealthyFeedPromotes proves a
// healthy leg whose digests arrived only through the direct feed still arms
// the reference: the accepted healthy usage sample (carrying no digests of
// its own) promotes the direct-fed vector instead of invalidating it.
func TestPromptCacheCollapse_SeamDirectOnlyHealthyFeedPromotes(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	ctx := t.Context()
	healthy := seamVector("sys", "history", "user")
	d.ObserveSeamDigests("seam-session", "backend", "model", "healthy", healthy)
	warm := seamColdLeg("seam-session", "healthy")
	warm.CacheReadTokens = 8000
	if err := d.OnUsage(ctx, warm); err != nil {
		t.Fatal(err)
	}
	mutated := seamVector("sys", "EDITED", "user")
	for i := range 5 {
		ev := seamColdLeg("seam-session", fmt.Sprint("cold-", i))
		ev.SeamDigests = mutated
		if err := d.OnUsage(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); !strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("direct-only healthy feed lost the reference in %s", text)
	}
}

// TestPromptCacheCollapse_SeamViaUsageHealthyInvalidatedWithoutDigests
// proves a later healthy leg without digests from either source clears the
// reference: the last known healthy prompt may no longer describe the
// baseline, so a later collapse reports unknown rather than compare against
// a stale vector.
func TestPromptCacheCollapse_SeamViaUsageHealthyInvalidatedWithoutDigests(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	ctx := t.Context()
	warm := seamColdLeg("seam-session", "healthy")
	warm.CacheReadTokens = 8000
	warm.SeamDigests = seamVector("a", "b")
	if err := d.OnUsage(ctx, warm); err != nil {
		t.Fatal(err)
	}
	// A second healthy epoch without digests (for example a wire-path leg)
	// invalidates the earlier reference.
	second := seamColdLeg("seam-session", "healthy-2")
	second.CacheReadTokens = 8000
	if err := d.OnUsage(ctx, second); err != nil {
		t.Fatal(err)
	}
	feeds := map[string][][32]byte{}
	for i := range 5 {
		ev := seamColdLeg("seam-session", fmt.Sprint("cold-", i))
		ev.SeamDigests = seamVector("a", "MUTATED")
		feeds[ev.BLegID] = ev.SeamDigests
		if err := d.OnUsage(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); strings.Contains(text, "seam_item_index") {
		t.Fatalf("stale healthy reference survived invalidation in %s", text)
	}
}

// TestPromptCacheCollapse_SeamViaUsageDuplicateIgnored proves the
// deduplication boundary is authoritative for state mutation: a repeated
// usage delta for an already-recorded leg changes no digest state.
func TestPromptCacheCollapse_SeamViaUsageDuplicateIgnored(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	ctx := t.Context()
	warm := seamColdLeg("seam-session", "healthy")
	warm.CacheReadTokens = 8000
	warm.SeamDigests = seamVector("a", "b", "c")
	if err := d.OnUsage(ctx, warm); err != nil {
		t.Fatal(err)
	}
	base := seamVector("a", "b", "c")
	for i := range 4 {
		leg := fmt.Sprint("cold-", i)
		ev := seamColdLeg("seam-session", leg)
		ev.SeamDigests = base
		if err := d.OnUsage(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	// First delivery for cold-4 wins; the late duplicate must not shift the pair.
	first := seamColdLeg("seam-session", "cold-4")
	first.SeamDigests = seamVector("X", "b", "c")
	if err := d.OnUsage(ctx, first); err != nil {
		t.Fatal(err)
	}
	dup := seamColdLeg("seam-session", "cold-4")
	dup.SeamDigests = seamVector("X", "Y", "c")
	if err := d.OnUsage(ctx, dup); err != nil {
		t.Fatal(err)
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"seam_item_index":0`) {
		t.Fatalf("missing seam_item_index 0 in %s", text)
	}
	if strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("duplicate delivery shifted the seam in %s", text)
	}
}

// TestPromptCacheCollapse_SeamViaUsageFirstWinsAcrossPaths proves the direct
// feed and the usage-riding feed share one first-wins rule: whichever path
// records a B-leg first owns it, regardless of feed/usage ordering.
func TestPromptCacheCollapse_SeamViaUsageFirstWinsAcrossPaths(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	ctx := t.Context()
	warm := seamColdLeg("seam-session", "healthy")
	warm.CacheReadTokens = 8000
	warm.SeamDigests = seamVector("a", "b", "c")
	if err := d.OnUsage(ctx, warm); err != nil {
		t.Fatal(err)
	}
	base := seamVector("a", "b", "c")
	for i := range 4 {
		leg := fmt.Sprint("cold-", i)
		d.ObserveSeamDigests("seam-session", "backend", "model", leg, base)
		if err := d.OnUsage(ctx, seamColdLeg("seam-session", leg)); err != nil {
			t.Fatal(err)
		}
	}
	// Direct feed records cold-4 first; the usage sample's different vector
	// for the same leg must not shift the pair.
	d.ObserveSeamDigests("seam-session", "backend", "model", "cold-4", seamVector("X", "b", "c"))
	ev := seamColdLeg("seam-session", "cold-4")
	ev.SeamDigests = seamVector("X", "Y", "c")
	if err := d.OnUsage(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"seam_item_index":0`) {
		t.Fatalf("missing seam_item_index 0 in %s", text)
	}
	if strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("second path shifted the seam in %s", text)
	}
}

// TestPromptCacheCollapse_SeamViaUsageTruncationUnknown proves a divergence
// hidden beyond the retention bound stays unknown on the usage path, exactly
// like the direct feed: bounded retention never fabricates a seam.
func TestPromptCacheCollapse_SeamViaUsageTruncationUnknown(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	mkLong := func(mutated bool) [][32]byte {
		n := collapseMaxItems + 32
		out := make([][32]byte, n)
		for i := range out {
			out[i] = seamDigest(fmt.Sprint("long-", i))
		}
		if mutated {
			out[collapseMaxItems+1] = seamDigest("long-mutated-beyond-bound")
		}
		return out
	}
	feeds := map[string][][32]byte{}
	for i := range 4 {
		feeds[fmt.Sprint("cold-", i)] = mkLong(false)
	}
	feeds["cold-4"] = mkLong(true)
	driveSeamCollapseViaUsage(t, d, "seam-session", mkLong(false), feeds)
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if strings.Contains(logs.String(), "seam_item_index") {
		t.Fatalf("bounded truncation fabricated a seam in %s", logs.String())
	}
}

// TestPromptCacheCollapse_SeamViaUsagePrivateWarning proves the usage-riding
// feed stays content-free: the episode log carries the seam index but never
// digests, session IDs, or B-leg IDs.
func TestPromptCacheCollapse_SeamViaUsagePrivateWarning(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	prev := seamVector("PRIVATE_A_0", "PRIVATE_A_1")
	last := seamVector("PRIVATE_A_0", "PRIVATE_B_1")
	feeds := map[string][][32]byte{}
	for i := range 4 {
		feeds[fmt.Sprint("cold-", i)] = prev
	}
	feeds["cold-4"] = last
	driveSeamCollapseViaUsage(t, d, "PRIVATE_SESSION", prev, feeds)
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("missing seam attribution in %s", text)
	}
	for _, vec := range [][][32]byte{prev, last} {
		for _, dg := range vec {
			if strings.Contains(text, string(dg[:])) {
				t.Fatalf("warning leaks digest %x", dg)
			}
		}
	}
	for _, secret := range []string{"PRIVATE_SESSION", "PRIVATE_A", "PRIVATE_B", "cold-"} {
		if strings.Contains(text, secret) {
			t.Fatalf("warning leaks %s", secret)
		}
	}
}

// TestPromptCacheCollapse_SeamViaUsageEmptyLegKeepsPriorPair proves legs
// without digests (feed disabled) leave the retained vectors unchanged: a key
// that never feeds digests trips exactly as before, without attribution.
func TestPromptCacheCollapse_SeamViaUsageEmptyLegKeepsPriorPair(t *testing.T) {
	var logs bytes.Buffer
	d, _ := newSeamDetector(&logs, nil)
	driveSeamCollapseViaUsage(t, d, "seam-session", nil, nil)
	if strings.Contains(logs.String(), "seam_item_index") {
		t.Fatalf("empty vectors fabricated a seam in %s", logs.String())
	}
}

// mutatingUsageObserver corrupts the digest backing array of every event it
// sees. Chained ahead of the collapse detector, it proves the fan-out
// boundary isolates the detector's input.
type mutatingUsageObserver struct{}

func (mutatingUsageObserver) OnUsage(_ context.Context, ev usage.Event) error {
	for i := range ev.SeamDigests {
		ev.SeamDigests[i] = seamDigest("MUTATED-by-observer")
	}
	return nil
}

// TestPromptCacheCollapse_SeamMutatingObserverIsolated proves an earlier
// usage observer cannot corrupt the detector: with a mutator chained ahead,
// the episode still reports the true first divergent index.
func TestPromptCacheCollapse_SeamMutatingObserverIsolated(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	chain := usage.ChainObservers(mutatingUsageObserver{}, d)
	base := seamVector("sys", "history", "user")
	mutated := seamVector("sys", "EDITED", "user")
	warm := seamColdLeg("seam-session", "healthy")
	warm.CacheReadTokens = 8000
	warm.SeamDigests = base
	ctx := t.Context()
	if err := chain.OnUsage(ctx, warm); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		leg := fmt.Sprint("cold-", i)
		ev := seamColdLeg("seam-session", leg)
		ev.SeamDigests = base
		if err := chain.OnUsage(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	ev := seamColdLeg("seam-session", "cold-4")
	ev.SeamDigests = mutated
	if err := chain.OnUsage(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("mutator shifted the attribution in %s", text)
	}
}

// TestPromptCacheCollapse_SeamDirectBeforeHealthyUsagePromotes proves the
// direct feed and the usage feed cooperate on a healthy leg: when the direct
// path already owns the leg's vector, the accepted healthy usage sample
// promotes that existing vector to the healthy reference instead of leaving
// the previous reference (or no reference) in place.
func TestPromptCacheCollapse_SeamDirectBeforeHealthyUsagePromotes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		priorHig   [][32]byte // earlier healthy reference, if any
		want       string
		wantAbsent bool
	}{
		// Healthy B arrives direct-first with [X, B] while the old reference
		// is [A, B]; the promotion must replace it, so the later [X, Y]
		// collapse reports 1, not the stale 0.
		{"stale reference replaced", seamVector("A", "B"), `"seam_item_index":1`, false},
		// No earlier reference at all: promotion arms it, so the collapse
		// is attributed instead of unknown.
		{"missing reference armed", nil, `"seam_item_index":1`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			d, reg := newSeamDetector(&logs, nil)
			ctx := t.Context()
			if tc.priorHig != nil {
				warm := seamColdLeg("seam-session", "healthy")
				warm.CacheReadTokens = 8000
				warm.SeamDigests = tc.priorHig
				if err := d.OnUsage(ctx, warm); err != nil {
					t.Fatal(err)
				}
			}
			// Healthy B: direct feed wins the vector, usage acceptance
			// promotes it to the reference.
			d.ObserveSeamDigests("seam-session", "backend", "model", "healthy-2", seamVector("X", "B"))
			healthy := seamColdLeg("seam-session", "healthy-2")
			healthy.CacheReadTokens = 8000
			healthy.SeamDigests = seamVector("X", "B")
			if err := d.OnUsage(ctx, healthy); err != nil {
				t.Fatal(err)
			}
			for i := range 5 {
				ev := seamColdLeg("seam-session", fmt.Sprint("cold-", i))
				ev.SeamDigests = seamVector("X", "Y")
				if err := d.OnUsage(ctx, ev); err != nil {
					t.Fatal(err)
				}
			}
			if got := collapseCount(t, reg); got != 1 {
				t.Fatalf("trips=%v want 1", got)
			}
			text := logs.String()
			if tc.wantAbsent {
				if strings.Contains(text, "seam_item_index") {
					t.Fatalf("unexpected seam attribution in %s", text)
				}
				return
			}
			if !strings.Contains(text, tc.want) {
				t.Fatalf("missing %s in %s", tc.want, text)
			}
			if strings.Contains(text, `"seam_item_index":0`) {
				t.Fatalf("stale reference survived promotion in %s", text)
			}
		})
	}
}
