package localturn

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

// IsNilHandler reports whether h is nil or a typed-nil handler value.
func IsNilHandler(h Handler) bool {
	return isNilValue(h)
}
