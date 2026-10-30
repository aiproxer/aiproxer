package proxymemory

import "testing"

func TestRedactPatterns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "an OpenAI-style sk key is redacted",
			in:   "use sk-abcdefghijklmnopqrstuvwx to authorize",
			want: "use [REDACTED_SECRET] to authorize",
		},
		{
			name: "a short sk lookalike is untouched",
			in:   "sk-short",
			want: "sk-short",
		},
		{
			name: "an AWS access key id is redacted",
			in:   "id AKIAIOSFODNN7EXAMPLE end",
			want: "id [REDACTED_SECRET] end",
		},
		{
			name: "a Bearer token is redacted",
			in:   "Authorization: Bearer abcdefghijklmnop12345",
			want: "Authorization: Bearer [REDACTED_SECRET]",
		},
		{
			name: "Bearer matching is case-insensitive and canonicalized",
			in:   "authorization: BEARER abcdefghijklmnop12345",
			want: "authorization: Bearer [REDACTED_SECRET]",
		},
		{
			name: "a Bearer token with base64 padding is redacted",
			in:   "Bearer abcdefghijklmnop==",
			want: "Bearer [REDACTED_SECRET]",
		},
		{
			name: "an api_key assignment is redacted",
			in:   `api_key = "abcdefgh"`,
			want: `api_key = "[REDACTED_SECRET]"`,
		},
		{
			name: "a single-quoted secret assignment is redacted",
			in:   "secret = 'abcdefgh'",
			want: "secret = '[REDACTED_SECRET]'",
		},
		{
			name: "a password assignment is redacted",
			in:   "password: hunter2hunter2",
			want: "password: [REDACTED_SECRET]",
		},
		{
			name: "the keyword case is preserved",
			in:   "PASSWORD: hunter2hunter2",
			want: "PASSWORD: [REDACTED_SECRET]",
		},
		{
			name: "a short assignment value is untouched",
			in:   "token: abc",
			want: "token: abc",
		},
		{
			name: "a plain sentence is untouched",
			in:   "a plain sentence about project layout",
			want: "a plain sentence about project layout",
		},
		{
			name: "the plural word secrets is untouched",
			in:   "the secrets of the craft",
			want: "the secrets of the craft",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Redact(tc.in); got != tc.want {
				t.Errorf("Redact(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
