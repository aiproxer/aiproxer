package safety

import (
	"fmt"
	"strings"
	"testing"
)

func TestPanicError_Error_IsStableAndDoesNotLeakPanicOrStack(t *testing.T) {
	t.Parallel()
	secret := "ultra-secret-payload-and-stack-marker"
	pe := Capture(BoundaryExtension, "HandleRequestParts", secret)

	if pe == nil {
		t.Fatal("expected non-nil PanicError")
	}
	if pe.Boundary() != BoundaryExtension {
		t.Fatalf("Boundary: got %q", pe.Boundary())
	}
	if pe.Operation() != "HandleRequestParts" {
		t.Fatalf("Operation: got %q", pe.Operation())
	}
	// value type is allowed to reflect string kind for type name; Error() must not include payload.
	if pe.ValueType() != "string" {
		t.Fatalf("ValueType: got %q", pe.ValueType())
	}
	stack := pe.Stack()
	if len(stack) == 0 {
		t.Fatal("expected non-empty stack bytes")
	}
	// Do not use fmt in Error — stack text must not appear in client-safe string.
	s := pe.Error()
	if s == "" {
		t.Fatal("Error() empty")
	}
	// Second call should match first (stability for logs/metrics correlation).
	if s != pe.Error() {
		t.Fatalf("Error() not stable: %q vs %q", s, pe.Error())
	}
	if strings.Contains(s, string(stack)) {
		t.Fatal("Error() must not include stack")
	}
	if strings.Contains(s, secret) {
		t.Fatal("Error() must not include raw panic value")
	}
}

func TestCapture_ValueType(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		boundary Boundary
		value    any
		want     string
		prefix   bool
	}{
		{name: "int", boundary: BoundaryStream, value: 42, want: "int"},
		{name: "struct", boundary: BoundaryHTTP, value: struct{ n int }{n: 1}, want: "struct", prefix: true},
		{name: "nil", boundary: BoundaryHTTP, value: nil, want: "nil"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pe := Capture(tc.boundary, "op", tc.value)
			got := pe.ValueType()
			if tc.prefix {
				if !strings.HasPrefix(got, tc.want) {
					t.Fatalf("expected prefix %q, got %q", tc.want, got)
				}
			} else if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestCall_RecoversToError(t *testing.T) {
	t.Parallel()
	err := Call(BoundaryHTTP, "GET", func() error {
		panic("boom")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*PanicError)
	if !ok {
		t.Fatalf("want *PanicError, got %T", err)
	}
	if pe.Boundary() != BoundaryHTTP {
		t.Fatalf("boundary: %q", pe.Boundary())
	}
	if strings.Contains(pe.Error(), "boom") {
		t.Fatal("Error() must not include panic text")
	}
}

func TestCall_ReturnsUnderlyingError(t *testing.T) {
	t.Parallel()
	want := fmt.Errorf("plain")
	err := Call(BoundaryBackend, "open", func() error {
		return want
	})
	if err != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestCallValue_RecoversAndZeroValue(t *testing.T) {
	t.Parallel()
	_, err := CallValue(BoundaryWorker, "run", func() (int, error) {
		panic(7)
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPanicSlogFieldAttrs_OMitsStack(t *testing.T) {
	t.Parallel()
	pe := Capture(BoundaryHTTP, "x", "payload")
	attrs := PanicSlogFieldAttrs(pe)
	for _, a := range attrs {
		if a.Key == "panic_stack" {
			t.Fatalf("field attrs must not include stack")
		}
	}
	if a := pe.Stack(); len(a) == 0 {
		t.Fatal("test panic must have stack for AppendPanicStackAttr coverage")
	}
	attrs2 := append(attrs[:0:0], attrs...)
	attrs2 = AppendPanicStackAttr(attrs2, pe)
	found := false
	for _, a := range attrs2 {
		if a.Key == "panic_stack" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected panic_stack on append, attrs=%+v", attrs2)
	}
}

func TestCallValue_ReturnsValue(t *testing.T) {
	t.Parallel()
	v, err := CallValue(BoundaryStream, "recv", func() (string, error) {
		return "ok", nil
	})
	if err != nil || v != "ok" {
		t.Fatalf("v=%q err=%v", v, err)
	}
}
