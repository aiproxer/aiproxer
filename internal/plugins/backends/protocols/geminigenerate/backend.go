// Package geminigenerate provides shared Gemini generateContent protocol execution.
package geminigenerate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/streampeek"
	"github.com/aiproxer/aiproxer/internal/plugins/features/codexclientcompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type Config struct {
	BackendID   string
	BaseURL     string
	APIKey      string
	APIKeys     []string
	Credentials []credpool.Credential
	HTTPClient  *http.Client
	// QuotaSwitchThreshold is the Retry-After duration strictly above which a
	// 429 (or long Gemini RetryInfo) quota-cools a key and advances
	// pre-output. Zero selects the proxy-wide default
	// (credpool.DefaultQuotaSwitchThreshold); it is bound from
	// upstream_credentials.quota_switch_retry_after at registration.
	QuotaSwitchThreshold time.Duration
	ModelInventory       modelinventory.Provider
}

func NewBackend(cfg Config) execbackend.Backend {
	id := strings.TrimSpace(cfg.BackendID)
	if id == "" {
		id = "gemini"
	}
	pool, err := credpool.NewPoolFromCredentials(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
	if err != nil {
		return newConfigErrorBackend(id, fmt.Errorf("%s: credentials: %w", id, err))
	}
	return execbackend.Backend{
		Caps:                                 defaultBackendCaps(),
		BackendPrefixes:                      []string{id},
		EnforcesMaxOutputTokens:              true,
		IgnoresAuthorityMaxOutputTokensClamp: codexclientcompat.IgnoresClampViaCodexUnsupportedGenParams,
		ModelInventory:                       cfg.ModelInventory,
		ResolveCaps: func(_ context.Context, call aipapi.Call, cand routing.AttemptCandidate) aipapi.BackendCaps {
			return ModelCapabilities(resolveModel(cand, call))
		},
		Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			if ctx == nil {
				return nil, fmt.Errorf("%s: %w", id, aipapi.ErrNilContext)
			}
			sp, err := StreamParamsForCall(&call, cand)
			if err != nil {
				return nil, err
			}
			// Ordered quota-aware attempts before any output: the first usable
			// key stays preferred; only auth-invalid or a 429 with a
			// trustworthy Retry-After/RetryInfo strictly above the configured
			// threshold advances to the next key. Short, missing or malformed
			// hints are a transient same-key throttle; 403/402 and other
			// failures return as-is without rotation.
			coord, cerr := credpool.NewCoordinator(pool, cfg.QuotaSwitchThreshold, nil)
			if cerr != nil {
				return nil, fmt.Errorf("%s: %w", id, cerr)
			}
			stream, runErr := credpool.Do(ctx, coord, func(err error) (credpool.FailureClass, string) {
				kind, hint := classifyGenaiAPIError(err)
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
				cli, cerr := newGenaiClient(ctx, cfg, cred.Secret)
				if cerr != nil {
					return nil, fmt.Errorf("%s: client: %w", id, cerr)
				}
				observePreparedInput(ctx, sp)
				seq := cli.Models.GenerateContentStream(ctx, sp.Model, sp.Contents, sp.Config)
				es := newGenaiStream(seq, id, call.MaxPendingWireEvents)
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
