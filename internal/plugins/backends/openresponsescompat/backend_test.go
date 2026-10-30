package openresponsescompat

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"gopkg.in/yaml.v3"
)

func mustBuild(t *testing.T, instanceID, raw string) execbackend.Backend {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	be, err := Build(instanceID, n, nil)
	if err != nil {
		t.Fatal(err)
	}
	return be
}

func buildErr(t *testing.T, instanceID, raw string) error {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	_, err := Build(instanceID, n, nil)
	return err
}

func openResponsesCall(op aipapi.Operation) aipapi.Call {
	return aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartText, Text: "hi"}},
		}},
		Invocation: aipapi.Invocation{
			Operation:     op,
			DeliveryMode:  aipapi.DeliveryModeNonStreaming,
			TransportMode: aipapi.TransportModeNonStreaming,
		},
	}
}

func TestBackend_EnforcesMaxOutputTokensOnWire(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", minimalYAML)

	// The generic OR backend genuinely serializes max output: it declares
	// EnforcesMaxOutputTokens so an authority spend-cap clamp binds, and the
	// create request carries the clamped max_output_tokens value on the wire.
	// Core routing relies on CanEnforceAuthorityMaxOutputTokens to admit (not
	// exclude) a candidate under a spend cap (executor_open_attempt clamp gate).
	if !be.EnforcesMaxOutputTokens {
		t.Fatal("generic OR backend must set EnforcesMaxOutputTokens=true")
	}
	maxTokens := 128
	call := itemAuthorityCreateCall()
	call.Options.MaxOutputTokens = &maxTokens
	if !be.CanEnforceAuthorityMaxOutputTokens(&call) {
		t.Fatal("CanEnforceAuthorityMaxOutputTokens must be true for a clamped call")
	}

	body, err := buildCreateRequest("my-or", testSpec(), call, routing.AttemptCandidate{Primary: routing.Primary{Model: "model-x"}})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, string(body))
	}
	if got := string(payload["max_output_tokens"]); got != "128" {
		t.Fatalf("max_output_tokens = %s, want 128 (spend-cap clamp must be serialized)", got)
	}
}

func TestFactory_LegacyAuthorityProjectsBeforeNetwork(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", minimalYAML)
	if be.Open == nil {
		t.Fatal("expected Open seam")
	}
	// A legacy message-authority call is projected to ordered items by the
	// explicit legacy→ordered-items projector, then rejected only when the
	// projected request is incomplete (empty candidate model). It must never
	// hit the old item-authority-only rejection.
	_, err := be.Open(context.Background(), openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("Open must reject an incomplete projected legacy request before network work")
	}
	if !errors.Is(err, ErrUnrepresentable) {
		t.Fatalf("Open error = %v, want ErrUnrepresentable (projected request incomplete)", err)
	}
	if strings.Contains(err.Error(), "item authority is required") {
		t.Fatalf("legacy authority must not be rejected outright: %q", err)
	}
}

func TestFactory_OpenNilContextRejected(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", minimalYAML)
	_, err := be.Open(nil, openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected nil context rejection")
	}
}

func TestFactory_TransportCapsAccurate(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", minimalYAML)
	tc := be.TransportCaps
	if !tc.Supports(aipapi.OperationOpenResponsesCreate, aipapi.TransportModeStreaming) {
		t.Fatal("create must support SSE streaming")
	}
	if !tc.Supports(aipapi.OperationOpenResponsesCreate, aipapi.TransportModeNonStreaming) {
		t.Fatal("create must support JSON non-streaming")
	}
	if !tc.Supports(aipapi.OperationContextCompaction, aipapi.TransportModeNonStreaming) {
		t.Fatal("compact must support non-streaming transport")
	}
	if tc.Supports(aipapi.OperationContextCompaction, aipapi.TransportModeStreaming) {
		t.Fatal("compact must not advertise streaming transport")
	}
	if tc.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeNonStreaming) {
		t.Fatal("openresponses backend must not advertise chat-completions transport")
	}
	if tc.Supports(aipapi.OperationOpenAIResponses, aipapi.TransportModeNonStreaming) {
		t.Fatal("openresponses backend must not advertise openai.responses transport")
	}
}

func TestBackend_CapsAccurate(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", minimalYAML+`capabilities: [ordered_items, tools]
`)
	caps := execbackend.EffectiveCaps(context.Background(), be, openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{})
	if !capsHas(caps, aipapi.CapabilityOrderedItems) {
		t.Fatal("ordered_items capability missing")
	}
	if !capsHas(caps, aipapi.CapabilityTools) {
		t.Fatal("tools capability missing")
	}
	if capsHas(caps, aipapi.CapabilityStreaming) {
		t.Fatal("streaming must not be advertised when not declared")
	}
	if capsHas(caps, aipapi.CapabilityAssistantPhase) {
		t.Fatal("assistant_phase must not be advertised when not declared")
	}
}

func TestBackend_DialectSupportApplied(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", minimalYAML+`dialects:
  item:
    - dialect: openresponses.2026-04-24
  compaction:
    - dialect: openresponses.2026-04-24
  extensions:
    - namespace: acme
      type: widget
`)
	ds := execbackend.EffectiveDialectSupport(context.Background(), be, openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{})
	if len(ds.ItemDialects) != 1 || ds.ItemDialects[0].Dialect != "openresponses.2026-04-24" {
		t.Fatalf("item dialects = %+v", ds.ItemDialects)
	}
	if len(ds.CompactionDialects) != 1 || ds.CompactionDialects[0].Dialect != "openresponses.2026-04-24" {
		t.Fatalf("compaction dialects = %+v", ds.CompactionDialects)
	}
	if len(ds.ExtensionTypes) != 1 || ds.ExtensionTypes[0].Type != "widget" {
		t.Fatalf("extensions = %+v", ds.ExtensionTypes)
	}
}

func TestFactory_InstanceIndependence(t *testing.T) {
	t.Parallel()
	beA := mustBuild(t, "or-a", `backend_prefix: prefix-a
base_url: https://api-a.example.com/openresponses/v1
capabilities: [ordered_items]
`)
	beB := mustBuild(t, "or-b", `backend_prefix: prefix-b
base_url: https://api-b.example.com/openresponses/v1
capabilities: [ordered_items, tools]
`)
	if len(beA.BackendPrefixes) != 1 || beA.BackendPrefixes[0] != "prefix-a" {
		t.Fatalf("instance A prefixes = %#v", beA.BackendPrefixes)
	}
	if len(beB.BackendPrefixes) != 1 || beB.BackendPrefixes[0] != "prefix-b" {
		t.Fatalf("instance B prefixes = %#v", beB.BackendPrefixes)
	}
	if beA.BackendPrefixes[0] == beB.BackendPrefixes[0] {
		t.Fatal("instances must not share a prefix")
	}
	capsA := execbackend.EffectiveCaps(context.Background(), beA, openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{})
	capsB := execbackend.EffectiveCaps(context.Background(), beB, openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{})
	if capsHas(capsA, aipapi.CapabilityTools) {
		t.Fatal("instance A must not inherit instance B capabilities")
	}
	if !capsHas(capsB, aipapi.CapabilityTools) {
		t.Fatal("instance B must keep its declared capabilities")
	}
	errA, errB := openErrOf(t, beA), openErrOf(t, beB)
	if !strings.Contains(errA.Error(), "prefix-a") || !strings.Contains(errB.Error(), "prefix-b") {
		t.Fatalf("instance-scoped errors wrong: A=%q B=%q", errA, errB)
	}
}

func TestFactory_PrefixSyntaxRejected(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "acme/prod", "acme:prod"} {
		err := buildErr(t, "or-inst", "backend_prefix: "+prefix+"\nbase_url: https://api.example.com/v1\n")
		if err == nil {
			t.Fatalf("expected prefix %q rejection", prefix)
		}
	}
}

func TestFactory_NoAuthBuilds(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", minimalYAML)
	if be.Open == nil {
		t.Fatal("expected Open seam for no-auth instance")
	}
}

func TestFactory_LifecycleAndInvalidBackendAreObservable(t *testing.T) {
	t.Parallel()

	var n yaml.Node
	if err := yaml.Unmarshal([]byte(minimalYAML), &n); err != nil {
		t.Fatal(err)
	}
	if _, err := LifecycleOpenResponsesCompatible("or-inst", n, nil, pluginreg.BackendFactoryDeps{}); err != nil {
		t.Fatalf("lifecycle build failed: %v", err)
	}

	be := NewBackend(BackendSpec{})
	if _, err := be.Open(context.Background(), openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{}); err == nil {
		t.Fatal("invalid backend must reject Open")
	}
}

func TestFactory_EnvCredentialNameAccepted(t *testing.T) {
	t.Parallel()
	raw := minimalYAML + "api_key_env_var_root: MY_OPENRESPONSES_KEY\n"
	be := mustBuild(t, "or-inst", raw)
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-or" {
		t.Fatalf("prefixes = %#v", be.BackendPrefixes)
	}
}

func TestFactory_StaticModelInventoryNoNetwork(t *testing.T) {
	t.Parallel()
	raw := minimalYAML + `models:
  source: inline
  items:
    - canonical_id: my-or/model-a
      native_id: model-a
`
	be := mustBuild(t, "or-inst", raw)
	if be.ModelInventory == nil {
		t.Fatal("expected static model inventory")
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("source = %q, want static_inline", snap.Source)
	}
	if len(snap.Models) != 1 || snap.Models[0].NativeID != "model-a" {
		t.Fatalf("models = %+v", snap.Models)
	}
}

func TestFactory_UnknownFieldRejectedWithInstanceIdentity(t *testing.T) {
	t.Parallel()
	err := buildErr(t, "or-inst-9", minimalYAML+"route_priority: low\n")
	if err == nil {
		t.Fatal("expected unknown field rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "route_priority") || !strings.Contains(msg, "or-inst-9") {
		t.Fatalf("error = %q, want unknown field + instance identity", msg)
	}
}

func TestFactory_ForbiddenSecretRejectedWithoutEcho(t *testing.T) {
	t.Parallel()
	secret := "sk-openresponses-factory-secret"
	err := buildErr(t, "or-inst", minimalYAML+"api_key: "+secret+"\n")
	if err == nil {
		t.Fatal("expected forbidden literal secret rejection")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed literal secret: %q", err)
	}
}

func TestFactory_EndpointSanitized(t *testing.T) {
	t.Parallel()
	be := mustBuild(t, "or-inst", `backend_prefix: my-or
base_url: https://api.example.com/openresponses/v1
`)
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-or" {
		t.Fatalf("prefixes = %#v", be.BackendPrefixes)
	}
}

func openErrOf(t *testing.T, be execbackend.Backend) error {
	t.Helper()
	_, err := be.Open(context.Background(), openResponsesCall(aipapi.OperationOpenResponsesCreate), routing.AttemptCandidate{})
	if err == nil {
		t.Fatal("expected Open error")
	}
	return err
}

func capsHas(caps aipapi.BackendCaps, targetCap aipapi.Capability) bool {
	if caps == nil {
		return false
	}
	_, ok := caps[targetCap]
	return ok
}
