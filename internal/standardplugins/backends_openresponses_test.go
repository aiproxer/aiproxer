package standardplugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/plugins/backends/openresponsescompat"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"gopkg.in/yaml.v3"
)

const openResponsesMinimalYAML = `backend_prefix: my-or
base_url: https://api.example.test/openresponses/v1
`

func openResponsesRow(t *testing.T, id, raw string) config.PluginConfig {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return config.PluginConfig{
		Kind:    CustomOpenResponsesCompatibleID,
		ID:      id,
		Enabled: true,
		Config:  n,
	}
}

func openResponsesNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOpenResponsesCompatible_FactoryRegisteredAsEssential(t *testing.T) {
	t.Parallel()
	if !IsEssentialBackendKind(CustomOpenResponsesCompatibleID) {
		t.Fatal("kind must be essential")
	}
	if !IsCustomCompatibleBackendKind(CustomOpenResponsesCompatibleID) {
		t.Fatal("kind must be custom-compatible")
	}
	if !IsOpenResponsesCompatibleBackendKind(CustomOpenResponsesCompatibleID) {
		t.Fatal("kind must be openresponses-compatible")
	}

	var found bool
	for _, e := range EssentialBackendBundle(UpstreamAPIKeys{}).Backends {
		if e.ID != CustomOpenResponsesCompatibleID {
			continue
		}
		found = true
		if e.LifecycleFactory == nil {
			t.Fatal("expected lifecycle factory")
		}
		if e.Profile.CredentialMode != pluginreg.CredentialStatic {
			t.Fatalf("credential mode = %q, want %q", e.Profile.CredentialMode, pluginreg.CredentialStatic)
		}
		if e.Source != pluginreg.BackendSourceBuiltinCompatible {
			t.Fatalf("source = %q, want %q", e.Source, pluginreg.BackendSourceBuiltinCompatible)
		}
	}
	if !found {
		t.Fatal("kind missing from essential bundle")
	}

	reg := customCompatibleRegistry(t)
	if !reg.HasBackend(CustomOpenResponsesCompatibleID) {
		t.Fatal("kind missing from essential registry")
	}
	source, ok := reg.BackendRegistrationSource(CustomOpenResponsesCompatibleID)
	if !ok || source != pluginreg.BackendSourceBuiltinCompatible {
		t.Fatalf("registration source = %q ok=%v", source, ok)
	}
}

func TestOpenResponsesCompatible_FactoryBuildNoAuthCaps(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	raw := openResponsesMinimalYAML + "capabilities: [ordered_items, streaming, tools]\n"
	be := buildCompatibleBackend(t, reg, CustomOpenResponsesCompatibleID, "or-build", openResponsesNode(t, raw), nil)

	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-or" {
		t.Fatalf("BackendPrefixes = %#v, want [my-or]", be.BackendPrefixes)
	}

	call := customCompatibleTestCall(aipapi.OperationOpenResponsesCreate)
	caps := execbackend.EffectiveCaps(context.Background(), be, call, routing.AttemptCandidate{})
	for _, want := range []aipapi.Capability{aipapi.CapabilityOrderedItems, aipapi.CapabilityStreaming, aipapi.CapabilityTools} {
		if !compatCapsHas(caps, want) {
			t.Fatalf("caps missing %q: %v", want, caps)
		}
	}
	if compatCapsHas(caps, aipapi.CapabilityVision) {
		t.Fatal("undeclared capability must not be advertised")
	}

	tc := execbackend.EffectiveTransportCaps(context.Background(), be, call, routing.AttemptCandidate{})
	if !tc.Supports(aipapi.OperationOpenResponsesCreate, aipapi.TransportModeStreaming) {
		t.Fatal("create streaming must be advertised")
	}
	if !tc.Supports(aipapi.OperationOpenResponsesCreate, aipapi.TransportModeNonStreaming) {
		t.Fatal("create non-streaming must be advertised")
	}
	if !tc.Supports(aipapi.OperationContextCompaction, aipapi.TransportModeNonStreaming) {
		t.Fatal("compact non-streaming must be advertised")
	}
	if tc.Supports(aipapi.OperationContextCompaction, aipapi.TransportModeStreaming) {
		t.Fatal("compact streaming must not be advertised")
	}
	if tc.Supports(aipapi.OperationOpenAIChatCompletions, aipapi.TransportModeNonStreaming) {
		t.Fatal("chat-completions transport must not be advertised")
	}

	// Explicit no-auth: no env root configured, build succeeds. The legacy
	// message-authority test call is projected to ordered items by the explicit
	// legacy→ordered-items projector, then rejected before network work because
	// the empty candidate cannot resolve a model.
	_, err := be.Open(context.Background(), call, routing.AttemptCandidate{})
	if !errors.Is(err, openresponsescompat.ErrUnrepresentable) {
		t.Fatalf("Open error = %v, want ErrUnrepresentable (projected request incomplete)", err)
	}
}

func TestOpenResponsesCompatible_FactoryMultipleInstancesNoCollision(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	beA := buildCompatibleBackend(t, reg, CustomOpenResponsesCompatibleID, "or-a", openResponsesNode(t, `backend_prefix: prefix-a
base_url: https://api-a.example.test/openresponses/v1
capabilities: [ordered_items]
`), nil)
	beB := buildCompatibleBackend(t, reg, CustomOpenResponsesCompatibleID, "or-b", openResponsesNode(t, `backend_prefix: prefix-b
base_url: https://api-b.example.test/openresponses/v1
capabilities: [ordered_items, tools]
`), nil)

	if len(beA.BackendPrefixes) != 1 || beA.BackendPrefixes[0] != "prefix-a" {
		t.Fatalf("instance A prefixes = %#v", beA.BackendPrefixes)
	}
	if len(beB.BackendPrefixes) != 1 || beB.BackendPrefixes[0] != "prefix-b" {
		t.Fatalf("instance B prefixes = %#v", beB.BackendPrefixes)
	}
	if beA.BackendPrefixes[0] == beB.BackendPrefixes[0] {
		t.Fatal("instances must not share a prefix")
	}

	call := customCompatibleTestCall(aipapi.OperationOpenResponsesCreate)
	capsA := execbackend.EffectiveCaps(context.Background(), beA, call, routing.AttemptCandidate{})
	capsB := execbackend.EffectiveCaps(context.Background(), beB, call, routing.AttemptCandidate{})
	if compatCapsHas(capsA, aipapi.CapabilityTools) {
		t.Fatal("instance A must not inherit instance B capabilities")
	}
	if !compatCapsHas(capsB, aipapi.CapabilityTools) {
		t.Fatal("instance B must keep its declared capabilities")
	}
}

func TestOpenResponsesCompatible_PrefixCollisionRejected(t *testing.T) {
	t.Parallel()
	err := ValidateCompatibleManifestOwnership([]config.PluginConfig{
		openResponsesRow(t, "a", openResponsesMinimalYAML),
		openResponsesRow(t, "b", openResponsesMinimalYAML),
	}, nil)
	if err == nil {
		t.Fatal("expected duplicate backend_prefix error")
	}
	var coll *pluginreg.OwnershipCollisionError
	if !errors.As(err, &coll) {
		t.Fatalf("error = %v, want OwnershipCollisionError", err)
	}
	if coll.Key != "my-or" {
		t.Fatalf("collision key = %q, want my-or", coll.Key)
	}
	msg := coll.Error()
	if !strings.Contains(msg, "a") || !strings.Contains(msg, "b") {
		t.Fatalf("error %q must name both instances", msg)
	}
}

func TestOpenResponsesCompatible_PrefixReservedRejected(t *testing.T) {
	t.Parallel()
	for _, owner := range CollectBuiltInBackendOwners(nil) {
		prefix := owner.Prefix
		err := ValidateCompatibleManifestOwnership([]config.PluginConfig{
			openResponsesRow(t, "or-copy", "backend_prefix: "+prefix+"\nbase_url: https://api.example.test/openresponses/v1\n"),
		}, nil)
		if err == nil {
			t.Fatalf("expected reserved backend_prefix %q rejection", prefix)
		}
		var coll *pluginreg.OwnershipCollisionError
		if !errors.As(err, &coll) {
			t.Fatalf("error for %q = %v, want OwnershipCollisionError", prefix, err)
		}
		if coll.Key != prefix {
			t.Fatalf("collision key = %q, want %q", coll.Key, prefix)
		}
	}
}

func TestOpenResponsesCompatible_ConfigUnknownFieldRejected(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	raw := openResponsesMinimalYAML + "openrouter_attribution: on\n"
	_, err := reg.BuildBackendWithLifecycle(CustomOpenResponsesCompatibleID, "or-inst", openResponsesNode(t, raw), nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected unknown config field rejection")
	}
	msg := err.Error()
	if !strings.Contains(msg, "openrouter_attribution") || !strings.Contains(msg, "or-inst") {
		t.Fatalf("error = %q, want unknown field + instance identity", msg)
	}
}

func TestOpenResponsesCompatible_ConfigEndpointPolicy(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	for _, tc := range []struct {
		name    string
		base    string
		invalid bool
	}{
		{name: "https_remote", base: "https://api.example.test/openresponses/v1"},
		{name: "http_loopback_dev_override", base: "http://127.0.0.1:9/openresponses/v1"},
		{name: "http_remote_rejected", base: "http://api.insecure.example/v1", invalid: true},
		{name: "relative_rejected", base: "/openresponses/v1", invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := reg.BuildBackendWithLifecycle(CustomOpenResponsesCompatibleID, "or-ep", openResponsesNode(t, "backend_prefix: my-or\nbase_url: "+tc.base+"\n"), nil, pluginreg.BackendFactoryDeps{})
			if tc.invalid && err == nil {
				t.Fatal("expected endpoint rejection")
			}
			if !tc.invalid && err != nil {
				t.Fatalf("unexpected endpoint error: %v", err)
			}
		})
	}
}

func TestOpenResponsesCompatible_NoAuthBuilds(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	be := buildCompatibleBackend(t, reg, CustomOpenResponsesCompatibleID, "or-noauth", openResponsesNode(t, openResponsesMinimalYAML), nil)
	if be.Open == nil {
		t.Fatal("expected Open seam")
	}
}

func TestOpenResponsesCompatible_FactoryForbiddenSecretNoEcho(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	secret := "sk-openresponses-standard-secret"
	raw := openResponsesMinimalYAML + "api_key: " + secret + "\n"
	_, err := reg.BuildBackendWithLifecycle(CustomOpenResponsesCompatibleID, "or-secret", openResponsesNode(t, raw), nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected forbidden literal secret rejection")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed literal secret: %q", err)
	}
}

func TestOpenResponsesCompatible_CapsRejectUnknownDeclaration(t *testing.T) {
	t.Parallel()
	reg := customCompatibleRegistry(t)
	raw := openResponsesMinimalYAML + "capabilities: [ordered_items, mind_reading]\n"
	_, err := reg.BuildBackendWithLifecycle(CustomOpenResponsesCompatibleID, "or-caps", openResponsesNode(t, raw), nil, pluginreg.BackendFactoryDeps{})
	if err == nil {
		t.Fatal("expected unknown capability rejection")
	}
	if !strings.Contains(err.Error(), "mind_reading") {
		t.Fatalf("error = %v, want unknown capability named", err)
	}
}

func TestOpenResponsesCompatible_DiagnosticsProjectCapabilitiesAndConformance(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		openResponsesRow(t, "or-diag", openResponsesMinimalYAML+`capabilities: [tools, streaming, ordered_items, tools]
models:
  source: inline
  items:
    - canonical_id: my-or/model-a
`),
	}}}
	rows := ProjectCompatibleBackendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	row := rows[0]
	if row.Origin != "built_in_compatible" || row.FactoryKind != CustomOpenResponsesCompatibleID {
		t.Fatalf("origin/factory = %+v", row)
	}
	if row.Profile != "2026-04-24" {
		t.Fatalf("profile=%q", row.Profile)
	}
	if row.Conformance != "profile:2026-04-24" {
		t.Fatalf("conformance=%q", row.Conformance)
	}
	want := []string{"ordered_items", "streaming", "tools"}
	if len(row.Capabilities) != len(want) {
		t.Fatalf("capabilities=%v, want %v", row.Capabilities, want)
	}
	for i := range want {
		if row.Capabilities[i] != want[i] {
			t.Fatalf("capabilities=%v, want %v", row.Capabilities, want)
		}
	}
	if row.InventoryState != "static_inline" {
		t.Fatalf("inventory_state=%q, want static_inline", row.InventoryState)
	}
	if row.ConfigError != "" {
		t.Fatalf("config_error=%q", row.ConfigError)
	}
}

func TestOpenResponsesCompatible_DiagnosticsInvalidConfigCarriesError(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Plugins: config.PluginsConfig{Backends: []config.PluginConfig{
		openResponsesRow(t, "or-bad", "backend_prefix: my-or\nbase_url: relative\n"),
	}}}
	rows := ProjectCompatibleBackendRows(cfg)
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].ConfigError == "" {
		t.Fatal("expected config error for invalid base_url")
	}
	if !strings.Contains(rows[0].ConfigError, "or-bad") {
		t.Fatalf("config error must carry instance identity: %q", rows[0].ConfigError)
	}
}

func compatCapsHas(caps aipapi.BackendCaps, targetCap aipapi.Capability) bool {
	if caps == nil {
		return false
	}
	_, ok := caps[targetCap]
	return ok
}
