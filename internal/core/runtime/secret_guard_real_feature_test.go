package runtime_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/featurebundle"
	"github.com/aiproxer/aiproxer/internal/infra/metrics"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/standardplugins"
	"github.com/aiproxer/aiproxer/internal/standardplugins/featurehost"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
	sdktraffic "github.com/aiproxer/aiproxer/pkg/aipsdk/traffic"
	dto "github.com/prometheus/client_model/go"
	"gopkg.in/yaml.v3"
)

type realSecretGuardHarness struct {
	metrics      *metrics.Bundle
	memSS        *memory.Store
	mgr          *app.Manager
	exec         *runtime.Executor
	call         *aipapi.Call
	before       aipapi.Call
	backendOpens atomic.Int32
	trafficCalls atomic.Int32
	auditCalls   atomic.Int32
	auditMu      sync.Mutex
	auditEvents  []sdk.DecisionEvent
	ownerID      string
}

type secretGuardEnv map[string]string

func (e secretGuardEnv) Lookup(name string) (string, bool) {
	v, ok := e[name]
	return v, ok
}

func (e secretGuardEnv) Snapshot() []string {
	out := make([]string, 0, len(e))
	for k, v := range e {
		out = append(out, k+"="+v)
	}
	return out
}

func TestExecutor_realSecretGuardScanLimitBlockAndRedactQuarantine(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"block", "redact"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			h := newRealSecretGuardHarness(t, action, "user-real-"+action)
			ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: h.ownerID})
			expectedAction := "block"
			if action == "log" {
				expectedAction = "log"
			}
			stream, execErr := h.exec.Execute(ctx, h.call)
			if stream != nil {
				_, _ = aipapi.Collect(t.Context(), stream)
			}
			if execErr == nil {
				t.Fatal("expected policy denial")
			}
			if !aipapi.IsPolicyDenied(execErr) {
				t.Fatalf("want policy denied, got %v", execErr)
			}
			if h.backendOpens.Load() != 0 {
				t.Fatalf("backend opened %d times want 0", h.backendOpens.Load())
			}
			if h.trafficCalls.Load() != 0 {
				t.Fatalf("traffic observer called %d times want 0", h.trafficCalls.Load())
			}
			if h.auditCalls.Load() != 1 {
				t.Fatalf("audit calls=%d want 1", h.auditCalls.Load())
			}
			assertAuditEvent(t, h.singleAuditEvent(), expectedAction, sdk.OutcomeBlock, sdk.QuarantineResultCommitted, false)
			if !strings.Contains(execErr.Error(), "start a new session") {
				t.Fatalf("client-safe denial missing session guidance: %v", execErr)
			}
			if strings.Contains(execErr.Error(), testkit.SyntheticOpenAIAPIKey) {
				t.Fatal("client denial must not leak secret value")
			}
			if !reflect.DeepEqual(h.call.Messages, h.before.Messages) {
				t.Fatal("scan-limit block path must leave the message payload unchanged")
			}
			sessionID := latestStoredSessionID(ctx, t, h.memSS, h.ownerID)
			assertStoredSessionQuarantined(ctx, t, h.memSS, h.mgr, sessionID)
			assertRealSecretGuardMetrics(t, h.metrics, action)
		})
	}
}

func TestExecutor_realSecretGuardLogScanLimitContinues(t *testing.T) {
	t.Parallel()
	h := newRealSecretGuardHarness(t, "log", "user-real-log")
	ctx := execview.WithPrincipal(t.Context(), execview.PrincipalView{ID: h.ownerID})
	stream, execErr := h.exec.Execute(ctx, h.call)
	if execErr != nil {
		t.Fatal(execErr)
	}
	if stream == nil {
		t.Fatal("expected backend stream on log scan-limit")
	}
	if _, err := aipapi.Collect(t.Context(), stream); err != nil {
		t.Fatal(err)
	}
	if h.backendOpens.Load() != 1 {
		t.Fatalf("backend opened %d times want 1", h.backendOpens.Load())
	}
	if h.trafficCalls.Load() == 0 {
		t.Fatal("traffic observer must run on log scan-limit")
	}
	if h.auditCalls.Load() != 1 {
		t.Fatalf("audit calls=%d want 1", h.auditCalls.Load())
	}
	assertAuditEvent(t, h.singleAuditEvent(), "log", sdk.OutcomeLog, sdk.QuarantineResultNA, false)
	if !reflect.DeepEqual(h.call.Messages, h.before.Messages) {
		t.Fatal("log scan-limit must not mutate the message payload")
	}
	sessionID := latestStoredSessionID(ctx, t, h.memSS, h.ownerID)
	assertStoredSessionActive(ctx, t, h.memSS, h.mgr, sessionID)
	assertRealSecretGuardMetrics(t, h.metrics, "log")
}

func newRealSecretGuardHarness(t *testing.T, action, ownerID string) *realSecretGuardHarness {
	t.Helper()
	secret := testkit.SyntheticOpenAIAPIKey
	first := "token=" + secret
	scanMaxBytes := len(first) + 1
	h := &realSecretGuardHarness{ownerID: ownerID}

	reg := pluginreg.NewRegistry()
	if err := standardplugins.InstallStandardBundleOn(reg, standardplugins.UpstreamAPIKeys{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Access:     config.AccessConfig{Mode: "single_user"},
		Server:     config.ServerConfig{Address: "127.0.0.1:8080", AuthMode: config.AuthModeNoAuth},
		Routing:    config.RoutingConfig{MaxAttempts: 3},
		Continuity: config.ContinuityConfig{InMemory: true},
		Observability: config.ObservabilityConfig{
			Metrics: config.MetricsConfig{Enabled: true, Path: "/metrics"},
		},
		SecureSession: config.SecureSessionConfig{
			Enabled: new(true),
			Store:   "memory",
		},
		Plugins: config.PluginsConfig{
			Backends: []config.PluginConfig{{
				Kind:    "openai-responses",
				ID:      "openai-only",
				Enabled: true,
				Config:  mustYAMLNode(t, openAIBackendYAML()),
			}},
			Features: []config.PluginConfig{{
				Kind:    "secrets-guard",
				ID:      "secrets-guard",
				Enabled: true,
				Config: mustYAMLNode(t, fmt.Sprintf(
					"action: %s\naudit_failure_policy: fail_closed\nscan_max_bytes: %d\n",
					action, scanMaxBytes,
				)),
			}},
		},
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatal(err)
	}

	bus := hooks.New(hooks.Config{})
	decisionObs := sdk.ObserverFunc(func(_ context.Context, ev sdk.DecisionEvent) error {
		h.auditCalls.Add(1)
		h.auditMu.Lock()
		h.auditEvents = append(h.auditEvents, ev)
		h.auditMu.Unlock()
		return nil
	})
	secretEnv := secretGuardEnv{
		"OPENAI_API_KEY": secret,
	}
	ps, err := runtimebundle.NewProcessServices(context.Background(), runtimebundle.ProcessServicesInput{
		Cfg: cfg,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		// Env-only credential provisioning (tasks 3/6): the openai-responses
		// backend YAML carries no secret material; the per-candidate
		// snapshot resolves OPENAI_API_KEY from this host environment.
		HostEnv: secretEnv,
		Opts: &runtimebundle.BuildOptions{
			PluginRegistry: reg,
			Production: runtimebundle.ProductionOptions{
				TrafficObservers: []sdktraffic.Observer{&countingTrafficObs{n: &h.trafficCalls}},
				FeatureHostRegistrations: []featurehost.Registration{
					(&secretguardhost.Binding{Environment: secretEnv}).Registration(),
				},
			},
		},
		Tracing: runtimebundle.ProcessTracing{
			Shutdown: func(context.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })

	candOpts := &runtimebundle.BuildOptions{PluginRegistry: reg}
	if ps.StandardFeatures != nil && cfg != nil {
		accessMode, _ := cfg.EffectiveAccessMode()
		host := featurebundle.HostContributions{
			TrafficObservers: []sdktraffic.Observer{&countingTrafficObs{n: &h.trafficCalls}},
		}
		genMerged, err := featurebundle.MergeFeatureSurfacesWithHost(reg, config.RegistrationsFromConfig(cfg), host)
		if err != nil {
			t.Fatal(err)
		}
		featOut, err := ps.StandardFeatures.CompileGeneration(context.Background(), featurehost.GenerationInput{
			Registrations:     config.RegistrationsFromConfig(cfg),
			HostRegistrations: []featurehost.Registration{(&secretguardhost.Binding{Environment: secretEnv}).Registration()},
			MergeSurface:      genMerged,
			Planes:            genMerged.Frozen,
			Lifecycles:        genMerged.Lifecycles,
			AccessMode:        accessMode,
			DecisionObserver:  decisionObs,
		})
		if err != nil {
			t.Fatal(err)
		}
		candOpts.FeaturePlanes = featOut.Planes
		candOpts.FeatureLifecycles = featOut.Lifecycles
		candOpts.ReplaceCandidateSurface = true
	}

	cand, err := runtimebundle.CompileCandidate(context.Background(), runtimebundle.GenerationCompileInput{
		Process:       ps,
		Bus:           bus,
		Candidate:     cfg,
		CandidateOpts: candOpts,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cand.Close() })
	if cand.RuntimeSnapshot() == nil || cand.Metrics() == nil {
		t.Fatal("CompileCandidate must return runtime snapshot and metrics")
	}

	fingerprintKey := secretGuardFingerprintKey(t)
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fingerprintKey), b2bualineage.New(cand.Store()), app.ManagerConfig{
		FingerprintKey: fingerprintKey,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatal(err)
	}

	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "openai-only:gpt-4"},
		Messages: []aipapi.Message{
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(first)},
			},
			{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(strings.Repeat("z", 16))},
			},
		},
	}

	h.metrics = cand.Metrics()
	h.memSS = memSS
	h.mgr = mgr
	h.exec = runtime.TestExecutor()
	h.exec.SessionDenialMapper = aipapidenial.MapToSessionDenial
	h.exec.Store = cand.Store()
	h.exec.Bus = bus
	h.exec.SecureSession = mgr
	h.exec.SecretGuardDecisionMetrics = cand.Metrics().SecretGuardDecisionSink()
	h.exec.ExtensionMetrics = cand.Metrics().ExtensionStageSink()
	h.exec.Now = func() time.Time { return time.Unix(2500, 0).UTC() }
	h.exec.Rand = routing.NewSeededRng(1)
	h.exec.RuntimeSnapshot = cand.RuntimeSnapshot()
	h.exec.Backends = map[string]execbackend.Backend{
		"openai-only": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				h.backendOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	h.call = call
	h.before = aipapi.CloneCall(*call)
	return h
}

func (h *realSecretGuardHarness) singleAuditEvent() sdk.DecisionEvent {
	h.auditMu.Lock()
	defer h.auditMu.Unlock()
	if len(h.auditEvents) != 1 {
		return sdk.DecisionEvent{}
	}
	return h.auditEvents[0]
}

func assertRealSecretGuardMetrics(t *testing.T, metricsBundle *metrics.Bundle, action string) {
	t.Helper()
	if metricsBundle == nil {
		t.Fatal("missing metrics bundle")
	}
	families, err := metricsBundle.Registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	metricAction := "block"
	if action == "log" {
		metricAction = "log"
	}
	assertCounterMetric(t, families, "aip_secret_guard_decisions_total", map[string]string{
		"action":          metricAction,
		"outcome":         metricAction,
		"source_category": "proxy_env",
	}, 1)
	assertCounterMetric(t, families, "aip_secret_guard_scan_limits_total", map[string]string{
		"action":          metricAction,
		"outcome":         "scan_limit",
		"source_category": "proxy_env",
	}, 1)
	if action == "log" {
		assertCounterMetric(t, families, "aip_secret_guard_quarantines_total", map[string]string{
			"action":          "block",
			"outcome":         "block",
			"source_category": "proxy_env",
		}, 0)
		return
	}
	assertCounterMetric(t, families, "aip_secret_guard_quarantines_total", map[string]string{
		"action":          "block",
		"outcome":         "block",
		"source_category": "proxy_env",
	}, 1)
}

func assertAuditEvent(t *testing.T, ev sdk.DecisionEvent, action string, outcome sdk.Outcome, quarantineResult string, backendDispatched bool) {
	t.Helper()
	if ev.Action != action {
		t.Fatalf("Action=%q want %q", ev.Action, action)
	}
	if ev.Outcome != outcome {
		t.Fatalf("Outcome=%q want %q", ev.Outcome, outcome)
	}
	if !ev.ScanLimitHit {
		t.Fatal("ScanLimitHit want true")
	}
	if ev.QuarantineResult != quarantineResult {
		t.Fatalf("QuarantineResult=%q want %q", ev.QuarantineResult, quarantineResult)
	}
	if ev.BackendDispatched != backendDispatched {
		t.Fatalf("BackendDispatched=%v want %v", ev.BackendDispatched, backendDispatched)
	}
}

func latestStoredSessionID(ctx context.Context, t *testing.T, store *memory.Store, ownerID string) string {
	t.Helper()
	sums, err := store.Summary(ctx, domain.SummaryQuery{OwnerID: ownerID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) == 0 {
		sums, err = store.Summary(ctx, domain.SummaryQuery{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(sums) == 0 {
		t.Fatal("expected session summary")
	}
	return string(sums[0].SessionID)
}

func assertStoredSessionQuarantined(ctx context.Context, t *testing.T, store *memory.Store, mgr *app.Manager, sessionID string) {
	t.Helper()
	if strings.TrimSpace(sessionID) == "" {
		t.Fatal("missing authoritative session id")
	}
	rec, err := store.LoadByID(ctx, domain.SessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Status.IsQuarantined() {
		t.Fatalf("status=%q want quarantined", rec.Status)
	}
	if rec.ResumeEligible {
		t.Fatal("resume eligible must be false for quarantined session")
	}
	if err := mgr.AssertActive(ctx, domain.SessionID(sessionID)); !errors.Is(err, domain.ErrSessionQuarantined) {
		t.Fatalf("AssertActive returned %v want ErrSessionQuarantined", err)
	}
}

func assertStoredSessionActive(ctx context.Context, t *testing.T, store *memory.Store, mgr *app.Manager, sessionID string) {
	t.Helper()
	if strings.TrimSpace(sessionID) == "" {
		t.Fatal("missing authoritative session id")
	}
	rec, err := store.LoadByID(ctx, domain.SessionID(sessionID))
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Status.IsActive() {
		t.Fatalf("status=%q want active", rec.Status)
	}
	if rec.Status.IsQuarantined() {
		t.Fatal("active session must not be quarantined")
	}
	if err := mgr.AssertActive(ctx, domain.SessionID(sessionID)); err != nil {
		t.Fatalf("AssertActive returned %v want nil", err)
	}
}

func assertCounterMetric(t *testing.T, families []*dto.MetricFamily, name string, wantLabels map[string]string, want float64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if !labelsMatch(metric.GetLabel(), wantLabels) {
				continue
			}
			if got := metric.GetCounter().GetValue(); got != want {
				t.Fatalf("%s=%v want %v for labels %v", name, got, want, wantLabels)
			}
			return
		}
	}
	if want == 0 {
		return
	}
	t.Fatalf("metric %s with labels %v not found", name, wantLabels)
}

func labelsMatch(labels []*dto.LabelPair, want map[string]string) bool {
	if len(labels) != len(want) {
		return false
	}
	for _, label := range labels {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}

func openAIBackendYAML() string {
	return `models:
  source: inline
  items:
    - canonical_id: openai/test-model
      native_id: test-model
      display_name: Test Model
`
}

func mustYAMLNode(t *testing.T, raw string) yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	return n
}
