package anthropicmessages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/credpool"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

func TestCacheControllerPartialReadIsNotRenewed(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var gotTarget CacheTarget
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "secret-c0" {
			t.Fatalf("x-api-key=%q, want credential-affine key", got)
		}
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&map[string]any{})
		mu.Unlock()
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":1,"output_tokens":0,"cache_read_input_tokens":10}}`))
	}))
	defer server.Close()

	read := int64(100)
	total := int64(100)
	controller, err := NewCacheController(CacheControllerConfig{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		ResolveAPIKey: func(_ context.Context, target CacheTarget) (string, error) {
			mu.Lock()
			gotTarget = target
			mu.Unlock()
			if target.AccountID != "c0" {
				t.Fatalf("target account=%q, want exact credential c0", target.AccountID)
			}
			return "secret-c0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	observation, err := controller.IssueTarget(CacheTarget{
		ALegID: "a", BLegID: "b", BackendInstanceID: "anthropic", TargetID: "target", GenerationID: "generation", Model: "claude", TTL: "5m",
		Renewal:   RenewalSnapshot{RawRequest: json.RawMessage(`{"model":"claude","messages":[{"role":"user","content":"hi"}]}`)},
		AccountID: "c0", Evidence: promptcache.CacheEvidence{CacheReadTokens: &read, TotalTokens: &total},
	}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := controller.Renew(context.Background(), promptcache.RenewRequest{Handle: observation.Handle, OperationID: "keepwarm:test:1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Result.Status != promptcache.Stale {
		t.Fatalf("partial prefix status=%q, want stale", resp.Result.Status)
	}
	if controller.TargetCount() != 1 {
		t.Fatal("partial result should leave classification to the manager; target unexpectedly removed")
	}
	mu.Lock()
	defer mu.Unlock()
	if gotTarget.AccountID != "c0" {
		t.Fatalf("resolved target=%q, want c0", gotTarget.AccountID)
	}
}

func TestRenewalCoversTargetCombinesCacheReadAndWriteEvidence(t *testing.T) {
	t.Parallel()
	readTarget := int64(80)
	writeTarget := int64(20)
	readReported := int64(70)
	writeReported := int64(30)
	if !renewalCoversTarget(&readReported, &writeReported, CacheTarget{Evidence: promptcache.CacheEvidence{CacheReadTokens: &readTarget, CacheWriteTokens: &writeTarget}}) {
		t.Fatal("combined read/write coverage was not recognized")
	}
	zero := int64(0)
	writeOnly := int64(20)
	if !renewalCoversTarget(&zero, &writeOnly, CacheTarget{Evidence: promptcache.CacheEvidence{CacheReadTokens: &zero, CacheWriteTokens: &writeOnly}}) {
		t.Fatal("write-only coverage with zero read evidence was not recognized")
	}
	partialWrite := int64(10)
	if renewalCoversTarget(&readReported, &partialWrite, CacheTarget{Evidence: promptcache.CacheEvidence{CacheReadTokens: &readTarget, CacheWriteTokens: &writeTarget}}) {
		t.Fatal("partial combined coverage was classified as complete")
	}
}

func TestRenewalBodyPreservesCompatibleToolSettings(t *testing.T) {
	t.Parallel()
	body, err := renewalBody(RenewalSnapshot{RawRequest: json.RawMessage(`{"model":"claude","max_tokens":256,"stream":true,"messages":[],"tools":[{"name":"shell","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["max_tokens"] != float64(0) || got["stream"] != false {
		t.Fatalf("renewal controls = %#v", got)
	}
	if _, ok := got["tools"]; !ok {
		t.Fatal("renewal dropped the original tool definitions")
	}
	if _, ok := got["tool_choice"]; !ok {
		t.Fatal("renewal dropped compatible tool_choice")
	}
}

// TestRenewalBodyRejectsNullRawSnapshot proves a JSON null raw snapshot is an
// invalid snapshot, not a nil-map panic.
func TestRenewalBodyRejectsNullRawSnapshot(t *testing.T) {
	t.Parallel()
	if _, err := renewalBody(RenewalSnapshot{RawRequest: json.RawMessage(`null`)}); err == nil {
		t.Fatal("null raw renewal snapshot must be rejected")
	}
}

func TestRenewalBodyPreservesGeneratedToolWireShape(t *testing.T) {
	t.Parallel()
	parallel := false
	call := aipapi.Call{
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("run a command")}}},
		Tools: []aipapi.ToolDef{{
			Name:        "shell",
			Description: "run shell commands",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}}}`),
		}},
		ToolChoice: aipapi.ToolChoice{Mode: aipapi.ToolChoiceAuto},
		Options:    aipapi.GenerationOptions{ParallelToolCalls: &parallel},
	}
	params, err := ParamsForCall(&call, routing.AttemptCandidate{Primary: routing.Primary{Model: "claude"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := renewalBody(renewalSnapshotFromParams(params, "5m"))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if wire["model"] != "claude" || wire["max_tokens"] != float64(0) || wire["stream"] != false {
		t.Fatalf("renewal controls/model = %#v", wire)
	}
	tools, ok := wire["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("renewal tools = %#v, want one generated tool", wire["tools"])
	}
	tool, ok := tools[0].(map[string]any)
	if !ok || tool["name"] != "shell" {
		t.Fatalf("renewal tool = %#v", tools[0])
	}
	choice, ok := wire["tool_choice"].(map[string]any)
	if !ok || choice["type"] != "auto" || choice["disable_parallel_tool_use"] != true {
		t.Fatalf("renewal tool_choice = %#v", wire["tool_choice"])
	}
	cacheControl, ok := wire["cache_control"].(map[string]any)
	if !ok || cacheControl["type"] != "ephemeral" || cacheControl["ttl"] != "5m" {
		t.Fatalf("renewal cache_control = %#v", wire["cache_control"])
	}
}

func TestAnthropicRenewalCapabilityRejectsForcedToolAndThinkingShapes(t *testing.T) {
	t.Parallel()
	be := NewBackend(Config{
		BackendID:       "anthropic",
		BaseURL:         "https://example.invalid",
		Credentials:     []credpool.Credential{{ID: "c0", Secret: "secret"}},
		CacheEnrollment: "automatic",
		CacheTTL:        "5m",
	})
	candidate := routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic", Model: "claude"}}
	plain := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}}}
	profile := be.ResolvePromptCacheProfile(context.Background(), plain, candidate)
	if !profile.RenewalSupported {
		t.Fatal("plain request should be eligible for certified active renewal")
	}

	toolCall := plain
	toolCall.Tools = []aipapi.ToolDef{{Name: "shell", Description: "run shell", Parameters: []byte(`{"type":"object"}`)}}
	toolProfile := be.ResolvePromptCacheProfile(context.Background(), toolCall, candidate)
	if !toolProfile.ObservationSupported || !toolProfile.RenewalSupported {
		t.Fatalf("normal auto tool request should advertise active renewal: %+v", toolProfile)
	}

	forcedAny := toolCall
	forcedAny.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceAny}
	forcedAnyProfile := be.ResolvePromptCacheProfile(context.Background(), forcedAny, candidate)
	if forcedAnyProfile.ObservationSupported || forcedAnyProfile.RenewalSupported {
		t.Fatalf("forced-any tool request advertised cache support: %+v", forcedAnyProfile)
	}

	forcedSpecific := toolCall
	forcedSpecific.ToolChoice = aipapi.ToolChoice{Mode: aipapi.ToolChoiceRequired, Name: "shell"}
	forcedSpecificProfile := be.ResolvePromptCacheProfile(context.Background(), forcedSpecific, candidate)
	if forcedSpecificProfile.ObservationSupported || forcedSpecificProfile.RenewalSupported {
		t.Fatalf("forced-specific tool request advertised cache support: %+v", forcedSpecificProfile)
	}

	formatted := plain
	formatted.Options.ResponseMIMEType = "application/json"
	formattedProfile := be.ResolvePromptCacheProfile(context.Background(), formatted, candidate)
	if formattedProfile.RenewalSupported {
		t.Fatal("response-format request must not advertise active renewal")
	}

	thinkingBackend := NewBackend(Config{
		BackendID:          "anthropic-thinking",
		BaseURL:            "https://example.invalid",
		Credentials:        []credpool.Credential{{ID: "c0", Secret: "secret"}},
		CacheEnrollment:    "automatic",
		CacheTTL:           "5m",
		ThinkingFromEffort: true,
	})
	thinking := plain
	thinking.Options.ReasoningEffort = "high"
	thinkingProfile := thinkingBackend.ResolvePromptCacheProfile(context.Background(), thinking, routing.AttemptCandidate{Primary: routing.Primary{Backend: "anthropic-thinking", Model: "claude"}})
	if thinkingProfile.ObservationSupported || thinkingProfile.RenewalSupported {
		t.Fatalf("thinking request advertised cache support: %+v", thinkingProfile)
	}
}
