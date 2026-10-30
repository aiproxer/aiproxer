package engine_test

import (
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCharacterize_SingleUserMatcherConfiguredAndPrefixPreservation(t *testing.T) {
	t.Parallel()

	t.Run("matcher_unconfigured_defaults_to_preserve_prefix_and_asterisk_mask", func(t *testing.T) {
		t.Parallel()
		env := &countingEnvironment{vals: map[string]string{
			"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
		}}
		src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
			MatcherConfigured: false,
		})
		require.NoError(t, err)
		require.Equal(t, 1, src.EntryCount())

		m, err := src.MatcherResolver().Resolve(t.Context())
		require.NoError(t, err)
		require.NotNil(t, m)

		redacted, findings, err := m.RedactString(t.Context(), testkit.SyntheticOpenAIAPIKey)
		require.NoError(t, err)
		require.Len(t, findings, 1)

		wantMask := "sk-" + strings.Repeat("*", len(testkit.SyntheticOpenAIAPIKey)-len("sk-"))
		assert.Equal(t, wantMask, redacted)
		assert.True(t, strings.HasPrefix(redacted, "sk-"))
	})

	t.Run("matcher_configured_respects_custom_mask_and_prefix_policy", func(t *testing.T) {
		t.Parallel()
		env := &countingEnvironment{vals: map[string]string{
			"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
		}}
		src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
			MatcherConfigured: true,
			Matcher: engine.MatcherOptions{
				PreserveKnownPrefixes: false,
				MaskByte:              '#',
			},
		})
		require.NoError(t, err)
		require.Equal(t, 1, src.EntryCount())

		m, err := src.MatcherResolver().Resolve(t.Context())
		require.NoError(t, err)
		require.NotNil(t, m)

		redacted, findings, err := m.RedactString(t.Context(), testkit.SyntheticOpenAIAPIKey)
		require.NoError(t, err)
		require.Len(t, findings, 1)

		wantMask := strings.Repeat("#", len(testkit.SyntheticOpenAIAPIKey))
		assert.Equal(t, wantMask, redacted)
		assert.False(t, strings.HasPrefix(redacted, "sk-"))
	})
}

func TestCharacterize_SingleUserNilEnvAndEmptyOptions(t *testing.T) {
	t.Parallel()

	src, err := engine.NewSingleUserSource(nil, engine.SingleUserOptions{})
	require.NoError(t, err)
	assert.Equal(t, engine.ModeSingleUser, src.AccessMode())
	assert.Equal(t, 0, src.EntryCount())
	assert.Nil(t, src.SourceCategories())

	resolver := src.MatcherResolver()
	require.NotNil(t, resolver)

	m, err := resolver.Resolve(t.Context())
	require.NoError(t, err)
	require.NotNil(t, m)

	findings, err := m.ScanString(t.Context(), "sample-non-secret-content")
	require.NoError(t, err)
	assert.Nil(t, findings)

	redacted, findings, err := m.RedactString(t.Context(), "sample-non-secret-content")
	require.NoError(t, err)
	assert.Equal(t, "sample-non-secret-content", redacted)
	assert.Nil(t, findings)
}

func TestCharacterize_MultiUserSourceInvariants(t *testing.T) {
	t.Parallel()

	env := &panicEnvironment{}
	src, err := engine.NewMultiUserSource(env)
	require.NoError(t, err)
	assert.Equal(t, engine.ModeMultiUser, src.AccessMode())
	assert.Equal(t, 0, src.EntryCount())
	assert.Equal(t, []string{string(sdk.SourceCategoryRequestCred)}, src.SourceCategories())
	assert.Equal(t, 0, env.calls)

	// Resolve with empty context returns nil, nil
	mEmpty, err := src.MatcherResolver().Resolve(t.Context())
	require.NoError(t, err)
	assert.Nil(t, mEmpty)
	assert.Equal(t, 0, env.calls)

	// Resolve with request matcher context returns bound matcher
	ctxWithMatcher := sdk.WithRequestMatcher(t.Context(), stubSDKMatcher{})
	mBound, err := src.MatcherResolver().Resolve(ctxWithMatcher)
	require.NoError(t, err)
	assert.NotNil(t, mBound)
	assert.Equal(t, 0, env.calls)
}

func TestCharacterize_DisabledSourceInvariants(t *testing.T) {
	t.Parallel()

	src := engine.NewDisabledSource()
	assert.Equal(t, engine.ModeSingleUser, src.AccessMode(), "neutral default posture")
	assert.Equal(t, 0, src.EntryCount())
	assert.Nil(t, src.SourceCategories())

	resolver := src.MatcherResolver()
	require.NotNil(t, resolver)
	m, err := resolver.Resolve(t.Context())
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestCharacterize_NilAndOpaqueMatcherSafety(t *testing.T) {
	t.Parallel()

	t.Run("typed_nil_Matcher_pointer", func(t *testing.T) {
		t.Parallel()
		var nilM *engine.Matcher
		assert.Nil(t, nilM.ScanBytes([]byte("hello")))
		assert.Nil(t, nilM.ScanString("hello"))

		outBytes, fBytes := nilM.RedactBytes([]byte("hello"))
		assert.Equal(t, []byte("hello"), outBytes)
		assert.Nil(t, fBytes)

		outStr, fStr := nilM.RedactString("hello")
		assert.Equal(t, "hello", outStr)
		assert.Nil(t, fStr)
	})

	t.Run("AsMatcher_nil_adapter", func(t *testing.T) {
		t.Parallel()
		sdkMatcher := engine.AsMatcher(nil)
		require.NotNil(t, sdkMatcher)

		findings, err := sdkMatcher.ScanString(t.Context(), "hello")
		require.NoError(t, err)
		assert.Nil(t, findings)

		redacted, findings, err := sdkMatcher.RedactString(t.Context(), "hello")
		require.NoError(t, err)
		assert.Equal(t, "hello", redacted)
		assert.Nil(t, findings)
	})

	t.Run("NewStaticMatcherResolver_nil_catalog", func(t *testing.T) {
		t.Parallel()
		res := engine.NewStaticMatcherResolver(nil, engine.MatcherOptions{})
		require.NotNil(t, res)

		m, err := res.Resolve(t.Context())
		require.NoError(t, err)
		require.NotNil(t, m)

		redacted, findings, err := m.RedactString(t.Context(), "hello")
		require.NoError(t, err)
		assert.Equal(t, "hello", redacted)
		assert.Nil(t, findings)
	})
}
