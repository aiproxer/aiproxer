package http

import (
	"fmt"
	"testing"
)

func ExampleCoarsePathGroup() {
	fmt.Println(CoarsePathGroup("/v1/models/list"))
	// Output: /v1
}

func TestCoarsePathGroup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", "/"},
		{"root", "/", "/"},
		{"v1_nested", "/v1/foo", "/v1"},
		{"v1_trailing_slash", "/v1/foo/", "/v1"},
		{"whitespace_trims", "  /chat/completions  ", "/chat"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CoarsePathGroup(tc.in); got != tc.want {
				t.Fatalf("CoarsePathGroup(%q)=%q want %q", tc.in, got, tc.want)
			}
		})
	}
}
