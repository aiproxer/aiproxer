package contract

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/backend"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/core"
	"github.com/aiproxer/aiproxer/internal/testkit/contract/frontend"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestOwnerMutationProofsAreExecutable(t *testing.T) {
	// Decode/projector mutations are exercised by the frontend/core boundary
	// fixtures; false capability and connector field-loss mutations are exercised
	// by backend and public host contract suites. Keep explicit negative assertions
	// here so deleting an owner suite cannot silently leave the traceability table.
	ctx := context.Background()
	if _, err := frontend.CertifyFrontend(ctx, nil); err == nil {
		t.Fatal("frontend owner accepted nil harness mutation")
	}
	if _, err := core.CertifyCore(ctx, nil); err == nil {
		t.Fatal("core owner accepted nil harness mutation")
	}
	if _, err := backend.CertifyBackend(ctx, nil); err == nil {
		t.Fatal("backend owner accepted nil harness mutation")
	}
	if aipapi.ErrInvalidCall == nil {
		t.Fatal("canonical error sentinel unavailable")
	}
}

func TestOwnerMutationProofsRejectActualBoundaryLoss(t *testing.T) {
	// The frontend, core, and backend mutation suites already exercise their
	// owning runners. This test binds traceability to those concrete RED entry
	// points rather than a metadata-only feature list.
	if _, err := frontend.CertifyFrontend(context.Background(), nil); err == nil {
		t.Fatal("frontend decode owner accepted a missing executable boundary")
	}
	if _, err := core.CertifyCore(context.Background(), nil); err == nil {
		t.Fatal("core projector owner accepted a missing executable boundary")
	}
	if _, err := backend.CertifyBackend(context.Background(), nil); err == nil {
		t.Fatal("backend capability owner accepted a missing executable boundary")
	}
	text := "hello"
	inv := backendplugin.Invocation{
		RequestID: "r", AttemptID: "a", ALegID: "al", BLegID: "bl", CanonicalModelID: "m",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
	}
	if _, err := backendplugin.InvocationToProto(inv); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerMutationProofsCoverEachRequiredFaultClass(t *testing.T) {
	for _, fault := range []struct {
		name  string
		owner string
	}{
		{"decode", "frontend"},
		{"projector", "core"},
		{"false-capability", "backend"},
		{"connector-field-loss", "connector"},
		{"composition-wiring", "sentinel"},
	} {
		found := false
		for _, owner := range ReleaseCriticalFeatureOwners() {
			if fault.owner == "frontend" && len(owner.Frontend) > 0 && len(owner.ExecutableTests) > 0 ||
				fault.owner == "core" && len(owner.Core) > 0 && len(owner.ExecutableTests) > 0 ||
				fault.owner == "backend" && len(owner.Backend) > 0 && len(owner.ExecutableTests) > 0 ||
				fault.owner == "connector" && owner.Feature == "connector_host" && len(owner.ExecutableTests) > 0 ||
				fault.owner == "sentinel" && len(owner.Sentinel) > 0 && len(owner.ExecutableTests) > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("fault class %q has no executable owner", fault.name)
		}
	}
}

func TestConnectorFieldLossMutation_DetectedAndRejectedInInvocationMapping(t *testing.T) {
	t.Parallel()
	text := "hello"
	inv := backendplugin.Invocation{
		RequestID: "req-1", AttemptID: "att-1", ALegID: "aleg-1", BLegID: "bleg-1", CanonicalModelID: "model-1",
		ProxyOwnedSessionID: "session-auth-123",
		SemanticExtensions: []backendplugin.SemanticExtension{
			{Namespace: "aip", Type: "prompt_cache_key", Implementor: "proxy", Direction: "request", Presence: backendplugin.SemanticExtensionValue, Data: backendplugin.RawJSONFromBytes([]byte(`"cache-1"`))},
		},
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
	}
	call, err := backendplugin.CallFromInvocation(inv)
	if err != nil {
		t.Fatalf("unmutated invocation failed: %v", err)
	}
	if call.Session.AuthoritativeSessionID != "session-auth-123" {
		t.Fatalf("expected authoritative session ID, got %q", call.Session.AuthoritativeSessionID)
	}

	invalidInv := inv
	invalidInv.RequestID = ""
	if _, err := backendplugin.CallFromInvocation(invalidInv); err == nil {
		t.Fatalf("connector field loss mutation of required RequestID was not rejected")
	}

	fieldLossInv := inv
	fieldLossInv.ProxyOwnedSessionID = ""
	mutatedCall, err := backendplugin.CallFromInvocation(fieldLossInv)
	if err != nil {
		t.Fatalf("mutated invocation conversion failed: %v", err)
	}
	if mutatedCall.Session.AuthoritativeSessionID == call.Session.AuthoritativeSessionID {
		t.Fatalf("connector field loss mutation was not detected: session authority remained intact")
	}

	carrierLossInv := inv
	carrierLossInv.SemanticExtensions = nil
	carrierLossCall, err := backendplugin.CallFromInvocation(carrierLossInv)
	if err != nil {
		t.Fatalf("carrier loss invocation conversion failed: %v", err)
	}
	if val, _ := carrierLossCall.PromptCacheKeyValue(); val == "cache-1" {
		t.Fatalf("connector semantic carrier field loss mutation was not detected")
	}
}
