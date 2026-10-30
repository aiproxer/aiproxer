package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/credpool"
)

func (i *instance) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if i.kind == FactoryKindCloud {
		return i.listCloudModels(ctx, limit)
	}
	return i.listLocalModels(ctx, limit)
}

func (i *instance) listLocalModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	models, err := i.client().ListModels(ctx, limit)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	if len(models) == 0 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama: local discovery returned no models")
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(models))
	for _, m := range models {
		caps := backendplugin.CapabilitySummary{Streaming: true}
		if c, err := i.fetchCaps(ctx, m.ID); err == nil {
			caps = c
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + m.ID, NativeModelID: m.ID, FactoryKind: i.kind,
			Capabilities: caps,
		})
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: i.kind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

func (i *instance) listCloudModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	endpoint := i.cfg.ModelsURL
	if endpoint == "" {
		endpoint = DefaultCloudTags
	}
	// Quota-aware selection across ordered host-issued credentials: the
	// first usable key stays preferred, auth-invalid keys advance pre-output,
	// and a throttled key only advances on a trustworthy long Retry-After.
	// All failures are secret-free.
	raw, err := cloudTagsWithKeys(ctx, i.hc, endpoint, i.pool, i.quotaThreshold)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama-cloud: tags json: %w", err)
	}
	out := make([]backendplugin.ModelDescriptor, 0, len(payload.Models))
	for _, row := range payload.Models {
		native := strings.TrimSuffix(strings.TrimSpace(row.Name), "-cloud")
		if native == "" {
			continue
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: i.kind + "/" + native, NativeModelID: native, FactoryKind: i.kind,
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("ollama-cloud: discovery returned no models")
	}
	return backendplugin.ListModelsResponse{
		Models: out, InventorySource: i.kind, FetchedUnixMS: time.Now().UnixMilli(),
	}, nil
}

// cloudTagsStatusError carries a non-2xx tags status with its Retry-After
// hint for typed quota classification. It never embeds secret material.
type cloudTagsStatusError struct {
	status     int
	retryAfter string
}

func (e *cloudTagsStatusError) Error() string {
	if e == nil {
		return "ollama-cloud: tags HTTP error"
	}
	return fmt.Sprintf("ollama-cloud: tags HTTP %d", e.status)
}

// classifyCloudTags maps tags-probe failures to coordinator classes: 401 is
// auth-invalid and advances; 429 carries the raw Retry-After hint for
// threshold-gated rotation (missing/short hints stay transient without
// consuming the next key); 408/5xx are retryable; anything else is neutral.
func classifyCloudTags(err error) (credpool.FailureClass, string) {
	if err == nil {
		return credpool.FailureNeutral, ""
	}
	var statusErr *cloudTagsStatusError
	if !errors.As(err, &statusErr) || statusErr == nil {
		return credpool.FailureNeutral, ""
	}
	switch {
	case statusErr.status == http.StatusUnauthorized:
		return credpool.FailureAuthInvalid, ""
	case statusErr.status == http.StatusTooManyRequests:
		return credpool.FailureRateLimited, strings.TrimSpace(statusErr.retryAfter)
	case statusErr.status == http.StatusRequestTimeout || statusErr.status >= 500:
		return credpool.FailureRetryable, ""
	default:
		return credpool.FailureNeutral, ""
	}
}

func cloudTagsWithKeys(ctx context.Context, hc *http.Client, endpoint string, pool *credpool.Pool, threshold time.Duration) ([]byte, error) {
	if pool == nil {
		return nil, credpool.ErrNoUsableCredential
	}
	coord, err := credpool.NewCoordinator(pool, threshold, nil)
	if err != nil {
		return nil, err
	}
	return credpool.Do(ctx, coord, classifyCloudTags, func(ctx context.Context, cred credpool.Credential) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(cred.Secret) != "" {
			req.Header.Set("Authorization", "Bearer "+cred.Secret)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return nil, fmt.Errorf("ollama-cloud: tags: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &cloudTagsStatusError{status: resp.StatusCode, retryAfter: resp.Header.Get("Retry-After")}
		}
		return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	})
}

func (i *instance) fetchCaps(ctx context.Context, nativeID string) (backendplugin.CapabilitySummary, error) {
	root := i.cfg.NativeRoot
	if root == "" {
		root = nativeRootFromBase(i.cfg.BaseURL)
	}
	body, _ := json.Marshal(map[string]string{"name": nativeID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(root, "/")+"/api/show", bytes.NewReader(body))
	if err != nil {
		return backendplugin.CapabilitySummary{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.hc.Do(req)
	if err != nil {
		return backendplugin.CapabilitySummary{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return backendplugin.CapabilitySummary{}, fmt.Errorf("show HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return backendplugin.CapabilitySummary{}, err
	}
	return capsFromOllama(payload.Capabilities), nil
}
