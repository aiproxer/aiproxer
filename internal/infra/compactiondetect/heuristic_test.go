package compactiondetect

import (
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

// bigText renders n characters deterministically (used to build token-heavy
// canonical requests without megabytes of literals).
func bigText(n int) string {
	return strings.Repeat("abcdefghijklmnopqrstuvwxyz0123456789", n/36+1)[:n]
}

// itemCall builds an item-authoritative canonical call with one leading
// prefix item and the given tail item texts.
func itemCall(prefix string, tails ...string) aipapi.Call {
	items := []aipapi.Item{{
		Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted,
		Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: prefix}},
	}}
	for i, tt := range tails {
		role := aipapi.RoleAssistant
		if i%2 == 1 {
			role = aipapi.RoleUser
		}
		items = append(items, aipapi.Item{
			Kind: aipapi.ItemKindMessage, Role: role, Status: aipapi.ItemStatusCompleted,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: tt}},
		})
	}
	return aipapi.Call{
		ID:         "item-call",
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenAIChatCompletions, DeliveryMode: aipapi.DeliveryModeStreaming},
		Items:      items,
	}
}

// TestFingerprint_deterministicAndBounded proves fingerprint hashing is
// deterministic, content-sensitive, and retains no source text (requirements
// 5.7, 7.3): the stored fingerprint carries only counts/hashes/timestamps.
func TestFingerprint_deterministicAndBounded(t *testing.T) {
	t.Parallel()
	call := itemCall(bigText(40000), "tail-one", "tail-two")
	at := time.Unix(5, 0)
	fp1, _ := fingerprint(call, at)
	fp2, _ := fingerprint(call, at.Add(time.Second))
	if fp1.EstimatedTokens != fp2.EstimatedTokens || fp1.ItemCount != fp2.ItemCount ||
		fp1.TailLen != fp2.TailLen || fp1.PrefixHash != fp2.PrefixHash || fp1.TailHashes != fp2.TailHashes {
		t.Fatalf("fingerprint not deterministic: %+v vs %+v", fp1, fp2)
	}
	if fp1.TailLen != 2 || fp1.PrefixItems != 3 {
		t.Fatalf("tail/prefix bookkeeping wrong: %+v", fp1)
	}
	if fp1.EstimatedTokens < heuristicPriorTokens {
		t.Fatalf("big fixture below prior floor: %d", fp1.EstimatedTokens)
	}
	// Different content must hash differently.
	other, _ := fingerprint(itemCall(bigText(40000)+"x", "tail-one", "tail-two"), at)
	if other.PrefixHash == fp1.PrefixHash {
		t.Fatal("prefix hash must change when content changes")
	}
}

// TestHeuristic_positiveMatch proves a large same-A-leg rewrite with a
// retained recent tail and removed older prefix completes heuristically once
// with history_heuristic evidence and the generic rule id (requirements
// 5.1-5.2, 5.5).
func TestHeuristic_positiveMatch(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	prev := itemCall(bigText(40000), "tail-one", "tail-two")
	cur := itemCall(bigText(6000), "tail-one", "tail-two")

	if evs := d.RequestOpened(reqMeta("tr-h1"), prev); len(evs) != 0 {
		t.Fatalf("first request must not emit: %+v", evs)
	}
	evs := d.RequestOpened(reqMeta("tr-h2"), cur)
	if len(evs) != 1 {
		t.Fatalf("events=%v want one heuristic completion", evs)
	}
	ev := evs[0]
	if ev.Phase != compaction.PhaseCompleted || ev.Evidence != compaction.EvidenceHistoryHeuristic {
		t.Fatalf("event wrong: %+v", ev)
	}
	if ev.RuleID != HeuristicRuleID {
		t.Fatalf("RuleID=%q want %q", ev.RuleID, HeuristicRuleID)
	}
	if ev.TraceID != "tr-h2" || ev.ALegID != "a-leg-1" {
		t.Fatalf("correlation missing: %+v", ev)
	}
}

// TestHeuristic_negativeCases proves ambiguous transitions emit nothing
// (requirements 5.3-5.4): resets, fresh short requests, same-size rewrites,
// reordered tails, small prior context, and near-threshold reductions never
// match from token reduction alone.
func TestHeuristic_negativeCases(t *testing.T) {
	t.Parallel()
	big := itemCall(bigText(40000), "tail-one", "tail-two")
	cases := []struct {
		name string
		prev aipapi.Call
		cur  aipapi.Call
	}{
		{"reset drops the tail", big, itemCall(bigText(6000), "unrelated-a", "unrelated-b")},
		{"fresh short request", big, itemCall("hi")},
		{"same-size rewrite", big, itemCall(bigText(40000), "tail-one", "tail-two")},
		{"reordered tail", big, itemCall(bigText(6000), "tail-two", "tail-one")},
		{"small prior context", itemCall(bigText(2000), "tail-one", "tail-two"), itemCall("tiny")},
		{"near-threshold reduction", big, itemCall(bigText(32000), "tail-one", "tail-two")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := testDetector(t)
			if evs := d.RequestOpened(reqMeta("tr-neg-a"), tc.prev); len(evs) != 0 {
				t.Fatalf("setup emitted: %+v", evs)
			}
			evs := d.RequestOpened(reqMeta("tr-neg-b"), tc.cur)
			if len(evs) != 0 {
				t.Fatalf("negative %q emitted %+v", tc.name, evs)
			}
		})
	}
}

// TestHeuristic_otherALegNeverMatches proves fingerprints never cross
// authoritative A-legs (requirement 5.1): the same rewrite on a different
// A-leg is not compared.
func TestHeuristic_otherALegNeverMatches(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	prev := itemCall(bigText(40000), "tail-one", "tail-two")
	cur := itemCall(bigText(6000), "tail-one", "tail-two")

	if evs := d.RequestOpened(compaction.PreservationMeta{TraceID: "a1", ALegID: "leg-a", BLegID: "b", AttemptSeq: 1}, prev); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	evs := d.RequestOpened(compaction.PreservationMeta{TraceID: "b1", ALegID: "leg-b", BLegID: "b", AttemptSeq: 1}, cur)
	if len(evs) != 0 {
		t.Fatalf("cross-leg heuristic emitted: %+v", evs)
	}
}

// TestHeuristic_strictPostSuppressesDuplicate proves strict post evidence on
// the baseline response suppresses the duplicate heuristic completion for the
// next request (requirement 5.6).
func TestHeuristic_strictPostSuppressesDuplicate(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	prev := itemCall(bigText(40000), "tail-one", "tail-two")
	cur := itemCall(bigText(6000), "tail-one", "tail-two")

	if evs := d.RequestOpened(reqMeta("tr-sp1"), prev); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	// The response to the baseline request carries a strict post marker.
	if got := d.ResponseReleased(resMeta("tr-sp1"), assistantItem("[CONTEXT SUMMARY]: compacted")); len(got) != 1 {
		t.Fatalf("strict post completion missing: %+v", got)
	}
	// The same rewrite that would heuristically match is now suppressed.
	if evs := d.RequestOpened(reqMeta("tr-sp2"), cur); len(evs) != 0 {
		t.Fatalf("strict post evidence did not suppress heuristic: %+v", evs)
	}
}

// TestHeuristic_strictOnlyForSameBaseline proves the suppression flag resets
// for a fresh baseline so a later real compaction still fires.
func TestHeuristic_strictOnlyForSameBaseline(t *testing.T) {
	t.Parallel()
	d := testDetector(t)
	if evs := d.RequestOpened(reqMeta("tr-1"), itemCall(bigText(40000), "t1", "t2")); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	if got := d.ResponseReleased(resMeta("tr-1"), assistantItem("[CONTEXT SUMMARY]: compacted")); len(got) != 1 {
		t.Fatalf("strict completion missing: %+v", got)
	}
	// Ordinary request stores a fresh baseline; the flag must reset.
	if evs := d.RequestOpened(reqMeta("tr-2"), itemCall("ordinary turn")); len(evs) != 0 {
		t.Fatalf("ordinary emitted: %+v", evs)
	}
	if evs := d.RequestOpened(reqMeta("tr-3"), itemCall(bigText(40000), "u1", "u2")); len(evs) != 0 {
		t.Fatalf("setup emitted: %+v", evs)
	}
	if got := d.ResponseReleased(resMeta("tr-3"), assistantItem("ordinary response")); len(got) != 0 {
		t.Fatalf("ordinary response completed unexpectedly: %+v", got)
	}
	evs := d.RequestOpened(reqMeta("tr-4"), itemCall(bigText(6000), "u1", "u2"))
	if len(evs) != 1 || evs[0].Evidence != compaction.EvidenceHistoryHeuristic {
		t.Fatalf("heuristic after fresh baseline missing: %+v", evs)
	}
}
