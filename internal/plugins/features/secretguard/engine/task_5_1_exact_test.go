package engine_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTask51_SingleUserExactCatalogPreservesInventoryAndMatcher records the
// existing exact local protection contract in one end-to-end engine fixture:
// proxy and popular names, operator includes, exclusions, minimum length,
// case-sensitive matching, known-prefix preservation, and the configured mask.
func TestTask51_SingleUserExactCatalogPreservesInventoryAndMatcher(t *testing.T) {
	t.Parallel()

	const opaque = "Opaque-Local-Secret-2026"
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":      testkit.SyntheticOpenAIAPIKey,
		"GITHUB_TOKEN":        "github-popular-token-2026",
		"OPAQUE_LOCAL_SECRET": opaque,
		"NPM_TOKEN":           "excluded-popular-token-2026",
		"SHORT_SECRET":        "short",
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{"OPAQUE_LOCAL_SECRET", "SHORT_SECRET"},
		ExcludeEnv:        []string{"NPM_TOKEN"},
		MinSecretBytes:    8,
		MatcherConfigured: true,
		Matcher: engine.MatcherOptions{
			PreserveKnownPrefixes: true,
			MaskByte:              '#',
		},
	})
	require.NoError(t, err)
	require.Equal(t, 3, src.EntryCount(), "proxy + popular + operator entries")
	assert.Equal(t, 1, env.snapshotCalls)
	assert.Contains(t, src.SourceCategories(), "proxy_env")
	assert.Contains(t, src.SourceCategories(), "popular_env")
	assert.Contains(t, src.SourceCategories(), "operator_env")

	matcher, err := src.MatcherResolver().Resolve(t.Context())
	require.NoError(t, err)
	require.NotNil(t, matcher)

	findings, err := matcher.ScanString(t.Context(), "secret="+opaque)
	require.NoError(t, err)
	assert.Len(t, findings, 1, "opaque environment values remain exact-protected")

	findings, err = matcher.ScanString(t.Context(), "secret="+strings.ToLower(opaque))
	require.NoError(t, err)
	assert.Empty(t, findings, "exact matching remains case-sensitive")

	findings, err = matcher.ScanString(t.Context(), "secret=excluded-popular-token-2026")
	require.NoError(t, err)
	assert.Empty(t, findings, "excluded popular names must not enter the catalog")

	findings, err = matcher.ScanString(t.Context(), "secret=short")
	require.NoError(t, err)
	assert.Empty(t, findings, "values below minimum length must not enter the catalog")

	redacted, findings, err := matcher.RedactString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, "sk-"+strings.Repeat("#", len(testkit.SyntheticOpenAIAPIKey)-len("sk-")), redacted)

	redacted, findings, err = matcher.RedactString(t.Context(), opaque)
	require.NoError(t, err)
	require.Len(t, findings, 1)
	assert.Equal(t, strings.Repeat("#", len(opaque)), redacted)
}
