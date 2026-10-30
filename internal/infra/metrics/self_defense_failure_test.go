package metrics_test

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	geoipingress "github.com/aiproxer/aiproxer/internal/stdhttp/geoip"
	"github.com/aiproxer/aiproxer/internal/stdhttp/selfdefense"
	"github.com/prometheus/client_golang/prometheus"
)

// This file is the external half of the label-safety and totality proof. It lives
// in the metrics package directory so it can use the in-package test seam, and it
// is an external test package so it may also import the real data-plane gate:
// internal/stdhttp/selfdefense and internal/stdhttp/contract both import
// internal/infra/metrics, which an in-package test could not do without an import
// cycle. Every collaborator below is the production type, not a stand-in.

const selfDefenseFailureNow = "2026-03-02T12:00:00Z"

// selfDefenseFailureStack is the real ingress self-defense gate over a real
// bounded adaptive state, observing through a real *metrics.SelfDefenseProm whose
// label lookup panics, all wrapped in the production recovery middleware.
type selfDefenseFailureStack struct {
	handler http.Handler
	state   *ingressdefense.State
	metrics *metrics.SelfDefenseProm
}

func newSelfDefenseFailureStack(t *testing.T) selfDefenseFailureStack {
	t.Helper()
	state, err := ingressdefense.NewState(ingressdefense.StateLimits{MaxEntries: 64, StateTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	prom := metrics.RegisterSelfDefenseProm(prometheus.NewRegistry())
	if prom == nil {
		t.Fatal("RegisterSelfDefenseProm returned nil")
	}
	// The fault: a label sink whose declared variable-label arity never matches
	// the values SelfDefenseProm supplies, so prometheus.CounterVec panics inside
	// WithLabelValues on every observation.
	metrics.InstallPanickingLabelSink(prom)

	policy := ingressdefense.Policy{
		Enabled: true, AuthFailures: 2, FailureWindow: time.Minute,
		InitialQuarantine: time.Hour, MaxQuarantine: 2 * time.Hour,
	}
	now, err := time.Parse(time.RFC3339, selfDefenseFailureNow)
	if err != nil {
		t.Fatal(err)
	}
	fixedNow := func() time.Time { return now }
	gate := selfdefense.Middleware(selfdefense.Input{
		Policy:          &policy,
		State:           state,
		Resolver:        geoipingress.ResolverConfig{Source: geoipingress.SourceDirect},
		ImpossiblePaths: true,
		// A quarantined source that can positively prove it cannot authenticate is
		// the generic 429 case.
		Probe:    func(*http.Request) bool { return false },
		Observer: prom,
		Now:      fixedNow,
	}, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	return selfDefenseFailureStack{
		// The production panic boundary. A panic that escapes the gate is caught
		// here and becomes a 500, which is exactly the wire answer the guard in
		// self_defense_prom.go must prevent.
		handler: stdhttp.RecoveryMiddleware(nil, gate),
		state:   state,
		metrics: prom,
	}
}

func (s selfDefenseFailureStack) serve(t *testing.T, target, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://example.test"+target, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	return rec
}

// TestSelfDefenseMetricsFailureCannotChangeTheWireRefusal closes the second half
// of the design "Error Handling" rule, observer FAILURE rather than only observer
// ABSENCE.
//
// The gate calls the observer after it has already mutated the adaptive state but
// before it writes the response, so without the deferred recover in
// [metrics.SelfDefenseProm.Denial] and [metrics.SelfDefenseProm.QuarantineTransition]
// a metrics panic escapes the gate and the production recovery middleware rewrites
// the deliberate generic 404 into a 500. With the recover, the generic refusal is
// the answer on the wire and the state mutation survives.
func TestSelfDefenseMetricsFailureCannotChangeTheWireRefusal(t *testing.T) {
	t.Parallel()

	stack := newSelfDefenseFailureStack(t)
	const remote = "203.0.113.5:443"

	// Impossible path: the gate records a probe offense, calls the observer, and
	// then writes the generic 404. A panicking observer must cost only the
	// observation.
	if rec := stack.serve(t, "/.env", remote); rec.Code != http.StatusNotFound {
		t.Fatalf("impossible path with a panicking metrics label sink = %d, want the generic 404", rec.Code)
	}
	if got := stack.state.Len(); got != 1 {
		t.Fatalf("adaptive state after the impossible path = %d entries, want 1", got)
	}

	// Second offense escalates the quarantine; the next credential-free request is
	// the generic 429, again with a panicking observer on both observation paths.
	if rec := stack.serve(t, "/.env", remote); rec.Code != http.StatusNotFound {
		t.Fatalf("escalating impossible path = %d, want the generic 404", rec.Code)
	}
	if rec := stack.serve(t, "/v1/models", remote); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("quarantined request with a panicking metrics label sink = %d, want the generic 429", rec.Code)
	}
	quarantined, err := netip.ParseAddr(netip.AddrPortFrom(netip.MustParseAddr("203.0.113.5"), 443).Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !stack.state.IsQuarantined(quarantined, mustTime(t)) {
		t.Fatal("the quarantine must survive a failing metrics observation")
	}
	// A delegated, non-quarantined request is unaffected by the failing sink.
	if rec := stack.serve(t, "/v1/models", "198.51.100.9:443"); rec.Code != http.StatusOK {
		t.Fatalf("delegated request = %d, want the inner 200", rec.Code)
	}
}

// TestSelfDefenseMetricsFailureStillProjectsTheObserver pins that the failing
// sink is a Prometheus client fault inside the collector and not a nil or
// otherwise absent observer, so the failure proof above is measuring failure and
// not silently degenerating into the absence case.
func TestSelfDefenseMetricsFailureStillProjectsTheObserver(t *testing.T) {
	t.Parallel()

	var observer selfdefense.Observer = metrics.RegisterSelfDefenseProm(prometheus.NewRegistry())
	typed, ok := observer.(*metrics.SelfDefenseProm)
	if !ok {
		t.Fatalf("registered collector = %T, want *metrics.SelfDefenseProm", observer)
	}
	metrics.InstallPanickingLabelSink(typed)
	observer.Denial(ingressdefense.ReasonImpossiblePath)
	observer.QuarantineTransition(ingressdefense.ReasonActiveQuarantine, 4)
}

func mustTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, selfDefenseFailureNow)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
