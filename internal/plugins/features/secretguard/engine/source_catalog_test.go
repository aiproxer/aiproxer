package engine_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/plugins/features/secretguard/engine"
	"github.com/aiproxer/aiproxer/internal/testkit"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
)

type countingEnvironment struct {
	lookupCalls   int
	snapshotCalls int
	vals          map[string]string
}

func (c *countingEnvironment) Lookup(name string) (string, bool) {
	c.lookupCalls++
	v, ok := c.vals[name]
	return v, ok
}

func (c *countingEnvironment) Snapshot() []string {
	c.snapshotCalls++
	out := make([]string, 0, len(c.vals))
	for k, v := range c.vals {
		out = append(out, k+"="+v)
	}
	return out
}

func TestNewSingleUserSource_sparseProxyNames(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":   testkit.SyntheticOpenAIAPIKey,
		"OPENAI_API_KEY_2": testkit.SyntheticOpenRouterAPIKey,
		"OPENAI_API_KEY_7": testkit.SyntheticGeminiAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: false,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() < 3 {
		t.Fatalf("want at least 3 sparse proxy entries, got %d", src.EntryCount())
	}
	if env.snapshotCalls == 0 {
		t.Fatal("single-user source must Snapshot the environment")
	}
	if src.AccessMode() != engine.ModeSingleUser {
		t.Fatalf("access mode: got %v", src.AccessMode())
	}
}

func TestNewSingleUserSource_minSecretBytesExcludesShort(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":             testkit.SyntheticOpenAIAPIKey,
		"AIP_TEST_SECRETGUARD_SHORT": testkit.SyntheticShortSecret,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludeEnv:     []string{"AIP_TEST_SECRETGUARD_SHORT"},
		MinSecretBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 1 {
		t.Fatalf("short secrets below min_secret_bytes must be excluded; entry count=%d", src.EntryCount())
	}
}

func TestNewSingleUserSource_duplicateValueAliases(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":     testkit.SyntheticDuplicateValueAliasA,
		"ANTHROPIC_API_KEY":  testkit.SyntheticDuplicateValueAliasB,
		"OPENROUTER_API_KEY": testkit.SyntheticOpenRouterAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		MinSecretBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 2 {
		t.Fatalf("duplicate values must dedupe; EntryCount=%d want 2", src.EntryCount())
	}
}

func TestNewSingleUserSource_PopularIncludeExclude(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"GITHUB_TOKEN":      testkit.SyntheticAnthropicSecretGuardKey,
		"AWS_ACCESS_KEY_ID": "AKIA" + strings.Repeat("X", 16),
		"AIP_TEST_OPERATOR": testkit.SyntheticGeminiAPIKey,
		"NPM_TOKEN":         testkit.SyntheticOpenAIAPIKey,
		"OPENAI_API_KEY":    testkit.SyntheticOpenRouterAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{"AIP_TEST_OPERATOR"},
		ExcludeEnv:        []string{"NPM_TOKEN"},
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	// OPENAI_API_KEY + GITHUB_TOKEN + AIP_TEST_OPERATOR; NPM_TOKEN excluded; AWS_ACCESS_KEY_ID not auto-loaded
	if src.EntryCount() != 3 {
		t.Fatalf("EntryCount=%d want 3 (exclude wins; AWS_ACCESS_KEY_ID not popular)", src.EntryCount())
	}
}

func TestNewSingleUserSource_AWSAccessKeyIDNotAutoLoaded(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"AWS_ACCESS_KEY_ID":     "AKIA" + strings.Repeat("Y", 16),
		"AWS_SECRET_ACCESS_KEY": testkit.SyntheticOpenAIAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 1 {
		t.Fatalf("only AWS_SECRET_ACCESS_KEY should load; EntryCount=%d", src.EntryCount())
	}
}

type includeLookupEnv struct {
	lookupCalls   int
	snapshotCalls int
	snapshotVals  map[string]string
	lookupOnly    map[string]string
}

func (e *includeLookupEnv) Snapshot() []string {
	e.snapshotCalls++
	out := make([]string, 0, len(e.snapshotVals))
	for k, v := range e.snapshotVals {
		out = append(out, k+"="+v)
	}
	return out
}

func (e *includeLookupEnv) Lookup(name string) (string, bool) {
	e.lookupCalls++
	if v, ok := e.snapshotVals[name]; ok {
		return v, true
	}
	v, ok := e.lookupOnly[name]
	return v, ok
}

func TestNewSingleUserSource_IncludeEnvLookupWhenAbsentFromSnapshot(t *testing.T) {
	t.Parallel()
	env := &includeLookupEnv{
		snapshotVals: map[string]string{
			"OPENAI_API_KEY": testkit.SyntheticOpenAIAPIKey,
		},
		lookupOnly: map[string]string{
			"AIP_TEST_SECRETGUARD_INCLUDE": testkit.SyntheticGeminiAPIKey,
		},
	}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludeEnv:     []string{"AIP_TEST_SECRETGUARD_INCLUDE"},
		MinSecretBytes: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 2 {
		t.Fatalf("IncludeEnv via Lookup must add entry; EntryCount=%d", src.EntryCount())
	}
	if env.lookupCalls == 0 {
		t.Fatal("IncludeEnv missing from snapshot must call Lookup")
	}
}

type stubSDKMatcher struct{}

func (stubSDKMatcher) ScanBytes(context.Context, []byte) ([]sdk.Finding, error) {
	return nil, nil
}

func (stubSDKMatcher) ScanString(context.Context, string) ([]sdk.Finding, error) {
	return nil, nil
}

func (stubSDKMatcher) RedactBytes(context.Context, []byte) ([]byte, []sdk.Finding, error) {
	return nil, nil, nil
}

func (stubSDKMatcher) RedactString(context.Context, string) (string, []sdk.Finding, error) {
	return "", nil, nil
}

func TestNewDisabledSource_neverCallsEnvironment(t *testing.T) {
	t.Parallel()
	src := engine.NewDisabledSource()
	if src.EntryCount() != 0 {
		t.Fatalf("EntryCount=%d want 0", src.EntryCount())
	}
	resolver := src.MatcherResolver()
	if resolver == nil {
		t.Fatal("disabled source must provide a MatcherResolver")
	}
	got, err := resolver.Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatal("disabled resolver must return (nil, nil)")
	}
}

func TestNewMultiUserSource_ContextMatcherResolver(t *testing.T) {
	t.Parallel()
	env := &panicEnvironment{}
	src, err := engine.NewMultiUserSource(env)
	if err != nil {
		t.Fatal(err)
	}
	if src.AccessMode() != engine.ModeMultiUser {
		t.Fatalf("access mode: got %v", src.AccessMode())
	}
	if src.EntryCount() != 0 {
		t.Fatalf("EntryCount=%d want 0", src.EntryCount())
	}

	ctx := sdk.WithRequestMatcher(t.Context(), stubSDKMatcher{})
	got, err := src.MatcherResolver().Resolve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("multi-user resolver must return context matcher")
	}
}

func TestPopularSecretEnvNames_excludesIDsAndPaths(t *testing.T) {
	t.Parallel()
	forbidden := map[string]struct{}{
		"AWS_ACCESS_KEY_ID":              {},
		"AWS_PROFILE":                    {},
		"GOOGLE_APPLICATION_CREDENTIALS": {},
		"CURL_CA_BUNDLE":                 {},
	}
	for _, name := range engine.PopularSecretEnvNames {
		if _, bad := forbidden[name]; bad {
			t.Fatalf("PopularSecretEnvNames must not include %q", name)
		}
	}
	if !slices.Contains(engine.PopularSecretEnvNames, "AWS_SECRET_ACCESS_KEY") {
		t.Fatal("PopularSecretEnvNames must include AWS_SECRET_ACCESS_KEY")
	}
}

func TestPopularSecretEnvNames_uniqueNonEmpty(t *testing.T) {
	t.Parallel()
	if len(engine.PopularSecretEnvNames) == 0 {
		t.Fatal("PopularSecretEnvNames must be non-empty")
	}
	seen := make(map[string]struct{}, len(engine.PopularSecretEnvNames))
	for _, name := range engine.PopularSecretEnvNames {
		if name == "" {
			t.Fatal("PopularSecretEnvNames must not contain empty names")
		}
		if _, dup := seen[name]; dup {
			t.Fatalf("PopularSecretEnvNames duplicate %q", name)
		}
		seen[name] = struct{}{}
	}
}

func TestNewSingleUserSource_genericPopularAPIKeyAndToken(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"CONTEXT7_API_KEY": testkit.SyntheticOpenAIAPIKey,
		"APIFY_TOKEN":      testkit.SyntheticOpenRouterAPIKey,
		"TAVILY_API_KEY":   testkit.SyntheticGeminiAPIKey,
		"FOO_SECRET":       testkit.SyntheticAnthropicSecretGuardKey,
		"MY_PASSWORD":      testkit.SyntheticBearerCredential,
		"BAR_KEY":          testkit.SyntheticDuplicateValueAliasA,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 3 {
		t.Fatalf("EntryCount=%d want 3 (generic _API_KEY/_TOKEN only)", src.EntryCount())
	}
	if !slices.Contains(src.SourceCategories(), string(sdk.SourceCategoryPopularEnv)) {
		t.Fatalf("categories=%v want popular_env", src.SourceCategories())
	}

	m, err := src.MatcherResolver().Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		testkit.SyntheticOpenAIAPIKey,
		testkit.SyntheticOpenRouterAPIKey,
		testkit.SyntheticGeminiAPIKey,
	} {
		findings, scanErr := m.ScanString(t.Context(), value)
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		if len(findings) != 1 {
			t.Fatalf("findings=%d want 1", len(findings))
		}
		if findings[0].SourceCategory != sdk.SourceCategoryPopularEnv {
			t.Fatalf("category=%q want popular_env", findings[0].SourceCategory)
		}
	}
	for _, value := range []string{
		testkit.SyntheticAnthropicSecretGuardKey,
		testkit.SyntheticBearerCredential,
		testkit.SyntheticDuplicateValueAliasA,
	} {
		findings, scanErr := m.ScanString(t.Context(), value)
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		if len(findings) != 0 {
			t.Fatalf("unrelated suffix must not load; findings=%d", len(findings))
		}
	}
}

func TestNewSingleUserSource_includePopularEnvFalseDoesNotInfer(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":   testkit.SyntheticOpenAIAPIKey,
		"CONTEXT7_API_KEY": testkit.SyntheticGeminiAPIKey,
		"APIFY_TOKEN":      testkit.SyntheticOpenRouterAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: false,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 1 {
		t.Fatalf("EntryCount=%d want 1 (proxy only; no popular inference)", src.EntryCount())
	}
	if !slices.Contains(src.SourceCategories(), string(sdk.SourceCategoryProxyEnv)) {
		t.Fatalf("categories=%v want proxy_env", src.SourceCategories())
	}
	if slices.Contains(src.SourceCategories(), string(sdk.SourceCategoryPopularEnv)) {
		t.Fatalf("categories=%v must not include popular_env when IncludePopularEnv is false", src.SourceCategories())
	}

	m, err := src.MatcherResolver().Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	proxyFindings, err := m.ScanString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxyFindings) != 1 || proxyFindings[0].SourceCategory != sdk.SourceCategoryProxyEnv {
		t.Fatalf("OPENAI_API_KEY must remain proxy_env; findings=%d category=%q",
			len(proxyFindings), categoryOrEmpty(proxyFindings))
	}
	for _, value := range []string{
		testkit.SyntheticGeminiAPIKey,
		testkit.SyntheticOpenRouterAPIKey,
	} {
		findings, scanErr := m.ScanString(t.Context(), value)
		if scanErr != nil {
			t.Fatal(scanErr)
		}
		if len(findings) != 0 {
			t.Fatalf("IncludePopularEnv false must not infer popular secrets; findings=%d", len(findings))
		}
	}
}

func TestNewSingleUserSource_includeEnvOverridesPublicPrefixExclusion(t *testing.T) {
	t.Parallel()
	const name = "NEXT_PUBLIC_SERVICE_API_KEY"
	env := &countingEnvironment{vals: map[string]string{
		name: testkit.SyntheticOpenAIAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{name},
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 1 {
		t.Fatalf("IncludeEnv must load public-prefixed name; EntryCount=%d", src.EntryCount())
	}
	m, err := src.MatcherResolver().Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	findings, err := m.ScanString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings=%d want 1", len(findings))
	}
	if findings[0].SourceCategory != sdk.SourceCategoryOperatorEnv {
		t.Fatalf("category=%q want operator_env", findings[0].SourceCategory)
	}
}

func TestNewSingleUserSource_csrfTokenExcludedUnlessIncludeEnv(t *testing.T) {
	t.Parallel()
	const name = "CSRF_TOKEN"
	env := &countingEnvironment{vals: map[string]string{
		name: testkit.SyntheticOpenAIAPIKey,
	}}

	excluded, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if excluded.EntryCount() != 0 {
		t.Fatalf("CSRF_TOKEN must not load via popular inference; EntryCount=%d", excluded.EntryCount())
	}

	included, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		IncludeEnv:        []string{name},
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if included.EntryCount() != 1 {
		t.Fatalf("IncludeEnv must load CSRF_TOKEN; EntryCount=%d", included.EntryCount())
	}
	m, err := included.MatcherResolver().Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	findings, err := m.ScanString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings=%d want 1", len(findings))
	}
	if findings[0].SourceCategory != sdk.SourceCategoryOperatorEnv {
		t.Fatalf("category=%q want operator_env", findings[0].SourceCategory)
	}
}

func TestNewSingleUserSource_excludeEnvWinsOverPopularInference(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"CONTEXT7_API_KEY": testkit.SyntheticOpenAIAPIKey,
		"APIFY_TOKEN":      testkit.SyntheticOpenRouterAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		ExcludeEnv:        []string{"CONTEXT7_API_KEY", "APIFY_TOKEN"},
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 0 {
		t.Fatalf("ExcludeEnv must win over popular inference; EntryCount=%d", src.EntryCount())
	}
}

func TestNewSingleUserSource_proxyCredentialStaysProxyEnvWithPopularInference(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"OPENAI_API_KEY":   testkit.SyntheticOpenAIAPIKey,
		"CONTEXT7_API_KEY": testkit.SyntheticGeminiAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 2 {
		t.Fatalf("EntryCount=%d want 2", src.EntryCount())
	}
	cats := src.SourceCategories()
	if !slices.Contains(cats, string(sdk.SourceCategoryProxyEnv)) {
		t.Fatalf("categories=%v want proxy_env", cats)
	}
	if !slices.Contains(cats, string(sdk.SourceCategoryPopularEnv)) {
		t.Fatalf("categories=%v want popular_env", cats)
	}

	m, err := src.MatcherResolver().Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	proxyFindings, err := m.ScanString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(proxyFindings) != 1 || proxyFindings[0].SourceCategory != sdk.SourceCategoryProxyEnv {
		t.Fatalf("OPENAI_API_KEY must remain proxy_env; findings=%d category=%q",
			len(proxyFindings), categoryOrEmpty(proxyFindings))
	}
	popularFindings, err := m.ScanString(t.Context(), testkit.SyntheticGeminiAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(popularFindings) != 1 || popularFindings[0].SourceCategory != sdk.SourceCategoryPopularEnv {
		t.Fatalf("CONTEXT7_API_KEY must be popular_env; findings=%d category=%q",
			len(popularFindings), categoryOrEmpty(popularFindings))
	}
}

func TestNewSingleUserSource_exactPopularFallbackStillLoads(t *testing.T) {
	t.Parallel()
	env := &countingEnvironment{vals: map[string]string{
		"STRIPE_SECRET_KEY":     testkit.SyntheticOpenAIAPIKey,
		"AZURE_CLIENT_SECRET":   testkit.SyntheticOpenRouterAPIKey,
		"AWS_SECRET_ACCESS_KEY": testkit.SyntheticGeminiAPIKey,
	}}
	src, err := engine.NewSingleUserSource(env, engine.SingleUserOptions{
		IncludePopularEnv: true,
		MinSecretBytes:    8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if src.EntryCount() != 3 {
		t.Fatalf("exact PopularSecretEnvNames fallback EntryCount=%d want 3", src.EntryCount())
	}
	m, err := src.MatcherResolver().Resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	findings, err := m.ScanString(t.Context(), testkit.SyntheticOpenAIAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].SourceCategory != sdk.SourceCategoryPopularEnv {
		t.Fatalf("exact fallback must be popular_env; findings=%d category=%q",
			len(findings), categoryOrEmpty(findings))
	}
}

func categoryOrEmpty(findings []sdk.Finding) sdk.SourceCategory {
	if len(findings) == 0 {
		return ""
	}
	return findings[0].SourceCategory
}
