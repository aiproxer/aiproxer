package backendplugin_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/infra/logging"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// operatorOmissionWarning is the fixed, content-free host warning the adapter
// records for the private SIWC omission marker. It is deliberately independent
// of the connector's canonical warning text so a leak would be visible.
const operatorOmissionWarning = "openai-chatgpt-plan: request degraded under the SIWC preview policy"

// withOperatorLogger installs the production operator logger shape as the
// process default. The returned buffer is the actual host log the operator sees.
func withOperatorLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	logger, err := logging.NewLogger(config.LoggingConfig{Level: "warn", Format: "json"}, &logs)
	if err != nil {
		t.Fatalf("build the operator logger: %v", err)
	}
	previous := slog.Default()
	slog.SetDefault(logger)
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// decodePlanOmissionRequest decodes one default-Codex-shaped request carrying
// both the F1 prompt-cache hint and the F3 hosted web-search declaration through
// the production frontend.
func decodePlanOmissionRequest(t *testing.T) *openairesponses.DecodedCreate {
	t.Helper()
	body := `{
		"model":"gpt-5",
		"stream":false,
		"prompt_cache_key":"` + planPromptCacheKey + `",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"first question"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first answer"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"follow up"}]}
		],
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"web_search_preview"}
		]
	}`
	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: planKind + ":gpt-5",
	})
	if err != nil {
		t.Fatalf("decode the combined F1+F3 request through the production frontend: %v", err)
	}
	if decoded.Call.PromptCacheKey != planPromptCacheKey {
		t.Fatalf("decoded prompt cache key = %q, want %q", decoded.Call.PromptCacheKey, planPromptCacheKey)
	}
	if _, ok := decoded.Call.Extensions[webSearchDeclarationsLiteral]; !ok {
		t.Fatalf("the frontend lost the hosted web-search carrier: %v", decoded.Call.Extensions)
	}
	if err := decoded.Call.Validate(); err != nil {
		t.Fatalf("the decoded combined call is not canonical: %v", err)
	}
	return decoded
}

// TestHostIntegration_OpenAIChatGPTPlanOmissionWarningIsOperatorVisibleOnce is
// the real spawned-connector/host acceptance for the paired F1+F3 fix:
//
//   - the default-Codex prompt-cache hint crosses the F1 semantic carrier;
//   - the hosted web-search declaration is omitted upstream;
//   - the exact private marker is consumed by the host adapter and never reaches
//     the client canonical stream;
//   - the actual operator logger receives exactly one fixed, content-free
//     warning with no client cache id, declaration text or extension detail;
//   - stream completion and usage survive unchanged.
func TestHostIntegration_OpenAIChatGPTPlanOmissionWarningIsOperatorVisibleOnce(t *testing.T) {
	logs := withOperatorLogger(t)
	reference := newPlanHostReference(t)
	backend := buildPlanHostBackend(t, reference, stagePlanHostStore(t), "plan-omission-warning", nil)

	decoded := decodePlanOmissionRequest(t)
	call := *decoded.Call
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, err := backend.Open(ctx, call, planPromptCacheCandidate("plan-omission-warning"))
	if err != nil {
		t.Fatalf("open the staged connector: %v", err)
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
	if collected.InputTokens != 10 || collected.OutputTokens != 5 || collected.TotalTokens != 15 {
		t.Fatalf("downstream usage = %d/%d/%d, want the provider-reported 10/5/15",
			collected.InputTokens, collected.OutputTokens, collected.TotalTokens)
	}
	if len(collected.Warnings) != 0 {
		t.Fatalf("the private omission marker leaked into the client canonical warnings: %v", collected.Warnings)
	}

	requests := reference.captured()
	if len(requests) != 1 {
		t.Fatalf("the provider must observe exactly ONE request, got %d", len(requests))
	}
	body := requests[0].Body
	for _, forbidden := range []string{planPromptCacheKey, `"prompt_cache_key"`, "web_search"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("provider request must not carry %q:\n%s", forbidden, body)
		}
	}

	recorded := logs.String()
	if got := strings.Count(recorded, operatorOmissionWarning); got != 1 {
		t.Fatalf("operator omission warnings = %d, want exactly one; log=%s", got, recorded)
	}
	for _, forbidden := range []string{
		planPromptCacheKey,
		`"prompt_cache_key"`,
		"web_search",
		"Extensions[",
		"siwc_preview_omission_v1",
	} {
		if strings.Contains(recorded, forbidden) {
			t.Fatalf("operator log must not contain %q; log=%s", forbidden, recorded)
		}
	}

	// The warning itself also proves both omissions reached policy: the F1 cache
	// hint crossed the ABI and the F3 hosted declaration was consumed.
	if len(collected.Warnings) != 0 {
		t.Fatalf("unexpected client warnings: %v", collected.Warnings)
	}
}

// TestHostIntegration_OpenAIChatGPTPlanNoOmissionOrStrictEmitsNoOperatorWarning
// proves the negative side of the paired contract: no served request means no
// operator warning. It covers a strict refusal and a request with no omission.
func TestHostIntegration_OpenAIChatGPTPlanNoOmissionOrStrictEmitsNoOperatorWarning(t *testing.T) {
	t.Run("strict refusal", func(t *testing.T) {
		logs := withOperatorLogger(t)
		reference := newPlanHostReference(t)
		backend := buildPlanHostBackend(t, reference, stagePlanHostStore(t), "plan-omission-strict",
			map[string]string{"policy_mode": "strict"})

		decoded := decodePlanOmissionRequest(t)
		call := *decoded.Call
		call.Invocation.Operation = aipapi.OperationOpenAIResponses
		call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		stream, err := backend.Open(ctx, call, planPromptCacheCandidate("plan-omission-strict"))
		if err != nil {
			t.Fatalf("open the strict staged connector: %v", err)
		}
		_, err = aipapi.Collect(ctx, stream)
		if err == nil {
			t.Fatal("the strict posture must refuse the omission")
		}
		var classified *adapter.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != "failed_precondition" {
			t.Fatalf("strict refusal = %v, want a failed_precondition classification", err)
		}
		if requests := reference.captured(); len(requests) != 0 {
			t.Fatalf("the strict refusal reached the provider: %d request(s)", len(requests))
		}
		if got := strings.Count(logs.String(), operatorOmissionWarning); got != 0 {
			t.Fatalf("strict refusal operator warnings = %d, want 0; log=%s", got, logs.String())
		}
	})

	t.Run("no omission", func(t *testing.T) {
		logs := withOperatorLogger(t)
		reference := newPlanHostReference(t)
		backend := buildPlanHostBackend(t, reference, stagePlanHostStore(t), "plan-omission-none", nil)

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		call := aipapi.Call{
			ID:      "plan-no-omission",
			Session: aipapi.SessionRef{ALegID: "aleg-plan-no-omission"},
			Invocation: aipapi.Invocation{
				Operation:    aipapi.OperationOpenAIResponses,
				DeliveryMode: aipapi.DeliveryModeNonStreaming,
			},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello plan")}}},
		}
		stream, err := backend.Open(ctx, call, planPromptCacheCandidate("plan-omission-none"))
		if err != nil {
			t.Fatalf("open the staged connector: %v", err)
		}
		collected, err := aipapi.Collect(ctx, stream)
		if err != nil {
			t.Fatalf("collect the no-omission result: %v", err)
		}
		if len(collected.Warnings) != 0 {
			t.Fatalf("no-omission client warnings = %v, want none", collected.Warnings)
		}
		if got := strings.Count(logs.String(), operatorOmissionWarning); got != 0 {
			t.Fatalf("no-omission operator warnings = %d, want 0; log=%s", got, logs.String())
		}
	})
}
