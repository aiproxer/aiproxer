// Package anthropicmessages provides shared Anthropic Messages protocol execution.
package anthropicmessages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/checkcfg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/streampeek"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type Config struct {
	BackendID     string
	BaseURL       string
	APIKey        string
	APIKeys       []string
	Credentials   []credpool.Credential
	HTTPClient    *http.Client
	SDKMaxRetries *int
	// QuotaSwitchThreshold is the Retry-After duration strictly above which a
	// 429 quota-cools a key and advances pre-output. Zero selects the
	// proxy-wide default (credpool.DefaultQuotaSwitchThreshold); it is bound
	// from upstream_credentials.quota_switch_retry_after at registration.
	QuotaSwitchThreshold time.Duration
	ModelInventory       modelinventory.Provider
	ProviderCounter      accountingapp.ProviderCounter
	NormalizeRoles       bool
	NormalizeModel       func(string) string
	ThinkingFromEffort   bool
	OmitToolChoice       bool
	// CompatibleModeAuth enables optional credentials for built-in compatible
	// modes: empty resolved keys omit x-api-key. Native Anthropic must leave false.
	CompatibleModeAuth bool
	CacheEnrollment    string
	CacheTTL           string
	// CacheObservation, when non-nil, issues a renewable target from committed
	// foreground cache evidence on successful terminals. It is supplied by the
	// owning plugin only for automatic enrollment; nil keeps emission off.
	CacheObservation CacheObservationHook
}

func NewBackend(cfg Config) execbackend.Backend {
	id := strings.TrimSpace(cfg.BackendID)
	if id == "" {
		id = "anthropic"
	}
	if err := checkcfg.RequireNonEmpty(id, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorBackend(id, err)
	}
	if err := validateCacheEnrollment(cfg.CacheEnrollment, cfg.CacheTTL); err != nil {
		return newConfigErrorBackend(id, err)
	}
	pool, noAuth, err := buildCompatibleOrRequiredPool(cfg)
	if err != nil {
		return newConfigErrorBackend(id, fmt.Errorf("%s: credentials: %w", id, err))
	}
	cacheRuntime, cacheErr := newCacheRuntime(cfg, pool, noAuth)
	if cacheErr != nil {
		return newConfigErrorBackend(id, fmt.Errorf("%s: cache runtime: %w", id, cacheErr))
	}
	backendCaps := defaultBackendCaps()
	if cfg.ThinkingFromEffort {
		backendCaps[aipapi.CapabilityReasoning] = struct{}{}
	}

	be := execbackend.Backend{
		Caps:                                 backendCaps,
		ReplaySupport:                        ReplaySupport(),
		BackendPrefixes:                      []string{id},
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: codexclientcompat.IgnoresClampViaCodexUnsupportedGenParams,
		ProviderCounter:                      cfg.ProviderCounter,
		ModelInventory:                       cfg.ModelInventory,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			caps := ModelCapabilities(resolveModel(cand, call))
			if cfg.ThinkingFromEffort {
				caps[aipapi.CapabilityReasoning] = struct{}{}
			}
			return caps
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", id, aipapi.ErrNilContext)
			}
			p, err := paramsForCall(&call, cand, cfg.NormalizeRoles, cfg.NormalizeModel)
			if cfg.OmitToolChoice {
				p.ToolChoice = anthropic.ToolChoiceUnionParam{}
			}
			if err != nil {
				return nil, err
			}
			cacheState := cacheRuntime.state(ctx, call, p, cand, id)
			if noAuth {
				cli := newSDKClientForSecret(cfg, "")
				observePreparedInput(ctx, p)
				stream := cli.Messages.NewStreaming(ctx, p, cacheEnrollmentOptions(cfg)...)
				es := newMessageStreamWithCache(stream, id, call.MaxPendingWireEvents, cacheState)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewManagedPrependFirst(ev, es), nil
				}
				_ = es.Close()
				return nil, rerr
			}
			// Ordered quota-aware attempts before any output: the first usable
			// key stays preferred; only auth-invalid or a 429 with a
			// trustworthy Retry-After strictly above the configured threshold
			// advances to the next key. Short, missing or malformed hints are
			// a transient same-key throttle; 403/402 and other failures
			// return as-is without rotation.
			coord, cerr := credpool.NewCoordinator(pool, cfg.QuotaSwitchThreshold, nil)
			if cerr != nil {
				return nil, fmt.Errorf("%s: %w", id, cerr)
			}
			stream, runErr := credpool.Do(ctx, coord, func(err error) (credpool.FailureClass, string) {
				kind, hint := classifyAnthropicAPIError(err)
				switch kind {
				case apiFailureAuthInvalid:
					return credpool.FailureAuthInvalid, ""
				case apiFailureRateLimited:
					return credpool.FailureRateLimited, hint
				case apiFailureRetryable:
					return credpool.FailureRetryable, ""
				default:
					return credpool.FailureNeutral, ""
				}
			}, func(ctx context.Context, cred credpool.Credential) (aipapi.ManagedEventStream, error) {
				if cacheState != nil {
					cacheState.lineage.BackendInstanceID = id
					cacheState.credentialID = cred.ID
					if m := strings.TrimSpace(cand.Primary.Model); m != "" {
						cacheState.lineage.CanonicalModelID = m
					}
				}
				cli := newSDKClientForSecret(cfg, cred.Secret)
				requestOpts := append([]option.RequestOption(nil), cacheEnrollmentOptions(cfg)...)
				requestOpts = append(requestOpts, thinkingRequestOptions(call.Options.ReasoningEffort, cfg.ThinkingFromEffort)...)
				if cfg.ThinkingFromEffort && reasoningEffortEnablesThinking(call.Options.ReasoningEffort) {
					requestOpts = append(requestOpts, option.WithHeader("anthropic-beta", "interleaved-thinking-2025-05-14"))
				}
				observePreparedInput(ctx, p)
				stream := cli.Messages.NewStreaming(ctx, p, requestOpts...)
				es := newMessageStreamWithCache(stream, id, call.MaxPendingWireEvents, cacheState)
				ev, rerr := es.Recv(ctx)
				if rerr == nil {
					return streampeek.NewManagedPrependFirst(ev, es), nil
				}
				_ = es.Close()
				return nil, rerr
			})
			if runErr == nil {
				return stream, nil
			}
			if errors.Is(runErr, credpool.ErrTransientThrottle) ||
				errors.Is(runErr, credpool.ErrQuotaExhausted) ||
				errors.Is(runErr, credpool.ErrUpstreamRetryable) ||
				errors.Is(runErr, credpool.ErrNoUsableCredential) {
				return nil, aipapi.RecoverablePreOutputError(runErr)
			}
			return nil, runErr
		},
	}
	cacheRuntime.configureBackend(&be, func(call *aipapi.Call, cand routing.AttemptCandidate) (anthropic.MessageNewParams, error) {
		return paramsForCall(call, cand, cfg.NormalizeRoles, cfg.NormalizeModel)
	})
	return be
}

func buildCompatibleOrRequiredPool(cfg Config) (*credpool.Pool, bool, error) {
	if !cfg.CompatibleModeAuth {
		pool, err := credpool.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
		return pool, false, err
	}
	if len(cfg.Credentials) > 0 {
		pool, err := credpool.New(cfg.Credentials)
		return pool, false, err
	}
	secrets := make([]string, 0, 1+len(cfg.APIKeys))
	seen := make(map[string]struct{})
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		secrets = append(secrets, s)
	}
	add(cfg.APIKey)
	for _, k := range cfg.APIKeys {
		add(k)
	}
	if len(secrets) == 0 {
		return nil, true, nil
	}
	creds := make([]credpool.Credential, len(secrets))
	for i, s := range secrets {
		creds[i] = credpool.Credential{Secret: s}
	}
	pool, err := credpool.New(creds)
	return pool, false, err
}

func newConfigErrorBackend(id string, err error) execbackend.Backend {
	return execbackend.Backend{
		Caps:            defaultBackendCaps(),
		BackendPrefixes: []string{id},
		ModelInventory:  modelinventory.ErrorProvider{Err: err},
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}
