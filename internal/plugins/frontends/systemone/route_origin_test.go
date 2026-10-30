package systemone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

// routeOriginExecutor records the route origin the frontend reported and the
// route it selected.
type routeOriginExecutor struct {
	origin   routing.RouteOrigin
	selector string
}

func (e *routeOriginExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.origin = routing.RouteOriginFromContext(ctx)
	e.selector = call.Route.Selector
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventDecisionResult, Decision: &aipapi.DecisionResult{
			Model:   "jev",
			Answers: []aipapi.DecisionAnswer{{QuestionID: "q", Kind: aipapi.DecisionKindNoul, PTrue: 0.8}},
		}},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *routeOriginExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *routeOriginExecutor) WallClock() func() time.Time { return nil }

// TestHandlerSpec_reportsTheRouteOriginOfTheResolvedRoute proves the
// SystemOne frontend reports the origin of the route it selected.
//
// The frontend resolves its route from the body model, so the origin is known
// there: a body model that names no known prefix falls back to the default
// route, and that route is the proxy's own, while a model that names a known
// prefix is the client's route.
func TestHandlerSpec_reportsTheRouteOriginOfTheResolvedRoute(t *testing.T) {
	t.Parallel()

	serve := func(t *testing.T, exec *routeOriginExecutor, body string) {
		t.Helper()
		spec := handlerSpec(aipsdk.FrontendMountOptions{
			Exec:          exec,
			DefaultRoute:  "decision:jev-latest",
			RoutePrefixes: []string{"decision"},
		}, Config{})
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
	}

	t.Run("implicit default", func(t *testing.T) {
		t.Parallel()
		exec := &routeOriginExecutor{}
		serve(t, exec, `{"model":"jev","state":"x","questions":{"q":{"type":"noul"}}}`)
		if exec.selector != "decision:jev-latest" {
			t.Fatalf("route = %q want the default route", exec.selector)
		}
		if exec.origin != routing.RouteOriginDefault {
			t.Fatalf("origin = %q want default", exec.origin)
		}
	})

	t.Run("explicit route", func(t *testing.T) {
		t.Parallel()
		exec := &routeOriginExecutor{}
		serve(t, exec, `{"model":"decision:jev","state":"x","questions":{"q":{"type":"noul"}}}`)
		if exec.selector != "decision:jev" {
			t.Fatalf("route = %q", exec.selector)
		}
		if exec.origin != routing.RouteOriginExplicit {
			t.Fatalf("origin = %q want explicit", exec.origin)
		}
	})
}
