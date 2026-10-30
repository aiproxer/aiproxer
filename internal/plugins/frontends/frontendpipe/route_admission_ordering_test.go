package frontendpipe_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
)

// TestPipeOrdering_BodyModelRouteDoesNotBypassDecodeAdmission proves a
// body-dependent route extraction is decode-admission work.
//
// Extracting the route from the body is a full-body JSON parse: it scans the
// whole payload and, for a large model id, allocates a copy of it. That is
// exactly the work decode admission bounds, so it must run under the acquired
// permit, together with the protocol decode it prepares. A rejected permit
// therefore rejects the request before any body parse happens.
//
// The check is differential: the same rejected request is served once with
// body-model routing enabled and once without it. Every other stage — the
// bounded body read, the JSON preflight, the admission decision — is identical,
// so the difference in allocated bytes is the body-dependent route parse.
func TestPipeOrdering_BodyModelRouteDoesNotBypassDecodeAdmission(t *testing.T) {
	// Not parallel: the allocation measurement must not race other tests.
	largeModel := strings.Repeat("m", 4<<20)
	body := `{"model":"` + largeModel + `","x":1}`

	serve := func(routeFromBodyModel bool) (int, uint64, *orderingLog, *orderingExec, *orderingAdmission) {
		log := &orderingLog{}
		exec := &orderingExec{log: log}
		adm := &orderingAdmission{log: log}
		obs := &orderingObserver{log: log}
		spec := newOrderingSpec(log, exec, adm, obs)
		spec.RouteFromBodyModel = routeFromBodyModel
		spec.MaxRequestBodyBytes = int64(len(body)) + 1
		// The decode budget admits far less than this body: the permit is
		// deliberately rejected.
		adm.reject = true
		adm.rejectErr = decodeqos.ErrOverweight

		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(body))
		rec := httptest.NewRecorder()

		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		frontendpipe.ServeHTTP(&spec, rec, req)
		runtime.ReadMemStats(&after)
		return rec.Code, after.TotalAlloc - before.TotalAlloc, log, exec, adm
	}

	// Warm the shared machinery so the differential measures the route parse.
	_, _, _, _, _ = serve(false)

	codeWithout, allocWithout, _, _, admWithout := serve(false)
	codeWith, allocWith, log, exec, adm := serve(true)

	if codeWithout != http.StatusTooManyRequests || codeWith != http.StatusTooManyRequests {
		t.Fatalf("status = %d (no body route) / %d (body route) want %d: a rejected decode permit must reject the request",
			codeWithout, codeWith, http.StatusTooManyRequests)
	}
	for name, a := range map[string]*orderingAdmission{"without": admWithout, "with": adm} {
		if a.calls != 1 {
			t.Fatalf("%s: admit calls = %d want 1", name, a.calls)
		}
		if len(a.weights) != 1 || a.weights[0] != int64(len(body)) {
			t.Fatalf("%s: admit weights = %v want [%d]", name, a.weights, len(body))
		}
		if a.releases != 0 {
			t.Fatalf("%s: releases = %d want 0: no permit was acquired", name, a.releases)
		}
	}
	if exec.called {
		t.Fatal("executor ran for a rejected decode permit")
	}
	for _, event := range log.snapshot() {
		if strings.HasPrefix(event, "decode:") {
			t.Fatalf("protocol decode ran for a rejected decode permit: %q", event)
		}
	}

	// A body-dependent route parse copies the large model id, so enabling it
	// would add roughly the model id's size to the allocation total. The
	// threshold is well below one copy and well above measurement noise.
	if allocWith > allocWithout {
		if delta := allocWith - allocWithout; delta > uint64(len(largeModel))/2 {
			t.Fatalf("body-model routing allocated %d extra bytes (%d vs %d) for a rejected request: a body-dependent route parser ran before decode admission",
				delta, allocWith, allocWithout)
		}
	}
}

// TestPipeOrdering_CustomBodyRouteDoesNotRunBeforeAdmission proves a custom
// body-dependent route hook is decode-admission work: a rejected permit
// rejects the request before the hook runs.
//
// The hook is instrumentable, so the check is deterministic (invocation count
// zero), not only differential. It mirrors the SystemOne authority: when
// present it replaces the shared body-model path and wins even over a
// pre-admission/header route.
func TestPipeOrdering_CustomBodyRouteDoesNotRunBeforeAdmission(t *testing.T) {
	// Not parallel: deterministic hook-count check must not race other tests.
	largeModel := strings.Repeat("m", 4<<20)
	body := `{"model":"decision:` + largeModel + `","x":1}`

	serve := func() (int, *orderingLog, *orderingExec, *orderingAdmission, *atomic.Int64) {
		log := &orderingLog{}
		exec := &orderingExec{log: log}
		adm := &orderingAdmission{log: log}
		obs := &orderingObserver{log: log}
		spec := newOrderingSpec(log, exec, adm, obs)
		spec.RouteFromBodyModel = false
		spec.MaxRequestBodyBytes = int64(len(body)) + 1
		spec.RoutePrefixes = routeselect.NewPrefixSet([]string{"decision"})
		spec.DefaultRouteSelector = "decision:default"
		var hookCalls atomic.Int64
		spec.ResolveBodyRoute = func(b []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
			hookCalls.Add(1)
			return routeselect.NewPrefixSet([]string{"decision"}).ResolveFromModelOrDefault(b, "decision:default")
		}
		adm.reject = true
		adm.rejectErr = decodeqos.ErrOverweight

		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(body))
		req.Header.Set("X-AIP-Route", "decision:header")
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)
		return rec.Code, log, exec, adm, &hookCalls
	}

	code, log, exec, adm, hookCalls := serve()

	if code != http.StatusTooManyRequests {
		t.Fatalf("status = %d want %d: a rejected decode permit must reject the request", code, http.StatusTooManyRequests)
	}
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("body hook calls = %d want 0: custom body route must not run before decode admission", got)
	}
	if adm.calls != 1 {
		t.Fatalf("admit calls = %d want 1", adm.calls)
	}
	if len(adm.weights) != 1 || adm.weights[0] != int64(len(body)) {
		t.Fatalf("admit weights = %v want [%d]", adm.weights, len(body))
	}
	if adm.releases != 0 {
		t.Fatalf("releases = %d want 0: no permit was acquired", adm.releases)
	}
	if exec.called {
		t.Fatal("executor ran for a rejected decode permit")
	}
	for _, event := range log.snapshot() {
		if strings.HasPrefix(event, "decode:") {
			t.Fatalf("protocol decode ran for a rejected decode permit: %q", event)
		}
	}
}

// TestPipeOrdering_CustomBodyRouteRunsUnderHeldPermit proves the accepted
// path holds one permit across body-hook resolution and decode, then releases
// exactly once before execution. The custom hook is authoritative, including
// over a pre-admission/header route.
func TestPipeOrdering_CustomBodyRouteRunsUnderHeldPermit(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)
	spec.RouteFromBodyModel = false
	prefixes := routeselect.NewPrefixSet([]string{"decision"})
	spec.RoutePrefixes = prefixes
	spec.DefaultRouteSelector = "decision:default"
	spec.ResolvePreAdmissionRoute = func(_ http.Header, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		log.add("pre-resolver")
		return routing.ResolvedRoute{}
	}
	spec.ResolveBodyRoute = func(body []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		log.add("body-resolver")
		return prefixes.ResolveFromModelOrDefault(body, "decision:default")
	}
	spec.Decode = func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
		log.add("decode:" + dctx.RouteSelector)
		return &frontendpipe.Decoded{Call: orderingValidCall(), Stream: false, RouteSelector: dctx.RouteSelector}, nil
	}

	body := `{"model":"decision:from-body","x":1}`
	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(body))
	req.Header.Set("X-AIP-Route", "decision:header")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	events := log.snapshot()
	assertOrder(t, events, "pre-resolver", "admit", "body-resolver", "decode:decision:from-body", "release", "execute")
	if adm.calls != 1 || adm.releases != 1 {
		t.Fatalf("admit=%d releases=%d want exactly 1/1 (single TryAdmit, no release/reacquire)", adm.calls, adm.releases)
	}
	// Authoritative body hook wins over the header, even though the header
	// named a route.
	for _, ev := range events {
		if ev == "decode:decision:header" {
			t.Fatalf("header route survived authoritative body hook: %q", events)
		}
	}
}

// TestPipeOrdering_CustomBodyRouteDecoderErrorReleasesPermit proves a decoder
// failure under the held permit prevents execution, releases exactly once,
// and preserves the invalid-JSON mapping. Panic safety is owned by
// decodeqos.Guard (see TestGuard_panicDoesNotLeakLimiterPermit and
// TestGuard_releasesOnReturnAndPanic): the body hook and decode run inside
// the guard, so a panic still runs the deferred release.
func TestPipeOrdering_CustomBodyRouteDecoderErrorReleasesPermit(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)
	spec.RouteFromBodyModel = false
	prefixes := routeselect.NewPrefixSet([]string{"decision"})
	spec.RoutePrefixes = prefixes
	spec.DefaultRouteSelector = "decision:default"
	var hookCalls atomic.Int64
	spec.ResolveBodyRoute = func(body []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		hookCalls.Add(1)
		log.add("body-resolver")
		return prefixes.ResolveFromModelOrDefault(body, "decision:default")
	}
	spec.Decode = func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
		log.add("decode:" + dctx.RouteSelector)
		return nil, errors.New("decode boom")
	}

	body := `{"model":"decision:from-body","x":1}`
	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(body))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d: decoder errors map to invalid JSON", rec.Code, http.StatusBadRequest)
	}
	if hookCalls.Load() != 1 {
		t.Fatalf("body hook calls=%d want 1: hook runs once under the held permit even when decode fails", hookCalls.Load())
	}
	if adm.calls != 1 {
		t.Fatalf("admit calls=%d want 1: exactly one TryAdmit", adm.calls)
	}
	if adm.releases != 1 {
		t.Fatalf("releases=%d want 1: the held permit is released exactly once on decoder failure", adm.releases)
	}
	if exec.called {
		t.Fatal("executor ran after decoder failure")
	}
	events := log.snapshot()
	assertOrder(t, events, "admit", "body-resolver", "decode:decision:from-body", "release")
	for _, ev := range events {
		if ev == "execute" {
			t.Fatalf("execute ran after decoder failure: %q", events)
		}
	}
}
