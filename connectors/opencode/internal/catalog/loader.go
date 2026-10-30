package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/connectors/opencode/internal/catalog/vendor"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

type ModelLoaderConfig struct {
	BaseURL     string
	APIKey      string
	APIKeys     []string
	Credentials []string
	HTTPClient  *http.Client
	Kind        BackendKind
}

func FetchRemoteModelEntries(ctx context.Context, cfg ModelLoaderConfig) ([]ModelEntry, error) {
	if ctx == nil {
		return nil, modelinventory.ErrNilContext
	}
	keys := orderedSecrets(cfg.APIKey, cfg.APIKeys, cfg.Credentials)
	if len(keys) == 0 {
		return nil, fmt.Errorf("opencode: no API credentials")
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/") + "/models"
	var err error
	var body []byte
	for i, key := range keys {
		body, err = getJSON(ctx, cfg.HTTPClient, endpoint, map[string]string{"Authorization": "Bearer " + key})
		if err == nil {
			break
		}
		// An auth-invalid key must not shadow a valid ordered key: advance
		// to the next credential. Any other outcome (including throttles,
		// which carry no trustworthy rotation hint on this probe path)
		// returns as-is without consuming further credentials.
		if !isAuthInvalid(err) || i == len(keys)-1 {
			return nil, err
		}
	}
	entries, err := ParseModelsResponse(body)
	if err != nil {
		return nil, err
	}
	return enrichRemoteModelMetadata(cfg.Kind, entries), nil
}

// orderedSecrets returns trimmed non-empty secrets in stable priority order:
// the legacy single key first, then ordered key sets. The Configure path
// delivers already-deduplicated host slots via Credentials.
func orderedSecrets(key string, keySets ...[]string) []string {
	var out []string
	add := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	add(key)
	for _, keys := range keySets {
		for _, k := range keys {
			add(k)
		}
	}
	return out
}

// isAuthInvalid reports an HTTP 401 discovery failure without matching on
// secret material: only the typed status is inspected.
func isAuthInvalid(err error) bool {
	var statusErr *httpStatusError
	return errors.As(err, &statusErr) && statusErr.status == http.StatusUnauthorized
}

func enrichRemoteModelMetadata(kind BackendKind, entries []ModelEntry) []ModelEntry {
	out := slices.Clone(entries)
	for i := range out {
		if strings.TrimSpace(out[i].AISDKPackage) != "" {
			continue
		}
		out[i].AISDKPackage = aiSDKPackageForRemoteModel(kind, out[i].RawID)
	}
	return out
}

func aiSDKPackageForRemoteModel(kind BackendKind, rawID string) string {
	lower := strings.ToLower(strings.TrimSpace(rawID))
	switch kind {
	case BackendGo:
		if strings.HasPrefix(lower, "minimax-") || strings.HasPrefix(lower, "qwen") {
			return "@ai-sdk/anthropic"
		}
		return "@ai-sdk/openai-compatible"
	case BackendZen:
		switch {
		case strings.HasPrefix(lower, "claude-") || strings.HasPrefix(lower, "qwen"):
			return "@ai-sdk/anthropic"
		case strings.HasPrefix(lower, "gemini-"):
			return "@ai-sdk/google"
		case strings.HasPrefix(lower, "gpt-"):
			return "@ai-sdk/openai"
		default:
			return "@ai-sdk/openai-compatible"
		}
	default:
		return ""
	}
}

func LoadModelEntries(ctx context.Context, cfg ModelLoaderConfig, fallback []ModelEntry) ([]ModelEntry, modelinventory.Source, []string, error) {
	if ctx == nil {
		return nil, "", nil, modelinventory.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return nil, "", nil, err
	}

	entries, err := FetchRemoteModelEntries(ctx, cfg)
	if err == nil && len(entries) > 0 {
		return entries, modelinventory.SourceRemote, []string{}, nil
	}
	// Caller cancellation is never masked by fallback: a canceled fetch
	// propagates instead of serving the static list.
	if ctx.Err() != nil {
		return nil, "", nil, ctx.Err()
	}
	if len(fallback) == 0 {
		if err != nil {
			return nil, "", nil, fmt.Errorf("opencode: remote model discovery failed: %w", err)
		}
		return nil, "", nil, fmt.Errorf("opencode: remote model discovery returned no models")
	}
	models := slices.Clone(fallback)
	if len(models) == 0 {
		return nil, "", nil, fmt.Errorf("opencode: fallback model list contained no usable models")
	}
	warnings := []string{}
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("remote model discovery failed: %v", err))
	} else {
		warnings = append(warnings, "remote model discovery returned no models")
	}
	return models, modelinventory.SourceStaticInline, warnings, nil
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string) ([]byte, error) {
	if ctx == nil {
		return nil, modelinventory.ErrNilContext
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("opencode: model discovery request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("opencode: model discovery HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &httpStatusError{
			status: resp.StatusCode,
			msg:    fmt.Sprintf("opencode: model discovery HTTP status %d: %s", resp.StatusCode, strings.TrimSpace(string(body))),
		}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("opencode: model discovery read: %w", err)
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("opencode: model discovery decode: invalid JSON")
	}
	return body, nil
}

func modelInventorySnapshot(kind BackendKind, entries []ModelEntry, source modelinventory.Source, warnings []string, vendors vendor.VendorResolver) modelinventory.Snapshot {
	return modelinventory.Snapshot{
		Source:   source,
		LoadedAt: time.Now(),
		Models:   InventoryModels(kind, entries, vendors),
		Warnings: warnings,
	}
}

// httpStatusError carries a non-2xx discovery status for typed inspection
// (for example auth-invalid rotation) without embedding secret material.
type httpStatusError struct {
	status int
	msg    string
}

func (e *httpStatusError) Error() string {
	if e == nil {
		return "opencode: model discovery HTTP error"
	}
	return e.msg
}
