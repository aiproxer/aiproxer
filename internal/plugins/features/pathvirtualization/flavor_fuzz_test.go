package pathvirtualization_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// FuzzClassifyPathNeverPanics locks the parser invariants for arbitrary bytes:
// classification never panics, acceptance and rejection stay mutually
// exclusive, and an accepted path is reproduced byte-for-byte from its root and
// remainder so no normalization, dot-segment resolution, or case folding can
// creep into flavor parsing.
func FuzzClassifyPathNeverPanics(f *testing.F) {
	for _, tc := range flavorCases() {
		f.Add(tc.path)
	}
	f.Add("")
	f.Fuzz(func(t *testing.T, path string) {
		parsed, reason := pathvirtualization.ClassifyPath(path)
		again, againReason := pathvirtualization.ClassifyPath(path)
		if parsed != again || reason != againReason {
			t.Fatalf("classification is not deterministic for %q: %+v/%q then %+v/%q", path, parsed, reason, again, againReason)
		}
		if reason == pathvirtualization.SkipReasonNone {
			if parsed.Flavor == pathvirtualization.FlavorUnsupported {
				t.Fatalf("accepted %q with FlavorUnsupported", path)
			}
			if parsed.Root == "" {
				t.Fatalf("accepted %q with an empty root", path)
			}
			if parsed.Root+parsed.Rest != path {
				t.Fatalf("root+rest = %q does not reproduce input %q", parsed.Root+parsed.Rest, path)
			}
			return
		}
		if parsed != (pathvirtualization.ParsedPath{}) {
			t.Fatalf("rejected %q but returned %+v", path, parsed)
		}
	})
}
