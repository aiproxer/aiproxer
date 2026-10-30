package toolcall

import (
	"cmp"
	"slices"
)

func MaterializeSorted(finalizers []Finalizer) []Finalizer {
	if len(finalizers) == 0 {
		return nil
	}
	h := make([]Finalizer, 0, len(finalizers))
	for _, f := range finalizers {
		if f != nil {
			h = append(h, f)
		}
	}
	if len(h) == 0 {
		return nil
	}
	if len(h) == 1 {
		return h
	}
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(a, b int) int {
		fa, fb := h[a], h[b]
		if c := cmp.Compare(fa.Order(), fb.Order()); c != 0 {
			return c
		}
		if c := cmp.Compare(fa.ID(), fb.ID()); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	out := make([]Finalizer, len(h))
	for i, old := range idx {
		out[i] = h[old]
	}
	return out
}
