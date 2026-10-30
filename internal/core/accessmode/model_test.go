package accessmode

import (
	"errors"
	"testing"
)

func TestNormalizeMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		raw  string
		want Mode
		err  bool
	}{
		{raw: "", want: ModeSingleUser},
		{raw: "   ", want: ModeSingleUser},
		{raw: "single_user", want: ModeSingleUser},
		{raw: "SINGLE_USER", want: ModeSingleUser},
		{raw: "multi_user", want: ModeMultiUser},
		{raw: "Multi_User", want: ModeMultiUser},
		{raw: "solo", err: true},
	} {
		got, err := NormalizeMode(tc.raw)
		if tc.err {
			if err == nil || !errors.Is(err, ErrUnknownAccessMode) {
				t.Fatalf("raw %q: want %v, got %v", tc.raw, ErrUnknownAccessMode, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("raw %q: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("raw %q: want %q got %q", tc.raw, tc.want, got)
		}
	}
}
