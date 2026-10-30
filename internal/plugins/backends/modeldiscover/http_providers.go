package modeldiscover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

const (
	defaultAnthropicAPIVersion = "2023-06-01"
	defaultGeminiBaseURL       = "https://generativelanguage.googleapis.com"
)

type OpenAICompatibleModelsProvider struct {
	BaseURL           string
	ModelsEndpoint    string
	APIKey            string
	APIKeys           []string
	Credentials       []string
	HTTPClient        *http.Client
	CanonicalPrefix   string
	PreserveVendorIDs bool
	// QuotaSwitchThreshold is the Retry-After duration strictly above which
	// a 429 advances inventory selection to the next ordered key. Zero
	// selects the proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at candidate build.
	QuotaSwitchThreshold time.Duration
	// CompatibleModeAuth allows remote inventory without Authorization when no key exists.
	CompatibleModeAuth bool
}

func (p OpenAICompatibleModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	secrets := orderedSecrets(p.APIKey, p.APIKeys, p.Credentials)
	if len(secrets) == 0 {
		if !p.CompatibleModeAuth {
			return modelinventory.Snapshot{}, errors.New("model discovery: no API credentials")
		}
		return p.loadModelsWithKey(ctx, "")
	}
	// Inventory selection follows the shared ordered quota-aware contract on
	// a per-call pool: 401 and long-hint 429s advance to the next ordered
	// key, while short/missing/malformed hints never consume a secondary
	// key. Pool state stays local to this inventory call so background
	// discovery never cools serving credentials.
	pool, err := poolFromSecrets(secrets)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	coord, err := credpool.NewCoordinator(pool, p.QuotaSwitchThreshold, nil)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	snap, runErr := credpool.Do(ctx, coord, classifyInventoryError, func(ctx context.Context, cred credpool.Credential) (modelinventory.Snapshot, error) {
		return p.loadModelsWithKey(ctx, cred.Secret)
	})
	if runErr == nil {
		return snap, nil
	}
	return modelinventory.Snapshot{}, inventoryAttemptError(runErr)
}

func (p OpenAICompatibleModelsProvider) loadModelsWithKey(ctx context.Context, key string) (modelinventory.Snapshot, error) {
	headers := map[string]string{}
	if strings.TrimSpace(key) != "" {
		headers["Authorization"] = "Bearer " + strings.TrimSpace(key)
	} else if !p.CompatibleModeAuth {
		return modelinventory.Snapshot{}, errors.New("model discovery: no API credentials")
	}
	endpoint := strings.TrimSpace(p.ModelsEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/") + "/models"
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := getJSON(ctx, p.HTTPClient, endpoint, headers, &payload); err != nil {
		return modelinventory.Snapshot{}, err
	}
	models := make([]modelinventory.Model, 0, len(payload.Data))
	for _, row := range payload.Data {
		native := strings.TrimSpace(row.ID)
		if native == "" {
			continue
		}
		canonical := canonicalWithPrefix(p.CanonicalPrefix, native, p.PreserveVendorIDs)
		models = append(models, modelinventory.Model{CanonicalID: canonical, NativeID: native, DisplayName: native})
	}
	return snapshot(models)
}

type AnthropicModelsProvider struct {
	BaseURL         string
	ModelsEndpoint  string
	APIKey          string
	APIKeys         []string
	Credentials     []string
	HTTPClient      *http.Client
	CanonicalPrefix string
	// QuotaSwitchThreshold is the Retry-After duration strictly above which
	// a 429 advances inventory selection to the next ordered key. Zero
	// selects the proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at candidate build.
	QuotaSwitchThreshold time.Duration
	// CompatibleModeAuth allows remote inventory without x-api-key when no key exists.
	CompatibleModeAuth bool
}

func (p AnthropicModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	secrets := orderedSecrets(p.APIKey, p.APIKeys, p.Credentials)
	if len(secrets) == 0 {
		if !p.CompatibleModeAuth {
			return modelinventory.Snapshot{}, errors.New("model discovery: no API credentials")
		}
		return p.loadModelsWithKey(ctx, "")
	}
	pool, err := poolFromSecrets(secrets)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	coord, err := credpool.NewCoordinator(pool, p.QuotaSwitchThreshold, nil)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	snap, runErr := credpool.Do(ctx, coord, classifyInventoryError, func(ctx context.Context, cred credpool.Credential) (modelinventory.Snapshot, error) {
		return p.loadModelsWithKey(ctx, cred.Secret)
	})
	if runErr == nil {
		return snap, nil
	}
	return modelinventory.Snapshot{}, inventoryAttemptError(runErr)
}

func (p AnthropicModelsProvider) loadModelsWithKey(ctx context.Context, key string) (modelinventory.Snapshot, error) {
	headers := map[string]string{
		"anthropic-version": defaultAnthropicAPIVersion,
	}
	if strings.TrimSpace(key) != "" {
		headers["x-api-key"] = strings.TrimSpace(key)
	} else if !p.CompatibleModeAuth {
		return modelinventory.Snapshot{}, errors.New("model discovery: no API credentials")
	}
	endpoint := strings.TrimSpace(p.ModelsEndpoint)
	if endpoint == "" {
		endpoint = strings.TrimRight(strings.TrimSpace(p.BaseURL), "/") + "/v1/models"
	}
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := getJSON(ctx, p.HTTPClient, endpoint, headers, &payload); err != nil {
		return modelinventory.Snapshot{}, err
	}
	models := make([]modelinventory.Model, 0, len(payload.Data))
	prefix := strings.Trim(strings.TrimSpace(p.CanonicalPrefix), "/")
	if prefix == "" {
		prefix = "anthropic"
	}
	for _, row := range payload.Data {
		native := strings.TrimSpace(row.ID)
		if native == "" {
			continue
		}
		models = append(models, modelinventory.Model{
			CanonicalID: prefix + "/" + native,
			NativeID:    native,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	return snapshot(models)
}

type GeminiModelsProvider struct {
	BaseURL     string
	APIKey      string
	APIKeys     []string
	Credentials []string
	HTTPClient  *http.Client
	// QuotaSwitchThreshold is the Retry-After duration strictly above which
	// a 429 advances inventory selection to the next ordered key. Zero
	// selects the proxy-wide default; it is bound from
	// upstream_credentials.quota_switch_retry_after at candidate build.
	QuotaSwitchThreshold time.Duration
}

func (p GeminiModelsProvider) LoadModels(ctx context.Context) (modelinventory.Snapshot, error) {
	secrets := orderedSecrets(p.APIKey, p.APIKeys, p.Credentials)
	if len(secrets) == 0 {
		return modelinventory.Snapshot{}, errors.New("model discovery: no API credentials")
	}
	// Inventory selection follows the shared ordered quota-aware contract on
	// a per-call pool: 401 and long-hint 429s advance to the next ordered
	// key, while short/missing/malformed hints never consume a secondary
	// key. Pool state stays local to this inventory call so background
	// discovery never cools serving credentials.
	pool, err := poolFromSecrets(secrets)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	coord, err := credpool.NewCoordinator(pool, p.QuotaSwitchThreshold, nil)
	if err != nil {
		return modelinventory.Snapshot{}, err
	}
	snap, runErr := credpool.Do(ctx, coord, classifyInventoryError, func(ctx context.Context, cred credpool.Credential) (modelinventory.Snapshot, error) {
		return p.loadModelsWithKey(ctx, cred.Secret)
	})
	if runErr == nil {
		return snap, nil
	}
	return modelinventory.Snapshot{}, inventoryAttemptError(runErr)
}

func (p GeminiModelsProvider) loadModelsWithKey(ctx context.Context, key string) (modelinventory.Snapshot, error) {
	if strings.TrimSpace(key) == "" {
		return modelinventory.Snapshot{}, errors.New("model discovery: no API credentials")
	}
	base := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
	if base == "" {
		base = defaultGeminiBaseURL
	}
	endpoint := base + "/v1beta/models"
	var payload struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
		} `json:"models"`
	}
	headers := map[string]string{
		"x-goog-api-key": key,
	}
	if err := getJSON(ctx, p.HTTPClient, endpoint, headers, &payload); err != nil {
		return modelinventory.Snapshot{}, err
	}
	models := make([]modelinventory.Model, 0, len(payload.Models))
	for _, row := range payload.Models {
		native := strings.TrimPrefix(strings.TrimSpace(row.Name), "models/")
		if native == "" {
			continue
		}
		models = append(models, modelinventory.Model{
			CanonicalID: "google/" + native,
			NativeID:    native,
			DisplayName: strings.TrimSpace(row.DisplayName),
		})
	}
	return snapshot(models)
}

// httpStatusError carries a non-2xx inventory status with its raw Retry-After
// hint for the quota-aware selector. Error() is stable and never includes
// response bodies or secret material.
type httpStatusError struct {
	Status     int
	RetryAfter string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("model discovery HTTP status %d", e.Status)
}

// orderedSecrets returns trimmed non-empty secrets in stable priority order
// with first-occurrence de-duplication.
func orderedSecrets(key string, keySets ...[]string) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if _, ok := seen[s]; ok {
			return
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	add(key)
	for _, set := range keySets {
		for _, s := range set {
			add(s)
		}
	}
	return out
}

// poolFromSecrets builds a per-call ordered pool from non-empty secrets.
func poolFromSecrets(secrets []string) (*credpool.Pool, error) {
	credentials := make([]credpool.Credential, 0, len(secrets))
	for _, secret := range secrets {
		credentials = append(credentials, credpool.Credential{Secret: secret})
	}
	return credpool.New(credentials)
}

// classifyInventoryError translates one inventory attempt into the shared
// selection contract: 401 retires the key, a 429 carries its Retry-After
// hint, and anything else returns as-is without touching pool state.
func classifyInventoryError(err error) (credpool.FailureClass, string) {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr != nil {
		switch statusErr.Status {
		case http.StatusUnauthorized:
			return credpool.FailureAuthInvalid, ""
		case http.StatusTooManyRequests:
			return credpool.FailureRateLimited, statusErr.RetryAfter
		case http.StatusRequestTimeout:
			return credpool.FailureRetryable, ""
		default:
			if statusErr.Status >= 500 {
				return credpool.FailureRetryable, ""
			}
			return credpool.FailureNeutral, ""
		}
	}
	return credpool.FailureNeutral, ""
}

// inventoryAttemptError unwraps a coordinator stop to the underlying attempt
// error, preserving the long-standing inventory error text while keeping
// short-throttle and exhaustion stops secret-free.
func inventoryAttemptError(runErr error) error {
	var statusErr *httpStatusError
	if errors.As(runErr, &statusErr) && statusErr != nil {
		return statusErr
	}
	return runErr
}

func firstSecret(key string, keySets ...[]string) (string, error) {
	if s := strings.TrimSpace(key); s != "" {
		return s, nil
	}
	for _, keys := range keySets {
		for _, key := range keys {
			if s := strings.TrimSpace(key); s != "" {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("model discovery: no API credentials")
}

func getJSON(ctx context.Context, client *http.Client, endpoint string, headers map[string]string, out any) error {
	if ctx == nil {
		return modelinventory.ErrNilContext
	}
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("model discovery request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("model discovery HTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		failure := &httpStatusError{Status: resp.StatusCode}
		if resp.Header != nil {
			failure.RetryAfter = strings.TrimSpace(resp.Header.Get("Retry-After"))
		}
		return failure
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("model discovery decode: %w", err)
	}
	return nil
}

func snapshot(models []modelinventory.Model) (modelinventory.Snapshot, error) {
	if len(models) == 0 {
		return modelinventory.Snapshot{}, fmt.Errorf("model discovery returned no models")
	}
	bounded, warnings := boundInventoryModels(models)
	return modelinventory.Snapshot{
		Source:   modelinventory.SourceRemote,
		LoadedAt: time.Now(),
		Models:   slices.Clone(bounded),
		Warnings: warnings,
	}, nil
}

func canonicalWithPrefix(prefix, native string, preserveVendor bool) string {
	native = strings.TrimSpace(native)
	if preserveVendor && strings.Contains(native, "/") {
		return native
	}
	prefix = strings.Trim(strings.TrimSpace(prefix), "/")
	return prefix + "/" + strings.TrimPrefix(native, "models/")
}
