package openairesponses

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/checkcfg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/modeldiscover"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicaps"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaiusage"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/streampeek"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// Config configures the OpenAI Responses backend connector (official SDK).
// BaseURL must include the API prefix (e.g. https://api.openai.com/v1).
type Config struct {
	BaseURL string
	APIKey  string
	// APIKeys is the ordered credential list for this backend instance.
	// When non-empty, APIKey should match APIKeys[0] for SDK compatibility.
	APIKeys []string
	// Credentials is the structured credential list. When set, it takes precedence
	// over APIKey/APIKeys and preserves non-secret credential IDs for diagnostics.
	Credentials []credpool.Credential
	// HTTPClient is optional; when nil the SDK default is used.
	HTTPClient *http.Client
	// SDKMaxRetries optionally sets the official SDK MaxRetries (nil = SDK default).
	// Integration tests that assert a single upstream attempt on 429/401 should use a pointer to 0.
	SDKMaxRetries *int
	// QuotaSwitchThreshold is the Retry-After duration strictly above which a
	// 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at registration.
	QuotaSwitchThreshold time.Duration
	DefaultVerbosity     aipapi.VerbosityLevel
}

const DefaultBaseURL = "https://api.openai.com/v1"

// New returns a runtime backend that invokes the OpenAI Responses API using openai-go.
func New(cfg Config) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(err)
	}
	if err := normalizeDefaultVerbosity(&cfg.DefaultVerbosity); err != nil {
		return newConfigErrorBackend(err)
	}
	pool, err := openaicred.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
	if err != nil {
		return newConfigErrorBackend(fmt.Errorf("%s: credentials: %w", ID, err))
	}
	prefixes := []string{ID}
	return execbackend.Backend{
		Caps:                                 openaicaps.HostedFull,
		ReplaySupport:                        aipapi.ReasoningReplaySupport{},
		BackendPrefixes:                      prefixes,
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: codexclientcompat.IgnoresClampViaCodexUnsupportedGenParams,
		ModelInventory: modeldiscover.OpenAICompatibleModelsProvider{
			BaseURL:              cfg.BaseURL,
			APIKey:               cfg.APIKey,
			APIKeys:              cfg.APIKeys,
			Credentials:          credentialSecrets(cfg.Credentials),
			QuotaSwitchThreshold: cfg.QuotaSwitchThreshold,
			HTTPClient:           cfg.HTTPClient,
			CanonicalPrefix:      "openai",
		},
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			return openaicaps.ForHostedModelCompatibleReplay(resolveModel(cand, call), prefixes)
		},
		ResolveReplaySupport: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.ReasoningReplaySupport {
			return openaicaps.ResolveCompatibleReplaySupport(openaicaps.FlavorResponses, resolveModel(cand, call), prefixes)
		},
		ResolveEffortSupport: resolveEffortSupport(cfg),
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", ID, aipapi.ErrNilContext)
			}
			if call.Options.Verbosity == "" {
				call.Options.Verbosity = cfg.DefaultVerbosity
			}
			p, err := ParamsForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			return openaicred.ExecuteWithCredentialPool(ctx, ID, pool, cfg.QuotaSwitchThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
				cli := openaicred.NewClient(cfg.BaseURL, cred.Secret, cfg.HTTPClient, cfg.SDKMaxRetries)
				if call.Invocation.TransportMode == aipapi.TransportModeNonStreaming {
					observePreparedInput(ctx, p)
					resp, nerr := cli.Responses.New(ctx, p)
					if nerr != nil {
						return nil, nerr
					}
					events, evErr := CompletionEvents(*resp)
					if evErr != nil {
						return nil, evErr
					}
					return openaiusage.NewProviderEvidenceStream(events, "openai.responses.v2"), nil
				}
				observePreparedInput(ctx, p)
				raw := cli.Responses.NewStreaming(ctx, p)
				es := NewSDKStream(raw, call.MaxPendingWireEvents)
				return streampeek.PeekFirst(ctx, es)
			})
		},
	}
}

func normalizeDefaultVerbosity(value *aipapi.VerbosityLevel) error {
	parsed, err := aipapi.ParseVerbosityLevel(string(*value))
	if err != nil {
		return fmt.Errorf("%s: default_verbosity: %w", ID, err)
	}
	*value = parsed
	return nil
}

func credentialSecrets(credentials []credpool.Credential) []string {
	if len(credentials) == 0 {
		return nil
	}
	out := make([]string, 0, len(credentials))
	for _, cred := range credentials {
		out = append(out, cred.Secret)
	}
	return out
}

func newConfigErrorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            openaicaps.HostedFull,
		BackendPrefixes: []string{ID},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			return openaicaps.ForHostedModel(resolveModel(cand, call))
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
