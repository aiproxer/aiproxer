package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// TestDirectAnthropicPromptCacheEffectLive is deliberately opt-in. It is not
// part of default unit/quality runs and must never make generic scheduler tests
// depend on provider credentials or network availability.
func TestDirectAnthropicPromptCacheEffectLive(t *testing.T) {
	t.Parallel()
	if os.Getenv("AIP_ANTHROPIC_CACHE_LIVE") != "1" {
		t.Skip("set AIP_ANTHROPIC_CACHE_LIVE=1 to run the direct Anthropic cache-effect gate")
	}
	apiKey := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	model := strings.TrimSpace(os.Getenv("AIP_ANTHROPIC_CACHE_MODEL"))
	if apiKey == "" || model == "" {
		t.Skip("ANTHROPIC_API_KEY and AIP_ANTHROPIC_CACHE_MODEL are required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AIP_ANTHROPIC_CACHE_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	ttl := strings.TrimSpace(os.Getenv("AIP_ANTHROPIC_CACHE_TTL"))
	if ttl == "" {
		ttl = "5m"
	}
	if err := ValidateCacheConfig(CacheEnrollmentAutomatic, ttl); err != nil {
		t.Fatal(err)
	}

	capture := &liveRequestCapture{base: http.DefaultTransport}
	client := &http.Client{Transport: capture, Timeout: 45 * time.Second}
	body := fmt.Appendf(nil, `{"model":%q,"max_tokens":1,"stream":false,"system":[{"type":"text","text":%q,"cache_control":{"type":"ephemeral","ttl":%q}}],"messages":[{"role":"user","content":"Reply with one word."}]}`, model, strings.Repeat("cache residency contract prefix ", 96), ttl)

	first, err := liveAnthropicCall(context.Background(), client, baseURL, apiKey, body)
	if err != nil {
		t.Fatal(err)
	}
	if first.Usage.CacheCreationInputTokens == nil || *first.Usage.CacheCreationInputTokens <= 0 {
		t.Fatalf("foreground request did not prove cache creation: %+v", first.Usage)
	}

	controller, err := NewCacheController(CacheControllerConfig{BaseURL: baseURL, APIKey: apiKey, HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	input := first.Usage.InputTokens
	write := first.Usage.CacheCreationInputTokens
	total := totalPtr(input, first.Usage.OutputTokens)
	observation, err := controller.IssueTarget(CacheTarget{
		ALegID: "live-a", BLegID: "live-b", BackendInstanceID: "direct-anthropic", TargetID: "live-target", GenerationID: "live-generation", Model: model,
		Renewal: RenewalSnapshot{
			Model:    model,
			System:   []RenewalSystemBlock{{Type: "text", Text: strings.Repeat("cache residency contract prefix ", 96), CacheControl: &RenewalCacheControl{Type: "ephemeral", TTL: ttl}}},
			Messages: []RenewalMessage{{Role: "user", Content: "Reply with one word."}},
		},
		TTL: ttl, Evidence: promptcache.CacheEvidence{InputTokens: input, CacheWriteTokens: write, TotalTokens: total},
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := controller.Renew(context.Background(), promptcache.RenewRequest{Handle: observation.Handle, OperationID: "live-maintenance-1"})
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Result.Status != promptcache.Renewed || renewed.Accounting == nil {
		t.Fatalf("zero-output renewal did not prove residency: %+v", renewed)
	}
	if renewed.Accounting.OutputTokens != nil && *renewed.Accounting.OutputTokens != 0 {
		t.Fatalf("maintenance emitted model output: %+v", renewed.Accounting)
	}
	maintenanceBody := capture.LastBody()
	if strings.Contains(string(maintenanceBody), `"stream":true`) || strings.Contains(string(maintenanceBody), `"thinking"`) || strings.Contains(string(maintenanceBody), `"tool_choice"`) {
		t.Fatalf("maintenance request retained incompatible fields: %s", maintenanceBody)
	}
	if !strings.Contains(string(maintenanceBody), `"max_tokens":0`) {
		t.Fatalf("maintenance request was not zero-output: %s", maintenanceBody)
	}

	subsequent, err := liveAnthropicCall(context.Background(), client, baseURL, apiKey, body)
	if err != nil {
		t.Fatal(err)
	}
	if subsequent.Usage.CacheReadInputTokens == nil || *subsequent.Usage.CacheReadInputTokens <= 0 {
		t.Fatalf("subsequent request did not observe cache-read evidence: %+v", subsequent.Usage)
	}
}

type liveRequestCapture struct {
	base http.RoundTripper
	last []byte
}

func (c *liveRequestCapture) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	c.last = append(c.last[:0], body...)
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	return c.base.RoundTrip(req)
}
func (c *liveRequestCapture) LastBody() []byte { return append([]byte(nil), c.last...) }

type liveAnthropicResponse struct {
	Usage anthropicUsage `json:"usage"`
}

func liveAnthropicCall(ctx context.Context, client *http.Client, baseURL, apiKey string, body []byte) (liveAnthropicResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/v1/messages", strings.NewReader(string(body)))
	if err != nil {
		return liveAnthropicResponse{}, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("anthropic-version", APIVersion)
	req.Header.Set("x-api-key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return liveAnthropicResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return liveAnthropicResponse{}, fmt.Errorf("anthropic live status %d", resp.StatusCode)
	}
	var out liveAnthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return liveAnthropicResponse{}, err
	}
	return out, nil
}
