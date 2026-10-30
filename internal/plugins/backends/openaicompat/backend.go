package openaicompat

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/checkcfg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicaps"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openaicred"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/openai/openai-go/v3/option"
)

type Flavor string

const (
	FlavorChat      Flavor = "chat"
	FlavorResponses Flavor = "responses"
)

type BackendSpec struct {
	ID                string
	BaseURL           string
	APIKey            string
	APIKeys           []string
	Credentials       []credpool.Credential
	HTTPClient        *http.Client
	SDKMaxRetries     *int
	RateLimitFallback time.Duration
	// QuotaSwitchThreshold is the Retry-After duration strictly above which
	// a 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at candidate build.
	QuotaSwitchThreshold time.Duration

	// CompatibleModeAuth enables optional credentials for built-in compatible
	// modes: an empty resolved key set proceeds without Authorization headers.
	// Native hosted backends must leave this false (dummy-credential policy).
	CompatibleModeAuth bool
	Quirks             CompatibleQuirks
	// QuirkObserver counts applied strict-provider quirks; nil stays silent,
	// so generic custom-compatible kinds (zero value) are metric-silent.
	QuirkObserver QuirkObserver

	ClientOptions              func(aipapi.Call, routing.AttemptCandidate) []option.RequestOption
	RequestOptions             func(aipapi.Call) []option.RequestOption
	ResolveModel               func(routing.AttemptCandidate, aipapi.Call) string
	ResolveFlavor              func(aipapi.Call) Flavor
	Flavor                     Flavor
	WireDomainAnyAcceptedModel *bool
	Inventory                  modelinventory.Provider
}

func NewBackend(spec BackendSpec) execbackend.Backend {
	if err := checkcfg.RequireNonEmpty(spec.ID, "base_url", spec.BaseURL); err != nil {
		return newConfigErrorBackend(spec.ID, err)
	}
	pool, noAuth, err := buildCompatibleOrRequiredPool(spec)
	if err != nil {
		return newConfigErrorBackend(spec.ID, fmt.Errorf("%s: credentials: %w", spec.ID, err))
	}
	prefixes := []string{spec.ID}
	be := execbackend.Backend{
		Caps:          openaicaps.HostedFull,
		ReplaySupport: aipapi.ReasoningReplaySupport{},
		ResolveReplaySupport: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.ReasoningReplaySupport {
			model := resolveModel(spec, cand, call)
			flavor := openaicaps.FlavorChat
			if resolveFlavor(spec, call) == FlavorResponses {
				flavor = openaicaps.FlavorResponses
			}
			return openaicaps.ResolveCompatibleReplaySupport(flavor, model, prefixes)
		},
		TransportCaps:                        hostedTransportCaps(),
		BackendPrefixes:                      prefixes,
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: codexclientcompat.IgnoresClampViaCodexUnsupportedGenParams,
		ModelInventory:                       spec.Inventory,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			return openaicaps.ForHostedModelCompatibleReplay(resolveModel(spec, cand, call), prefixes)
		},
		ResolveWireCaps: func(_ context.Context, cand routing.AttemptCandidate) aipapi.BackendCaps {
			caps := openaicaps.ForHostedModelCompatibleReplay(cand.Primary.Model, prefixes)
			if spec.Flavor == FlavorResponses {
				out := aipapi.NewBackendCaps()
				for c := range caps {
					out[c] = struct{}{}
				}
				out[aipapi.CapabilityOrderedItems] = struct{}{}
				return out
			}
			return caps
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", spec.ID, aipapi.ErrNilContext)
			}
			req := InvokeRequest{
				ProviderID:    spec.ID,
				Call:          call,
				Candidate:     cand,
				Quirks:        spec.Quirks,
				QuirkObserver: spec.QuirkObserver,
			}
			if spec.RequestOptions != nil {
				req.SDKOptions = spec.RequestOptions(call)
			}
			targetPool := pool
			if noAuth {
				targetPool = nil
			}
			// The quota-switch threshold selects long quota exhaustion; it is
			// bound from the candidate snapshot's upstream_credentials policy
			// (zero selects the coordinator default).
			return openaicred.ExecuteWithCredentialPool(ctx, spec.ID, targetPool, spec.QuotaSwitchThreshold, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
				stream, openErr := openOnce(ctx, spec, req, call, cand, cred.Secret)
				if openErr == nil {
					// Report the target only for the credential attempt that
					// produced a usable stream, so a failed pre-output rotation
					// cannot leave a stale target in the collector.
					execbackend.ReportAttemptIdentity(ctx, execbackend.AttemptIdentity{
						Cache: attemptCacheTargetFingerprint(spec.BaseURL, string(resolveFlavor(spec, call)), cred.RemoteRegion),
					})
				}
				return stream, openErr
			})
		},
	}
	targetPool := pool
	if noAuth {
		targetPool = nil
	}
	return attachWireProof(be, spec, targetPool, spec.Inventory)
}

func buildCompatibleOrRequiredPool(spec BackendSpec) (*credpool.Pool, bool, error) {
	if !spec.CompatibleModeAuth {
		pool, err := openaicred.NewPoolFromCredentials(spec.APIKey, spec.APIKeys, spec.Credentials)
		return pool, false, err
	}
	creds, err := optionalCompatibleCredentials(spec.APIKey, spec.APIKeys, spec.Credentials)
	if err != nil {
		return nil, false, err
	}
	if len(creds) == 0 {
		return nil, true, nil
	}
	pool, err := credpool.New(creds)
	return pool, false, err
}

func optionalCompatibleCredentials(apiKey string, apiKeys []string, credentials []credpool.Credential) ([]credpool.Credential, error) {
	if len(credentials) > 0 {
		out := make([]credpool.Credential, 0, len(credentials))
		for i, c := range credentials {
			if c.Secret == "" {
				return nil, fmt.Errorf("credpool: empty secret at index %d", i)
			}
			out = append(out, c)
		}
		return out, nil
	}
	seen := make(map[string]struct{})
	out := make([]credpool.Credential, 0, 1+len(apiKeys))
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, credpool.Credential{Secret: s})
	}
	add(apiKey)
	for _, k := range apiKeys {
		add(k)
	}
	return out, nil
}

func openOnce(ctx context.Context, spec BackendSpec, req InvokeRequest, call aipapi.Call, cand routing.AttemptCandidate, apiSecret string) (aipapi.ManagedEventStream, error) {
	var clientOptions []option.RequestOption
	if spec.ClientOptions != nil {
		clientOptions = spec.ClientOptions(call, cand)
	}
	cli := openaicred.NewClientWithOptions(spec.BaseURL, apiSecret, spec.HTTPClient, spec.SDKMaxRetries, clientOptions)

	var es aipapi.ManagedEventStream
	var openErr error
	switch resolveFlavor(spec, call) {
	case FlavorResponses:
		es, openErr = OpenResponses(ctx, cli, req)
	default:
		es, openErr = OpenChat(ctx, cli, req)
	}
	if openErr != nil {
		return nil, openErr
	}
	return PeekFirstEvent(ctx, es)
}

func newConfigErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            openaicaps.HostedFull,
		TransportCaps:   hostedTransportCaps(),
		BackendPrefixes: []string{id},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) aipapi.BackendCaps {
			return openaicaps.HostedFull
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}

func hostedTransportCaps() aipapi.BackendTransportCaps {
	return aipapi.NewBackendTransportCaps(
		aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIChatCompletions,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		},
		aipapi.OperationTransportSupport{
			Operation: aipapi.OperationOpenAIResponses,
			Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
		},
	)
}

func resolveModel(spec BackendSpec, cand routing.AttemptCandidate, call aipapi.Call) string {
	if spec.ResolveModel == nil {
		return cand.Primary.Model
	}
	return spec.ResolveModel(cand, call)
}

func resolveFlavor(spec BackendSpec, call aipapi.Call) Flavor {
	if spec.ResolveFlavor == nil {
		return FlavorChat
	}
	return spec.ResolveFlavor(call)
}
