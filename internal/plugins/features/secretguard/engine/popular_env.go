package engine

import (
	"slices"
	"strings"
)

// PopularSecretEnvNames is the curated exact-name registry of common non-proxy
// credential environment variables loaded when SingleUserOptions.IncludePopularEnv
// is true. It remains the exact fallback for secret names not covered by generic
// `_API_KEY` / `_TOKEN` inference. Credential *paths*, *ids*, and profile names
// are intentionally omitted.
//
// Explicitly not included (unless listed in IncludeEnv):
//   - AWS_ACCESS_KEY_ID
//   - AWS_PROFILE
//   - GOOGLE_APPLICATION_CREDENTIALS
//   - CURL_CA_BUNDLE
var PopularSecretEnvNames = []string{
	"AWS_SECRET_ACCESS_KEY",
	"AWS_SESSION_TOKEN",
	"GITHUB_TOKEN",
	"GH_TOKEN",
	"GITLAB_TOKEN",
	"NPM_TOKEN",
	"PYPI_TOKEN",
	"SLACK_BOT_TOKEN",
	"STRIPE_SECRET_KEY",
	"AZURE_CLIENT_SECRET",
	"GOOGLE_API_KEY",
	"DIGITALOCEAN_ACCESS_TOKEN",
	"TWILIO_AUTH_TOKEN",
	"SENDGRID_API_KEY",
	"HEROKU_API_KEY",
	"CLOUDFLARE_API_TOKEN",
	"DATADOG_API_KEY",
	"SENTRY_AUTH_TOKEN",
	"TERRAFORM_TOKEN",
	"VAULT_TOKEN",
}

// Frontend-bundled public env prefixes are never inferred as popular secrets.
// Explicit IncludeEnv can still load these names; ExcludeEnv still wins.
var frontendPublicEnvPrefixes = []string{
	"NEXT_PUBLIC_",
	"VITE_",
	"PUBLIC_",
	"EXPO_PUBLIC_",
	"REACT_APP_",
	"GATSBY_",
	"NUXT_PUBLIC_",
	"VUE_APP_",
}

// Anti-CSRF / anti-XSRF token names are never inferred as popular secrets.
// Matching is underscore-delimited segment equality only (not substring).
// Explicit IncludeEnv can still load these names; ExcludeEnv still wins.
var antiCSRFEnvSegments = []string{"CSRF", "XSRF", "CRSF"}

// isPopularSecretEnvName reports whether name should be loaded as popular_env when
// IncludePopularEnv is true. Exact PopularSecretEnvNames always match. Otherwise
// uppercase names ending in _API_KEY or _TOKEN match unless they use a frontend
// public prefix or contain an anti-CSRF segment. Proxy credential category
// precedence is enforced by inventory.
func isPopularSecretEnvName(name string) bool {
	if name == "" {
		return false
	}
	if slices.Contains(PopularSecretEnvNames, name) {
		return true
	}
	if !isUpperSnakeEnvName(name) {
		return false
	}
	for _, prefix := range frontendPublicEnvPrefixes {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	if hasAntiCSRFEnvSegment(name) {
		return false
	}
	return strings.HasSuffix(name, "_API_KEY") || strings.HasSuffix(name, "_TOKEN")
}

func hasAntiCSRFEnvSegment(name string) bool {
	for part := range strings.SplitSeq(name, "_") {
		if slices.Contains(antiCSRFEnvSegments, part) {
			return true
		}
	}
	return false
}

func isUpperSnakeEnvName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}
