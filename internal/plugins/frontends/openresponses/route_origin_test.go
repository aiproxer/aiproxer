package openresponses_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/continuation"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

// routeOriginExecutor records the route origin of every call it executes.
type routeOriginExecutor struct {
	origins   []routing.RouteOrigin
	selectors []string
}

func (e *routeOriginExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.origins = append(e.origins, routing.RouteOriginFromContext(ctx))
	e.selectors = append(e.selectors, call.Route.Selector)
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *routeOriginExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }

func (e *routeOriginExecutor) WallClock() func() time.Time { return nil }

// TestHandler_continuationRouteIsNotTheDefaultRoute proves a continuation that
// restores its route from the parent lineage does not keep the default route's
// origin.
//
// The pipeline resolves a route before the decode, so a continuation without a
// model looks like an implicit default. The route the request ends up with
// comes from the parent, so the origin is the lineage: the request uses the
// current request-bound registry, not the generation's frozen default binding.
func TestHandler_continuationRouteIsNotTheDefaultRoute(t *testing.T) {
	store := continuation.NewMemoryStore()
	sc := aipcont.Scope{TenantID: "tenant-1", PrincipalID: "principal-1"}
	parentID := seedParentRecord(t, store, sc, aipcont.RecordStatusCompleted)

	exec := &routeOriginExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             exec,
		ContinuationStore:    store,
		DefaultRouteSelector: "stub:default-model",
		RoutePrefixes:        []string{"stub"},
	})

	body := `{"previous_response_id":"` + parentID.String() + `","input":"new-input"}`
	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(exec.origins) != 1 {
		t.Fatalf("executor calls = %d want 1", len(exec.origins))
	}
	if exec.origins[0] != routing.RouteOriginLineage {
		t.Fatalf("origin = %q want lineage", exec.origins[0])
	}
	if exec.selectors[0] != "gpt-4o" {
		t.Fatalf("route = %q want the parent lineage route", exec.selectors[0])
	}
}

// TestHandler_implicitDefaultKeepsTheDefaultRouteOrigin proves a request
// without a route and without a continuation keeps the default route's origin.
func TestHandler_implicitDefaultKeepsTheDefaultRouteOrigin(t *testing.T) {
	exec := &routeOriginExecutor{}
	handler := openresponses.NewHandler(openresponses.HandlerConfig{
		AllowUnauthenticated: true,
		Authorizer:           staticAuth{tenant: "tenant-1", principal: "principal-1", allow: true},
		Executor:             exec,
		DefaultRouteSelector: "stub:default-model",
		RoutePrefixes:        []string{"stub"},
	})

	req := httptest.NewRequest(http.MethodPost, "/openresponses/v1/responses", bytes.NewBufferString(`{"model":"plain-model","input":"hi"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if len(exec.origins) != 1 {
		t.Fatalf("executor calls = %d want 1", len(exec.origins))
	}
	if exec.origins[0] != routing.RouteOriginDefault {
		t.Fatalf("origin = %q want default", exec.origins[0])
	}
	if exec.selectors[0] != "stub:default-model" {
		t.Fatalf("route = %q want the default route", exec.selectors[0])
	}
}
