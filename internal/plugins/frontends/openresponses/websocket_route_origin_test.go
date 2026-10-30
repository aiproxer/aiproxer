package openresponses_test

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/openresponses"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// wsRouteOriginExecutor records the route origin and the route of every turn
// it executes.
type wsRouteOriginExecutor struct {
	mu        sync.Mutex
	origins   []routing.RouteOrigin
	selectors []string
}

func (e *wsRouteOriginExecutor) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.origins = append(e.origins, routing.RouteOriginFromContext(ctx))
	e.selectors = append(e.selectors, call.Route.Selector)
	return fixedStream(
		aipapi.Event{Kind: aipapi.EventResponseStarted},
		aipapi.Event{Kind: aipapi.EventMessageStarted},
		aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"},
		aipapi.Event{Kind: aipapi.EventResponseFinished},
	), nil
}

func (e *wsRouteOriginExecutor) CancelALeg(context.Context, aipapi.ALegCancelRequest) error {
	return nil
}

func (e *wsRouteOriginExecutor) WallClock() func() time.Time { return nil }

// TestWebSocketTurn_reportsTheRouteOriginPerTurn proves every WebSocket turn
// carries its own route origin.
//
// The origin is per turn, not per connection: consecutive turns may name a
// route, use the default route, or continue a chain. A turn whose route the
// proxy chose keeps the generation's frozen model binding, and a turn the
// client routed does not.
func TestWebSocketTurn_reportsTheRouteOriginPerTurn(t *testing.T) {
	exec := &wsRouteOriginExecutor{}
	runner := openresponses.NewSessionRunner(openresponses.SessionRunnerConfig{
		Executor:             exec,
		ResponseIDSource:     deterministicResponseMetadata{id: "resp_origin", now: time.Unix(1_700_000_400, 0)},
		ResponseClock:        deterministicResponseMetadata{id: "resp_origin", now: time.Unix(1_700_000_400, 0)},
		DefaultRouteSelector: "stub:default-model",
		RoutePrefixes:        []string{"stub"},
	})
	handler := openresponses.NewWebSocketHandler(openresponses.WebSocketHandlerConfig{
		AllowUnauthenticated: true,
		Config:               wsTestConfig(nil),
		Runner:               runner,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	conn := wsDial(t, srv, nil)

	wsText(t, conn, `{"type":"response.create","model":"plain-model","input":"first"}`)
	wsReadUntilTerminal(t, conn, 3*time.Second)
	wsText(t, conn, `{"type":"response.create","model":"stub:model-a","input":"second"}`)
	wsReadUntilTerminal(t, conn, 3*time.Second)
	_ = conn.Close()

	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.origins) != 2 {
		t.Fatalf("executor calls = %d want 2", len(exec.origins))
	}
	if exec.origins[0] != routing.RouteOriginDefault {
		t.Fatalf("turn 1 origin = %q want default", exec.origins[0])
	}
	if exec.selectors[0] != "stub:default-model" {
		t.Fatalf("turn 1 route = %q want the default route", exec.selectors[0])
	}
	if exec.origins[1] != routing.RouteOriginExplicit {
		t.Fatalf("turn 2 origin = %q want explicit", exec.origins[1])
	}
	if exec.selectors[1] != "stub:model-a" {
		t.Fatalf("turn 2 route = %q", exec.selectors[1])
	}
}
