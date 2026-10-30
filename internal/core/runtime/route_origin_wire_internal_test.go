package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
)

// captureWireNativeBackend captures the model identity the wire lane opens the
// backend with.
func captureWireNativeBackend(captured *string) execbackend.Backend {
	return execbackend.Backend{
		OpenWire: func(_ context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
			*captured = req.Candidate.Primary.NativeModel
			return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream([]aipapi.Event{
				{Kind: aipapi.EventResponseFinished},
			})}, nil
		},
	}
}

// TestTask13_1_RouteOriginIsFinalAtTheOverride proves the wire lane honors the
// final route authority: a route an administrator's override selected is not
// the proxy's own default route, so the generation's frozen binding must not be
// applied to it, even when the override selected exactly the same expression.
func TestTask13_1_RouteOriginIsFinalAtTheOverride(t *testing.T) {
	frozen := routing.DefaultRouteBindings{{
		Backend: "default",
		Model:   "gpt-4o",
		Native:  "native-frozen",
	}}
	wireContext := func(ctx context.Context) context.Context {
		ctx = execview.WithPrincipal(ctx, execview.PrincipalView{ID: "usr-wire-origin"})
		ctx = routing.WithRouteOrigin(ctx, routing.RouteOriginDefault)
		return routing.WithDefaultRouteBindings(ctx, frozen)
	}

	t.Run("client route keeps the frozen binding", func(t *testing.T) {
		var native string
		ex, _, _ := setupTestExecutor(t)
		ex.Backends = map[string]execbackend.Backend{"default": captureWireNativeBackend(&native)}
		src := newTestSource(`{"prompt":"hello"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

		if _, err := ex.ExecuteLargeBody(wireContext(context.Background()), acc, src); err != nil {
			t.Fatalf("ExecuteLargeBody: %v", err)
		}
		if native != "native-frozen" {
			t.Fatalf("native = %q want the frozen binding", native)
		}
	})

	t.Run("override of the same selector keeps no frozen binding", func(t *testing.T) {
		var native string
		ex, _, _ := setupTestExecutor(t)
		ex.Backends = map[string]execbackend.Backend{"default": captureWireNativeBackend(&native)}
		ex.RouteOverrideReader = &testRouteOverrideReader{
			snapshotFunc: func(context.Context, string) (routeoverride.State, error) {
				return routeoverride.State{Active: true, Selector: "gpt-4o"}, nil
			},
		}
		src := newTestSource(`{"prompt":"hello"}`)
		acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)

		res, err := ex.ExecuteLargeBody(wireContext(context.Background()), acc, src)
		if err != nil {
			t.Fatalf("ExecuteLargeBody: %v", err)
		}
		if res.Facts.EffectiveModel != "gpt-4o" {
			t.Fatalf("effective model = %q want the override's selector", res.Facts.EffectiveModel)
		}
		if native != "" {
			t.Fatalf("native = %q want no frozen binding for an overridden route", native)
		}
	})
}
