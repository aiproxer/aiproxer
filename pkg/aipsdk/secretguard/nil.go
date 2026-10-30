package secretguard

import "reflect"

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

// IsNilObserver reports whether o is nil or a typed-nil observer value.
func IsNilObserver(o Observer) bool {
	return isNilValue(o)
}

// IsNilGuard reports whether g is nil or a typed-nil guard value.
func IsNilGuard(g Guard) bool {
	return isNilValue(g)
}
