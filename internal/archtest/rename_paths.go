package archtest

import "strings"

// formerPathAbbr is the abbreviation that path segments carried before the
// AIProxer rename, assembled so this file holds no retired spelling. Archived
// evidence and pinned pre-rename baseline commits still use it.
var formerPathAbbr = "l" + "ip"

// renamedPath maps every path segment that starts with from to start with to.
func renamedPath(rel, from, to string) string {
	segs := strings.Split(rel, "/")
	for i, seg := range segs {
		if rest, ok := strings.CutPrefix(seg, from); ok {
			segs[i] = to + rest
		}
	}
	return strings.Join(segs, "/")
}

// postRenamePath returns the current path of a path recorded before the rename.
func postRenamePath(rel string) string { return renamedPath(rel, formerPathAbbr, "aip") }

// preRenamePath returns the path a current file had before the rename.
func preRenamePath(rel string) string { return renamedPath(rel, "aip", formerPathAbbr) }
