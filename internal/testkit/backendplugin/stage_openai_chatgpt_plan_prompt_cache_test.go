package backendplugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// planPromptCacheKey is the client cache id of the staged request. It is a
// non-secret test value whose only job is to make an accidental forward visible.
const planPromptCacheKey = "codex-session-abc"

// unknownSemanticMarker is deliberately distinctive so a diagnostic that
// echoed an unknown extension's value would be caught.
const unknownSemanticMarker = "do-not-echo-unknown-semantic"

// decodePlanPromptCacheRequest decodes a default-Codex-shaped OpenAI Responses
// request through the production frontend. Codex always sends prompt_cache_key;
// the connector's public preview route has no such field, so the approved SIWC
// policy must receive the hint through the negotiated semantic carrier and omit
// it explicitly instead of the ABI rejecting the request before policy.
func decodePlanPromptCacheRequest(t *testing.T) *openairesponses.DecodedCreate {
	t.Helper()
	body := `{
		"model":"gpt-5",
		"stream":false,
		"prompt_cache_key":"` + planPromptCacheKey + `",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"follow up"}]}
		]
	}`
	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: planKind + ":gpt-5",
	})
	if err != nil {
		t.Fatalf("decode the prompt-cache request through the production frontend: %v", err)
	}
	if decoded.Call.PromptCacheKey != planPromptCacheKey {
		t.Fatalf("decoded prompt cache key = %q, want %q", decoded.Call.PromptCacheKey, planPromptCacheKey)
	}
	if err := decoded.Call.Validate(); err != nil {
		t.Fatalf("the decoded prompt-cache call is not canonical: %v", err)
	}
	return decoded
}

// planPromptCacheCandidate addresses one staged SIWC instance.
func planPromptCacheCandidate(instanceID string) routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary: routing.Primary{Backend: instanceID, Model: planKind + "/gpt-5"},
		Key:     instanceID + ":" + planKind + "/gpt-5",
	}
}

// buildPlanHostBackend stages the real reference connector, installs it through
// the host's discover→trust→catalog→install→activate path and configures one
// instance. The connector executable, manifest, ABI, descriptor and service are
// the production ones; only the provider endpoint is the test-only loopback
// reference, exactly as in the other staged SIWC tests.
func buildPlanHostBackend(
	t *testing.T,
	reference *planHostReference,
	storePath string,
	instanceID string,
	extraConfig map[string]string,
) execbackend.Backend {
	t.Helper()
	root := bpkit.StageOpenAIChatGPTPlanReference(t, reference.URL())

	res, reg := resolveCoexistenceCatalog(t, root)
	if res.CatalogErr != nil {
		t.Fatalf("catalog error: %v", res.CatalogErr)
	}
	host := processhost.NewHost(processhost.Config{})
	t.Cleanup(func() { _ = host.Close() })
	if err := runtimebundle.InstallDiscoveredExports(reg, host, res.Installable, runtimebundle.DiscoveredInstallOptions{}); err != nil {
		t.Fatalf("install discovered exports: %v", err)
	}

	config := map[string]string{"profile_store_path": storePath}
	maps.Copy(config, extraConfig)
	instanceConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatalf("marshal instance config: %v", err)
	}
	return buildCoexistenceBackend(t, reg, planKind, instanceID, string(instanceConfig))
}

// promptCacheHistory returns the authored roles and texts of a provider request.
func promptCacheHistory(t *testing.T, body []byte) (roles, texts []string) {
	t.Helper()
	var document struct {
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the provider request: %v", err)
	}
	for _, item := range document.Input {
		if item.Type != "message" {
			continue
		}
		roles = append(roles, item.Role)
		for _, content := range item.Content {
			if content.Type == "input_text" {
				texts = append(texts, content.Text)
			}
		}
	}
	return roles, texts
}

// TestHostIntegration_OpenAIChatGPTPlanPromptCacheKeyIsOmittedNotForwarded is
// the observable completion of the F1 blocker:
//
//   - a default-Codex prompt_cache_key decoded by the production frontend
//     crosses the real host→connector ABI after negotiation;
//   - the approved SIWC interoperable policy serves the call and records the
//     omission instead of the ABI failing before policy;
//   - the provider observes no prompt_cache_key field and never the client
//     cache id, and the authored history is replayed exactly.
func TestHostIntegration_OpenAIChatGPTPlanPromptCacheKeyIsOmittedNotForwarded(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	backend := buildPlanHostBackend(t, reference, stagePlanHostStore(t), "plan-prompt-cache", nil)

	decoded := decodePlanPromptCacheRequest(t)
	call := *decoded.Call
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, err := backend.Open(ctx, call, planPromptCacheCandidate("plan-prompt-cache"))
	if err != nil {
		t.Fatalf("the negotiated semantic carrier must let the prompt-cache hint reach the SIWC policy, got: %v", err)
	}
	collected, err := aipapi.Collect(ctx, stream)
	if err != nil {
		t.Fatalf("collect the downstream non-stream result: %v", err)
	}
	if got := collected.Text.String(); got != "Hello from the plan." {
		t.Fatalf("downstream text = %q, want the complete provider answer", got)
	}
	if !collected.FinishReceived {
		t.Fatal("the downstream result must carry the terminal completion")
	}

	requests := reference.captured()
	if len(requests) != 1 {
		t.Fatalf("the provider must observe exactly ONE request, got %d", len(requests))
	}
	body := requests[0].Body
	if strings.Contains(string(body), planPromptCacheKey) {
		t.Fatalf("the client prompt cache id must never reach the provider:\n%s", body)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the provider request body: %v", err)
	}
	if _, ok := document["prompt_cache_key"]; ok {
		t.Fatalf("the provider request must not carry prompt_cache_key:\n%s", body)
	}

	roles, texts := promptCacheHistory(t, body)
	if want := []string{"user", "assistant", "user"}; !slices.Equal(roles, want) {
		t.Fatalf("provider history roles = %v, want the exact authored order %v", roles, want)
	}
	if want := []string{"first question", "first answer", "follow up"}; !slices.Equal(texts, want) {
		t.Fatalf("provider history texts = %v, want the exact authored trajectory %v", texts, want)
	}
}

// TestHostIntegration_OpenAIChatGPTPlanStrictPromptCacheKeyRefusalStopsBeforeProvider
// proves that the strict posture still refuses the hint after the ABI learns to
// carry it. A pre-fix ABI failure would also stop before the provider, so the
// test first requires Open to succeed: negotiation must hand the hint to the
// policy, and only the policy may make the refusal.
func TestHostIntegration_OpenAIChatGPTPlanStrictPromptCacheKeyRefusalStopsBeforeProvider(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	backend := buildPlanHostBackend(t, reference, stagePlanHostStore(t), "plan-prompt-cache-strict",
		map[string]string{"policy_mode": "strict"})

	decoded := decodePlanPromptCacheRequest(t)
	call := *decoded.Call
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, err := backend.Open(ctx, call, planPromptCacheCandidate("plan-prompt-cache-strict"))
	if err != nil {
		t.Fatalf("the ABI must accept the semantic carrier before the policy decides: %v", err)
	}
	_, err = aipapi.Collect(ctx, stream)
	if err == nil {
		t.Fatal("the strict posture must refuse the prompt-cache hint")
	}
	var classified *adapter.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("the strict refusal must reach the host as a classified failure, got %T: %v", err, err)
	}
	if classified.Code != "failed_precondition" {
		t.Fatalf("strict prompt-cache refusal code = %q, want failed_precondition for an unsupported capability", classified.Code)
	}
	if classified.Retryable || classified.OutputCommitted {
		t.Fatalf("strict prompt-cache refusal must be terminal without committed output: %+v", classified)
	}
	if strings.Contains(err.Error(), planPromptCacheKey) {
		t.Fatalf("the strict refusal must not echo the client cache id: %v", err)
	}
	if requests := reference.captured(); len(requests) != 0 {
		t.Fatalf("the strict refusal reached the provider: %d request(s)", len(requests))
	}
}

// TestHostIntegration_OpenAIChatGPTPlanUnknownSemanticExtensionFailsClosed
// proves the other half of advertising the bounded residual carrier: only the
// recognized prompt-cache alias may be omitted. Any other semantic residual has
// no public carrier, so the connector must refuse it before provider I/O instead
// of dropping it, and the refusal must not echo the extension's value.
func TestHostIntegration_OpenAIChatGPTPlanUnknownSemanticExtensionFailsClosed(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	backend := buildPlanHostBackend(t, reference, stagePlanHostStore(t), "plan-prompt-cache-unknown", nil)

	decoded := decodePlanPromptCacheRequest(t)
	call := *decoded.Call
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
	call.SemanticExtensions = append(call.SemanticExtensions, aipapi.SemanticExtension{
		Namespace:   "acme",
		Type:        "beta",
		Implementor: "acme",
		Direction:   aipapi.SemanticExtensionDirectionRequest,
		Presence:    aipapi.SemanticExtensionValue,
		Data:        json.RawMessage(`{"marker":"` + unknownSemanticMarker + `"}`),
	})
	if err := call.Validate(); err != nil {
		t.Fatalf("the unknown-semantic fixture must be a valid canonical call: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, err := backend.Open(ctx, call, planPromptCacheCandidate("plan-prompt-cache-unknown"))
	if err != nil {
		t.Fatalf("the ABI must carry the residual semantic so the connector can decide: %v", err)
	}
	_, err = aipapi.Collect(ctx, stream)
	if err == nil {
		t.Fatal("an unknown semantic extension must fail closed, not be dropped")
	}
	var classified *adapter.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("the unknown-semantic refusal must reach the host as a classified failure, got %T: %v", err, err)
	}
	if classified.Code != "failed_precondition" {
		t.Fatalf("unknown-semantic refusal code = %q, want failed_precondition", classified.Code)
	}
	if strings.Contains(err.Error(), unknownSemanticMarker) || strings.Contains(err.Error(), planPromptCacheKey) {
		t.Fatalf("the refusal must not echo client extension data: %v", err)
	}
	if requests := reference.captured(); len(requests) != 0 {
		t.Fatalf("the unknown-semantic refusal reached the provider: %d request(s)", len(requests))
	}
}
