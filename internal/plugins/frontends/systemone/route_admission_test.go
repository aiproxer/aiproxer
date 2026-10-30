package systemone

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

type admissionStub struct {
	calls     int
	weights   []int64
	releases  int
	reject    bool
	rejectErr error
}

func (a *admissionStub) TryAcquire(_ context.Context, weight int64) (func(), bool, error) {
	a.calls++
	a.weights = append(a.weights, weight)
	if a.reject {
		return nil, false, a.rejectErr
	}
	return func() { a.releases++ }, true, nil
}

type execStub struct {
	calls int
}

func (e *execStub) Execute(_ context.Context, _ *aipapi.Call) (aipapi.EventStream, error) {
	e.calls++
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventDecisionResult, Decision: &aipapi.DecisionResult{
			Model:   "jev",
			Answers: []aipapi.DecisionAnswer{{QuestionID: "q", Kind: aipapi.DecisionKindNoul, PTrue: 0.8}},
		}},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *execStub) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }
func (e *execStub) WallClock() func() time.Time                                { return nil }

func validSystemOneBody(model string) string {
	return `{"model":` + quoteJSONString(model) + `,"state":"x","questions":{"q":{"type":"noul"}}}`
}

func quoteJSONString(s string) string {
	// Minimal JSON string quoting for test models (no special chars except maybe colon).
	// Models in these tests are ASCII without quotes/backslashes.
	return `"` + s + `"`
}

// TestSystemOne_RejectedAdmissionDoesNotParseBody proves a rejected decode
// permit rejects a SystemOne request with 429 without a full-body model parse.
func TestSystemOne_RejectedAdmissionDoesNotParseBody(t *testing.T) {
	// Not parallel: differential allocation measurement.
	largeModel := "decision:" + strings.Repeat("m", 4<<20)
	body := validSystemOneBody(largeModel)

	serve := func(withBodyHook bool) (int, uint64, *admissionStub, *execStub) {
		adm := &admissionStub{reject: true, rejectErr: decodeqos.ErrOverweight}
		exec := &execStub{}
		opts := aipsdk.FrontendMountOptions{
			Exec:                exec,
			DefaultRoute:        "decision:default",
			RoutePrefixes:       []string{"decision"},
			MaxRequestBodyBytes: int64(len(body)) + 1,
			DecodeAdmission:     adm,
		}
		spec := handlerSpec(opts, Config{})
		if !withBodyHook {
			spec.ResolveBodyRoute = nil
			spec.RouteFromBodyModel = false
		}
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		rec := httptest.NewRecorder()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		frontendpipe.ServeHTTP(spec, rec, req)
		runtime.ReadMemStats(&after)
		return rec.Code, after.TotalAlloc - before.TotalAlloc, adm, exec
	}

	// Warm shared machinery so the differential measures the route parse.
	_, _, _, _ = serve(false)

	codeWithout, allocWithout, admWithout, execWithout := serve(false)
	codeWith, allocWith, admWith, execWith := serve(true)

	if codeWithout != http.StatusTooManyRequests || codeWith != http.StatusTooManyRequests {
		t.Fatalf("status = %d (no hook) / %d (hook) want %d", codeWithout, codeWith, http.StatusTooManyRequests)
	}
	for name, adm := range map[string]*admissionStub{"without": admWithout, "with": admWith} {
		if adm.calls != 1 {
			t.Fatalf("%s: admit calls = %d want 1", name, adm.calls)
		}
		if len(adm.weights) != 1 || adm.weights[0] != int64(len(body)) {
			t.Fatalf("%s: admit weights = %v want [%d]", name, adm.weights, len(body))
		}
		if adm.releases != 0 {
			t.Fatalf("%s: releases = %d want 0: no permit was acquired", name, adm.releases)
		}
	}
	if execWithout.calls != 0 || execWith.calls != 0 {
		t.Fatalf("executor ran for rejected permit: without=%d with=%d", execWithout.calls, execWith.calls)
	}
	// A body-dependent route parse copies the large model id. The threshold is
	// well below one copy and well above measurement noise.
	if allocWith > allocWithout {
		if delta := allocWith - allocWithout; delta > uint64(len(largeModel))/2 {
			t.Fatalf("SystemOne body routing allocated %d extra bytes (%d vs %d) for rejected request", delta, allocWith, allocWithout)
		}
	}
}

// TestSystemOne_BodyRouteCompatibilityMatrix proves SystemOne routing keeps
// body-only authority, default fallback, and semantic provenance, including
// under a conflicting route header.
func TestSystemOne_BodyRouteCompatibilityMatrix(t *testing.T) {
	t.Parallel()

	serve := func(body, defaultRoute, header string) (string, routing.RouteOrigin, int) {
		exec := &routeOriginExecutor{}
		spec := handlerSpec(aipsdk.FrontendMountOptions{
			Exec:          exec,
			DefaultRoute:  defaultRoute,
			RoutePrefixes: []string{"decision"},
		}, Config{})
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(body))
		if header != "" {
			req.Header.Set("X-AIP-Route", header)
		}
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		return exec.selector, exec.origin, rec.Code
	}

	validBody := func(model string) string { return validSystemOneBody(model) }

	t.Run("explicit body prefix differing from default", func(t *testing.T) {
		t.Parallel()
		sel, origin, code := serve(validBody("decision:jev"), "decision:other", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d", code)
		}
		if sel != "decision:jev" || origin != routing.RouteOriginExplicit {
			t.Fatalf("got %q %q want decision:jev explicit", sel, origin)
		}
	})

	t.Run("explicit body identical to default stays explicit", func(t *testing.T) {
		t.Parallel()
		sel, origin, code := serve(validBody("decision:jev"), "decision:jev", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d", code)
		}
		if sel != "decision:jev" || origin != routing.RouteOriginExplicit {
			t.Fatalf("got %q %q want decision:jev explicit", sel, origin)
		}
	})

	t.Run("unprefixed body falls back to default", func(t *testing.T) {
		t.Parallel()
		sel, origin, code := serve(validBody("jev"), "decision:default", "")
		if code != http.StatusOK {
			t.Fatalf("code=%d", code)
		}
		if sel != "decision:default" || origin != routing.RouteOriginDefault {
			t.Fatalf("got %q %q want decision:default default", sel, origin)
		}
	})

	t.Run("conflicting header plus known body prefix: body wins", func(t *testing.T) {
		t.Parallel()
		sel, origin, code := serve(validBody("decision:jev"), "decision:default", "decision:header")
		if code != http.StatusOK {
			t.Fatalf("code=%d", code)
		}
		if sel != "decision:jev" || origin != routing.RouteOriginExplicit {
			t.Fatalf("got %q %q want decision:jev explicit (body wins)", sel, origin)
		}
	})

	t.Run("conflicting header plus unprefixed body: default wins", func(t *testing.T) {
		t.Parallel()
		sel, origin, code := serve(validBody("jev"), "decision:default", "decision:header")
		if code != http.StatusOK {
			t.Fatalf("code=%d", code)
		}
		if sel != "decision:default" || origin != routing.RouteOriginDefault {
			t.Fatalf("got %q %q want decision:default default (header ignored)", sel, origin)
		}
	})

	t.Run("no default plus header: header ignored by explicit plan invariant", func(t *testing.T) {
		t.Parallel()
		// #1385 plan invariant 2: a client X-AIP-Route must not influence
		// SystemOne, even when the body route and the operator default both
		// resolve to empty. This is an intentional authority change from the
		// pre-PR behavior, which leaked the header through the shared
		// pre-admission fallback when no default was configured. The body hook
		// is authoritative, so decode preserves the client model, the header
		// never supplies a route, and the origin stays unset rather than
		// claiming the proxy's default.
		exec := &routeOriginExecutor{}
		adm := &admissionStub{}
		spec := handlerSpec(aipsdk.FrontendMountOptions{
			Exec:            exec,
			DefaultRoute:    "",
			RoutePrefixes:   []string{"decision"},
			DecodeAdmission: adm,
		}, Config{})
		req := httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(validBody("jev")))
		req.Header.Set("X-AIP-Route", "decision:header")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(spec, rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code=%d want %d: decode preserves the client model when no default is configured", rec.Code, http.StatusOK)
		}
		if adm.calls != 1 || adm.releases != 1 {
			t.Fatalf("admit=%d releases=%d want exactly 1/1: one permit held across body route and decode", adm.calls, adm.releases)
		}
		if exec.selector != "jev" {
			t.Fatalf("selector=%q want %q: header must not supply the SystemOne route", exec.selector, "jev")
		}
		if exec.selector == "decision:header" {
			t.Fatalf("header route leaked into SystemOne: %q", exec.selector)
		}
		if exec.origin != routing.RouteOriginUnset {
			t.Fatalf("origin=%q want unset: an empty default is not the proxy's default route", exec.origin)
		}
	})
}
