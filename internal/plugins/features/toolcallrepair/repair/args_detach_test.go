package repair_test

import (
	"bytes"
	"testing"
)

// assertArgsDetached proves out does not alias input: mutating out must leave input unchanged.
func assertArgsDetached(t *testing.T, input, out []byte) {
	t.Helper()
	if len(out) == 0 {
		t.Fatal("expected non-empty ArgsJSON for detachment check")
	}
	orig := append([]byte(nil), input...)
	out[0] ^= 0xff
	if !bytes.Equal(input, orig) {
		t.Fatal("ArgsJSON aliases caller/input bytes")
	}
	out[0] ^= 0xff
}
