package anthropicmessages

import (
	"context"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

type cacheRuntime struct {
	hook               CacheObservationHook
	controller         *CacheController
	ttl                string
	thinkingFromEffort bool
}

func newCacheRuntime(cfg Config, pool *credpool.Pool, noAuth bool) (*cacheRuntime, error) {
	if strings.TrimSpace(cfg.CacheEnrollment) != "automatic" || noAuth {
		return nil, nil
	}
	runtime := &cacheRuntime{ttl: strings.TrimSpace(cfg.CacheTTL), thinkingFromEffort: cfg.ThinkingFromEffort}
	if cfg.CacheObservation != nil {
		runtime.hook = cfg.CacheObservation
		return runtime, nil
	}
	controller, err := NewCacheController(CacheControllerConfig{
		BaseURL:    cfg.BaseURL,
		HTTPClient: cfg.HTTPClient,
		ResolveAPIKey: func(ctx context.Context, target CacheTarget) (string, error) {
			_ = ctx
			if target.AccountID != "" {
				cred, acquireErr := pool.AcquireByID(time.Now(), target.AccountID)
				if acquireErr != nil {
					return "", acquireErr
				}
				return cred.Secret, nil
			}
			cred, acquireErr := pool.Acquire(time.Now(), nil)
			if acquireErr != nil {
				return "", acquireErr
			}
			return cred.Secret, nil
		},
	})
	if err != nil {
		return nil, err
	}
	runtime.controller = controller
	runtime.hook = func(ctx context.Context, in CacheObservation) (promptcache.Observation, error) {
		targetID := promptcache.TargetID(strings.TrimSpace(in.Lineage.BLegID))
		if targetID == "" {
			targetID = "anthropic-target"
		}
		generationID := promptcache.GenerationID(strings.TrimSpace(in.Lineage.BackendInstanceID))
		if generationID == "" {
			generationID = promptcache.GenerationID(targetID)
		}
		return controller.IssueTarget(CacheTarget{
			ALegID:            in.Lineage.ALegID,
			BLegID:            in.Lineage.BLegID,
			BackendInstanceID: in.Lineage.BackendInstanceID,
			TargetID:          targetID,
			GenerationID:      generationID,
			Model:             in.Model,
			Renewal:           in.Renewal,
			TTL:               runtime.ttl,
			Evidence:          in.Evidence,
			AccountID:         in.CredentialID,
		}, in.ObservedAt)
	}
	return runtime, nil
}

func toolsCompatibleWithRenewal(call aipapi.Call, params anthropic.MessageNewParams) bool {
	if len(params.Tools) == 0 {
		return true
	}
	switch call.ToolChoice.Mode {
	case "", aipapi.ToolChoiceAuto:
		return true
	default:
		return false
	}
}

func (r *cacheRuntime) eligible(call aipapi.Call, params anthropic.MessageNewParams) bool {
	if r == nil {
		return false
	}
	if !toolsCompatibleWithRenewal(call, params) || call.Options.Temperature != nil || call.Options.TopP != nil || call.Options.ResponseMIMEType != "" {
		return false
	}
	return !r.thinkingFromEffort || strings.TrimSpace(call.Options.ReasoningEffort) == ""
}

func (r *cacheRuntime) state(ctx context.Context, call aipapi.Call, params anthropic.MessageNewParams, candidate routing.AttemptCandidate, backendID string) *cacheStreamState {
	if r == nil || r.hook == nil || !r.eligible(call, params) {
		return nil
	}
	lineage, _ := promptcache.ObservationLineageFromContext(ctx)
	if lineage.BackendInstanceID == "" {
		lineage.BackendInstanceID = backendID
	}
	if lineage.CanonicalModelID == "" {
		lineage.CanonicalModelID = strings.TrimSpace(candidate.Primary.Model)
	}
	state := &cacheStreamState{
		hook:       r.hook,
		lineage:    lineage,
		renewal:    renewalSnapshotFromParams(params, r.ttl),
		ttl:        r.ttl,
		observedAt: time.Now().UTC(),
	}
	if model := strings.TrimSpace(candidate.Primary.Model); model != "" {
		state.renewal.Model = model
	}
	return state
}

func (r *cacheRuntime) configureBackend(backend *execbackend.Backend, normalize func(*aipapi.Call, routing.AttemptCandidate) (anthropic.MessageNewParams, error)) {
	if r == nil || r.hook == nil || backend == nil {
		return
	}
	backend.ResolvePromptCacheProfile = func(ctx context.Context, call aipapi.Call, candidate routing.AttemptCandidate) promptcache.Profile {
		params, err := normalize(&call, candidate)
		eligible := err == nil && r.eligible(call, params)
		return promptcache.Profile{
			ObservationSupported: eligible,
			RenewalSupported:     eligible && r.controller != nil,
			LifecycleKinds:       []promptcache.LifecycleKind{promptcache.LifecycleSlidingExpiry},
		}
	}
	if r.controller != nil {
		backend.RenewPromptCache = r.controller.Renew
		backend.ReleasePromptCache = r.controller.Release
	}
}
