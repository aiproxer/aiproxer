package request

import (
	"cmp"
	"reflect"
	"slices"
)

// participantLess is design §17 ordering: order, id, registration index.
func participantLess(orderA, orderB int, idA, idB string, regIdxA, regIdxB int) int {
	if c := cmp.Compare(orderA, orderB); c != 0 {
		return c
	}
	if c := cmp.Compare(idA, idB); c != 0 {
		return c
	}
	return cmp.Compare(regIdxA, regIdxB)
}

func isNilTransform(t Transform) bool {
	if t == nil {
		return true
	}
	rv := reflect.ValueOf(t)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// MaterializeSorted returns a copy of transforms sorted for stable execution
// (same rule as the hook bus: ascending Order, ID, registration index).
func MaterializeSorted(transforms []Transform) []Transform {
	if len(transforms) == 0 {
		return nil
	}
	h := slices.Clone(transforms)
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		if isNilTransform(a) && isNilTransform(b) {
			return cmp.Compare(hi, hj)
		}
		if isNilTransform(a) {
			return -1
		}
		if isNilTransform(b) {
			return 1
		}
		return participantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
	})
	out := make([]Transform, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}

// MaterializeAttemptsSorted returns a copy of attempt transforms sorted for stable
// execution (same rule as [MaterializeSorted]: ascending Order, ID, registration index).
// Nil entries are not special-cased; callers must not pass nil interfaces (matches
// [MaterializeSorted] for request.Transform).
func MaterializeAttemptsSorted(transforms []AttemptTransform) []AttemptTransform {
	if len(transforms) == 0 {
		return nil
	}
	h := slices.Clone(transforms)
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		return participantLess(a.Order(), b.Order(), a.ID(), b.ID(), hi, hj)
	})
	out := make([]AttemptTransform, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
