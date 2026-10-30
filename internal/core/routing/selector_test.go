package routing

import (
	"net/url"
	"testing"
)

func TestPrimaryTrimmedParam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		params url.Values
		key    string
		want   string
	}{
		{name: "nil params", key: "provider", want: ""},
		{name: "missing param", params: url.Values{"other": {"value"}}, key: "provider", want: ""},
		{name: "trims first value", params: url.Values{"provider": {"  deepinfra/turbo  "}}, key: "provider", want: "deepinfra/turbo"},
		{name: "uses first value", params: url.Values{"provider": {"a", "b"}}, key: "provider", want: "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := (Primary{Params: tc.params}).TrimmedParam(tc.key)
			if got != tc.want {
				t.Fatalf("TrimmedParam(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}
