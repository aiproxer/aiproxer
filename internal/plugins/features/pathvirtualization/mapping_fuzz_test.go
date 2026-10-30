package pathvirtualization_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// FuzzMappingNeverPanics locks the derivation invariants for arbitrary bytes:
// mapping derivation is pure and deterministic, an accepted root is reproduced
// byte-for-byte, a rejected root yields a zero mapping with a bounded reason, and
// no mapping ever leaks a filesystem, dot-segment, or host-OS dependency.
func FuzzMappingNeverPanics(f *testing.F) {
	for _, tc := range goldenVectors() {
		f.Add(tc.root)
	}
	for _, tc := range flavorCases() {
		f.Add(tc.path)
	}
	f.Add("")
	f.Add("/")
	f.Add("C:")
	f.Add(`\\.\PIPE\aip`)
	f.Fuzz(func(t *testing.T, root string) {
		mapping, reason := pathvirtualization.DeriveMapping(root)
		again, againReason := pathvirtualization.DeriveMapping(root)
		if mapping != again || reason != againReason {
			t.Fatalf("derivation is not deterministic for %q: %+v/%q then %+v/%q",
				root, mapping, reason, again, againReason)
		}
		if reason == pathvirtualization.SkipReasonNone {
			if mapping.Flavor == pathvirtualization.FlavorUnsupported {
				t.Fatalf("accepted %q with FlavorUnsupported", root)
			}
			if mapping.RealRoot != root {
				t.Fatalf("RealRoot = %q, want the original spelling %q", mapping.RealRoot, root)
			}
			if len(mapping.WorkspaceTag) != workspaceTagChars {
				t.Fatalf("tag = %q has %d characters, want %d",
					mapping.WorkspaceTag, len(mapping.WorkspaceTag), workspaceTagChars)
			}
			if mapping.VirtualRoot != "" {
				if !strings.Contains(mapping.VirtualRoot, mapping.WorkspaceTag) {
					t.Fatalf("virtual root %q does not embed the tag %q", mapping.VirtualRoot, mapping.WorkspaceTag)
				}
				if len(mapping.VirtualRoot) >= len(mapping.RealRoot) {
					t.Fatalf("alias %q is not strictly shorter than the real root %q",
						mapping.VirtualRoot, mapping.RealRoot)
				}
			}
			return
		}
		if mapping != (pathvirtualization.Mapping{}) {
			t.Fatalf("rejected %q but returned %+v", root, mapping)
		}
	})
}

// FuzzMappingVirtualizeExpandRoundTrip locks the reversible-translation contract
// for arbitrary roots and path bytes: mapping operations never panic, a rewrite is
// always a segment-boundary prefix substitution that keeps the untouched suffix,
// virtualization is idempotent, and expansion never emits the reserved alias
// namespace.
func FuzzMappingVirtualizeExpandRoundTrip(f *testing.F) {
	for _, tc := range roundTripCases() {
		f.Add(tc.root, tc.realPath)
	}
	for _, tc := range goldenVectors() {
		f.Add(tc.root, tc.root)
	}
	for _, tc := range virtualizeCases() {
		f.Add(tc.root, tc.path)
	}
	for _, tc := range flavorCases() {
		f.Add(tc.path, tc.path+"/fuzz")
	}
	f.Add(`/home/dev/projects/aiproxer-example-project`, `/home/dev/projects/aiproxer-example-project/src/main.go`)
	f.Add(`C:\Users\dev\source\repos\aiproxer-example-project`, `C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`)
	f.Add(`\\build01\team\source\repos\aiproxer-example-project`, `\\build01\team\source\repos\aiproxer-example-project\src\main.go`)
	f.Add(`\\?\C:\Users\dev\source\repos\aiproxer-example-project`, `\\?\C:\Users\dev\source\repos\aiproxer-example-project\src\main.go`)
	f.Add(`\\?\UNC\build01\team\source\repos\aiproxer-example-project`, `\\?\UNC\build01\team\source\repos\aiproxer-example-project\src\main.go`)
	// Roots whose real-root length and matched-prefix length diverge: a
	// trailing-separator run can make a root long enough for an alias that is
	// still not shorter than the prefix that is actually replaced.
	for _, root := range []string{
		"/ab" + strings.Repeat("/", 35),
		`C:` + strings.Repeat(`\`, 100),
		`\\build01\share` + strings.Repeat(`\`, 30),
		`\\?\C:` + strings.Repeat(`\`, 100),
		`\\?\UNC\build01\share` + strings.Repeat(`\`, 30),
		// A POSIX root whose matched prefix is exactly as long as its alias, with
		// and without the trailing separator that makes the root itself longer.
		"/" + strings.Repeat("a", 35),
		"/" + strings.Repeat("a", 35) + "/",
		"/" + strings.Repeat("a", 36),
	} {
		f.Add(root, root+"/src/main.go")
		f.Add(root, root)
	}
	f.Fuzz(func(t *testing.T, root, path string) {
		mapping, reason := pathvirtualization.DeriveMapping(root)
		if reason != pathvirtualization.SkipReasonNone {
			// A rejected root must leave every path untouched, and a reserved alias
			// must still fail closed: with no usable mapping, no alias resolves.
			virtual, changed := mapping.VirtualizePath(path)
			if changed || virtual != path {
				t.Fatalf("rejected root %q rewrote %q to %q", root, path, virtual)
			}
			expanded, result := mapping.ExpandPath(path)
			switch result {
			case pathvirtualization.ExpandResultExpanded:
				t.Fatalf("rejected root %q expanded %q to %q", root, path, expanded)
			case pathvirtualization.ExpandResultNotApplicable:
				if expanded != path {
					t.Fatalf("rejected root %q returned %q for an ordinary path", root, expanded)
				}
			default:
				if expanded != "" {
					t.Fatalf("rejected root %q returned %q for a rejected alias", root, expanded)
				}
			}
			return
		}

		virtual, changed := mapping.VirtualizePath(path)
		if virtual != path && !changed {
			t.Fatalf("VirtualizePath(%q) rewrote it to %q while reporting no change", path, virtual)
		}
		if changed {
			// The rewrite is a strict shortening, and the alias must not still
			// expose the real root it replaces.
			if len(virtual) >= len(path) {
				t.Fatalf("alias %q is not shorter than %q", virtual, path)
			}
			if mapping.VirtualRoot != "" && strings.Contains(virtual, mapping.RealRoot) {
				t.Fatalf("alias %q still exposes the real root %q", virtual, mapping.RealRoot)
			}
			// The untouched suffix survives the round trip byte-for-byte. When the
			// path spells the mapping's real root exactly, expansion must
			// reproduce the path itself; the only lossy case is a real root that
			// carries its own trailing separator, whose spelling wins instead.
			exact := realRootSpelledExactly(mapping, path)
			restored, result := mapping.ExpandPath(virtual)
			if result != pathvirtualization.ExpandResultExpanded {
				t.Fatalf("ExpandPath(%q) result = %v, want expanded", virtual, result)
			}
			if exact && restored != path {
				t.Fatalf("round trip is lossy: %q -> %q -> %q", path, virtual, restored)
			}
			// Re-virtualizing the restored path must return the same alias, so the
			// namespace translation is an involution. That is a property of a
			// byte-exact round trip only, under the same precondition the
			// restoration check above uses. Outside it, expansion substituted the
			// mapping's own real-root spelling (design.md 152), so the restored path
			// carries different boundary bytes: a real root whose trailing separator
			// is part of the root, or a path that spells the root with different case
			// or separator style. Such a restored path need not be virtualizable again
			// at all.
			//
			// The property that holds in both cases is checked below: expansion yields
			// a real path that carries no alias, and virtualizing an alias is a no-op.
			if exact {
				revirtualized, changedAgain := mapping.VirtualizePath(restored)
				if !changedAgain || revirtualized != virtual {
					t.Fatalf("round trip is not reversible: %q -> %q -> %q -> %q",
						path, virtual, restored, revirtualized)
				}
			}
			// Expansion hands the client a real path, never an alias.
			if strings.Contains(restored, reservedNamespaceV1) {
				t.Fatalf("expanded path %q still carries the reserved namespace", restored)
			}
			// A lossy round trip must not drift: if the restored path can be
			// virtualized again, the second alias still expands to a real-root path
			// and still leaks no alias, so repeated translation stays inside the two
			// namespaces no matter which spelling the client used.
			if second, changedSecond := mapping.VirtualizePath(restored); changedSecond {
				secondReal, secondResult := mapping.ExpandPath(second)
				if secondResult != pathvirtualization.ExpandResultExpanded {
					t.Fatalf("ExpandPath(%q) result = %v, want expanded", second, secondResult)
				}
				if !strings.HasPrefix(secondReal, mapping.RealRoot) {
					t.Fatalf("second round trip expanded %q to %q, want a path under %q",
						second, secondReal, mapping.RealRoot)
				}
				if strings.Contains(secondReal, reservedNamespaceV1) {
					t.Fatalf("second round trip produced %q, which still carries the reserved namespace", secondReal)
				}
			}
		}
		// Reapplying virtualization to an already virtualized path is a no-op.
		if virtual != path {
			second, changedSecond := mapping.VirtualizePath(virtual)
			if changedSecond || second != virtual {
				t.Fatalf("re-virtualizing %q returned %q (changed=%v)", virtual, second, changedSecond)
			}
		}
		// Expansion never manufactures an alias for a path that carries none.
		if !changed {
			expanded, result := mapping.ExpandPath(path)
			if result == pathvirtualization.ExpandResultExpanded {
				t.Fatalf("ExpandPath(%q) reported expansion but returned %q", path, expanded)
			}
			switch result {
			case pathvirtualization.ExpandResultNotApplicable:
				if expanded != path {
					t.Fatalf("ExpandPath(%q) returned %q without reporting an expansion", path, expanded)
				}
			default:
				// A rejected reserved alias releases no path value at all, so the
				// namespace can never leak out through an ignored result code.
				if expanded != "" {
					t.Fatalf("ExpandPath(%q) rejected with %v but still returned %q",
						path, result, expanded)
				}
			}
		}
	})
}

// realRootSpelledExactly reports the precondition under which a round trip must be
// byte-for-byte reversible: the path spells the mapping's real root with exactly
// the same bytes and continues past it.
//
// Two shapes are deliberately excluded because expansion reconstructs the
// mapping's own RealRoot spelling (design.md 152): a real root that carries a
// trailing separator, where the root's spelling replaces the path's boundary
// separator, and a path that spells the root with different case or separator
// style, which only matches under the flavor's tolerant comparison.
func realRootSpelledExactly(mapping pathvirtualization.Mapping, path string) bool {
	if len(path) <= len(mapping.RealRoot) || mapping.RealRoot == "" {
		return false
	}
	if isPathSeparator(mapping.RealRoot[len(mapping.RealRoot)-1]) {
		return false
	}
	return strings.HasPrefix(path, mapping.RealRoot)
}

// isPathSeparator reports whether c is either path separator. The mapper never
// consults the host separator set, so neither does the test.
func isPathSeparator(c byte) bool {
	return c == '/' || c == '\\'
}
