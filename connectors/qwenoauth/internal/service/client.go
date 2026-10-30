package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func resolveModel(factoryKind string, inv backendplugin.Invocation, call aipapi.Call, defaultModel string) string {
	m := strings.TrimSpace(inv.CanonicalModelID)
	if m == "" || m == factoryKind {
		if inv.NativeModelID != "" && inv.NativeModelID != factoryKind {
			m = inv.NativeModelID
		} else if defaultModel != "" {
			m = defaultModel
		} else {
			m = DefaultModel
		}
	} else {
		if after, ok := strings.CutPrefix(m, factoryKind+"/"); ok {
			m = after
		}
	}
	m = strings.TrimSpace(m)
	if m == "" || m == factoryKind {
		if defaultModel != "" {
			m = defaultModel
		} else {
			m = DefaultModel
		}
	}
	return m
}

func ListModels(ctx context.Context, cfg Config, tp TokenProvider, hc *http.Client, limit uint32) (backendplugin.ListModelsResponse, error) {
	if hc == nil {
		hc = http.DefaultClient
	}

	fetch := func(tok string) (*http.Response, []byte, error) {
		endpoint := fmt.Sprintf("%s/models", strings.TrimRight(cfg.GetInferenceURL(), "/"))
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("User-Agent", DefaultUserAgent)
		req.Header.Set("Accept", "application/json")

		resp, err := hc.Do(req)
		if err != nil {
			return nil, nil, fmt.Errorf("qwen-oauth: list models: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("qwen-oauth: read models response: %w", err)
		}
		return resp, body, nil
	}

	token, err := tp.Token(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("qwen-oauth: acquire token: %w", err)
	}

	resp, body, err := fetch(token)
	if err != nil {
		return backendplugin.ListModelsResponse{}, err
	}

	if resp.StatusCode == http.StatusForbidden {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("qwen-oauth: entitlement access forbidden (403): %s", string(body))
	}

	if resp.StatusCode == http.StatusUnauthorized {
		newToken, refErr := tp.ForceRefresh(ctx)
		if refErr != nil {
			return backendplugin.ListModelsResponse{}, fmt.Errorf("qwen-oauth: 401 token refresh: %w", refErr)
		}
		resp, body, err = fetch(newToken)
		if err != nil {
			return backendplugin.ListModelsResponse{}, err
		}
		if resp.StatusCode == http.StatusForbidden {
			return backendplugin.ListModelsResponse{}, fmt.Errorf("qwen-oauth: entitlement access forbidden (403): %s", string(body))
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("qwen-oauth: list models failed (status %d): %s", resp.StatusCode, string(body))
	}

	type modelsPayload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	var parsed modelsPayload
	if err := json.Unmarshal(body, &parsed); err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("qwen-oauth: decode models response: %w", err)
	}

	out := make([]backendplugin.ModelDescriptor, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		out = append(out, backendplugin.ModelDescriptor{
			CanonicalModelID: FactoryKind + "/" + id,
			NativeModelID:    id,
			FactoryKind:      FactoryKind,
			Capabilities:     backendplugin.CapabilitySummary{Streaming: true},
		})
		if limit > 0 && uint32(len(out)) >= limit {
			break
		}
	}

	return backendplugin.ListModelsResponse{
		Models:          out,
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}
