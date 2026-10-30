package prerequest

import (
	"cmp"
	"reflect"
	"slices"
)

func isNilHandler(h Handler) bool {
	if h == nil {
		return true
	}
	rv := reflect.ValueOf(h)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// MaterializeSorted returns handlers sorted by Order, ID, and original registration index.
func MaterializeSorted(in []Handler) []Handler {
	if len(in) == 0 {
		return nil
	}
	h := slices.Clone(in)
	idx := make([]int, len(h))
	for i := range idx {
		idx[i] = i
	}
	slices.SortFunc(idx, func(hi, hj int) int {
		a, b := h[hi], h[hj]
		if isNilHandler(a) && isNilHandler(b) {
			return cmp.Compare(hi, hj)
		}
		if isNilHandler(a) {
			return -1
		}
		if isNilHandler(b) {
			return 1
		}
		if a.Order() < b.Order() {
			return -1
		}
		if a.Order() > b.Order() {
			return 1
		}
		if a.ID() < b.ID() {
			return -1
		}
		if a.ID() > b.ID() {
			return 1
		}
		return cmp.Compare(hi, hj)
	})
	out := make([]Handler, len(h))
	for k, ii := range idx {
		out[k] = h[ii]
	}
	return out
}
