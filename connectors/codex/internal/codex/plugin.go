package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/connectors/codex/internal/catalog"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/checkcfg"
	"github.com/aiproxer/aiproxer/connectors/codex/internal/routingstub"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

// errManagedAccountsExhausted signals that the managed WebSocket open failed because
// every managed account was unusable due to account-level auth/rate-limit rejection,
// not a WebSocket transport problem. openWithFallback uses it to skip the global WS
// cooldown: the bad accounts are already marked in the credpool and excluded from
// future selection, so disabling WS for the whole backend would only delay recovery
// of accounts whose per-account cooldown expires sooner than the WS fallback window.
var errManagedAccountsExhausted = errors.New("managed oauth accounts exhausted")

var backendCaps = aipapi.NewBackendCaps(
	aipapi.CapabilityStreaming,
	aipapi.CapabilityTools,
	aipapi.CapabilityVision,
	aipapi.CapabilityDocuments,
	aipapi.CapabilityReasoning,
	aipapi.CapabilityParallelToolCalls,
)

type backendRuntime struct {
	mu           sync.Mutex
	cfg          Config
	oauth        *accountStore
	downgrade    downgradePolicy
	usageEst     *usageEstimator
	cooldown     *transportCooldown
	wsSessions   *wsSessionStore
	continuation *wsContinuationStore
	sessionTurns *sessionTurnCounter
	native       *NativeContextCoordinator
}

func New(cfg Config) (*Engine, error) {
	if err := checkcfg.RequireNonEmpty(ID, "base_url", cfg.BaseURL); err != nil {
		return newConfigErrorEngine(err), nil
	}
	rt := &backendRuntime{}
	if cfg.NativeContext == nil {
		defaulted := DefaultNativeContextConfig()
		cfg.NativeContext = &defaulted
	}
	resolved, store, err := resolveBackendConfig(cfg)
	if err != nil {
		return newConfigErrorEngine(err), nil
	}
	if resolved.NativeContext != nil {
		norm, err := resolved.NativeContext.NormalizeAndValidate()
		if err != nil {
			return newConfigErrorEngine(err), nil
		}
		resolved.NativeContext = &norm
	}
	if err := validateVerbosityBumpConfig(resolved); err != nil {
		return newConfigErrorEngine(err), nil
	}
	transport, err := NormalizeTransport(resolved.Transport, resolved.ExperimentalWebSocket)
	if err != nil {
		return newConfigErrorEngine(err), nil
	}
	resolved.Transport = transport
	if resolved.WebSocketFallbackCooldown <= 0 {
		resolved.WebSocketFallbackCooldown = DefaultWebSocketFallbackCooldown
	}
	usageEst, err := newUsageEstimator()
	if err != nil {
		return nil, err
	}
	rt.downgrade = newDowngradePolicy(resolved)
	rt.cfg = resolved
	rt.oauth = store
	rt.usageEst = usageEst
	rt.cooldown = newTransportCooldown(resolved.WebSocketFallbackCooldown)
	rt.wsSessions = newWSSessionStore()
	rt.continuation = newWSContinuationStore(codexContinuationTTL, codexContinuationMaxEntries)
	rt.native = newNativeContextCoordinator(resolved, "")
	rt.sessionTurns = newSessionTurnCounter(sessionTurnTTL, sessionTurnMaxEntries)
	if store == nil {
		if err := checkcfg.RequireNonEmpty(ID, "access_token", resolved.AccessToken); err != nil {
			return newConfigErrorEngine(err), nil
		}
	}
	return &Engine{rt: rt, inventory: inventoryProvider(rt.cfg)}, nil
}

func resolveBackendConfig(cfg Config) (Config, *accountStore, error) {
	if !cfg.ManagedOAuthEnabled {
		// Static-token mode: the access token arrives via host Configure
		// secrets (service boundary). No connector-side auth.json fallback.
		return cfg, nil, nil
	}
	// Managed subscription OAuth is an explicitly classified separate path:
	// the account store is the sole source. A missing or unusable store
	// fails closed instead of falling back to ambient auth.json files, and
	// accounts are never pooled as interchangeable quota peers.
	store, err := newAccountStore(cfg)
	if err != nil {
		return cfg, nil, err
	}
	if !store.hasUsable() {
		return cfg, nil, fmt.Errorf("%s: no usable managed oauth accounts", ID)
	}
	return cfg, store, nil
}

func (rt *backendRuntime) open(ctx context.Context, call aipapi.Call, cand routingstub.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	rt.mu.Lock()
	cfg := rt.cfg
	store := rt.oauth
	usageEst := rt.usageEst
	cooldown := rt.cooldown
	wsSessions := rt.wsSessions
	continuation := rt.continuation
	downgrade := rt.downgrade
	sessionTurns := rt.sessionTurns
	native := rt.native
	rt.mu.Unlock()
	if store != nil {
		return openWithFallback(
			ctx, &cfg, cooldown,
			func() (aipapi.ManagedEventStream, error) {
				return openManaged(ctx, &cfg, store, native, call, cand, downgrade, usageEst, continuation, sessionTurns)
			},
			func() (aipapi.ManagedEventStream, error) {
				return openManagedWS(ctx, &cfg, store, native, call, cand, downgrade, usageEst, wsSessions, continuation, sessionTurns)
			},
		)
	}
	return openWithFallback(
		ctx, &cfg, cooldown,
		func() (aipapi.ManagedEventStream, error) {
			return openHTTP(ctx, &cfg, rt, native, downgrade, call, cand, sessionTurns)
		},
		func() (aipapi.ManagedEventStream, error) {
			return openWS(ctx, &cfg, native, downgrade, usageEst, wsSessions, continuation, call, cand, sessionTurns)
		},
	)
}

// openWithFallback orchestrates transport selection for both the static-token
// and managed paths. HTTPS is used directly when configured or when the WS
// cooldown is active; WebSocket is used strictly when configured; auto mode
// tries WS and falls back to HTTPS only on a WS fallback-eligible error,
// recording the cooldown. The openHTTPS/openWS closures carry the path-specific
// account wiring so this helper stays free of managed/static differences.
func openWithFallback(
	ctx context.Context,
	cfg *Config,
	cooldown *transportCooldown,
	openHTTPS, openWS func() (aipapi.ManagedEventStream, error),
) (aipapi.ManagedEventStream, error) {
	switch cfg.Transport {
	case TransportHTTPS:
		return openHTTPS()
	case TransportWebSocket:
		return openWS()
	default:
		if cooldown.active() {
			return openHTTPS()
		}
		es, err := openWS()
		if err == nil {
			return es, nil
		}
		if es != nil {
			return es, err
		}
		// Account-level exhaustion from the managed WS path is not a WebSocket
		// transport problem: the bad accounts are already marked and excluded, and
		// HTTPS fallback may still succeed with a usable account. Skip the global WS
		// cooldown so a later-recovered account can use WS again without waiting out
		// the fallback window.
		if errors.Is(err, errManagedAccountsExhausted) {
			return openHTTPS()
		}
		if isWSFallbackError(ctx, err) {
			cooldown.markFailed()
			return openHTTPS()
		}
		return nil, err
	}
}

// selectManagedSession prepares the per-account session state shared by the WS
// and HTTP managed paths: picks an account for the conversation, derives the
// per-account call config, and resolves the plan-scoped model. The returned
// callCfg is a caller-owned copy so per-call mutation (e.g. OAuth refresh on
// the static path) never leaks back into the stored account config.
func selectManagedSession(env *codexOpenEnv, cfg *Config, store *accountStore, policy downgradePolicy) (managedAccount, Config, string, error) {
	acct, err := store.selectAccountForSession(env.convID)
	if err != nil {
		return managedAccount{}, Config{}, "", err
	}
	callCfg := callCfgFromAccount(cfg, acct)
	planType := firstNonEmpty(acct.PlanType, cfg.PlanTypeHint)
	return acct, callCfg, policy.modelForPlan(env.originalModel, planType), nil
}

type managedOpenAttemptFn func(ctx context.Context, env *codexOpenEnv, callCfg *Config, model string, usageEst *usageEstimator) (aipapi.ManagedEventStream, *http.Response, error)

func prepareNativeContextForOpen(ctx context.Context, env *codexOpenEnv, cfg *Config, call aipapi.Call, model string, native *NativeContextCoordinator, continuation *wsContinuationStore) error {
	return env.prepareNativeContext(ctx, cfg, call, model, native, func() {
		continuation.invalidateLineage(cfg, call, &env.payload)
	})
}

func openManagedAccountLoop(ctx context.Context, cfg *Config, store *accountStore, call aipapi.Call, cand routingstub.AttemptCandidate, policy downgradePolicy, usageEst *usageEstimator, attempt managedOpenAttemptFn, turns *sessionTurnCounter) (aipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand, policy, turns)
	if err != nil {
		return nil, err
	}
	retries := maxManagedRetries(store)
	for range retries {
		acct, callCfg, model, err := selectManagedSession(env, cfg, store, policy)
		if err != nil {
			env.releaseVerbosityTurn()
			return nil, fmt.Errorf("%s: no usable managed oauth accounts: %w", ID, errManagedAccountsExhausted)
		}
		es, resp, err := attempt(ctx, env, &callCfg, model, usageEst)
		if err == nil {
			if resp != nil {
				if qh := codexQuotaHeaders(resp.Header); len(qh) > 0 {
					_ = store.persistQuotaHeaders(acct, qh)
				}
			}
			env.commitVerbosityTurn()
			return es, nil
		}
		if es != nil {
			env.releaseVerbosityTurn()
			return es, err
		}
		if resp != nil {
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				store.markAuthInvalid(acct)
				continue
			case http.StatusTooManyRequests:
				now := store.now()
				store.markRateLimited(acct, credpool.CooldownFromRetryAfterOrFallback(resp.Header.Get("Retry-After"), now, store.fallback))
				continue
			}
		}
		if status := nativeContextStatus(err); status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusTooManyRequests {
			if store != nil {
				if status == http.StatusTooManyRequests {
					store.markRateLimited(acct, credpool.CooldownFromRetryAfterOrFallback("", store.now(), store.fallback))
				} else {
					store.markAuthInvalid(acct)
				}
				continue
			}
		}
		env.releaseVerbosityTurn()
		return nil, err
	}
	env.releaseVerbosityTurn()
	return nil, fmt.Errorf("%s: no usable managed oauth accounts: %w", ID, errManagedAccountsExhausted)
}

func openManagedWS(ctx context.Context, cfg *Config, store *accountStore, native *NativeContextCoordinator, call aipapi.Call, cand routingstub.AttemptCandidate, policy downgradePolicy, usageEst *usageEstimator, wsSessions *wsSessionStore, continuation *wsContinuationStore, turns *sessionTurnCounter) (aipapi.ManagedEventStream, error) {
	return openManagedAccountLoop(ctx, cfg, store, call, cand, policy, usageEst, func(ctx context.Context, env *codexOpenEnv, callCfg *Config, model string, usageEst *usageEstimator) (aipapi.ManagedEventStream, *http.Response, error) {
		if err := prepareNativeContextForOpen(ctx, env, callCfg, call, model, native, continuation); err != nil {
			return nil, nil, err
		}
		es, resp, err := openWSPrepared(ctx, env, callCfg, model, call, usageEst, wsSessions, continuation)
		return env.wrapNativeUsage(es, err), resp, err
	}, turns)
}

func openManaged(ctx context.Context, cfg *Config, store *accountStore, native *NativeContextCoordinator, call aipapi.Call, cand routingstub.AttemptCandidate, policy downgradePolicy, usageEst *usageEstimator, continuation *wsContinuationStore, turns *sessionTurnCounter) (aipapi.ManagedEventStream, error) {
	return openManagedAccountLoop(ctx, cfg, store, call, cand, policy, usageEst, func(ctx context.Context, env *codexOpenEnv, callCfg *Config, model string, usageEst *usageEstimator) (aipapi.ManagedEventStream, *http.Response, error) {
		if err := prepareNativeContextForOpen(ctx, env, callCfg, call, model, native, continuation); err != nil {
			return nil, nil, err
		}
		body, err := env.marshalWithModel(model)
		if err != nil {
			return nil, nil, err
		}
		attempt := env.newAttempt(ctx, cfg, call, body, usageEst)
		resp, err := attempt.doRequest(callCfg)
		if err != nil {
			return nil, nil, err
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			b := readLimitedClose(resp)
			return nil, resp, upstreamHTTPError(resp.StatusCode, b)
		}
		es, response, err := completeCodexOpenAttempt(attempt, resp, callCfg)
		return env.wrapNativeUsage(es, err), response, err
	}, turns)
}

func maxManagedRetries(store *accountStore) int {
	if store == nil || len(store.meta) == 0 {
		return 1
	}
	return len(store.meta)
}

func openHTTP(ctx context.Context, cfg *Config, rt *backendRuntime, native *NativeContextCoordinator, policy downgradePolicy, call aipapi.Call, cand routingstub.AttemptCandidate, turns *sessionTurnCounter) (aipapi.ManagedEventStream, error) {
	env, err := prepareCodexOpenEnv(ctx, cfg, call, cand, policy, turns)
	if err != nil {
		return nil, err
	}
	model := policy.modelForPlan(env.originalModel, cfg.PlanTypeHint)
	if err := prepareNativeContextForOpen(ctx, env, cfg, call, model, native, rt.continuation); err != nil {
		env.releaseVerbosityTurn()
		return nil, err
	}
	body, err := env.marshalWithModel(model)
	if err != nil {
		env.releaseVerbosityTurn()
		return nil, err
	}
	attempt := env.newAttempt(ctx, cfg, call, body, rt.usageEst)
	resp, err := attempt.doRequest(cfg)
	if err != nil {
		env.releaseVerbosityTurn()
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		b := readLimitedClose(resp)
		if strings.TrimSpace(cfg.RefreshToken) == "" {
			env.releaseVerbosityTurn()
			return nil, upstreamHTTPError(resp.StatusCode, b)
		}
		refreshedCfg, refreshErr := refreshOAuthAccessToken(ctx, *cfg, env.client)
		if refreshErr != nil {
			env.releaseVerbosityTurn()
			return nil, fmt.Errorf("%s: oauth refresh after HTTP %d: %w", ID, resp.StatusCode, refreshErr)
		}
		*cfg = refreshedCfg
		rt.mu.Lock()
		rt.cfg.AccessToken = refreshedCfg.AccessToken
		if strings.TrimSpace(refreshedCfg.RefreshToken) != "" {
			rt.cfg.RefreshToken = refreshedCfg.RefreshToken
		}
		rt.mu.Unlock()
		// A refreshed static credential can carry a different account identity.
		// Rebuild native preparation from the immutable baseline rather than letting
		// the old account's checkpoint rewrite cross the identity boundary.
		if err := prepareNativeContextForOpen(ctx, env, cfg, call, model, native, rt.continuation); err != nil {
			env.releaseVerbosityTurn()
			return nil, err
		}
		resp, err = attempt.doRequest(cfg)
		if err != nil {
			env.releaseVerbosityTurn()
			return nil, err
		}
	}
	es, _, err := completeCodexOpenAttempt(attempt, resp, cfg)
	es = env.wrapNativeUsage(es, err)
	if err != nil {
		env.releaseVerbosityTurn()
	} else {
		env.commitVerbosityTurn()
	}
	return es, err
}

func callCfgFromAccount(cfg *Config, acct managedAccount) Config {
	callCfg := *cfg
	callCfg.AccessToken = acct.AccessToken
	callCfg.AccountID = acct.ID
	if acct.RefreshToken != "" {
		callCfg.RefreshToken = acct.RefreshToken
	}
	return callCfg
}

func doCodexRequest(ctx context.Context, client *http.Client, endpoint string, body []byte, cfg *Config, convID string) (*http.Response, error) {
	return doCodexRequestWithHeaders(ctx, client, endpoint, body, cfg, convID, nil)
}

func doCodexRequestWithHeaders(ctx context.Context, client *http.Client, endpoint string, body []byte, cfg *Config, convID string, extra http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%s: build request: %w", ID, err)
	}
	applyCodexHeaders(req, *cfg, convID)
	for key, values := range extra {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	start := time.Now()
	if debugTurnsEnabled() {
		slog.DebugContext(
			ctx, "codex.debug.http_request_start",
			"endpoint_path", endpointPath(endpoint),
			"body_bytes", len(body),
			"conversation_id", convID,
		)
	}
	resp, err := client.Do(req)
	if err != nil {
		if debugTurnsEnabled() {
			slog.DebugContext(
				ctx, "codex.debug.http_request_done",
				"duration_ms", time.Since(start).Milliseconds(),
				"status", "error",
				"error", err.Error(),
			)
		}
		return nil, fmt.Errorf("%s: request: %w", ID, err)
	}
	if debugTurnsEnabled() {
		slog.DebugContext(
			ctx, "codex.debug.http_request_done",
			"duration_ms", time.Since(start).Milliseconds(),
			"status", resp.StatusCode,
		)
	}
	return resp, nil
}

func endpointPath(raw string) string {
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	return raw
}

func transportCaps() aipapi.BackendTransportCaps {
	return aipapi.NewBackendTransportCaps(aipapi.OperationTransportSupport{
		Operation: aipapi.OperationOpenAIResponses,
		Modes:     []aipapi.TransportMode{aipapi.TransportModeStreaming, aipapi.TransportModeNonStreaming},
	})
}

func inventoryProvider(cfg Config) modelinventory.Provider {
	if len(cfg.Models) > 0 {
		models := make([]modelinventory.Model, 0, len(cfg.Models))
		for _, m := range cfg.Models {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			models = append(models, modelinventory.Model{
				CanonicalID: ID + "/" + m,
				NativeID:    m,
				DisplayName: m,
			})
		}
		return modelinventory.StaticProvider{
			Source: modelinventory.SourceStaticInline,
			Models: models,
		}
	}
	if cfg.CatalogLoadOpts.Enabled {
		return catalog.InventoryProvider{
			Opts: cfg.CatalogLoadOpts,
			Map: func(cat *catalog.Catalog, _ catalog.Source) []modelinventory.Model {
				return catalogInventoryModels(cat)
			},
		}
	}
	return modelinventory.StaticProvider{
		Source: modelinventory.SourceStaticBuiltin,
		Models: catalogInventoryModels(cfg.ModelCatalog),
	}
}

// catalogInventoryModels builds the built-in inventory from the auto-discovered
// catalog's routable slugs. When the catalog is nil (e.g. tests without DI),
// the shipped fallback snapshot is loaded so no slugs are hardcoded here.
func catalogInventoryModels(cat *catalog.Catalog) []modelinventory.Model {
	slugs := catalog.RoutableSlugsOrFallback(cat)
	if len(slugs) == 0 {
		return []modelinventory.Model{}
	}
	out := make([]modelinventory.Model, 0, len(slugs))
	for _, id := range slugs {
		out = append(out, modelinventory.Model{
			CanonicalID: ID + "/" + id,
			NativeID:    id,
			DisplayName: id,
		})
	}
	return out
}

func newConfigErrorEngine(err error) *Engine {
	return &Engine{inventory: modelinventory.ErrorProvider{Err: err}, cfgErr: err}
}
