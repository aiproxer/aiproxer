package response

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

// MaterializeSorted returns a copy of factories sorted for stable execution
// (same rule as request/completion: ascending Order, ID, registration index).
// Nil entries are not special-cased; callers must not pass nil interfaces.
func MaterializeSorted(factories []StreamObserverFactory) []StreamObserverFactory {
	if len(factories) == 0 {
		return nil
	}
	h := slices.Clone(factories)
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return participantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
	})
	out := make([]StreamObserverFactory, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
