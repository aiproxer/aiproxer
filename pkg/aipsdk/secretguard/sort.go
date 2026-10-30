package secretguard

import (
	"cmp"
	"slices"
)

// MaterializeSorted returns a defensive copy of guards sorted by Order ascending
// and then ID. Stable sorting preserves original order for exact ties.
func MaterializeSorted(in []Guard) []Guard {
	if len(in) == 0 {
		return nil
	}
	out := make([]Guard, 0, len(in))
	for _, g := range in {
		if !IsNilGuard(g) {
			out = append(out, g)
		}
	}
	if len(out) == 0 {
		return nil
	}
	slices.SortStableFunc(out, func(a, b Guard) int {
		if c := cmp.Compare(a.Order(), b.Order()); c != 0 {
			return c
		}
		return cmp.Compare(a.ID(), b.ID())
	})
	return out
}
