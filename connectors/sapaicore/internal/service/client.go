package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connector-support/openaicompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func resolveDeployment(cfg Config, inv backendplugin.Invocation) (string, error) {
	id := strings.TrimSpace(inv.CanonicalModelID)
	if id == "" || id == FactoryKind {
		if inv.NativeModelID != "" && inv.NativeModelID != FactoryKind {
			id = inv.NativeModelID
		} else if cfg.DeploymentID != "" {
			id = cfg.DeploymentID
		} else {
			return "", fmt.Errorf("sapaicore: deployment_id is required (specify deployment_id in config or use invocation model sapaicore/{deployment-id})")
		}
	}
	if after, ok := strings.CutPrefix(id, FactoryKind+"/"); ok {
		id = after
	}
	id = strings.TrimSpace(id)
	if id == "" || id == FactoryKind {
		if cfg.DeploymentID != "" {
			id = cfg.DeploymentID
		} else {
			return "", fmt.Errorf("sapaicore: deployment_id is required (specify deployment_id in config or use invocation model sapaicore/{deployment-id})")
		}
	}
	return id, nil
}

func ResolveFlavor(call aipapi.Call) openaicompat.Flavor {
	if call.Invocation.Operation == aipapi.OperationOpenAIResponses ||
		call.Invocation.Operation == aipapi.OperationOpenResponsesCreate {
		return openaicompat.FlavorResponses
	}
	return openaicompat.FlavorChat
}

type deploymentResource struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type lmDeploymentsResponse struct {
	Count     int                  `json:"count"`
	Resources []deploymentResource `json:"resources"`
}

func ListModels(ctx context.Context, cfg Config, sk ServiceKey, tp TokenProvider, hc *http.Client, limit uint32) (backendplugin.ListModelsResponse, error) {
	token, err := tp.Token(ctx)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("sapaicore: list models: acquire token: %w", err)
	}

	url := cfg.AIAPIOrigin(sk.ServiceURLs.AIAPIURL) + "/v2/lm/deployments"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("sapaicore: new list models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("AI-Resource-Group", cfg.ResourceGroup)
	req.Header.Set("Accept", "application/json")

	resp, err := hc.Do(req)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("sapaicore: list models request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("sapaicore: read list models response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("sapaicore: list models failed with status %d: %s", resp.StatusCode, string(body))
	}

	var parsed lmDeploymentsResponse
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Resources) == 0 {
		var arr []deploymentResource
		if json.Unmarshal(body, &arr) == nil && len(arr) > 0 {
			parsed.Resources = arr
		}
	}

	out := make([]backendplugin.ModelDescriptor, 0, len(parsed.Resources))
	for _, d := range parsed.Resources {
		id := strings.TrimSpace(d.ID)
		if id == "" {
			continue
		}
		status := strings.ToUpper(strings.TrimSpace(d.Status))
		if status != "RUNNING" {
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
