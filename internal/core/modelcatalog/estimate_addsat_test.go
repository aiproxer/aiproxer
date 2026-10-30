package modelcatalog

import (
	"math"
	"testing"
)

func TestAddSaturatingInt64_clampsAtMaxInt64(t *testing.T) {
	t.Parallel()
	if got := addSaturatingInt64(math.MaxInt64-3, 10); got != math.MaxInt64 {
		t.Fatalf("overflow clamp: got %d want %d", got, math.MaxInt64)
	}
	if got := addSaturatingInt64(1, 2); got != 3 {
		t.Fatalf("small sum: got %d", got)
	}
	if got := addSaturatingInt64(-5, 10); got != 10 {
		t.Fatalf("negative a treated as zero: got %d", got)
	}
}
