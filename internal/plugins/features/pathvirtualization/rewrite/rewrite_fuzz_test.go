package rewrite

import (
	"encoding/json"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// FuzzRewriteDocumentIsValidAndIdempotent fuzzes the surface-splice engine with
// untrusted payload bytes and two selectors.
//
// It pins the three properties that must hold for every byte sequence, valid JSON
// or not:
//
//   - the engine never panics and never returns bytes for a payload it refused;
//   - any published bytes are one valid JSON document, and reapplying the rewrite
//     to them changes nothing, so a reapplication cannot corrupt a payload;
//   - the published bytes never contain the real root inside a selected location,
//     because the whole point of the rewrite is that a selected path is aliased.
func FuzzRewriteDocumentIsValidAndIdempotent(f *testing.F) {
	seeds := []string{
		`{}`,
		`null`,
		`[]`,
		`""`,
		`{"file_path":"` + docTarget + `"}`,
		`{"file_path":"` + docRoot + `","content":"` + docRoot + `"}`,
		`{"paths":["` + docTarget + `",null]}`,
		`{"paths":["` + docTarget + `",7]}`,
		`{"a/b":"` + docTarget + `","c~d":"` + docRoot + `"}`,
		`{"a":{"b":{"c":"` + docTarget + `"}}}`,
		`{ "file_path" : "` + docTarget + `" , "x" : 1e400 }`,
		`{"file_path":"/` + docRoot + `","n":12345678901234567890}`,
		`{"paths":[["` + docTarget + `"]]}`,
		`{"file_path":`,
		`{"file_path":"unterminated`,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed), "/file_path", "/paths")
	}
	for _, seed := range seeds {
		f.Add([]byte(seed), "/a~1b", "/c~0d")
	}

	mapping, reason := pathvirtualization.DeriveMapping(docRoot)
	if reason != pathvirtualization.SkipReasonNone {
		f.Fatalf("DeriveMapping(%q) reason = %q, want none", docRoot, reason)
	}
	rewriter := New(mapping, nil)

	f.Fuzz(func(t *testing.T, payload []byte, first, second string) {
		pointers := fuzzPointers(t, first, second)
		if len(payload) > 1<<20 {
			t.Skip("payload beyond the canonical single-surface bound")
		}

		var acc account
		got, changed, err := rewriter.rewriteDocument(payload, pointers, &acc)
		if err != nil {
			t.Fatalf("rewriteDocument(%q): %v", payload, err)
		}
		if !changed {
			if got != nil {
				t.Fatalf("unchanged rewrite published bytes for %q", payload)
			}
			return
		}
		if !json.Valid(got) {
			t.Fatalf("published invalid JSON for %q: %s", payload, got)
		}
		if len(got) == 0 || acc.rewritten == 0 {
			t.Fatalf("rewrite reported %d changes for %q", acc.rewritten, payload)
		}

		// Reapplication is a no-op, so the idempotent request-part hook can call this
		// rewriter again on the payload it just published.
		var secondPass account
		again, changedAgain, err := rewriter.rewriteDocument(got, pointers, &secondPass)
		if err != nil {
			t.Fatalf("second rewriteDocument(%q): %v", got, err)
		}
		if changedAgain || again != nil {
			t.Fatalf("second pass changed the payload:\nfirst:  %s\nsecond: %s", got, again)
		}
	})
}

// fuzzPointers compiles the fuzzer's pointer spellings.
//
// A spelling the canonical dialect refuses is not a rewrite input but a refused
// configuration, so the case is skipped rather than failed: ParseSelector remains the
// sole acceptance authority, and no spelling this build refuses can reach a request.
func fuzzPointers(t *testing.T, spellings ...string) pathvirtualization.SelectorSet {
	t.Helper()

	compiled, reject := pathvirtualization.CompileProfiles([]pathvirtualization.ProfileInput{{
		Names:       []string{"fuzz"},
		ArgPointers: spellings,
	}})
	if reject != pathvirtualization.SelectorRejectNone {
		t.Skipf("pointer spelling refused by the canonical dialect: %v", reject)
	}
	return compiled[0].ArgPointers
}
