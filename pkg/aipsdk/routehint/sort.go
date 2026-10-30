package routehint

import (
	"cmp"
	"slices"
)

func participantLess(orderA, orderB int, idA, idB string, regIdxA, regIdxB int) int {
	if c := cmp.Compare(orderA, orderB); c != 0 {
		return c
	}
	if c := cmp.Compare(idA, idB); c != 0 {
		return c
	}
	return cmp.Compare(regIdxA, regIdxB)
}

// MaterializeSorted returns providers sorted for stable execution (design §17).
func MaterializeSorted(providers []Provider) []Provider {
	if len(providers) == 0 {
		return nil
	}
	h := slices.Clone(providers)
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		if a == nil && b == nil {
			return 0
		}
		if a == nil {
			return -1
		}
		if b == nil {
			return 1
		}
		return participantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
	})
	out := make([]Provider, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
