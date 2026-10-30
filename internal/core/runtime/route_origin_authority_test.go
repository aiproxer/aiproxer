package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/modelinventory"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/request"
)

// routeOriginTestRegistry publishes the canonical-to-native mapping the current
// request-bound registry knows: a refresh remapped the canonical id the
// generation's frozen binding still remembers.
func routeOriginTestRegistry(t *testing.T, backendID string, models ...modelinventory.Model) routing.NativeModelResolver {
	t.Helper()
	rt := modelregistry.NewRuntime(modelregistry.RuntimeConfig{
		Inventories: []modelregistry.BackendInventory{{
			BackendID:       backendID,
			Kind:            "custom-openai-legacy-compatible",
			BackendPrefixes: []string{backendID},
			Provider: modelinventory.StaticProvider{
				Source:   modelinventory.SourceStaticInline,
				LoadedAt: time.Unix(100, 0).UTC(),
				Models:   models,
			},
		}},
	})
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("model registry start: %v", err)
	}
	return rt.BoundView()
}

func routeOriginTestContext(t *testing.T, resolver routing.NativeModelResolver, bindings routing.DefaultRouteBindings) context.Context {
	t.Helper()
	ctx := routing.WithNativeModelResolver(context.Background(), resolver)
	ctx = routing.WithRouteOrigin(ctx, routing.RouteOriginDefault)
	ctx = routing.WithDefaultRouteBindings(ctx, bindings)
	return ctx
}

// TestExecutor_routeOriginIsFinalAtTheRouteAuthority proves the route origin
// describes the route the request actually runs, not merely the route the
// frontend first resolved.
//
// A route a later core authority selected — an administrator's override, or a
// request transform that rewrote the selector — belongs to that authority, so
// it uses the current request-bound registry even when it selects exactly the
// expression the frontend would have selected. Only a route the proxy chose
// itself and nobody replaced keeps the generation's frozen binding.
func TestExecutor_routeOriginIsFinalAtTheRouteAuthority(t *testing.T) {
	t.Parallel()

	frozen := routing.DefaultRouteBindings{{
		Backend: "vendorbe",
		Model:   "vendor/a",
		Native:  "native-v1",
	}}
	// The current registry remapped the canonical id, so the frozen binding and
	// the current view disagree and the assertion distinguishes them.
	resolver := routeOriginTestRegistry(t, "vendorbe", modelinventory.Model{
		CanonicalID: "vendor/a",
		NativeID:    "native-v2",
	})

	openNative := func(t *testing.T, backends ...string) (*routeOpenCapture, map[string]execbackend.Backend) {
		t.Helper()
		cap := &routeOpenCapture{}
		out := make(map[string]execbackend.Backend, len(backends))
		for _, backend := range backends {
			out[backend] = overrideStreamingBackend(cap, backend)
		}
		return cap, out
	}

	t.Run("unchanged default route keeps the frozen binding", func(t *testing.T) {
		t.Parallel()
		cap, backends := openNative(t, "vendorbe")
		ex, _ := routePlanLifetimeExecutor(t, backends)
		ex.RouteOverrideReader = nil
		ctx := routeOriginTestContext(t, resolver, frozen)

		collectExecute(t, ex, ctx, routePlanLifetimeCall("vendorbe:vendor/a", "ov-origin-plain"))
		opens := cap.snapshot()
		if len(opens) != 1 {
			t.Fatalf("opens = %+v want 1", opens)
		}
		if opens[0].native != "native-v1" {
			t.Fatalf("native = %q want the frozen native-v1", opens[0].native)
		}
	})

	t.Run("override of the same selector uses the current registry", func(t *testing.T) {
		t.Parallel()
		cap, backends := openNative(t, "clientbe", "vendorbe")
		ex, st := routePlanLifetimeExecutor(t, backends)
		seed := seedOverrideALeg(t, ex, st, "ov-origin-override", "vendorbe:vendor/a")
		resetRouteOpenCapture(cap)
		ctx := routeOriginTestContext(t, resolver, frozen)

		collectExecute(t, ex, ctx, resumeOverrideCall(seed, "vendorbe:vendor/a"))
		opens := cap.snapshot()
		if len(opens) != 1 {
			t.Fatalf("opens = %+v want 1", opens)
		}
		if opens[0].native != "native-v2" {
			t.Fatalf("native = %q want the current registry's native-v2", opens[0].native)
		}
	})

	t.Run("override to a canonical owned by another backend keeps the rejection", func(t *testing.T) {
		t.Parallel()
		_, backends := openNative(t, "clientbe", "vendorbe", "otherbe")
		ex, st := routePlanLifetimeExecutor(t, backends)
		// The registry publishes vendor/a from another backend only, so the
		// current view rejects the original backend's claim.
		otherResolver := routeOriginTestRegistry(t, "otherbe", modelinventory.Model{
			CanonicalID: "vendor/a",
			NativeID:    "native-other",
		})
		seed := seedOverrideALeg(t, ex, st, "ov-origin-moved", "vendorbe:vendor/a")
		ctx := routeOriginTestContext(t, otherResolver, frozen)

		stream, err := ex.Execute(ctx, resumeOverrideCall(seed, "vendorbe:vendor/a"))
		if stream == nil && err == nil {
			t.Fatal("expected a rejected execution")
		}
		if err == nil {
			_, err = aipapi.Collect(context.Background(), stream)
		}
		if err == nil {
			t.Fatal("expected the current registry to reject the moved canonical id")
		}
		if !errors.Is(err, routing.ErrWrongBackendCanonical) && !strings.Contains(err.Error(), "wrong backend") {
			t.Fatalf("error = %v want the wrong-backend rejection", err)
		}
	})

	t.Run("request transform rewrite uses the current registry", func(t *testing.T) {
		t.Parallel()
		cap, backends := openNative(t, "vendorbe")
		ex, _ := routePlanLifetimeExecutor(t, backends)
		ex.SelectorAliases = mustAliasResolver(t, "vendorbe:alias/a", "vendorbe:vendor/a")
		ex.RuntimeSnapshot = newRequestSnapshotWithTransform(t, "vendorbe:vendor/a", "vendorbe:alias/a")
		ctx := routeOriginTestContext(t, resolver, frozen)

		collectExecute(t, ex, ctx, routePlanLifetimeCall("vendorbe:vendor/a", "ov-origin-transform"))
		opens := cap.snapshot()
		if len(opens) != 1 {
			t.Fatalf("opens = %+v want 1", opens)
		}
		if opens[0].native != "native-v2" {
			t.Fatalf("native = %q want the current registry's native-v2", opens[0].native)
		}
	})
}

// mustAliasResolver builds an alias resolver for one exact rewrite.
func mustAliasResolver(t *testing.T, pattern, replacement string) *routing.AliasResolver {
	t.Helper()
	resolver, err := routing.NewAliasResolver([]routing.ModelAliasRule{{Pattern: pattern, Replacement: replacement}})
	if err != nil {
		t.Fatalf("alias resolver: %v", err)
	}
	return resolver
}

// newRequestSnapshotWithTransform installs one pre-request transform that
// rewrites the selector, with the rest of the pipeline neutral.
func newRequestSnapshotWithTransform(t *testing.T, from, to string) *extensions.RequestRuntimeSnapshot {
	t.Helper()
	bus := hooks.New(hooks.Config{})
	return extensions.NewRequestRuntimeSnapshot(bus, extensions.SnapshotOptions{
		Workspace: voidWorkspaceResolver{},
		FeaturePlanes: testkit.FreezeTestBundle(testkit.TestFeatureBundle{
			RequestTransforms: []request.Transform{selectorRewriteTransform{from: from, to: to}},
		}),
	})
}
