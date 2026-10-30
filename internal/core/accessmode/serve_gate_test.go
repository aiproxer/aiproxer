package accessmode

import (
	"errors"
	"sort"
	"testing"
)

func TestValidateServeModeGate(t *testing.T) {
	t.Parallel()
	flagTrue := true
	flagFalse := false
	cases := map[string]struct {
		mode Mode
		flag *bool
		want error
	}{
		"single_user_no_flag":             {mode: ModeSingleUser, flag: nil, want: nil},
		"single_user_flag_false":          {mode: ModeSingleUser, flag: &flagFalse, want: nil},
		"single_user_flag_true":           {mode: ModeSingleUser, flag: &flagTrue, want: ErrMultiUserFlagInconsistent},
		"multi_user_no_flag":              {mode: ModeMultiUser, flag: nil, want: ErrMultiUserFlagRequired},
		"multi_user_flag_false":           {mode: ModeMultiUser, flag: &flagFalse, want: ErrMultiUserFlagRequired},
		"multi_user_flag_true":            {mode: ModeMultiUser, flag: &flagTrue, want: nil},
		"empty_mode_defaults_single_user": {mode: "", flag: nil, want: nil},
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		tc := cases[name]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := ValidateServeModeGate(tc.mode, tc.flag)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if err == nil || !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}
