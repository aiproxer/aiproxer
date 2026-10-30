package pathvirtualization_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/pathvirtualization"
)

// FuzzReservedAliasOutcomesAreBounded locks the reserved-namespace rule of
// design.md 186-195 for arbitrary bytes: recognition never panics, a path that
// spells the reserved marker without this mapping's own workspace tag can never be
// expanded, a rejection releases no path value at all, and expansion is
// deterministic.
//
// The stale-tag invariant is the fuzz form of requirement 6.5: a V1 alias always
// embeds the tag of the root that produced it, so a path that carries the reserved
// marker but not this workspace's tag cannot name a current alias root and must
// never be expanded against the current mapping.
func FuzzReservedAliasOutcomesAreBounded(f *testing.F) {
	for _, tc := range reservedAliasCases() {
		f.Add(tc.root, tc.path)
	}
	for _, tc := range reservedRootCases() {
		f.Add(tc.root, tc.root+`/src/main.go`)
	}
	for _, tc := range staleWorkspaceCases() {
		f.Add(tc.rootB, tc.rootA+`/src/main.go`)
	}
	f.Add(posixRoot, `/.__aip_v1__/w_`+posixTag+`/src/main.go`)
	f.Add(posixRoot, `/.__aip_v1__/`)
	f.Add(posixRoot, `/.__aip_v1__/w_tooshort`)
	f.Add(driveRoot, `C:\.__aip_v1__\w_`+driveTag+`\`)
	f.Add(uncRoot, `\\.__aip_v1__\w_`+uncTag+`\`)
	f.Add(extendedUNCRoot, `\\?\UNC\.__aip_v1__\w_`+extendedUNCTag+`\`)
	f.Add("", "")
	f.Fuzz(func(t *testing.T, root, alias string) {
		mapping, reason := pathvirtualization.DeriveMapping(root)
		got, result := mapping.ExpandPath(alias)
		again, againResult := mapping.ExpandPath(alias)
		if got != again || result != againResult {
			t.Fatalf("expansion is not deterministic for %q: %q/%v then %q/%v",
				alias, got, result, again, againResult)
		}
		if reason == pathvirtualization.SkipReasonNone &&
			strings.Contains(alias, reservedNamespaceV1) &&
			!carriesWorkspaceTag(mapping.Flavor, alias, mapping.WorkspaceTag) &&
			result == pathvirtualization.ExpandResultExpanded {
			t.Fatalf("ExpandPath(%q) expanded %q against root %q carrying tag %q",
				alias, got, root, mapping.WorkspaceTag)
		}
		switch result {
		case pathvirtualization.ExpandResultExpanded:
			if mapping.VirtualRoot != "" && strings.Contains(got, mapping.VirtualRoot) {
				t.Fatalf("expansion %q still carries the alias root %q", got, mapping.VirtualRoot)
			}
		case pathvirtualization.ExpandResultNotApplicable:
			if got != alias {
				t.Fatalf("ExpandPath(%q) returned %q without reporting an outcome", alias, got)
			}
		default:
			// A rejection releases no path value, so the reserved namespace can
			// never leak out through an ignored result code.
			if got != "" {
				t.Fatalf("ExpandPath(%q) rejected with %v but still returned %q", alias, result, got)
			}
		}
	})
}

// carriesWorkspaceTag reports whether alias spells tag under the comparison rules
// of the mapping's own flavor: byte-exact for POSIX, ASCII case-insensitive for
// the Windows flavors, matching how the mapper compares an alias against the tag
// it derived. Only ASCII bytes are folded, so no Unicode equivalence can hide a
// tag behind a look-alike character.
func carriesWorkspaceTag(flavor pathvirtualization.PathFlavor, alias, tag string) bool {
	if flavor == pathvirtualization.FlavorPOSIX {
		return strings.Contains(alias, tag)
	}
	folded := []byte(alias)
	for i, c := range folded {
		if c >= 'A' && c <= 'Z' {
			folded[i] = c + ('a' - 'A')
		}
	}
	return strings.Contains(string(folded), tag)
}
