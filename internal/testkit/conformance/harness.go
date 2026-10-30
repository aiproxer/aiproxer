package conformance

import (
	"context"
	"net/http"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/anthropic"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/bedrock"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openailegacy"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"gopkg.in/yaml.v3"
)

// DefaultModel returns the model name wired into routing.AttemptCandidate for a bundled backend ID.
func DefaultModel(backendID string) string {
	switch backendID {
	case BackendOpenResponses, BackendOpenRouter, BackendNVIDIA:
		// The generic OpenResponses backend and the connector columns use a small
		// canonical model the harness origins serve.
		return "gpt-4o-mini"
	case BackendLocalStub:
		// The local stub serves exactly one model.
		return "stub-default"
	default:
		if m := standardplugins.DefaultWireModel(backendID); m != "" {
			return m
		}
		// A connector column has no canonical default model: the harness names
		// the model its origin serves.
		return "gpt-4o-mini"
	}
}

// RouteSelector builds a core routing selector primary for a single-backend executor.
func RouteSelector(backendID, model string) string {
	if model == "" {
		model = DefaultModel(backendID)
	}
	return backendID + ":" + model
}

// NewTestExecutor wires a single backend against refBackendURL (or error injection URL) for conformance cells.
func NewTestExecutor(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) *runtime.Executor {
	tb.Helper()
	httpClient = testkit.IntegrationHTTPClient(httpClient)
	be := BackendFor(tb, backendID, upstreamBaseURL, httpClient)
	return newExecutorWithBackend(tb, backendID, be)
}

// NewTestExecutorDualCredential wires hosted OpenAI, Anthropic, and Gemini backends with two ordered
// API keys so credential pools are populated. Bedrock and ACP use the same construction as
// [NewTestExecutor] (no multi-key pool in this harness).
func NewTestExecutorDualCredential(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) *runtime.Executor {
	tb.Helper()
	httpClient = testkit.IntegrationHTTPClient(httpClient)
	be := BackendForDualCredential(tb, backendID, upstreamBaseURL, httpClient)
	return newExecutorWithBackend(tb, backendID, be)
}

func newExecutorWithBackend(tb testing.TB, backendID string, be execbackend.Backend) *runtime.Executor {
	tb.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(42)
	ex.Backends = map[string]execbackend.Backend{backendID: be}
	ex.DefaultBackend = backendID
	testkit.WireConformanceExecutorSecureSession(tb, ex)
	return ex
}

// BackendFor returns the bundled [execbackend.Backend] for upstreamBaseURL (httptest origin or /v1 layout per plugin).
func BackendFor(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) execbackend.Backend {
	tb.Helper()
	switch backendID {
	case openairesponses.ID:
		return openairesponses.New(openairesponses.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
	case openailegacy.ID:
		return openailegacy.New(openailegacy.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
	case anthropic.ID:
		return anthropic.New(anthropic.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     testkit.SyntheticAnthropicAPIKey,
			HTTPClient: httpClient,
		})
	case gemini.ID:
		return gemini.New(gemini.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
	case bedrock.ID:
		ctx, cancel := context.WithTimeout(context.Background(), bedrock.DefaultLoadConfigTimeout)
		defer cancel()
		return bedrock.NewWithContext(ctx, bedrock.Config{
			Region:          "us-east-1",
			AccessKeyID:     "AKID",
			SecretAccessKey: "SECRET",
			BaseEndpoint:    upstreamBaseURL,
			DisableHTTPS:    true,
			HTTPClient:      httpClient,
		})
	case BackendACP:
		// ACP is an executable connector column (connectors/acp); the harness
		// launches the real connector and drives it through the backendplugin
		// host adapter APIs (acp_connector.go). It is never an essential kind
		// and its protocol adapter is never linked into the root module.
		return acpConnectorBackend(tb, upstreamBaseURL)
	case BackendOpenResponses:
		// The generic OpenResponses backend is constructed from strict
		// compatible-mode YAML against the observing origin (Requirement 9.1).
		return buildOpenResponsesCompatibleBackend(tb, upstreamBaseURL, httpClient)
	case BackendOpenRouter, BackendNVIDIA:
		// OpenRouter/NVIDIA are optional connector columns (connectors/openrouter,
		// connectors/nvidia). Like ACP, the harness launches the real connector
		// executable and drives it through the backendplugin host adapter APIs
		// (connector_host.go). They stay optional and are never constructed as
		// essential bundled backends.
		return connectorHostBackend(tb, backendID, upstreamBaseURL)
	default:
		tb.Fatalf("unknown backend id %q", backendID)
		return execbackend.Backend{}
	}
}

// buildOpenResponsesCompatibleBackend constructs the generic remote OpenResponses
// backend for a conformance origin.
func buildOpenResponsesCompatibleBackend(tb testing.TB, upstreamBaseURL string, httpClient *http.Client) execbackend.Backend {
	tb.Helper()
	raw := "backend_prefix: harness-or\nbase_url: " + upstreamBaseURL + "\n"
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		tb.Fatalf("harness: openresponses config: %v", err)
	}
	be, err := openresponsescompat.Build("harness-or", n, httpClient)
	if err != nil {
		tb.Fatalf("harness: openresponses backend: %v", err)
	}
	return be
}

// BackendForDualCredential is like [BackendFor] but supplies a second synthetic key for hosted
// providers that support credential pools. Reference backends accept any non-empty key material
// used by conformance clients (sk-test, synthetic Anthropic key, fake Gemini key).
func BackendForDualCredential(tb testing.TB, backendID, upstreamBaseURL string, httpClient *http.Client) execbackend.Backend {
	tb.Helper()
	switch backendID {
	case openairesponses.ID:
		return openairesponses.New(openairesponses.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			APIKeys:    []string{"sk-test", "sk-test-pool2"},
			HTTPClient: httpClient,
		})
	case openailegacy.ID:
		return openailegacy.New(openailegacy.Config{
			BaseURL:    upstreamBaseURL + "/v1",
			APIKey:     "sk-test",
			APIKeys:    []string{"sk-test", "sk-test-pool2"},
			HTTPClient: httpClient,
		})
	case anthropic.ID:
		k := testkit.SyntheticAnthropicAPIKey
		return anthropic.New(anthropic.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     k,
			APIKeys:    []string{k, k + "-pool2"},
			HTTPClient: httpClient,
		})
	case gemini.ID:
		return gemini.New(gemini.Config{
			BaseURL:    upstreamBaseURL,
			APIKey:     "fake-key",
			APIKeys:    []string{"fake-key", "fake-key-pool2"},
			HTTPClient: httpClient,
		})
	case bedrock.ID, BackendACP, BackendOpenResponses, BackendOpenRouter, BackendNVIDIA:
		return BackendFor(tb, backendID, upstreamBaseURL, httpClient)
	default:
		tb.Fatalf("unknown backend id %q", backendID)
		return execbackend.Backend{}
	}
}

// GenAITestCtx is a shared background context for genai client construction in tests.
func GenAITestCtx() context.Context {
	return context.Background()
}
