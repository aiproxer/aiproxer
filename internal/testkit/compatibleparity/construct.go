package compatibleparity

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicompat"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

func zeroRetries() *int {
	z := 0
	return &z
}

// EssentialBackend builds the bundled essential adapter for family against baseURL.
func EssentialBackend(t *testing.T, family Family, baseURL string, client *http.Client) execbackend.Backend {
	t.Helper()
	switch family {
	case FamilyOpenAILegacy:
		return openailegacy.New(openailegacy.Config{
			BaseURL:       baseURL + "/v1",
			APIKey:        "sk-test-essential",
			HTTPClient:    client,
			SDKMaxRetries: zeroRetries(),
		})
	case FamilyOpenAIResponses:
		return openairesponses.New(openairesponses.Config{
			BaseURL:       baseURL + "/v1",
			APIKey:        "sk-test-essential",
			HTTPClient:    client,
			SDKMaxRetries: zeroRetries(),
		})
	case FamilyAnthropic:
		return anthropic.New(anthropic.Config{
			BaseURL:       baseURL,
			APIKey:        testkit.SyntheticAnthropicAPIKey,
			HTTPClient:    client,
			SDKMaxRetries: zeroRetries(),
		})
	default:
		t.Fatalf("unknown family %q", family)
		return execbackend.Backend{}
	}
}

// GenericBackend builds a compatible-mode adapter through the standardplugins
// factory registry (endpoint validation, credentials, inventory, tokenizer, admission).
func GenericBackend(t *testing.T, family Family, prefix, baseURL, apiKey string, client *http.Client) execbackend.Backend {
	t.Helper()
	return directGenericBackend(t, family, prefix, baseURL, apiKey, client)
}

// FactoryGenericBackend builds a compatible mode through the standardplugins
// factory registry (endpoint validation, credentials, inventory, tokenizer, admission).
func FactoryGenericBackend(t *testing.T, family Family, prefix, baseURL, envRoot, envValue string, client *http.Client) execbackend.Backend {
	t.Helper()
	if envRoot != "" {
		t.Setenv(envRoot, envValue)
	}
	factory := factoryKindFor(family)
	configBase := factoryBaseURL(family, baseURL)
	raw := fmt.Sprintf(`backend_prefix: %s
base_url: %s
api_key_env_var_root: %s
`, prefix, configBase, envRoot)
	var node yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &node); err != nil {
		t.Fatal(err)
	}
	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBackendsOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	res, err := reg.BuildBackendWithLifecycle(factory, prefix, node, client, pluginreg.BackendFactoryDeps{})
	if err != nil {
		t.Fatalf("BuildBackendWithLifecycle(%s): %v", factory, err)
	}
	return res.Backend
}

func directGenericBackend(t *testing.T, family Family, prefix, baseURL, apiKey string, client *http.Client) execbackend.Backend {
	t.Helper()
	switch family {
	case FamilyOpenAILegacy:
		return openaicompat.NewBackend(openaicompat.BackendSpec{
			ID:            prefix,
			BaseURL:       baseURL + "/v1",
			APIKey:        apiKey,
			HTTPClient:    client,
			SDKMaxRetries: zeroRetries(),
			ResolveFlavor: func(aipapi.Call) openaicompat.Flavor { return openaicompat.FlavorChat },
		})
	case FamilyOpenAIResponses:
		return openaicompat.NewBackend(openaicompat.BackendSpec{
			ID:            prefix,
			BaseURL:       baseURL + "/v1",
			APIKey:        apiKey,
			HTTPClient:    client,
			SDKMaxRetries: zeroRetries(),
			ResolveFlavor: func(aipapi.Call) openaicompat.Flavor { return openaicompat.FlavorResponses },
		})
	case FamilyAnthropic:
		key := testkit.SyntheticAnthropicAPIKey
		return anthropic.New(anthropic.Config{
			BaseURL:       baseURL,
			BackendPrefix: prefix,
			APIKey:        key,
			HTTPClient:    client,
			SDKMaxRetries: zeroRetries(),
		})
	default:
		t.Fatalf("unknown family %q", family)
		return execbackend.Backend{}
	}
}

// CandidateFor returns a routing candidate for the fixture model.
func CandidateFor(fx Fixture) routing.AttemptCandidate {
	return routing.AttemptCandidate{Primary: routing.Primary{Model: fx.Model}}
}

// ExecutionBaseURL returns the httptest origin used for a family (OpenAI adds /v1 in constructors).
func ExecutionBaseURL(family Family, srvURL string) string {
	return srvURL
}

func factoryKindFor(family Family) string {
	switch family {
	case FamilyOpenAILegacy:
		return standardplugins.CustomOpenAILegacyCompatibleID
	case FamilyOpenAIResponses:
		return standardplugins.CustomOpenAIResponsesCompatibleID
	case FamilyAnthropic:
		return standardplugins.CustomAnthropicCompatibleID
	default:
		return ""
	}
}

func factoryBaseURL(family Family, srvURL string) string {
	if family == FamilyAnthropic {
		return srvURL
	}
	return srvURL + "/v1"
}
