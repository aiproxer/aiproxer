package largebody

import (
	"io"
	"math"
	"testing"
)

// assertFailsClosedWithoutPanic asserts fn reports an error rather than
// panicking in make when the combined write buffer length would overflow int.
func assertFailsClosedWithoutPanic(t *testing.T, fn func() error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("overflowing combined length must fail closed, got panic: %v", r)
		}
	}()
	if err := fn(); err == nil {
		t.Fatal("overflowing combined length must return an error")
	}
}

// TestStreamingEscapeWriter_Write_OverflowingTailFailsClosed pins the
// invariant that Write never computes `s.tailLen + len(p)` into an allocation
// without checking for int overflow first.
func TestStreamingEscapeWriter_Write_OverflowingTailFailsClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		tailLen int
		payload []byte
	}{
		{name: "max_int_tail", tailLen: math.MaxInt, payload: []byte("x")},
		{name: "max_int_minus_one_tail", tailLen: math.MaxInt - 1, payload: []byte("xy")},
		{name: "max_int_minus_two_tail", tailLen: math.MaxInt - 2, payload: []byte("xyz")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sw := NewStreamingEscapeWriter(io.Discard)
			sw.tailLen = tc.tailLen
			assertFailsClosedWithoutPanic(t, func() error {
				_, err := sw.Write(tc.payload)
				return err
			})
		})
	}
}

// TestStreamingEscapeWriter_WriteString_OverflowingTailFailsClosed pins the
// same invariant for the WriteString entry point.
func TestStreamingEscapeWriter_WriteString_OverflowingTailFailsClosed(t *testing.T) {
	t.Parallel()

	sw := NewStreamingEscapeWriter(io.Discard)
	sw.tailLen = math.MaxInt
	assertFailsClosedWithoutPanic(t, func() error {
		_, err := sw.WriteString("x")
		return err
	})
}
