package anthropicmessages

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

const apiVersion = "2023-06-01"

const minimumRenewalOutputTokens = 0

var (
	ErrNoCacheEvidence = errors.New("anthropic: cache residency is not proven")
	ErrTargetNotFound  = errors.New("anthropic: cache target not found")
)

// RenewalSnapshot is the minimal request shape used by zero-output
// residency maintenance. RawRequest, when non-empty, is the exact wire body
// for the renewal, sanitized only for controls that cannot be used with
// max_tokens=0. Compatible tools and tool-choice settings remain intact so the
// provider sees the same cache-affecting request shape. RawRequest takes
// precedence over the typed System/Messages fields and guarantees byte-for-byte
// prefix reproduction.
type RenewalSnapshot struct {
	Model      string
	System     []RenewalSystemBlock
	Messages   []RenewalMessage
	RawRequest json.RawMessage `json:"-"`
}

type RenewalSystemBlock struct {
	Type         string               `json:"type,omitempty"`
	Text         string               `json:"text,omitempty"`
	CacheControl *RenewalCacheControl `json:"cache_control,omitempty"`
}

type RenewalCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type RenewalMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CacheTarget struct {
	ALegID            string
	BLegID            string
	BackendInstanceID string
	TargetID          promptcache.TargetID
	GenerationID      promptcache.GenerationID
	Model             string
	Renewal           RenewalSnapshot
	AccountID         string
	WorkspaceID       string
	TTL               string
	Evidence          promptcache.CacheEvidence
}

type CacheControllerConfig struct {
	BaseURL        string
	APIKey         string
	HTTPClient     *http.Client
	ResolveAPIKey  func(context.Context, CacheTarget) (string, error)
	MaxTargetBytes int
}

type CacheController struct {
	baseURL       string
	apiKey        string
	httpClient    *http.Client
	resolveAPIKey func(context.Context, CacheTarget) (string, error)
	maxBodyBytes  int
	mu            sync.Mutex
	next          uint64
	targets       map[string]CacheTarget
}

func NewCacheController(cfg CacheControllerConfig) (*CacheController, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("anthropic: cache controller base_url is required")
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	maxBody := cfg.MaxTargetBytes
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	return &CacheController{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"), apiKey: cfg.APIKey,
		httpClient: client, resolveAPIKey: cfg.ResolveAPIKey,
		maxBodyBytes: maxBody, targets: make(map[string]CacheTarget),
	}, nil
}

func (c *CacheController) IssueTarget(target CacheTarget, observedAt time.Time) (promptcache.Observation, error) {
	if c == nil {
		return promptcache.Observation{}, ErrTargetNotFound
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if target.Evidence.CacheReadTokens == nil && target.Evidence.CacheWriteTokens == nil {
		return promptcache.Observation{}, ErrNoCacheEvidence
	}
	if target.Evidence.CacheReadTokens != nil && *target.Evidence.CacheReadTokens == 0 && target.Evidence.CacheWriteTokens != nil && *target.Evidence.CacheWriteTokens == 0 {
		return promptcache.Observation{}, ErrNoCacheEvidence
	}
	if err := target.validate(); err != nil {
		return promptcache.Observation{}, err
	}
	var snapshotBytes []byte
	if len(target.Renewal.RawRequest) > 0 {
		snapshotBytes = target.Renewal.RawRequest
		if !json.Valid(snapshotBytes) {
			return promptcache.Observation{}, fmt.Errorf("anthropic: invalid raw renewal snapshot")
		}
	} else {
		var err error
		snapshotBytes, err = json.Marshal(target.Renewal)
		if err != nil {
			return promptcache.Observation{}, fmt.Errorf("anthropic: encode renewal snapshot: %w", err)
		}
	}
	if len(snapshotBytes) > c.maxBodyBytes {
		return promptcache.Observation{}, promptcache.ErrOversized
	}
	c.mu.Lock()
	c.next++
	handle := promptcache.Handle(fmt.Sprintf("anthropic-target-%d", c.next))
	c.targets[string(handle)] = target
	c.mu.Unlock()
	return c.observation(handle, target, observedAt), nil
}

func (t CacheTarget) validate() error {
	if strings.TrimSpace(t.ALegID) == "" || strings.TrimSpace(t.BLegID) == "" || strings.TrimSpace(t.BackendInstanceID) == "" || strings.TrimSpace(t.Model) == "" {
		return promptcache.ErrInvalid
	}
	if err := t.TargetID.Validate(true); err != nil {
		return err
	}
	if err := t.GenerationID.Validate(true); err != nil {
		return err
	}
	if err := validateCacheEnrollment("automatic", t.TTL); err != nil {
		return err
	}
	if len(t.Renewal.RawRequest) > 0 {
		if !json.Valid(t.Renewal.RawRequest) {
			return promptcache.ErrInvalid
		}
		return t.Evidence.Validate()
	}
	if strings.TrimSpace(t.Renewal.Model) == "" || len(t.Renewal.Messages) == 0 {
		return promptcache.ErrInvalid
	}
	for _, message := range t.Renewal.Messages {
		if strings.TrimSpace(message.Role) == "" || strings.TrimSpace(message.Content) == "" {
			return promptcache.ErrInvalid
		}
	}
	return t.Evidence.Validate()
}

func (c *CacheController) observation(handle promptcache.Handle, target CacheTarget, observedAt time.Time) promptcache.Observation {
	d, _ := time.ParseDuration(target.TTL)
	expires := observedAt.Add(d)
	return promptcache.Observation{
		ALegID: target.ALegID, BLegID: target.BLegID, BackendInstanceID: target.BackendInstanceID,
		TargetID: target.TargetID, GenerationID: target.GenerationID,
		Lifecycle: promptcache.LifecycleSlidingExpiry,
		Timing:    promptcache.Timing{ObservedAt: observedAt, ExpiresAt: &expires},
		Renewable: true, Handle: handle, Evidence: target.Evidence,
	}
}

func (c *CacheController) Renew(ctx context.Context, req promptcache.RenewRequest) (promptcache.RenewResponse, error) {
	if err := req.Validate(); err != nil {
		return promptcache.RenewResponse{}, err
	}
	c.mu.Lock()
	target, ok := c.targets[string(req.Handle)]
	c.mu.Unlock()
	if !ok {
		return promptcache.RenewResponse{}, promptcache.ErrStaleHandle
	}
	key := c.apiKey
	if c.resolveAPIKey != nil {
		var err error
		key, err = c.resolveAPIKey(ctx, target)
		if err != nil {
			return promptcache.RenewResponse{}, err
		}
	}
	body, err := renewalBody(target.Renewal)
	if err != nil {
		return promptcache.RenewResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return promptcache.RenewResponse{}, err
	}
	httpReq.Header.Set("content-type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	httpReq.Header.Set("anthropic-version", apiVersion)
	if strings.TrimSpace(key) != "" {
		httpReq.Header.Set("x-api-key", key)
	}
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return promptcache.RenewResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return promptcache.RenewResponse{}, fmt.Errorf("anthropic: cache renewal status %d", resp.StatusCode)
	}
	var payload struct {
		Usage anthropicUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return promptcache.RenewResponse{}, err
	}
	accounting := payload.Usage.accounting(req.OperationID)
	status := promptcache.Stale
	if renewalCoversTarget(payload.Usage.CacheReadInputTokens, payload.Usage.CacheCreationInputTokens, target) {
		if payload.Usage.CacheReadInputTokens != nil && *payload.Usage.CacheReadInputTokens > 0 {
			status = promptcache.Renewed
		} else {
			status = promptcache.ColdRecreated
		}
	}
	result := promptcache.RenewResult{Status: status, Evidence: promptcache.CacheEvidence{
		InputTokens: payload.Usage.InputTokens, OutputTokens: payload.Usage.OutputTokens,
		CacheReadTokens: payload.Usage.CacheReadInputTokens, CacheWriteTokens: payload.Usage.CacheCreationInputTokens,
		TotalTokens: totalAnthropic(payload.Usage.InputTokens, payload.Usage.OutputTokens, payload.Usage.CacheReadInputTokens, payload.Usage.CacheCreationInputTokens),
	}}
	if status == promptcache.Renewed || status == promptcache.ColdRecreated {
		now := time.Now().UTC()
		result.Observation = new(c.observation(req.Handle, target, now))
	}
	return promptcache.RenewResponse{Result: result, Accounting: accounting}, nil
}

type anthropicUsage struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
}

func (u anthropicUsage) accounting(dedupe string) *promptcache.AccountingEvidence {
	total := totalAnthropic(u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
	presence := aipapi.UsagePresence{InputTokens: u.InputTokens != nil, OutputTokens: u.OutputTokens != nil, CacheWriteTokens: u.CacheCreationInputTokens != nil, CacheReadTokens: u.CacheReadInputTokens != nil, TotalTokens: total != nil}
	return &promptcache.AccountingEvidence{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadInputTokens, CacheWriteTokens: u.CacheCreationInputTokens, TotalTokens: total, Presence: presence, Source: promptcache.AccountingSourceProviderReported, Authority: promptcache.AccountingAuthorityAuthoritative, Plane: promptcache.AccountingPlaneProviderBillable, DedupeKey: dedupe}
}

func renewalCoversTarget(reportedRead, reportedWrite *int64, target CacheTarget) bool {
	reported, reportedPresent := sumTokenPointers(reportedRead, reportedWrite)
	expected, expectedPresent := sumTokenPointers(target.Evidence.CacheReadTokens, target.Evidence.CacheWriteTokens)
	return reportedPresent && expectedPresent && expected > 0 && reported >= expected
}

func sumTokenPointers(values ...*int64) (int64, bool) {
	var total int64
	present := false
	for _, value := range values {
		if value == nil {
			continue
		}
		if *value < 0 {
			return 0, false
		}
		total += *value
		present = true
	}
	return total, present
}

func totalPtr(a, b *int64) *int64 {
	if a == nil && b == nil {
		return nil
	}
	var total int64
	if a != nil {
		total += *a
	}
	if b != nil {
		total += *b
	}
	return &total
}

func totalAnthropic(input, output, cacheRead, cacheWrite *int64) *int64 {
	has := false
	var total int64
	for _, v := range []*int64{input, output, cacheRead, cacheWrite} {
		if v != nil {
			total += *v
			has = true
		}
	}
	if !has {
		return nil
	}
	return &total
}

func pointer[T any](v T) *T { return new(v) }

func renewalBody(snapshot RenewalSnapshot) ([]byte, error) {
	if len(snapshot.RawRequest) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(snapshot.RawRequest, &raw); err != nil {
			return nil, fmt.Errorf("anthropic: invalid raw renewal snapshot: %w", err)
		}
		if raw == nil {
			// JSON null decodes without error into a nil map; a renewal body
			// must be an object to carry the sanitized controls.
			return nil, errors.New("anthropic: invalid raw renewal snapshot: not a JSON object")
		}
		raw["max_tokens"] = minimumRenewalOutputTokens
		raw["stream"] = false
		delete(raw, "thinking")
		delete(raw, "response_format")
		delete(raw, "temperature")
		delete(raw, "top_p")
		return json.Marshal(raw)
	}
	body := struct {
		Model     string               `json:"model"`
		MaxTokens int                  `json:"max_tokens"`
		Stream    bool                 `json:"stream"`
		System    []RenewalSystemBlock `json:"system,omitempty"`
		Messages  []RenewalMessage     `json:"messages"`
	}{
		Model: snapshot.Model, MaxTokens: minimumRenewalOutputTokens, Stream: false,
		System: snapshot.System, Messages: snapshot.Messages,
	}
	return json.Marshal(body)
}

func (c *CacheController) Release(_ context.Context, req promptcache.ReleaseRequest) error {
	if err := req.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	delete(c.targets, string(req.Handle))
	c.mu.Unlock()
	return nil
}

func (c *CacheController) TargetCount() int { c.mu.Lock(); defer c.mu.Unlock(); return len(c.targets) }

var _ promptcache.Controller = (*CacheController)(nil)
