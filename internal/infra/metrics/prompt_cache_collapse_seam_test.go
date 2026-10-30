package metrics

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
	"github.com/prometheus/client_golang/prometheus"
)

// Deterministic mutation fixtures for bounded digest-only seam attribution
// (issue #1074, production publisher issue #1367). Digests are SHA-256 over
// fixed labels: no content, no randomness, no raw identifiers beyond the
// fixed test session names.

func seamDigest(label string) [32]byte { return sha256.Sum256([]byte("seam-test:" + label)) }

func seamVector(labels ...string) [][32]byte {
	out := make([][32]byte, len(labels))
	for i, l := range labels {
		out[i] = seamDigest(l)
	}
	return out
}

func newSeamDetector(logs *bytes.Buffer, now func() time.Time) (*PromptCacheCollapse, *prometheus.Registry) {
	reg := prometheus.NewRegistry()
	if now == nil {
		d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.NewJSONHandler(logs, nil)), nil)
		return d, reg
	}
	d := NewPromptCacheCollapse(RegisterPromptCacheCollapseProm(reg), slog.New(slog.NewJSONHandler(logs, nil)), now)
	return d, reg
}

func seamColdLeg(session, bleg string) usage.Event {
	return usage.Event{SessionID: session, BackendID: "backend", Model: "model", BLegID: bleg, AttemptSeq: 1, InputTokens: 8000}
}

// driveSeamCollapse feeds a warm leg followed by five cold legs. healthy is
// the warm leg's per-item digest vector, fed through the warm leg's own
// usage sample (the production path, which alone can arm the healthy
// reference); a nil healthy leaves the reference unknown. digests maps a
// cold B-leg ID to its per-item digest vector fed through the direct entry
// point; legs without an entry feed no digests.
func driveSeamCollapse(t *testing.T, d *PromptCacheCollapse, session string, healthy [][32]byte, digests map[string][][32]byte) {
	t.Helper()
	warm := seamColdLeg(session, "healthy")
	warm.CacheReadTokens = 8000
	warm.SeamDigests = healthy
	if err := d.OnUsage(t.Context(), warm); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		leg := fmt.Sprint("cold-", i)
		if vec, ok := digests[leg]; ok {
			d.ObserveSeamDigests(session, "backend", "model", leg, vec)
		}
		if err := d.OnUsage(t.Context(), seamColdLeg(session, leg)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPromptCacheCollapse_SeamAttribution(t *testing.T) {
	base := []string{"sys", "history-1", "history-2", "user"}
	for _, tc := range []struct {
		name string
		last []string
		want int // first divergent index, or -1 for unknown (attr omitted)
	}{
		{"mid-sequence edit", []string{"sys", "history-1", "EDITED", "user"}, 2},
		{"prefix rewrite", []string{"REWRITTEN", "history-1", "history-2", "user"}, 0},
		{"appended message", []string{"sys", "history-1", "history-2", "user", "followup"}, 4},
		{"removed message", []string{"sys", "history-1", "history-2"}, 3},
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
			driveSeamCollapse(t, d, "seam-session", seamVector(base...), feeds)
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

func TestPromptCacheCollapse_SeamUnknownWithoutPair(t *testing.T) {
	for _, tc := range []struct {
		name  string
		feeds map[string][][32]byte
	}{
		{"no digests", nil},
		{"single leg only", map[string][][32]byte{"cold-4": seamVector("a", "b")}},
		{"empty vector ignored", map[string][][32]byte{"cold-3": {}, "cold-4": seamVector("a", "b")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			d, reg := newSeamDetector(&logs, nil)
			driveSeamCollapse(t, d, "seam-session", nil, tc.feeds)
			if got := collapseCount(t, reg); got != 1 {
				t.Fatalf("trips=%v want 1", got)
			}
			if strings.Contains(logs.String(), "seam_item_index") {
				t.Fatalf("unexpected seam attribution in %s", logs.String())
			}
		})
	}
}

func TestPromptCacheCollapse_SeamDuplicateIgnored(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	ctx := t.Context()
	warm := seamColdLeg("seam-session", "healthy")
	warm.CacheReadTokens = 8000
	base := seamVector("a", "b", "c")
	warm.SeamDigests = base
	if err := d.OnUsage(ctx, warm); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		leg := fmt.Sprint("cold-", i)
		d.ObserveSeamDigests("seam-session", "backend", "model", leg, base)
		if err := d.OnUsage(ctx, seamColdLeg("seam-session", leg)); err != nil {
			t.Fatal(err)
		}
	}
	// First delivery for cold-4 wins; the late duplicate must not shift the pair
	// (a shift would move the reported seam from index 0 to index 1).
	d.ObserveSeamDigests("seam-session", "backend", "model", "cold-4", seamVector("X", "b", "c"))
	d.ObserveSeamDigests("seam-session", "backend", "model", "cold-4", seamVector("X", "Y", "c"))
	if err := d.OnUsage(ctx, seamColdLeg("seam-session", "cold-4")); err != nil {
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

func TestPromptCacheCollapse_SeamTruncationUnknown(t *testing.T) {
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
	feeds["cold-4"] = mkLong(true) // divergence beyond the bound: must stay unknown
	driveSeamCollapse(t, d, "seam-session", mkLong(false), feeds)
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if strings.Contains(logs.String(), "seam_item_index") {
		t.Fatalf("bounded truncation fabricated a seam in %s", logs.String())
	}
}

func TestPromptCacheCollapse_SeamTruncatedLengthDifferenceUnknown(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	// The retained window is a proper prefix of the healthy reference, but the
	// healthy leg exceeded the retention bound: the length difference is
	// hidden beyond the bound and must stay unknown rather than surface as a
	// seam at the truncation point.
	long := make([][32]byte, collapseMaxItems+32)
	for i := range long {
		long[i] = seamDigest(fmt.Sprint("prefix-", i))
	}
	short := long[:collapseMaxItems-16]
	feeds := map[string][][32]byte{}
	for i := range 4 {
		feeds[fmt.Sprint("cold-", i)] = long
	}
	feeds["cold-4"] = short
	driveSeamCollapse(t, d, "seam-session", long, feeds)
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	if text := logs.String(); strings.Contains(text, "seam_item_index") {
		t.Fatalf("truncated length difference fabricated a seam in %s", text)
	}
}

func TestPromptCacheCollapse_SeamEvictedWithKey(t *testing.T) {
	for _, mode := range []string{"capacity", "idle", "history rotation"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Unix(1, 0)
			var logs bytes.Buffer
			d, reg := newSeamDetector(&logs, func() time.Time { return now })
			// A divergent pair: if eviction or rotation retains either vector,
			// the trip below reports seam_item_index 0 instead of staying
			// unattributed, so the assertion can actually fail.
			d.ObserveSeamDigests("seam-session", "backend", "model", "leg-a", seamVector("a", "b", "c"))
			d.ObserveSeamDigests("seam-session", "backend", "model", "leg-b", seamVector("X", "b", "c"))
			switch mode {
			case "capacity":
				for i := range collapseMaxKeys {
					d.ObserveSeamDigests(fmt.Sprint("session-", i), "backend", "model", "leg", seamVector("a", "b", "c"))
				}
			case "idle":
				now = now.Add(collapseIdle)
			case "history rotation":
				for i := range collapseLegHistory {
					if err := d.OnUsage(t.Context(), seamColdLeg("seam-session", fmt.Sprint("fill-", i))); err != nil {
						t.Fatal(err)
					}
				}
			}
			driveSeamCollapse(t, d, "seam-session", nil, nil)
			if got := collapseCount(t, reg); got != 1 {
				t.Fatalf("trips=%v want 1", got)
			}
			if strings.Contains(logs.String(), "seam_item_index") {
				t.Fatalf("evicted digests leaked into attribution in %s", logs.String())
			}
		})
	}
}

func TestPromptCacheCollapse_SeamPerKeyIsolation(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	// Session A mutates at index 1. Session B feeds no digests at all, so a
	// seam in session B's episode can only come from digest state shared
	// across keys.
	driveSeamCollapse(t, d, "session-a", seamVector("a", "b"), map[string][][32]byte{
		"cold-3": seamVector("a", "b"),
		"cold-4": seamVector("a", "MUTATED"),
	})
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("session-a trips=%v want 1", got)
	}
	if !strings.Contains(logs.String(), `"seam_item_index":1`) {
		t.Fatalf("missing session-a seam in %s", logs.String())
	}
	logs.Reset()
	driveSeamCollapse(t, d, "session-b", nil, nil)
	if got := collapseCount(t, reg); got != 2 {
		t.Fatalf("session-b trips=%v want 2", got)
	}
	if text := logs.String(); strings.Contains(text, "seam_item_index") {
		t.Fatalf("session-a seam leaked into session-b in %s", text)
	}
}

func TestPromptCacheCollapse_SeamPrivateWarning(t *testing.T) {
	var logs bytes.Buffer
	d, reg := newSeamDetector(&logs, nil)
	prev := seamVector("PRIVATE_A_0", "PRIVATE_A_1")
	last := seamVector("PRIVATE_A_0", "PRIVATE_B_1")
	feeds := map[string][][32]byte{}
	for i := range 4 {
		feeds[fmt.Sprint("cold-", i)] = prev
	}
	feeds["cold-4"] = last
	driveSeamCollapse(t, d, "PRIVATE_SESSION", prev, feeds)
	if got := collapseCount(t, reg); got != 1 {
		t.Fatalf("trips=%v want 1", got)
	}
	text := logs.String()
	if !strings.Contains(text, `"seam_item_index":1`) {
		t.Fatalf("missing seam attribution in %s", text)
	}
	for _, vec := range [][][32]byte{prev, last} {
		for _, dg := range vec {
			if strings.Contains(text, hex.EncodeToString(dg[:])) {
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
