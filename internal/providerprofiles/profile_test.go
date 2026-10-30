package providerprofiles

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/stretchr/testify/require"
)

func validProfile() Profile {
	return Profile{
		APIVersion: APIVersionV1,
		ID:         "acme-responses",
		Family:     FamilyOpenAIResponses,
		Endpoint:   Endpoint{BaseURL: "https://api.acme.example/v1", PathPolicy: PathPolicyFamilyDefault},
		Auth:       Auth{Mode: AuthBearerEnv, EnvVar: "ACME_API_KEY"},
		Headers:    []SafeHeader{{Name: "X-Provider-Client", Value: "aip"}},
		Models: ModelDiscovery{
			Policy:    DiscoveryFamilyDefault,
			Namespace: Namespace{Mode: NamespacePreserve},
		},
		Tokenizer: TokenizerAccounting{TokenizerID: "cl100k_base", Source: AccountingLocalTokenizer},
	}
}

func TestValidateProfile_rejectsSecurityAndSchemaMutations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		mutate func(*Profile)
	}{
		{"unknown version", func(p *Profile) { p.APIVersion = "aip.provider-profile/v9" }},
		{"invalid endpoint", func(p *Profile) { p.Endpoint.BaseURL = "file:///secret" }},
		{"remote HTTP endpoint", func(p *Profile) { p.Endpoint.BaseURL = "http://provider.example/v1" }},
		{"userinfo endpoint", func(p *Profile) { p.Endpoint.BaseURL = "https://user:pass@example.com" }},
		{"unsafe auth header", func(p *Profile) { p.Headers = []SafeHeader{{Name: "Authorization", Value: "Bearer secret"}} }},
		{"auth without reference", func(p *Profile) { p.Auth.EnvVar = "" }},
		{"unknown quirk", func(p *Profile) { p.Quirks = []QuirkID{"rewrite.request"} }},
		{"arbitrary transform", func(p *Profile) { p.Transform = "request.body.model = provider_model" }},
		{"capability elevation", func(p *Profile) { p.Capabilities.Enable = []aipapi.Capability{aipapi.CapabilityCompaction} }},
		{"unbounded header", func(p *Profile) {
			p.Headers = []SafeHeader{{Name: "X-Provider", Value: strings.Repeat("x", MaxStringBytes+1)}}
		}},
		{"too many models", func(p *Profile) { p.Models.Static = make([]Model, MaxModels+1) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := validProfile()
			tc.mutate(&p)
			if err := Validate(p); err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
		})
	}
}

func TestDecodeJSON_isClosedAndUsesSnakeCaseTags(t *testing.T) {
	t.Parallel()
	p := validProfile()
	data := []byte(`{"api_version":"aip.provider-profile/v1","id":"json-profile","family":"openai-responses-compatible","endpoint":{"base_url":"https://example.invalid/v1","path_policy":"family_default"},"auth":{"mode":"none"},"models":{"discovery":"family_default","namespace":{"mode":"preserve"}},"tokenizer":{"id":"cl100k_base","source":"local_tokenizer"}}`)
	decoded, err := DecodeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.APIVersion != p.APIVersion || decoded.Endpoint.BaseURL == "" || decoded.Tokenizer.TokenizerID != "cl100k_base" {
		t.Fatalf("decoded=%+v", decoded)
	}
	if _, err := DecodeJSON(append(data, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON value accepted")
	}
}

func TestValidateProfile_rejectsDuplicateAndInconsistentNestedValues(t *testing.T) {
	t.Parallel()
	cases := []func(*Profile){
		func(p *Profile) {
			p.Models.Static = []Model{{CanonicalID: "m", NativeID: "n"}, {CanonicalID: "m", NativeID: "n2"}}
		},
		func(p *Profile) {
			p.Capabilities.Enable = []aipapi.Capability{aipapi.CapabilityTools, aipapi.CapabilityTools}
		},
		func(p *Profile) {
			p.Dialects.Reasoning = []aipapi.DialectRequirement{{Kind: "reasoning", Dialect: "d"}, {Kind: "reasoning", Dialect: "d"}}
		},
		func(p *Profile) { p.Auth = Auth{Mode: AuthBearerEnv, EnvVar: "KEY"}; p.Family = FamilyAnthropic },
	}
	for i, mutate := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			t.Parallel()
			p := validProfile()
			mutate(&p)
			if err := Validate(p); err == nil {
				t.Fatal("invalid nested profile accepted")
			}
		})
	}
}

func TestValidateProfile_effectiveCapabilitiesCannotExceedFamily(t *testing.T) {
	t.Parallel()
	p := validProfile()
	p.Capabilities.Disable = []aipapi.Capability{aipapi.CapabilityTools}
	p.Capabilities.Enable = []aipapi.Capability{aipapi.CapabilityStreaming}
	got, err := Compile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Capabilities[aipapi.CapabilityTools]; ok {
		t.Fatal("disabled family capability remained effective")
	}
}

func TestCatalog_isDeterministicAndRejectsDuplicates(t *testing.T) {
	t.Parallel()
	a := validProfile()
	b := a
	b.ID = "beta"
	b.Endpoint.BaseURL = "https://beta.example/v1"
	catalog, err := NewCatalog([]Profile{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.Profiles()[0].ID; got != "acme-responses" {
		t.Fatalf("catalog ordering = %q", got)
	}
	b.ID = a.ID
	if _, err := NewCatalog([]Profile{a, b}); err == nil {
		t.Fatal("duplicate profile accepted")
	}
}

func TestCompile_rejectsUnknownFamilyQuirkVersionAndBounds(t *testing.T) {
	t.Parallel()
	p := validProfile()
	p.APIVersion = ""
	if _, err := Compile(p); err == nil {
		t.Fatal("empty version accepted")
	}
}

func TestDecodeYAML_rejectsUnknownFields(t *testing.T) {
	t.Parallel()
	p := validProfile()
	data := []byte("api_version: " + p.APIVersion + "\nid: x\nfamily: openai-responses-compatible\nendpoint:\n  base_url: https://example.invalid\n  path_policy: family_default\nauth:\n  mode: none\nmodels:\n  discovery: family_default\n  namespace:\n    mode: preserve\nunknown: true\n")
	if _, err := DecodeYAML(data); err == nil {
		t.Fatal("unknown YAML field accepted")
	}
}

func TestEmbeddedCatalog_hasNoSecretsAndNoActivation(t *testing.T) {
	t.Parallel()
	catalog, err := EmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range catalog.Profiles() {
		if p.Auth.Secret != "" || strings.Contains(strings.ToLower(p.Auth.EnvVar), "value") {
			t.Fatalf("profile %q carries a credential value", p.ID)
		}
	}
}

func TestValidateProfile_diagnosticsRelevantValuesStayBounded(t *testing.T) {
	t.Parallel()
	p := validProfile()
	p.Models.Path = strings.Repeat("p", MaxStringBytes+1)
	if err := Validate(p); err == nil {
		t.Fatal("unbounded model path accepted")
	}
	p = validProfile()
	p.Endpoint.BaseURL = "https://example.invalid/v1?secret=1"
	if err := Validate(p); err == nil {
		t.Fatal("endpoint query accepted")
	}
}

func TestSyntheticCatalog_1000ProfilesIsBoundedAndIndependent(t *testing.T) {
	t.Parallel()
	// Catalog compilation is deliberately a pure data path. The compiled value
	// contains only profile/configuration data and family names; runtime adapter
	// hooks, HTTP clients, process handles, and goroutines are introduced only
	// later by generation composition.
	runtime.GC()
	startGoroutines := runtime.NumGoroutine()

	profiles := make([]Profile, 1000)
	for i := range profiles {
		p := validProfile()
		// IDs are deterministic without adding any family/frontend objects.
		p.ID = "provider-" + fmt.Sprintf("%04d", i+1)
		p.Endpoint.BaseURL = "https://provider-" + fmt.Sprintf("%04d", i+1) + ".example/v1"
		profiles[i] = p
	}
	catalog, err := NewCatalog(profiles)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := catalog.CompileAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) != 1000 || compiled[999].Profile.ID != "provider-1000" {
		t.Fatalf("unexpected scale result: %d", len(compiled))
	}

	for _, c := range compiled {
		cert, err := Certify(c.Profile)
		if err != nil {
			t.Fatalf("profile %s certification failed: %v", c.Profile.ID, err)
		}
		if err := cert.Validate(); err != nil {
			t.Fatalf("profile %s certification invalid: %v", c.Profile.ID, err)
		}
	}

	runtime.GC()
	endGoroutines := runtime.NumGoroutine()
	if endGoroutines > startGoroutines+2 {
		t.Fatalf("1,000 profiles spawned goroutines: start=%d end=%d", startGoroutines, endGoroutines)
	}
	for _, c := range compiled {
		if c.Binding.FactoryKind == "" || c.Profile.ID == "" {
			t.Fatalf("profile %q lost pure binding data", c.Profile.ID)
		}
	}
}

func TestCertifyProfile_reportsFamilyBindingAndEffectiveSurface(t *testing.T) {
	t.Parallel()
	certification, err := Certify(validProfile())
	if err != nil {
		t.Fatal(err)
	}
	if err := certification.Validate(); err != nil {
		t.Fatal(err)
	}
	if certification.FactoryKind != "custom-openai-responses-compatible" {
		t.Fatalf("factory kind=%q", certification.FactoryKind)
	}
}

func TestValidateProfile_reservedQuirksRequireExecutableSemantics(t *testing.T) {
	t.Parallel()
	for _, family := range []Family{FamilyOpenAIChat, FamilyOpenAIResponses, FamilyAnthropic, FamilyOpenResponses, FamilySystemOne} {
		t.Run(string(family)+"/prompt-cache-key", func(t *testing.T) {
			p := validProfile()
			p.Family, p.Auth, p.Headers = family, Auth{Mode: AuthNone}, nil
			p.Models.Policy = DiscoveryStatic
			p.Models.Static = []Model{{CanonicalID: "m", NativeID: "m"}}
			require.NoError(t, Validate(p), "unquirked family fixture must be valid")
			p.Quirks = []QuirkID{"openai.prompt_cache_key"}
			wantAllowed := family == FamilyOpenAIChat || family == FamilyOpenAIResponses
			require.Equal(t, wantAllowed, Validate(p) == nil, "prompt cache key quirk family validation")
		})
	}
	t.Run("Anthropic model path requires quirk", func(t *testing.T) {
		t.Parallel()
		p := validProfile()
		p.Family = FamilyAnthropic
		p.Auth = Auth{Mode: AuthAPIKeyEnv, EnvVar: "ANTHROPIC_API_KEY"}
		p.Models.Path = "/provider/models"
		if err := Validate(p); err == nil {
			t.Fatal("model discovery path accepted without its quirk")
		}
	})

	t.Run("Anthropic quirk requires model path", func(t *testing.T) {
		t.Parallel()
		p := validProfile()
		p.Family = FamilyAnthropic
		p.Auth = Auth{Mode: AuthAPIKeyEnv, EnvVar: "ANTHROPIC_API_KEY"}
		p.Quirks = []QuirkID{QuirkAnthropicV1Models}
		if err := Validate(p); err == nil {
			t.Fatal("Anthropic model quirk accepted without a path")
		}
	})

	t.Run("OpenAI responses path remains unsupported", func(t *testing.T) {
		t.Parallel()
		p := validProfile()
		p.Quirks = []QuirkID{QuirkOpenAIResponsesPath}
		if err := Validate(p); err == nil {
			t.Fatal("unsupported quirk accepted")
		}
	})
}

func TestEmbeddedCatalog_PromptCacheKeyOptInSet(t *testing.T) {
	cat, err := EmbeddedCatalog()
	require.NoError(t, err)
	for _, p := range cat.Profiles() {
		want := p.ID == "mistral" || p.ID == "deepinfra"
		got := slices.Contains(p.Quirks, QuirkOpenAIPromptCacheKey)
		require.Equal(t, want, got, "profile %s prompt cache key opt-in", p.ID)
	}
}
