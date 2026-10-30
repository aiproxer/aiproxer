package localturn

import (
	"cmp"
	"slices"
)

// MaterializeSorted returns a defensive copy of handlers sorted by Order ascending
// and then ID. Stable sorting preserves original order for exact ties.
func MaterializeSorted(in []Handler) []Handler {
	if len(in) == 0 {
		return nil
	}
	out := make([]Handler, 0, len(in))
	for _, h := range in {
		if !IsNilHandler(h) {
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.SortStableFunc(out, func(a, b Handler) int {
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return out
}
