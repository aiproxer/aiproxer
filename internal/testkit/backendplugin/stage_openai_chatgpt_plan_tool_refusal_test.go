package backendplugin_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/processhost"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	bpkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

// TestHostIntegration_OpenAIChatGPTPlanRefusesDisagreeingToolResultCarriers
// proves the canonical pair with two populated, DIFFERENT tool-result carriers
// is refused explicitly before provider I/O, rather than one carrier being
// silently chosen after the ABI round trip.
//
// Message authority is the connector's production path, so the bridge must
// preserve both the literal Text and the structured Content for the projector
// to make this decision.
func TestHostIntegration_OpenAIChatGPTPlanRefusesDisagreeingToolResultCarriers(t *testing.T) {
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
	backend := buildCoexistenceBackend(t, reg, planKind, "plan-tool-refusal", string(instanceConfig))

	call := aipapi.Call{
		ID:      "plan-tool-refusal",
		Session: aipapi.SessionRef{ALegID: "aleg-plan-refusal"},
		Invocation: aipapi.Invocation{
			Operation:    aipapi.OperationOpenAIResponses,
			DeliveryMode: aipapi.DeliveryModeNonStreaming,
		},
		Messages: []aipapi.Message{
			{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("find the widget")}},
			{Role: aipapi.RoleAssistant, Parts: []aipapi.Part{{
				Kind: aipapi.PartJSON, ToolCallID: "call_widget_1", ToolName: "widget_lookup",
				Content: json.RawMessage(`{"id":"w1"}`),
			}}},
			{Role: aipapi.RoleTool, Parts: []aipapi.Part{{
				Kind: aipapi.PartToolResult, ToolCallID: "call_widget_1", ToolName: "widget_lookup",
				Text:    "the widget is available",
				Content: json.RawMessage(`{"ok":true}`),
			}}},
		},
	}
	if err := call.Validate(); err != nil {
		t.Fatalf("the disagreement fixture must be canonical-valid: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stream, err := backend.Open(ctx, call, routing.AttemptCandidate{
		Primary: routing.Primary{Backend: "plan-tool-refusal", Model: planKind + "/gpt-5"},
		Key:     "plan-tool-refusal:" + planKind + "/gpt-5",
	})
	if err == nil {
		_, err = aipapi.Collect(ctx, stream)
	}
	if err == nil {
		t.Fatal("a disagreeing tool-result carrier pair must be refused")
	}
	if requests := reference.captured(); len(requests) != 0 {
		t.Fatalf("a refused canonical call reached the provider %d time(s)", len(requests))
	}
}
