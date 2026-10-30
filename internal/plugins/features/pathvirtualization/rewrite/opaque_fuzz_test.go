package rewrite

// This file fuzzes the opaque-result recognizer with untrusted payload bytes. The
// properties it pins are the ones a false positive would violate, and they have to
// hold for every byte sequence, valid text or not:
//
//   - the recognizer never panics, and a payload it refuses is returned as the very
//     same string rather than a copy;
//   - anything it publishes is what a line-level rewrite would produce, so the
//     published bytes never grow: the alias is always shorter than the prefix it
//     replaces;
//   - reapplication changes nothing, so the idempotent request-part hook can run
//     this rewriter over its own output;
//   - the statistics agree with the bytes: a published payload reports at least one
//     rewrite, and a refused one reports none.
//
// The seed corpus is deliberately the false-positive shapes rather than only the
// accepted ones, because a fuzz input only ever finds a bug if it can reach the
// accepting branch by accident.

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// FuzzOpaqueTextIsConservativeAndIdempotent fuzzes one declared opaque mode against
// arbitrary text.
func FuzzOpaqueTextIsConservativeAndIdempotent(f *testing.F) {
	seeds := []string{
		"",
		" ",
		"\n\n",
		",,,,",
		"\t\t",
		docTarget,
		docRoot,
		docTarget + "\n" + docRoot + "/pkg/aipapi",
		docTarget + " " + docRoot + "/pkg/aipapi",
		docTarget + ", " + docRoot + "/pkg/aipapi",
		"Found 2 files:\n" + docTarget + "\n" + docRoot + "\n",
		docTarget + "\r\n" + docRoot + "\r\n",
		"cat " + docTarget,
		"const root = \"" + docRoot + "\"",
		"\t" + docTarget + ":22 +0x1d",
		`{"path":"` + docTarget + `"}`,
		"prefix" + docTarget,
		" " + docTarget,
		docAlias + "pkg/aipapi " + docTarget,
		"a" + docTarget,
		strings.Repeat(docTarget+",", 8),
		strings.Repeat("\n", 16),
	}
	for _, seed := range seeds {
		f.Add(seed, uint8(pathvirtualization.OpaqueResultModePathTokens))
		f.Add(seed, uint8(pathvirtualization.OpaqueResultModePathLines))
		f.Add(seed, uint8(pathvirtualization.OpaqueResultModeNone))
		f.Add(seed, uint8(200))
	}

	mapping, reason := pathvirtualization.DeriveMapping(docRoot)
	if reason != pathvirtualization.SkipReasonNone {
		f.Fatalf("DeriveMapping(%q) reason = %q, want none", docRoot, reason)
	}
	rewriter := New(mapping, nil)

	f.Fuzz(func(t *testing.T, payload string, rawMode uint8) {
		if len(payload) > 1<<20 {
			t.Skip("payload beyond the canonical single-surface bound")
		}
		mode := pathvirtualization.OpaqueResultMode(rawMode)

		var acc account
		published, changed := rewriter.rewriteOpaqueText(payload, mode, &acc)
		if !changed {
			if published != payload {
				t.Fatalf("a refused payload was republished as different bytes:\nin:  %q\nout: %q", payload, published)
			}
			if acc.rewritten != 0 {
				t.Fatalf("a refused payload reported %d rewrites", acc.rewritten)
			}
			if acc.eligible != 0 {
				t.Fatalf("a refused payload reported %d eligible leaves", acc.eligible)
			}
			// A refused payload is either the disabled default or a declared mode
			// that recognized nothing, and it records exactly one bounded reason.
			reason := SkipReasonOpaqueResultUnchanged
			if mode != pathvirtualization.OpaqueResultModeNone && mode.Valid() {
				reason = SkipReasonOpaqueResultBounded
			}
			if count, recorded := skipTally(acc, reason); !recorded || count != 1 {
				t.Fatalf("refused payload %q recorded %s x%d (recorded %v), want 1", payload, reason, count, recorded)
			}
			return
		}

		if len(published) >= len(payload) {
			t.Fatalf("published %d bytes for a %d-byte payload; a rewrite must shorten it", len(published), len(payload))
		}
		if acc.rewritten == 0 || acc.eligible < acc.rewritten {
			t.Fatalf("published bytes for %q but reported eligible/rewritten = %d/%d", payload, acc.eligible, acc.rewritten)
		}
		if acc.bytesAfter >= acc.bytesBefore {
			t.Fatalf("byte totals did not shrink for %q: %d -> %d", payload, acc.bytesBefore, acc.bytesAfter)
		}
		for _, skip := range acc.stats().Skips {
			if skip.Reason == SkipReasonOpaqueResultUnchanged || skip.Reason == SkipReasonOpaqueResultBounded {
				t.Fatalf("an accepted payload recorded the refusal reason %s", skip.Reason)
			}
		}

		// Reapplication is a no-op, which is what lets the idempotent request-part
		// hook call this rewriter again on what this one published.
		var secondPass account
		again, changedAgain := rewriter.rewriteOpaqueText(published, mode, &secondPass)
		if changedAgain {
			t.Fatalf("second pass changed the payload:\nfirst:  %q\nsecond: %q", published, again)
		}
	})
}

// skipTally returns the recorded count for one bounded reason and whether it was
// recorded at all.
func skipTally(acc account, reason SkipReason) (int, bool) {
	if reason == SkipReasonNone || reason >= skipReasonCount {
		return 0, false
	}
	count := acc.skips[reason]
	return count, count > 0
}
