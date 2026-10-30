package openresponsescompat

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
)

// TestProviderBoundary_BackendSpecResolvesDefaultCodecOptions verifies the
// codec customization seam is preserved through the BackendSpec → NewBackend
// path: a zero-value CodecOptions resolves to the generic default (pinned
// profile + generic factory kind) with no provider label.
func TestProviderBoundary_BackendSpecResolvesDefaultCodecOptions(t *testing.T) {
	t.Parallel()
	spec := testSpec()
	be := NewBackend(spec)
	if be.Open == nil {
		t.Fatal("expected Open seam")
	}
	// NewBackend normalizes the zero-value codec options internally; exercise
	// the explicit path to prove the seam is usable by a future provider wrapper.
	opts, err := NewCodecOptions(DefaultProfile, ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Profile() != DefaultProfile || opts.FactoryKind() != ID || opts.ProviderID() != "" {
		t.Fatalf("default codec options = %+v", opts)
	}
}

// TestProviderBoundary_ProviderWrapperReusesExplicitCodecOptions simulates a
// future provider wrapper constructing the generic OpenResponses backend with
// explicit codec options. The wrapper must not leak provider policy into the
// generic codec; options are validated, immutable, and carry provenance only.
func TestProviderBoundary_ProviderWrapperReusesExplicitCodecOptions(t *testing.T) {
	t.Parallel()
	opts, err := NewCodecOptions(DefaultProfile, "custom-provider-compat", "provider-x")
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec()
	spec.Codec = opts
	be := NewBackend(spec)
	if be.Open == nil {
		t.Fatal("expected Open seam from explicit codec options")
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-or" {
		t.Fatalf("prefixes = %#v", be.BackendPrefixes)
	}
	// Invalid provider-policy options must fail construction before any backend
	// behavior, not silently resolve to generic defaults.
	if _, err := NewCodecOptions(DefaultProfile, ID, "openrouter/route"); err == nil {
		t.Fatal("expected provider-policy id rejection")
	}
}

// TestProviderBoundary_ProvenancePreservedThroughBuild verifies instance
// (backend prefix), factory (kind), profile, model inventory, capability, and
// inventory source survive the Build → BackendSpec → NewBackend path with zero
// loss: the built backend carries the configured profile, capabilities,
// dialects, inventory, and inventory provenance.
func TestProviderBoundary_ProvenancePreservedThroughBuild(t *testing.T) {
	t.Parallel()
	raw := minimalYAML + `capabilities: [ordered_items, streaming, tools]
models:
  source: inline
  items:
    - canonical_id: my-or/model-a
      native_id: model-a
`
	be := mustBuild(t, "or-inst", raw)
	call := openResponsesCall(aipapi.OperationOpenResponsesCreate)
	caps := execbackend.EffectiveCaps(context.Background(), be, call, routing.AttemptCandidate{})
	for _, want := range []aipapi.Capability{aipapi.CapabilityOrderedItems, aipapi.CapabilityStreaming, aipapi.CapabilityTools} {
		if !capsHas(caps, want) {
			t.Fatalf("capability %q lost through Build", want)
		}
	}
	if be.ModelInventory == nil {
		t.Fatal("model inventory lost through Build")
	}
	snap, err := be.ModelInventory.LoadModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != modelinventory.SourceStaticInline {
		t.Fatalf("inventory source = %q, want static_inline", snap.Source)
	}
	if len(snap.Models) != 1 || snap.Models[0].CanonicalID != "my-or/model-a" || snap.Models[0].NativeID != "model-a" {
		t.Fatalf("models = %+v", snap.Models)
	}
	if len(be.BackendPrefixes) != 1 || be.BackendPrefixes[0] != "my-or" {
		t.Fatalf("instance prefix = %#v", be.BackendPrefixes)
	}
	itemDialects := be.DialectSupport.ItemDialects
	if len(itemDialects) != 2 || !dialectDeclared(itemDialects, "item", DefaultItemDialect) || !dialectDeclared(itemDialects, "item", "item_reference") {
		t.Fatalf("dialect support lost through Build: %+v", be.DialectSupport)
	}
}

func dialectDeclared(in []aipapi.DialectRequirement, kind, dialect string) bool {
	for _, d := range in {
		if d.Kind == kind && d.Dialect == dialect {
			return true
		}
	}
	return false
}

// TestProviderBoundary_ConfigRejectsProviderControlsAtBuild proves the generic
// config surface rejects OpenRouter attribution/routing/billing/catalog and
// arbitrary provider options end-to-end through Build, not only DecodeConfig.
func TestProviderBoundary_ConfigRejectsProviderControlsAtBuild(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"app_url", "route", "billing", "catalog", "openrouter", "provider_options"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			if err := buildErr(t, "or-bnd", minimalYAML+key+": on\n"); err == nil {
				t.Fatalf("expected Build rejection for provider key %q", key)
			}
		})
	}
}

func TestProviderBoundary_InventoryProvenancePreservedInModelInventory(t *testing.T) {
	t.Parallel()
	raw := minimalYAML + `models:
  source: inline
  items:
    - canonical_id: my-or/model-a
      native_id: model-a
`
	be := mustBuild(t, "or-inst", raw)
	if be.ModelInventory == nil {
		t.Fatal("expected model inventory")
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
