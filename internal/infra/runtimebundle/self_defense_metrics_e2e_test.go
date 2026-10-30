package runtimebundle_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	httpcontract "github.com/aiproxer/aiproxer/internal/stdhttp/contract"
)

// selfDefenseE2EKey exists only to make the transport-auth chain answerable. The
// gate's conservative probe reports DefinitelyNoCredential only for a request
// that carries no configured API-key header value, so a credential-bearing
// request must reach authentication and a credential-free one must not.
const selfDefenseE2EKey = "self-defense-e2e-key-0001"

// selfDefenseE2EConfig builds a real standard data-plane configuration: the
// local API-key auth chain, ingress self-defense with a small auth-failure
// threshold, and metrics either on or off.
func selfDefenseE2EConfig(metricsEnabled bool, authFailures int) *config.Config {
	cfg := processServicesTestConfig()
	cfg.Observability.Metrics.Enabled = metricsEnabled
	cfg.Server.Address = "127.0.0.1:8080"
	cfg.Access.SelfDefense.Adaptive.AuthFailures = new(authFailures)
	// The auth-event sink is disabled so the captured log proves the
	// self-defense path itself is silent, not that an unrelated sink is quiet.
	cfg.Auth = config.AuthConfig{
		Handler:            "local_api_key",
		RequiredLevel:      "api_key",
		EventFailurePolicy: "best_effort",
		EventDelivery:      "disabled",
		LocalAPIKeys: []config.AuthLocalAPIKeyRecord{{
			KeyID:       "self-defense-e2e",
			PrincipalID: "self-defense-e2e-user",
			Key:         selfDefenseE2EKey,
		}},
	}
	return cfg
}

// selfDefenseE2EStack is one fully composed standard HTTP data-plane stack: the
// real stdhttp composition, the real transport-auth chain, the real
// process-owned adaptive state, the real Prometheus bundle, and a capture of
// every log record.
type selfDefenseE2EStack struct {
	handler  http.Handler
	ps       *runtimebundle.ProcessServices
	observer httpcontract.SelfDefenseObserver
	logs     *bytes.Buffer
}

func newSelfDefenseE2EStack(t *testing.T, cfg *config.Config) selfDefenseE2EStack {
	t.Helper()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	opts := &runtimebundle.BuildOptions{PluginRegistry: pluginreg.NewRegistry()}
	if err := standardplugins.InstallStandardBundleOn(opts.PluginRegistry, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg:  cfg,
		Log:  log,
		Opts: opts,
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("NewProcessServices: %v", err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileCandidate: %v", err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	input := cand.StandardHTTPInput(cfg, nil, "")
	gen, err := runtimebundle.CompileGeneration(context.Background(), runtimebundle.GenerationCompileInput{
		Process: ps, Candidate: cfg, Compose: stdhttp.ComposeStandardHTTP,
	})
	if err != nil {
		t.Fatalf("CompileGeneration: %v", err)
	}
	t.Cleanup(func() { _ = gen.Close() })
	if gen.Handler() == nil {
		t.Fatal("the composed generation must publish a request handler")
	}
	return selfDefenseE2EStack{
		handler:  gen.Handler(),
		ps:       ps,
		observer: input.Security.SelfDefense.Observer,
		logs:     logs,
	}
}

func selfDefenseServe(handler http.Handler, target, remote, apiKey string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://example.test"+target, nil)
	req.RemoteAddr = remote
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// selfDefenseSeries returns the self-defense exposition keyed by
// "name{reason=...}"; the label-free entry gauge is keyed by its bare name.
func selfDefenseSeries(t *testing.T, ps *runtimebundle.ProcessServices) map[string]float64 {
	t.Helper()
	if ps.Metrics == nil {
		return nil
	}
	families, err := ps.Metrics.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	for _, f := range families {
		if !strings.HasPrefix(f.GetName(), "aip_self_defense_") {
			continue
		}
		for _, m := range f.GetMetric() {
			key := f.GetName()
			if len(m.GetLabel()) == 1 {
				key += "{" + m.GetLabel()[0].GetName() + "=" + m.GetLabel()[0].GetValue() + "}"
			}
			switch {
			case m.GetCounter() != nil:
				out[key] = m.GetCounter().GetValue()
			case m.GetGauge() != nil:
				out[key] = m.GetGauge().GetValue()
			}
		}
	}
	return out
}

// TestSelfDefenseDenialsAndTransitionsReachTheProcessMetrics is the end-to-end
// evidence that the previously nil generation projection observer is now wired:
// real requests through the real data plane move the real Prometheus series, and
// the entry gauge follows the process-owned adaptive state.
func TestSelfDefenseDenialsAndTransitionsReachTheProcessMetrics(t *testing.T) {
	t.Parallel()

	stack := newSelfDefenseE2EStack(t, selfDefenseE2EConfig(true, 2))
	if stack.ps.Metrics == nil || stack.ps.Metrics.SelfDefense == nil {
		t.Fatal("the fixture must run with metrics enabled")
	}
	if stack.observer != stack.ps.Metrics.SelfDefense {
		t.Fatalf("projected observer = %p, want the process collector %p", stack.observer, stack.ps.Metrics.SelfDefense)
	}

	// A matched impossible path is refused before authentication, records one
	// denial and one quarantine transition, and creates one state entry.
	if rec := selfDefenseServe(stack.handler, "/.env", "203.0.113.5:443", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("impossible path status = %d, want the generic 404", rec.Code)
	}
	series := selfDefenseSeries(t, stack.ps)
	if got := series["aip_self_defense_denials_total{reason=impossible_path}"]; got != 1 {
		t.Fatalf("impossible-path denials = %v, want 1 (series %v)", got, series)
	}
	if got := series["aip_self_defense_quarantine_transitions_total{reason=impossible_path}"]; got != 1 {
		t.Fatalf("impossible-path quarantine transitions = %v, want 1 (series %v)", got, series)
	}
	if got := series["aip_self_defense_state_entries"]; got != 1 {
		t.Fatalf("entry gauge = %v, want 1 (series %v)", got, series)
	}

	// A second probe from the same address escalates the quarantine, so the next
	// credential-free request is refused with the generic 429.
	if rec := selfDefenseServe(stack.handler, "/.env", "203.0.113.5:443", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("second impossible path status = %d, want the generic 404", rec.Code)
	}
	if rec := selfDefenseServe(stack.handler, "/v1/models", "203.0.113.5:443", ""); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("quarantined credential-free status = %d, want the generic 429", rec.Code)
	}
	series = selfDefenseSeries(t, stack.ps)
	if got := series["aip_self_defense_denials_total{reason=active_quarantine}"]; got != 1 {
		t.Fatalf("active-quarantine denials = %v, want 1 (series %v)", got, series)
	}
	if got := series["aip_self_defense_quarantine_transitions_total{reason=impossible_path}"]; got != 2 {
		t.Fatalf("escalated quarantine transitions = %v, want 2 (series %v)", got, series)
	}

	// A valid credential on a quarantined address still reaches authentication and
	// resets the adaptive state, which the scrape-driven gauge reflects.
	rec := selfDefenseServe(stack.handler, "/v1/models", "203.0.113.5:443", selfDefenseE2EKey)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusTooManyRequests {
		t.Fatalf("valid credential on a quarantined address was refused with %d", rec.Code)
	}
	if got := selfDefenseSeries(t, stack.ps)["aip_self_defense_state_entries"]; got != 0 {
		t.Fatalf("entry gauge after a successful full auth chain = %v, want 0", got)
	}
	if got := stack.ps.IngressDefense.Len(); got != 0 {
		t.Fatalf("adaptive state after a successful full auth chain = %d entries, want 0", got)
	}

	// Repeated unauthenticated failures from a fresh address reach the
	// auth-failure threshold and record the finite threshold transition.
	for range 2 {
		if rec := selfDefenseServe(stack.handler, "/v1/models", "198.51.100.9:443", "wrong-key-value"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("invalid credential status = %d, want the auth chain's 401", rec.Code)
		}
	}
	series = selfDefenseSeries(t, stack.ps)
	if got := series["aip_self_defense_quarantine_transitions_total{reason=auth_failure_threshold}"]; got != 1 {
		t.Fatalf("auth-failure quarantine transitions = %v, want 1 (series %v)", got, series)
	}

	// Requirement 9.5: no per-request hostile-event logging. The captured log
	// must not mention any hostile marker this run produced.
	logged := stack.logs.String()
	for _, marker := range []string{"/.env", "203.0.113.5", "198.51.100.9", "wrong-key-value", selfDefenseE2EKey} {
		if strings.Contains(logged, marker) {
			t.Fatalf("self-defense logged hostile request material %q at debug level:\n%s", marker, logged)
		}
	}
}

// selfDefenseDecisionRun is the observable security outcome of one identical
// request sequence, used to compare a metrics-enabled and a metrics-disabled
// process.
type selfDefenseDecisionRun struct {
	impossiblePath int
	escalation     int
	quarantine     int
	invalidAuth    int
	validAuth      int
	entries        int
	observerNil    bool
	registryNil    bool
}

// runSelfDefenseSequence drives the same hostile sequence against a stack and
// records every response status and the final adaptive state.
func runSelfDefenseSequence(t *testing.T, stack selfDefenseE2EStack) selfDefenseDecisionRun {
	t.Helper()
	run := selfDefenseDecisionRun{
		observerNil: stack.observer == nil,
		registryNil: stack.ps.Metrics == nil,
	}
	if rec := selfDefenseServe(stack.handler, "/.env", "192.0.2.5:443", ""); rec.Code == http.StatusNotFound {
		run.impossiblePath = rec.Code
	}
	if rec := selfDefenseServe(stack.handler, "/.env", "192.0.2.5:443", ""); rec.Code == http.StatusNotFound {
		run.escalation = rec.Code
	}
	if rec := selfDefenseServe(stack.handler, "/v1/models", "192.0.2.5:443", ""); rec.Code == http.StatusTooManyRequests {
		run.quarantine = rec.Code
	}
	if rec := selfDefenseServe(stack.handler, "/v1/models", "192.0.2.6:443", "wrong-key-value"); rec.Code == http.StatusUnauthorized {
		run.invalidAuth = rec.Code
	}
	if rec := selfDefenseServe(stack.handler, "/v1/models", "192.0.2.6:443", selfDefenseE2EKey); rec.Code > 0 {
		run.validAuth = rec.Code
	}
	run.entries = stack.ps.IngressDefense.Len()
	return run
}

// TestSelfDefenseMetricsAbsenceCannotChangeADecision proves the design rule that
// metrics are non-authoritative. With observability.metrics disabled the process
// owns no registry and the generation projects no observer at all, yet every
// response status and the resulting adaptive state are identical to the
// metrics-enabled run.
func TestSelfDefenseMetricsAbsenceCannotChangeADecision(t *testing.T) {
	t.Parallel()

	withMetrics := runSelfDefenseSequence(t, newSelfDefenseE2EStack(t, selfDefenseE2EConfig(true, 2)))
	withoutMetrics := runSelfDefenseSequence(t, newSelfDefenseE2EStack(t, selfDefenseE2EConfig(false, 2)))

	if withMetrics.observerNil {
		t.Fatal("a metrics-enabled generation must project the bounded self-defense observer")
	}
	if withMetrics.registryNil {
		t.Fatal("a metrics-enabled process must own the registry")
	}
	if !withoutMetrics.observerNil {
		t.Fatal("a metrics-disabled generation must project no observer")
	}
	if !withoutMetrics.registryNil {
		t.Fatal("a metrics-disabled process must own no metrics bundle at all")
	}
	if withoutMetrics.impossiblePath != http.StatusNotFound ||
		withoutMetrics.escalation != http.StatusNotFound ||
		withoutMetrics.quarantine != http.StatusTooManyRequests ||
		withoutMetrics.invalidAuth != http.StatusUnauthorized {
		t.Fatalf("metrics-disabled run did not enforce self-defense: %+v", withoutMetrics)
	}
	if withoutMetrics.validAuth == http.StatusUnauthorized || withoutMetrics.validAuth == http.StatusTooManyRequests {
		t.Fatalf("valid credential was refused without metrics: %+v", withoutMetrics)
	}
	if withoutMetrics.entries == 0 {
		t.Fatal("the fixture must leave adaptive state so the comparison is meaningful")
	}
	// Compare only the security decision and state fields; the observability
	// wiring itself is expected to differ.
	withoutMetrics.observerNil, withoutMetrics.registryNil = withMetrics.observerNil, withMetrics.registryNil
	if withoutMetrics != withMetrics {
		t.Fatalf("metrics presence changed a security outcome:\nwithout metrics = %+v\nwith metrics    = %+v", withoutMetrics, withMetrics)
	}
}

// TestSelfDefenseProjectedObserverIsTheProcessCollector pins the projection
// identity: every generation borrows the one process-owned collector, so several
// generations never open duplicate series.
func TestSelfDefenseProjectedObserverIsTheProcessCollector(t *testing.T) {
	t.Parallel()

	ps, input := selfDefenseGenerationInput(t, selfDefenseConfigWith(nil))
	if input.Security.SelfDefense.Observer == nil {
		t.Fatal("the metrics-enabled fixture must project the bounded observer")
	}
	if ps.Metrics == nil || ps.Metrics.SelfDefense == nil {
		t.Fatal("the process must own the self-defense collector")
	}
	var typed httpcontract.SelfDefenseObserver = ps.Metrics.SelfDefense
	if input.Security.SelfDefense.Observer != typed {
		t.Fatalf("projected observer = %p, want the process collector %p", input.Security.SelfDefense.Observer, typed)
	}
}
