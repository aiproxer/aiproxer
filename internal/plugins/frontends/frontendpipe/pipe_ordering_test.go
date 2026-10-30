package frontendpipe_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"

	"github.com/aiproxer/aiproxer/internal/plugins/frontends/decodeqos"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/execerr"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/frontendpipe"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
)

// Task 1.2 characterization: freeze the shared frontendpipe oracle order
// (design section 1):
// body read -> metadata-only pre-admission resolver/header selector ->
// shared preflight -> TryAdmit -> guarded body route (custom ResolveBodyRoute
// or shared RouteFromBodyModel)/Decode -> post-decode/traffic -> execute.
// A future large-payload candidate lane must not reorder these stages.

type orderingLog struct {
	mu     sync.Mutex
	events []string
}

func (l *orderingLog) add(ev string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
}

func (l *orderingLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.events))
	copy(out, l.events)
	return out
}

func indexOf(events []string, want string) int {
	for i, ev := range events {
		if ev == want {
			return i
		}
	}
	return -1
}

func assertOrder(t *testing.T, events []string, want ...string) {
	t.Helper()
	prev := -1
	for _, w := range want {
		i := indexOf(events[prev+1:], w)
		if i < 0 {
			t.Fatalf("event %q not found after position %d in %q", w, prev, events)
		}
		prev += 1 + i
	}
}

type orderingWire struct {
	log *orderingLog
}

func (w *orderingWire) write(rw http.ResponseWriter, status int, msg string) error {
	w.log.add("wire:" + msg)
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(status)
	return json.NewEncoder(rw).Encode(map[string]string{"error": msg})
}

func (w *orderingWire) WriteBodyTooLarge(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusRequestEntityTooLarge, "too large")
}

func (w *orderingWire) WriteReadBodyFailed(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "read failed")
}

func (w *orderingWire) WriteExecutorNotConfigured(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusInternalServerError, "no executor")
}

func (w *orderingWire) WritePreflightCanceled(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusServiceUnavailable, "canceled")
}

func (w *orderingWire) WriteInvalidJSON(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "invalid json")
}

func (w *orderingWire) WriteAdmissionReject(rw http.ResponseWriter, d decodeqos.Decision) error {
	return w.write(rw, d.Status, "admission")
}

func (w *orderingWire) WriteInvalidRequest(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusBadRequest, "invalid request")
}

func (w *orderingWire) WriteExecuteError(rw http.ResponseWriter, out execerr.Outcome) error {
	return w.write(rw, out.Status, "execute")
}

func (w *orderingWire) WriteEncodeFailed(rw http.ResponseWriter) error {
	return w.write(rw, http.StatusInternalServerError, "encode failed")
}

type orderingExec struct {
	log    *orderingLog
	called bool
	err    error
}

func (e *orderingExec) Execute(context.Context, *aipapi.Call) (aipapi.EventStream, error) {
	e.called = true
	e.log.add("execute")
	if e.err != nil {
		return nil, e.err
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func (e *orderingExec) CancelALeg(context.Context, aipapi.ALegCancelRequest) error { return nil }
func (e *orderingExec) WallClock() func() time.Time                                { return nil }

type orderingAdmission struct {
	log         *orderingLog
	calls       int
	weights     []int64
	releases    int
	reject      bool
	rejectErr   error
	weightLimit int64
}

func (a *orderingAdmission) TryAcquire(_ context.Context, weight int64) (func(), bool, error) {
	a.calls++
	a.weights = append(a.weights, weight)
	a.log.add("admit")
	if a.reject {
		return nil, false, a.rejectErr
	}
	return func() {
		a.releases++
		a.log.add("release")
	}, true, nil
}

type orderingObserver struct {
	log  *orderingLog
	body []byte
	leg  traffic.Leg
}

func (o *orderingObserver) OnObservation(_ context.Context, ev traffic.Observation) error {
	o.log.add("traffic")
	o.body = append([]byte(nil), ev.Body...)
	o.leg = ev.Leg
	return nil
}

func orderingValidCall() *aipapi.Call {
	return &aipapi.Call{
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hi")},
		}},
	}
}

func newOrderingSpec(log *orderingLog, exec *orderingExec, adm *orderingAdmission, obs *orderingObserver) frontendpipe.Spec[struct{}] {
	return frontendpipe.Spec[struct{}]{
		Config: frontendpipe.Config{
			Exec:                 exec,
			FrontendID:           "ordering",
			DecodeAdmission:      adm,
			TrafficPorts:         traffic.PortBundle{Obs: obs},
			DefaultRouteSelector: "stub:default",
			RoutePrefixes:        routeselect.NewPrefixSet([]string{"stub"}),
		},
		Wire: &orderingWire{log: log},
		MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
			if path == "/v1/create" {
				return frontendpipe.PathMatch{}, true
			}
			return frontendpipe.PathMatch{}, false
		},
		RouteFromBodyModel: true,
		Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			log.add("decode:" + dctx.RouteSelector)
			return &frontendpipe.Decoded{Call: orderingValidCall(), Stream: false, RouteSelector: dctx.RouteSelector}, nil
		},
		BuildEncodeOpts: func(*frontendpipe.Decoded) struct{} { return struct{}{} },
		WriteStream: func(context.Context, http.ResponseWriter, *aipapi.Call, aipapi.EventStream, struct{}) error {
			return nil
		},
		WriteNonStream: func(_ context.Context, w http.ResponseWriter, _ *aipapi.Call, es aipapi.EventStream, _ struct{}) error {
			if es != nil {
				_ = es.Close()
			}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"ok":true}`)
			return nil
		},
	}
}

func TestPipeOrdering_SuccessResolverOverridesHeaderBeforeDecode(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)
	body := `{"model":"stub:body-model","x":1}`
	spec.ResolvePreAdmissionRoute = func(headers http.Header, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		log.add("resolver")
		// Metadata-only: must not receive raw body bytes.
		return routing.ExplicitRoute("stub:resolver")
	}
	spec.AfterDecode = func(context.Context, *frontendpipe.Decoded) error {
		log.add("afterDecode")
		return nil
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(body))
	req.Header.Set("X-AIP-Route", "stub:header")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	events := log.snapshot()
	// Shared oracle: metadata-only resolver -> admit -> decode -> afterDecode -> traffic -> execute.
	assertOrder(t, events, "resolver", "admit", "decode:stub:resolver", "afterDecode", "traffic", "execute")
	if adm.calls != 1 {
		t.Fatalf("admit calls=%d want 1", adm.calls)
	}
	if len(adm.weights) != 1 || adm.weights[0] != int64(len(body)) {
		t.Fatalf("admit weights=%v want [%d]", adm.weights, len(body))
	}
	if adm.releases != 1 {
		t.Fatalf("releases=%d want 1 (permit held across guarded decode then released)", adm.releases)
	}
	if !exec.called {
		t.Fatal("executor not called")
	}
	if obs.leg != traffic.LegCTP {
		t.Fatalf("traffic leg=%q want client_to_proxy", obs.leg)
	}
	if string(obs.body) != body {
		t.Fatalf("traffic body=%q want original %q", obs.body, body)
	}
}

func TestPipeOrdering_HeaderWinsOverBodyModelWhenResolverAbsent(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"model":"stub:body-model"}`))
	req.Header.Set("X-AIP-Route", "stub:header")
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	events := log.snapshot()
	found := false
	for _, ev := range events {
		if ev == "decode:stub:header" {
			found = true
		}
		if ev == "decode:stub:body-model" {
			t.Fatalf("body model overrode header selector: %q", events)
		}
	}
	if !found {
		t.Fatalf("header selector not used: %q", events)
	}
}

func TestPipeOrdering_BodyModelAndDefaultFallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "known prefix uses body model", body: `{"model":"stub:from-body"}`, want: "decode:stub:from-body"},
		{name: "unknown model falls back to default", body: `{"model":"unknown-model"}`, want: "decode:stub:default"},
		{name: "missing model falls back to default", body: `{"x":1}`, want: "decode:stub:default"},
		{name: "late model still proven", body: `{"a":1,"b":[1,2,3],"model":"stub:late"}`, want: "decode:stub:late"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			log := &orderingLog{}
			exec := &orderingExec{log: log}
			adm := &orderingAdmission{log: log}
			obs := &orderingObserver{log: log}
			spec := newOrderingSpec(log, exec, adm, obs)

			req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			frontendpipe.ServeHTTP(&spec, rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			found := false
			for _, ev := range log.snapshot() {
				if ev == tc.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %q in %q", tc.want, log.snapshot())
			}
		})
	}
}

func TestPipeOrdering_ResolverRunsBeforePreflight(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)
	spec.ResolvePreAdmissionRoute = func(_ http.Header, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		log.add("resolver")
		return routing.ResolvedRoute{}
	}
	spec.ResolveBodyRoute = func(_ []byte, _ frontendpipe.PathMatch) routing.ResolvedRoute {
		log.add("body-resolver")
		return routing.ResolvedRoute{}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s want 400 invalid json", rec.Code, rec.Body.String())
	}
	events := log.snapshot()
	if indexOf(events, "resolver") < 0 {
		t.Fatalf("pre-admission resolver must run before preflight even for malformed JSON: %q", events)
	}
	if indexOf(events, "body-resolver") >= 0 {
		t.Fatalf("body resolver must not run after preflight failure: %q", events)
	}
	if indexOf(events, "admit") >= 0 {
		t.Fatalf("admission must not run after preflight failure: %q", events)
	}
	if indexOf(events, "decode:stub:default") >= 0 || indexOf(events, "decode:") >= 0 {
		t.Fatalf("decode must not run after preflight failure: %q", events)
	}
	if exec.called {
		t.Fatal("executor ran after preflight failure")
	}
}

func TestPipeOrdering_PreflightBeforeAdmission(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log, reject: true}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 (preflight owns malformed JSON even when admission saturated)", rec.Code)
	}
	if adm.calls != 0 {
		t.Fatalf("TryAdmit calls=%d want 0 (preflight precedes admission)", adm.calls)
	}
	if exec.called {
		t.Fatal("executor ran after preflight failure")
	}
}

func TestPipeOrdering_AdmissionBeforeDecode(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log, reject: true}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"a":1}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d want 429 admission reject", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != decodeqos.RetryAfterSeconds {
		t.Fatalf("Retry-After=%q want %q", got, decodeqos.RetryAfterSeconds)
	}
	events := log.snapshot()
	if indexOf(events, "admit") < 0 {
		t.Fatalf("admit not recorded: %q", events)
	}
	for _, ev := range events {
		if strings.HasPrefix(ev, "decode:") {
			t.Fatalf("decode ran after admission reject: %q", events)
		}
	}
	if exec.called {
		t.Fatal("executor ran after admission reject")
	}
	if adm.calls != 1 {
		t.Fatalf("admit calls=%d want exactly 1 (no second decision on fallback)", adm.calls)
	}
}

func TestPipeOrdering_MethodPathBodyExecPrecedence(t *testing.T) {
	t.Parallel()
	newSpec := func(log *orderingLog, maxBytes int64) (frontendpipe.Spec[struct{}], *orderingExec, *orderingAdmission) {
		exec := &orderingExec{log: log}
		adm := &orderingAdmission{log: log}
		spec := newOrderingSpec(log, exec, adm, &orderingObserver{log: log})
		spec.MaxRequestBodyBytes = maxBytes
		return spec, exec, adm
	}

	t.Run("method before path", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, exec, _ := newSpec(log, 0)
		req := httptest.NewRequest(http.MethodGet, "/unknown-path", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405", rec.Code)
		}
		if exec.called {
			t.Fatal("executor ran after method reject")
		}
	})

	t.Run("path before body", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, exec, _ := newSpec(log, 4)
		req := httptest.NewRequest(http.MethodPost, "/unknown-path", strings.NewReader(`{"a":123456}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 (path owns unknown route even for oversized body)", rec.Code)
		}
		if exec.called {
			t.Fatal("executor ran after path reject")
		}
	})

	t.Run("body before exec", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, exec, _ := newSpec(log, 4)
		spec.Exec = nil
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"a":123456}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status=%d want 413 (body limit owns oversized body even without executor)", rec.Code)
		}
		_ = exec
	})

	t.Run("exec check before decode", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, _, adm := newSpec(log, 0)
		spec.Exec = nil
		req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"a":1}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status=%d want 500 executor not configured", rec.Code)
		}
		if adm.calls != 0 {
			t.Fatalf("admit calls=%d want 0 (exec check precedes admission)", adm.calls)
		}
	})
}

func TestPipeOrdering_DecodePermitHeldAcrossRouteFromBodyModel(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"a":1,"model":"stub:late"}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	events := log.snapshot()
	// Guard holds one permit across RouteFromBodyModel defaulting + Decode.
	assertOrder(t, events, "admit", "decode:stub:late", "release")
	if adm.calls != 1 || adm.releases != 1 {
		t.Fatalf("admit=%d releases=%d want exactly 1/1 (single TryAdmit decision, no release/reacquire)", adm.calls, adm.releases)
	}
}

func TestPipeOrdering_AfterDecodeErrorSkipsTrafficAndExecute(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)
	spec.AfterDecode = func(context.Context, *frontendpipe.Decoded) error {
		log.add("afterDecode")
		return &frontendpipe.StatusError{Status: http.StatusBadRequest, Message: "after"}
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{"a":1}`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", rec.Code)
	}
	events := log.snapshot()
	assertOrder(t, events, "admit", "decode:stub:default", "afterDecode")
	if indexOf(events, "traffic") >= 0 {
		t.Fatalf("traffic emitted after AfterDecode error: %q", events)
	}
	if exec.called {
		t.Fatal("executor ran after AfterDecode error")
	}
}

func TestPipeOrdering_AltServeBeforePathAndBody(t *testing.T) {
	t.Parallel()
	log := &orderingLog{}
	exec := &orderingExec{log: log}
	adm := &orderingAdmission{log: log}
	obs := &orderingObserver{log: log}
	spec := newOrderingSpec(log, exec, adm, obs)
	spec.AltServe = func(_ context.Context, w http.ResponseWriter, _ *http.Request) bool {
		log.add("altserve")
		w.WriteHeader(http.StatusTeapot)
		return true
	}

	req := httptest.NewRequest(http.MethodPost, "/unknown-path", strings.NewReader(`{`))
	rec := httptest.NewRecorder()
	frontendpipe.ServeHTTP(&spec, rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status=%d want 418 from AltServe", rec.Code)
	}
	events := log.snapshot()
	if indexOf(events, "altserve") < 0 {
		t.Fatalf("altserve not recorded: %q", events)
	}
	if indexOf(events, "admit") >= 0 {
		t.Fatalf("admission ran after AltServe claimed request: %q", events)
	}
	if exec.called {
		t.Fatal("executor ran after AltServe")
	}
}

// Task 7.2 characterization: prove candidate gates (starting with CandidatePrerequisites)
// evaluate strictly after each frontend's existing outer checks (Method, AltServe, MatchPath).
// The shared pipeline must never evaluate candidate prerequisites or allocate candidate
// resources if an outer check short-circuits the request.
func TestPipeOrdering_CandidateGatesEvaluateStrictlyAfterOuterChecks(t *testing.T) {
	t.Parallel()

	newCandidateSpec := func(log *orderingLog) (frontendpipe.Spec[struct{}], *stubLargeBodyExecutor, *testProfile) {
		exec := &stubLargeBodyExecutor{}
		prof := &testProfile{}
		spec := frontendpipe.Spec[struct{}]{
			Config: frontendpipe.Config{
				Exec: exec,
			},
			Profile: prof,
			MatchPath: func(path string) (frontendpipe.PathMatch, bool) {
				log.add("matchpath:" + path)
				if path == "/v1/create" {
					return frontendpipe.PathMatch{}, true
				}
				return frontendpipe.PathMatch{}, false
			},
			AltServe: func(_ context.Context, w http.ResponseWriter, r *http.Request) bool {
				log.add("altserve:" + r.URL.Path)
				if r.URL.Path == "/v1/alt" {
					w.WriteHeader(http.StatusTeapot)
					return true
				}
				return false
			},
			Decode: func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
				log.add("decode")
				return &frontendpipe.Decoded{
					Call: &aipapi.Call{ID: "call_test"},
				}, nil
			},
			BuildEncodeOpts: func(decoded *frontendpipe.Decoded) struct{} {
				return struct{}{}
			},
			WriteNonStream: func(ctx context.Context, w http.ResponseWriter, call *aipapi.Call, es aipapi.EventStream, opts struct{}) error {
				w.WriteHeader(http.StatusOK)
				return nil
			},
		}
		return spec, exec, prof
	}

	t.Run("method check precedes candidate evaluation", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, _, _ := newCandidateSpec(log)

		// Non-POST request must be rejected with 405 Method Not Allowed before candidate logic.
		req := httptest.NewRequest(http.MethodGet, "/v1/create", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want 405 Method Not Allowed", rec.Code)
		}
		events := log.snapshot()
		if indexOf(events, "matchpath:/v1/create") >= 0 {
			t.Fatalf("matchpath evaluated after method reject: %v", events)
		}
		if indexOf(events, "decode") >= 0 {
			t.Fatalf("decode ran after method reject: %v", events)
		}
	})

	t.Run("altserve precedes candidate evaluation", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, _, _ := newCandidateSpec(log)

		// AltServe-handled request must be claimed before candidate logic or path matching.
		req := httptest.NewRequest(http.MethodPost, "/v1/alt", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusTeapot {
			t.Fatalf("status=%d want 418 Teapot from AltServe", rec.Code)
		}
		events := log.snapshot()
		if indexOf(events, "altserve:/v1/alt") < 0 {
			t.Fatalf("altserve event missing: %v", events)
		}
		if indexOf(events, "matchpath:/v1/alt") >= 0 {
			t.Fatalf("matchpath ran after AltServe claimed request: %v", events)
		}
		if indexOf(events, "decode") >= 0 {
			t.Fatalf("decode ran after AltServe: %v", events)
		}
	})

	t.Run("matchpath precedes candidate evaluation", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, _, _ := newCandidateSpec(log)

		// Unknown path must be rejected with 404 Not Found before candidate logic.
		req := httptest.NewRequest(http.MethodPost, "/v1/unknown", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status=%d want 404 Not Found", rec.Code)
		}
		events := log.snapshot()
		if indexOf(events, "matchpath:/v1/unknown") < 0 {
			t.Fatalf("matchpath event missing: %v", events)
		}
		if indexOf(events, "decode") >= 0 {
			t.Fatalf("decode ran after path reject: %v", events)
		}
	})

	t.Run("candidate prerequisites evaluation contract order", func(t *testing.T) {
		t.Parallel()
		// Verifies the structural ordering contract: CandidatePrerequisites(spec)
		// must only be evaluated after outer Method, AltServe, and MatchPath checks pass.
		log := &orderingLog{}
		spec, lbe, _ := newCandidateSpec(log)

		// Helper representing the prescribed outer-check-then-candidate-gate sequence.
		evaluateCandidatePipeline := func(r *http.Request) (status int, candidateEvaluated bool) {
			if r.Method != http.MethodPost {
				return http.StatusMethodNotAllowed, false
			}
			rec := httptest.NewRecorder()
			if spec.AltServe != nil && spec.AltServe(r.Context(), rec, r) {
				return rec.Code, false
			}
			if _, ok := spec.MatchPath(r.URL.Path); !ok {
				return http.StatusNotFound, false
			}
			// Outer checks passed: now evaluate candidate prerequisites.
			log.add("candidate_prerequisites")
			exec, ok := frontendpipe.CandidatePrerequisites(&spec)
			if !ok || exec == nil {
				return http.StatusOK, false // falls back to canonical
			}
			return http.StatusOK, true
		}

		// 1. GET -> 405, candidate not evaluated
		if status, cand := evaluateCandidatePipeline(httptest.NewRequest(http.MethodGet, "/v1/create", nil)); status != 405 || cand {
			t.Fatalf("GET: status=%d cand=%v want 405, false", status, cand)
		}
		// 2. AltServe -> 418, candidate not evaluated
		if status, cand := evaluateCandidatePipeline(httptest.NewRequest(http.MethodPost, "/v1/alt", nil)); status != 418 || cand {
			t.Fatalf("AltServe: status=%d cand=%v want 418, false", status, cand)
		}
		// 3. Unknown path -> 404, candidate not evaluated
		if status, cand := evaluateCandidatePipeline(httptest.NewRequest(http.MethodPost, "/v1/unknown", nil)); status != 404 || cand {
			t.Fatalf("Unknown path: status=%d cand=%v want 404, false", status, cand)
		}
		// 4. Valid path -> passes outer checks, candidate evaluated
		if status, cand := evaluateCandidatePipeline(httptest.NewRequest(http.MethodPost, "/v1/create", nil)); status != 200 || !cand {
			t.Fatalf("Valid path: status=%d cand=%v want 200, true", status, cand)
		}

		events := log.snapshot()
		assertOrder(t, events, "matchpath:/v1/create", "candidate_prerequisites")
		if exec, ok := frontendpipe.CandidatePrerequisites(&spec); !ok || exec != lbe {
			t.Fatalf("CandidatePrerequisites mismatch: want (%p, true), got (%p, %v)", lbe, exec, ok)
		}
	})

	t.Run("production ServeHTTP candidate gates never invoked after outer rejection", func(t *testing.T) {
		t.Parallel()
		log := &orderingLog{}
		spec, _, _ := newCandidateSpec(log)
		spec.Wire = &orderingWire{log: log}
		spec.Decode = func(dctx frontendpipe.DecodeContext) (*frontendpipe.Decoded, error) {
			log.add("decode")
			return &frontendpipe.Decoded{
				Call: orderingValidCall(),
			}, nil
		}
		spec.OnPreCaptureGate = func(r *http.Request, res frontendpipe.PreCaptureResult) {
			log.add("candidate_gates_evaluated")
		}

		// 1. GET -> 405 Method Not Allowed; candidate gates must NOT be reached.
		req1 := httptest.NewRequest(http.MethodGet, "/v1/create", strings.NewReader(`{}`))
		rec1 := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec1, req1)
		if rec1.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET: status=%d want 405", rec1.Code)
		}
		if indexOf(log.snapshot(), "candidate_gates_evaluated") >= 0 {
			t.Fatalf("candidate gates evaluated after GET method rejection: %v", log.snapshot())
		}

		// 2. AltServe -> 418 Teapot; candidate gates must NOT be reached.
		req2 := httptest.NewRequest(http.MethodPost, "/v1/alt", strings.NewReader(`{}`))
		rec2 := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec2, req2)
		if rec2.Code != http.StatusTeapot {
			t.Fatalf("AltServe: status=%d want 418", rec2.Code)
		}
		if indexOf(log.snapshot(), "candidate_gates_evaluated") >= 0 {
			t.Fatalf("candidate gates evaluated after AltServe claimed request: %v", log.snapshot())
		}

		// 3. Unknown path -> 404 Not Found; candidate gates must NOT be reached.
		req3 := httptest.NewRequest(http.MethodPost, "/v1/unknown", strings.NewReader(`{}`))
		rec3 := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec3, req3)
		if rec3.Code != http.StatusNotFound {
			t.Fatalf("Unknown path: status=%d want 404", rec3.Code)
		}
		if indexOf(log.snapshot(), "candidate_gates_evaluated") >= 0 {
			t.Fatalf("candidate gates evaluated after 404 path rejection: %v", log.snapshot())
		}

		// 4. Valid path -> passes outer checks, candidate gates ARE reached.
		req4 := httptest.NewRequest(http.MethodPost, "/v1/create", strings.NewReader(`{}`))
		rec4 := httptest.NewRecorder()
		frontendpipe.ServeHTTP(&spec, rec4, req4)
		if rec4.Code != http.StatusOK {
			t.Fatalf("Valid path: status=%d want 200", rec4.Code)
		}
		events := log.snapshot()
		assertOrder(t, events, "matchpath:/v1/create", "candidate_gates_evaluated")
	})
}
