package backendplugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/capabilities"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// webSearchDeclarationsLiteral is the documented cross-module wire contract:
// the OpenAI Responses frontend preserves hosted web-search declaration types
// under this opaque extension key, and the SIWC connector consumes the same
// literal. Both modules define it privately; this staged proof pins the two
// definitions together through the real executables and the real decode path.
const webSearchDeclarationsLiteral = "openairesponses.web_search_declarations"

// decodePlanWebSearchRequest decodes one real OpenAI Responses create request
// through the production frontend. The request carries a hosted web-search
// declaration, a caller-executed function tool, and a complete legal history.
func decodePlanWebSearchRequest(t *testing.T) *openairesponses.DecodedCreate {
	t.Helper()
	body := `{
		"model":"gpt-5",
		"stream":false,
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"find the weather"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"I will look it up."}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"thank you"}]}
		],
		"tools":[
			{"type":"function","name":"lookup","parameters":{"type":"object"}},
			{"type":"namespace","name":"multi_agent_v1","description":"Delegate control.","tools":[
				{"type":"function","name":"close_agent","parameters":{"type":"object"}}
			]},
			{"type":"web_search_preview","user_location":{"type":"approximate","city":"San Francisco"}}
		]
	}`
	decoded, err := openairesponses.DecodeCreateRequest([]byte(body), openairesponses.DecodeOptions{
		RouteSelector: planKind + ":gpt-5",
	})
	if err != nil {
		t.Fatalf("decode the hosted web-search request through the production frontend: %v", err)
	}
	raw, ok := decoded.Call.Extensions[webSearchDeclarationsLiteral]
	if !ok {
		t.Fatalf("the frontend must preserve the hosted declaration under the documented carrier, got %v", decoded.Call.Extensions)
	}
	var declarations []string
	if err := json.Unmarshal(raw, &declarations); err != nil {
		t.Fatalf("the carrier must be a JSON array of type strings: %v (%s)", err, raw)
	}
	if !slices.Equal(declarations, []string{"web_search_preview"}) {
		t.Fatalf("carrier declarations = %v, want only the exact hosted declaration", declarations)
	}
	names := make([]string, 0, len(decoded.Call.Tools))
	for _, tool := range decoded.Call.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	if want := []string{"lookup", "multi_agent_v1__close_agent"}; !slices.Equal(names, want) {
		t.Fatalf("canonical tool names = %v, want the top-level and namespace-projected functions %v", names, want)
	}
	if err := decoded.Call.Validate(); err != nil {
		t.Fatalf("the decoded call is not canonical: %v", err)
	}
	return decoded
}

// planWebSearchCandidate addresses the staged SIWC instance the same way the
// host does: the configured instance id plus the canonical model id.
func planWebSearchCandidate(instanceID string) routing.AttemptCandidate {
	return routing.AttemptCandidate{
		Primary: routing.Primary{Backend: instanceID, Model: planKind + "/gpt-5"},
		Key:     instanceID + ":" + planKind + "/gpt-5",
	}
}

// TestHostIntegration_OpenAIChatGPTPlanRuntimeProfileAdmitsHostedWebSearchCarriage
// is the end-to-end proof of requirement 1.7 and 14.12 after task 4.3:
//
//   - the real staged connector's runtime Resolve profile advertises
//     opaque-extension capability and the exact call extension;
//   - the same runtime facts, evaluated by the core admission function, admit
//     the decoded carrier call;
//   - the static descriptor capability alone (no exact extension) rejects it;
//   - the interoperable posture omits the declaration upstream without
//     dropping or fabricating a history item.
func TestHostIntegration_OpenAIChatGPTPlanRuntimeProfileAdmitsHostedWebSearchCarriage(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	storePath := stagePlanHostStore(t)
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

	instanceConfig, err := yaml.Marshal(map[string]string{"profile_store_path": storePath})
	if err != nil {
		t.Fatalf("marshal instance config: %v", err)
	}
	const instanceID = "plan-web-search"
	backend := buildCoexistenceBackend(t, reg, planKind, instanceID, string(instanceConfig))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	decoded := decodePlanWebSearchRequest(t)
	call := *decoded.Call
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming
	cand := planWebSearchCandidate(instanceID)

	// 1. The runtime Resolve profile is the admission authority. The connector's
	// base profile and both per-attempt Resolve callbacks must agree.
	if _, ok := backend.Caps[aipapi.CapabilityOpaqueExtensions]; !ok {
		t.Fatalf("the resolved backend profile must advertise opaque_extensions, got %v", backend.Caps)
	}
	exact := aipapi.ExtensionRequirement{Namespace: "call", Type: webSearchDeclarationsLiteral}
	if !slices.Contains(backend.DialectSupport.ExtensionTypes, exact) {
		t.Fatalf("the resolved backend profile must advertise the exact %+v extension, got %+v",
			exact, backend.DialectSupport.ExtensionTypes)
	}
	caps := backend.ResolveCaps(ctx, call, cand)
	if _, ok := caps[aipapi.CapabilityOpaqueExtensions]; !ok {
		t.Fatalf("per-attempt ResolveCaps lost opaque_extensions: %v", caps)
	}
	dialectSupport := backend.ResolveDialectSupport(ctx, call, cand)
	if !slices.Contains(dialectSupport.ExtensionTypes, exact) {
		t.Fatalf("per-attempt ResolveDialectSupport lost the exact extension: %+v", dialectSupport.ExtensionTypes)
	}
	transportCaps := backend.ResolveTransportCaps(ctx, call, cand)

	required := aipapi.DeriveProtocolRequirements(call)
	if !slices.Contains(required.ExtensionTypes, exact) {
		t.Fatalf("the decoded carrier call must require the exact %+v extension, got %+v", exact, required.ExtensionTypes)
	}

	// 2. The core admission function, fed by the real runtime Resolve facts,
	// admits the call losslessly.
	target := aipapi.LegacyProjectionTargetFromCaps(caps, aipapi.ReasoningReplaySupport{})
	target.SupportedExtensions = append([]aipapi.ExtensionRequirement(nil), dialectSupport.ExtensionTypes...)
	admitted := capabilities.AdmitCandidate(ctx, call, call.Invocation, cand, capabilities.CandidateFacts{
		Caps:             caps,
		TransportCaps:    transportCaps,
		DialectSupport:   dialectSupport,
		ProjectionTarget: target,
	})
	if admitted.Kind != aipapi.NegotiationLossless {
		t.Fatalf("the real runtime Resolve profile must admit the carrier call, got %v (%v)", admitted.Kind, admitted.Err())
	}

	// 3. Static descriptor capability alone is not interchangeable with runtime
	// admission: opaque_extensions without the exact resolved extension rejects.
	staticOnly := capabilities.AdmitCandidate(ctx, call, call.Invocation, cand, capabilities.CandidateFacts{
		Caps: aipapi.NewBackendCaps(
			aipapi.CapabilityStreaming,
			aipapi.CapabilityTools,
			aipapi.CapabilityOpaqueExtensions,
		),
		TransportCaps:    transportCaps,
		DialectSupport:   aipapi.DialectSupport{},
		ProjectionTarget: aipapi.LegacyProjectionTargetFromCaps(caps, aipapi.ReasoningReplaySupport{}),
	})
	if staticOnly.Kind != aipapi.NegotiationReject {
		t.Fatalf("static descriptor capability alone must not admit the carrier, got %v", staticOnly.Kind)
	}
	if !slices.Contains(staticOnly.Requirements.MissingExtensions, exact) {
		t.Fatalf("the static-only rejection must name the missing exact extension, got %+v",
			staticOnly.Requirements.MissingExtensions)
	}

	// 4. The interoperable posture serves the call. The provider sees no hosted
	// declaration of any kind, and every authored history item survives.
	stream, err := backend.Open(ctx, call, cand)
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

	requests := reference.captured()
	if len(requests) != 1 {
		t.Fatalf("the provider must observe exactly ONE request, got %d", len(requests))
	}
	body := requests[0].Body
	if strings.Contains(string(body), "web_search") {
		t.Fatalf("the provider request must not carry any hosted web-search declaration:\n%s", body)
	}
	var document struct {
		Tools []struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tools"`
		Input []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			Tools []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"input"`
	}
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("decode the provider request: %v", err)
	}
	var tools []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	tools = append(tools, document.Tools...)
	for _, item := range document.Input {
		if item.Type == "additional_tools" {
			tools = append(tools, item.Tools...)
		}
	}
	if len(tools) != 2 ||
		tools[0].Type != "function" || tools[1].Type != "function" {
		t.Fatalf("the provider request must carry exactly the two function declarations, got %+v", tools)
	}
	toolNames := []string{tools[0].Name, tools[1].Name}
	slices.Sort(toolNames)
	if want := []string{"lookup", "multi_agent_v1__close_agent"}; !slices.Equal(toolNames, want) {
		t.Fatalf("provider tool names = %v, want the top-level and namespace-projected functions %v", toolNames, want)
	}
	if strings.Contains(string(body), `"namespace"`) {
		t.Fatalf("the provider request must not carry a raw namespace declaration:\n%s", body)
	}
	var roles, texts []string
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
	if want := []string{"user", "assistant", "user"}; !slices.Equal(roles, want) {
		t.Fatalf("provider history roles = %v, want the exact authored order %v", roles, want)
	}
	if want := []string{"find the weather", "I will look it up.", "thank you"}; !slices.Equal(texts, want) {
		t.Fatalf("provider history texts = %v, want the exact authored trajectory %v", texts, want)
	}
}

// TestHostIntegration_OpenAIChatGPTPlanStrictWebSearchRefusalStopsBeforeProvider
// proves requirement 6.4's strict posture through the real staged executable:
// the declaration is refused before the provider request exists.
func TestHostIntegration_OpenAIChatGPTPlanStrictWebSearchRefusalStopsBeforeProvider(t *testing.T) {
	t.Parallel()
	reference := newPlanHostReference(t)
	storePath := stagePlanHostStore(t)
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

	instanceConfig, err := yaml.Marshal(map[string]string{
		"profile_store_path": storePath,
		"policy_mode":        "strict",
	})
	if err != nil {
		t.Fatalf("marshal instance config: %v", err)
	}
	const instanceID = "plan-web-search-strict"
	backend := buildCoexistenceBackend(t, reg, planKind, instanceID, string(instanceConfig))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	decoded := decodePlanWebSearchRequest(t)
	call := *decoded.Call
	call.Invocation.Operation = aipapi.OperationOpenAIResponses
	call.Invocation.DeliveryMode = aipapi.DeliveryModeNonStreaming

	stream, err := backend.Open(ctx, call, planWebSearchCandidate(instanceID))
	if err != nil {
		t.Fatalf("open the strict staged connector: %v", err)
	}
	_, err = aipapi.Collect(ctx, stream)
	if err == nil {
		t.Fatal("the strict posture must refuse the hosted declaration")
	}
	var classified *adapter.ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("the strict refusal must reach the host as a classified failure, got %T: %v", err, err)
	}
	if classified.Code != "failed_precondition" {
		t.Fatalf("strict refusal code = %q, want failed_precondition for an unsupported capability", classified.Code)
	}
	if classified.Retryable || classified.OutputCommitted {
		t.Fatalf("strict refusal must be terminal without committed output: %+v", classified)
	}
	if requests := reference.captured(); len(requests) != 0 {
		t.Fatalf("the strict refusal reached the provider: %d request(s)", len(requests))
	}
}
