package routing

import (
	"slices"
	"strings"
)

// FilterRoutePrefixes trims, drops invalid (empty, colon- or slash-bearing),
// dedups, and sorts backend route-selector prefixes. Shared by runtime bundle
// composition and frontend PrefixSet construction so the validation rule lives
// in one place. A prefix is the "<prefix>:" segment of a route selector; it must
// not itself contain ":" (which would make it a full selector) or "/" (which
// collides with provider-namespace model syntax).
func FilterRoutePrefixes(prefixes []string) []string {
	seen := make(map[string]struct{}, len(prefixes))
	out := make([]string, 0, len(prefixes))
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" || strings.Contains(prefix, ":") || strings.Contains(prefix, "/") {
			continue
		}
		if _, dup := seen[prefix]; dup {
			continue
		}
		seen[prefix] = struct{}{}
		out = append(out, prefix)
	}
	slices.Sort(out)
	return out
}
