package billing

import "reflect"

// isNilValue reports untyped nil and typed-nil chan/func/interface/map/pointer/
// slice values so boxed nil ports fail validation instead of panicking later.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
