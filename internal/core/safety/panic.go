package safety

import (
	"fmt"
	"reflect"
	"runtime/debug"
)

var _ error = (*PanicError)(nil)

// Boundary classifies where an isolated panic was observed (operator diagnostics).
type Boundary string

// Boundary values match design: internal stable strings, safe for metrics/log keys when bounded.
const (
	BoundaryHTTP      Boundary = "http_request"
	BoundaryExtension Boundary = "extension_execution"
	BoundaryBackend   Boundary = "backend_attempt"
	BoundaryStream    Boundary = "stream_processing"
	BoundaryWorker    Boundary = "owned_worker"
)

// PanicError is an [error] for a recovered application panic. [Error] returns only bounded,
// non-sensitive text suitable for wrapping; use [Stack] and logs for server-side details.
type PanicError struct {
	boundary  Boundary
	operation string
	valueType string
	stack     []byte
}

// Error returns a stable, client-safe message without the panic value or stack.
func (e *PanicError) Error() string {
	if e == nil {
		return ""
	}
	// Fixed shape; do not include operation/boundary free text from user input in a way that
	// could be confused with the panic payload — operation is expected to be static at call site.
	return fmt.Sprintf("isolated panic: boundary=%s operation=%s", e.boundary, e.operation)
}

// Boundary returns the failure class for diagnostics.
func (e *PanicError) Boundary() Boundary {
	if e == nil {
		return ""
	}
	return e.boundary
}

// Operation returns the logical operation name (static at the call site, not user data).
func (e *PanicError) Operation() string {
	if e == nil {
		return ""
	}
	return e.operation
}

// ValueType returns a short type name of the panic value (e.g. "string", "int") for server logs.
func (e *PanicError) ValueType() string {
	if e == nil {
		return ""
	}
	return e.valueType
}

// Stack returns captured stack bytes for server-side logging only. Do not send to clients.
func (e *PanicError) Stack() []byte {
	if e == nil {
		return nil
	}
	return e.stack
}

// Capture builds a [PanicError] from a recovered panic value. Call from the `recover()` branch
// in a deferred function, passing the `recover()` result.
func Capture(boundary Boundary, operation string, value any) *PanicError {
	return &PanicError{
		boundary:  boundary,
		operation: operation,
		valueType: safeValueTypeName(value),
		stack:     debug.Stack(),
	}
}

// safeValueTypeName uses reflect because panic payloads are untyped interface{} values.
func safeValueTypeName(value any) string {
	if value == nil {
		return "nil"
	}
	t := reflect.TypeOf(value)
	if t == nil {
		return "any"
	}
	return t.String()
}

// Call runs fn and turns a panic into a [PanicError]; otherwise returns the error from fn.
func Call(boundary Boundary, operation string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = Capture(boundary, operation, r)
		}
	}()
	return fn()
}

// CallValue runs fn like [Call] but preserves a typed return on success.
func CallValue[T any](boundary Boundary, operation string, fn func() (T, error)) (v T, err error) {
	defer func() {
		if r := recover(); r != nil {
			var zero T
			v = zero
			err = Capture(boundary, operation, r)
		}
	}()
	return fn()
}
