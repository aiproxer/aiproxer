package continuation

import (
	"errors"
	"math"
	"testing"
)

func TestCheckedAddItemCountsBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		a, b int
		want int
		over bool
	}{
		{name: "zero", a: 0, b: 0, want: 0},
		{name: "small", a: 2, b: 3, want: 5},
		{name: "atMaxInt", a: math.MaxInt - 1, b: 1, want: math.MaxInt},
		{name: "overflowLeft", a: math.MaxInt, b: 1, over: true},
		{name: "overflowRight", a: 1, b: math.MaxInt, over: true},
		{name: "overflowBoth", a: math.MaxInt, b: math.MaxInt, over: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkedAddItemCounts(tc.a, tc.b)
			if tc.over {
				if !errors.Is(err, ErrMaterializedItemsExceeded) {
					t.Fatalf("got error %v, want ErrMaterializedItemsExceeded", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}
