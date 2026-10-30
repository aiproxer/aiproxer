package safety

import (
	"log/slog"
)

// PanicSlogFieldAttrs returns boundary, operation, and panic_value_type attributes for a
// non-nil [PanicError]. It returns an empty slice when pe is nil.
func PanicSlogFieldAttrs(pe *PanicError) []slog.Attr {
	if pe == nil {
		return nil
	}
	out := make([]slog.Attr, 0, 3)
	if b := pe.Boundary(); b != "" {
		out = append(out, slog.String("panic_boundary", string(b)))
	}
	if op := pe.Operation(); op != "" {
		out = append(out, slog.String("operation", op))
	}
	if vt := pe.ValueType(); vt != "" {
		out = append(out, slog.String("panic_value_type", vt))
	}
	return out
}

// AppendPanicStackAttr appends a "panic_stack" attribute when pe has stack bytes. For server
// logs only; do not add to client responses. When pe is nil, attrs is returned unchanged.
func AppendPanicStackAttr(attrs []slog.Attr, pe *PanicError) []slog.Attr {
	if pe == nil {
		return attrs
	}
	if st := pe.Stack(); len(st) > 0 {
		attrs = append(attrs, slog.String("panic_stack", string(st)))
	}
	return attrs
}
