package engine

import "testing"

func TestIsPopularSecretEnvName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		env  string
		want bool
	}{
		{name: "generic_api_key", env: "CONTEXT7_API_KEY", want: true},
		{name: "generic_token", env: "APIFY_TOKEN", want: true},
		{name: "exact_fallback", env: "STRIPE_SECRET_KEY", want: true},
		{name: "proxy_shaped_still_popular_name", env: "OPENAI_API_KEY", want: true},
		{name: "public_next", env: "NEXT_PUBLIC_FOO_API_KEY", want: false},
		{name: "public_vite", env: "VITE_API_KEY", want: false},
		{name: "public_public", env: "PUBLIC_TOKEN", want: false},
		{name: "public_expo", env: "EXPO_PUBLIC_API_KEY", want: false},
		{name: "public_react", env: "REACT_APP_API_KEY", want: false},
		{name: "public_gatsby", env: "GATSBY_TOKEN", want: false},
		{name: "public_nuxt", env: "NUXT_PUBLIC_API_KEY", want: false},
		{name: "public_vue", env: "VUE_APP_TOKEN", want: false},
		{name: "uppercase_api_key", env: "ACME_API_KEY", want: true},
		{name: "uppercase_token", env: "ACME_TOKEN", want: true},
		{name: "lowercase", env: "acme_api_key", want: false},
		{name: "mixed_case", env: "Acme_API_KEY", want: false},
		{name: "lowercase_suffix", env: "ACME_api_key", want: false},
		{name: "suffix_with_trailer", env: "ACME_API_KEY_BACKUP", want: false},
		{name: "bare_key_suffix", env: "ACME_KEY", want: false},
		{name: "unrelated_secret", env: "FOO_SECRET", want: false},
		{name: "empty", env: "", want: false},
		{name: "exclude_csrf_token", env: "CSRF_TOKEN", want: false},
		{name: "exclude_xsrf_token", env: "XSRF_TOKEN", want: false},
		{name: "exclude_crsf_token", env: "CRSF_TOKEN", want: false},
		{name: "exclude_anti_csrf_token", env: "ANTI_CSRF_TOKEN", want: false},
		{name: "exclude_app_xsrf_token", env: "APP_XSRF_TOKEN", want: false},
		{name: "substring_mycsrf_still_inferred", env: "MYCSRF_TOKEN", want: true},
		{name: "substring_xsrfish_still_inferred", env: "XSRFISH_TOKEN", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isPopularSecretEnvName(tc.env); got != tc.want {
				t.Fatalf("isPopularSecretEnvName(%q)=%v want %v", tc.env, got, tc.want)
			}
		})
	}
}
