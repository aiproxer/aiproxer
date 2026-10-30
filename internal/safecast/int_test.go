package safecast

import (
	"math"
	"testing"
)

func TestIntFromInt64Clamp_RoundTrip(t *testing.T) {
	t.Parallel()
	if got := IntFromInt64Clamp(42); got != 42 {
		t.Fatalf("got %d", got)
	}
}

func TestIntFromInt64Clamp_ClampMax(t *testing.T) {
	t.Parallel()
	if got := IntFromInt64Clamp(math.MaxInt64); got != math.MaxInt {
		t.Fatalf("got %d want %d", got, math.MaxInt)
	}
}

func TestIntFromInt64Clamp_ClampMin(t *testing.T) {
	t.Parallel()
	if got := IntFromInt64Clamp(math.MinInt64); got != math.MinInt {
		t.Fatalf("got %d want %d", got, math.MinInt)
	}
}

func TestInt32FromIntClamp_Identity(t *testing.T) {
	t.Parallel()
	if got := Int32FromIntClamp(42); got != 42 {
		t.Fatalf("got %d", got)
	}
}

func TestInt32FromIntClamp_Max(t *testing.T) {
	t.Parallel()
	if got := Int32FromIntClamp(math.MaxInt32 + 1); got != math.MaxInt32 {
		t.Fatalf("got %d want %d", got, math.MaxInt32)
	}
}

func TestInt32FromIntClamp_Min(t *testing.T) {
	t.Parallel()
	if got := Int32FromIntClamp(math.MinInt32 - 1); got != math.MinInt32 {
		t.Fatalf("got %d want %d", got, math.MinInt32)
	}
}
