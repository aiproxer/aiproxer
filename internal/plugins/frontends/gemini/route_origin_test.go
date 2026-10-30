package gemini_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/gemini"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
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
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "ok"},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *routeOriginExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *routeOriginExecutor) WallClock() func() time.Time { return nil }

// TestHandler_reportsTheRouteOriginOfTheResolvedRoute proves the Gemini
// frontend reports the origin of the route it selected.
//
// The frontend resolves its route from the URL-path model, so the origin is
// known there and not reconstructed later: a URL-path model that names no
// known prefix falls back to the default route, and that route is the proxy's
// own, while a model that names a known prefix is the client's route.
func TestHandler_reportsTheRouteOriginOfTheResolvedRoute(t *testing.T) {
	t.Parallel()
	const body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`

	t.Run("implicit default", func(t *testing.T) {
		t.Parallel()
		exec := &routeOriginExecutor{}
		h := &gemini.Handler{
			Exec:                 exec,
			DefaultRouteSelector: "stub:gemini-2.0-flash",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		}
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.0-flash:generateContent", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if exec.selector != "stub:gemini-2.0-flash" {
			t.Fatalf("route = %q want the default route", exec.selector)
		}
		if exec.origin != routing.RouteOriginDefault {
			t.Fatalf("origin = %q want default", exec.origin)
		}
	})

	t.Run("explicit route", func(t *testing.T) {
		t.Parallel()
		exec := &routeOriginExecutor{}
		h := &gemini.Handler{
			Exec:                 exec,
			DefaultRouteSelector: "stub:gemini-2.0-flash",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		}
		req := httptest.NewRequest(http.MethodPost, "/v1beta/models/stub:gemini-2.0-flash:generateContent", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
		}
		if exec.selector != "stub:gemini-2.0-flash" {
			t.Fatalf("route = %q", exec.selector)
		}
		if exec.origin != routing.RouteOriginExplicit {
			t.Fatalf("origin = %q want explicit", exec.origin)
		}
	})
}
