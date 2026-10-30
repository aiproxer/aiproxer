package runtimebundle_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/pluginreg"
	"github.com/aiproxer/aiproxer/internal/stdhttp"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"gopkg.in/yaml.v3"
)

// recordingUsageLimitPort is the task 5.3 runtime-host fake: it satisfies the
// four-method usage-limit port (optionally the full set of optional host
// surfaces) and records attach and operation calls so the tests pin the
// borrowed-attachment contract without any real authority store or service.
type recordingUsageLimitPort struct {
	full bool // implements Status/Limits/Decisions and the setters

	mu                                 sync.Mutex
	admit, settle, release, applyUsage int
	stageMetrics                       int
	evidenceSink                       int
	sink                               authority.EvidenceSink
	closed                             bool
	closeCalls                         int
}

func (p *recordingUsageLimitPort) record(op string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch op {
	case "admit":
		p.admit++
	case "settle":
		p.settle++
	case "release":
		p.release++
	case "apply":
		p.applyUsage++
	}
}

func (p *recordingUsageLimitPort) counts() (admit, settle, release, apply int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.admit, p.settle, p.release, p.applyUsage
}

func (p *recordingUsageLimitPort) attachCounts() (stage, evidence int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stageMetrics, p.evidenceSink
}

func (p *recordingUsageLimitPort) attachedSink() authority.EvidenceSink {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sink
}

func (p *recordingUsageLimitPort) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// Close exists to prove the runtime never closes the borrowed port.
func (p *recordingUsageLimitPort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.closeCalls++
	return nil
}

func (p *recordingUsageLimitPort) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	p.record("admit")
	// Mirror a matched strict rule: the reservation listing is what makes the
	// runtime track the hold and settle it on completion.
	reservation := usagelimit.AdmissionReservation{
		ReservationID:  "fake-reservation-1",
		RuleID:         "tenant.requests",
		ReservedAmount: usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 1},
	}
	return usagelimit.AdmissionResult{
		Allowed:        true,
		Outcome:        usagelimit.DecisionOutcomeAllow,
		Reserved:       true,
		ReservationID:  reservation.ReservationID,
		SelectedRuleID: reservation.RuleID,
		ReservedAmount: reservation.ReservedAmount,
		Reservations:   []usagelimit.AdmissionReservation{reservation},
	}, nil
}

func (p *recordingUsageLimitPort) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	p.record("settle")
	return usagelimit.SettleResult{}, nil
}

func (p *recordingUsageLimitPort) Release(context.Context, usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	p.record("release")
	return usagelimit.ReleaseResult{}, nil
}

func (p *recordingUsageLimitPort) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	p.record("apply")
	return usagelimit.ApplyUsageResult{}, nil
}

func (p *recordingUsageLimitPort) SetStageMetrics(usagelimit.StageMetrics) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stageMetrics++
}

func (p *recordingUsageLimitPort) SetEvidenceSink(sink authority.EvidenceSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evidenceSink++
	p.sink = sink
}

func (p *recordingUsageLimitPort) Status(context.Context) (cp.AccountingAuthorityStatus, error) {
	return cp.AccountingAuthorityStatus{State: cp.AccountingAuthorityReady}, nil
}

func (p *recordingUsageLimitPort) Limits(context.Context, cp.AccountingLimitStatusQuery) (usagelimit.LimitStatusResult, error) {
	return usagelimit.LimitStatusResult{State: usagelimit.QueryStateReady}, nil
}

func (p *recordingUsageLimitPort) Decisions(context.Context, cp.AccountingDecisionQuery) (usagelimit.DecisionHistoryResult, error) {
	return usagelimit.DecisionHistoryResult{State: usagelimit.QueryStateReady}, nil
}

var (
	_ usagelimit.Authority = (*recordingUsageLimitPort)(nil)
	_ io.Closer            = (*recordingUsageLimitPort)(nil)
)

// evidenceOnlyUsageLimitPort implements the four-method port plus only the
// optional evidence setter, proving a port without stage/query surfaces is
// still attachable and usable.
type evidenceOnlyUsageLimitPort struct {
	mu     sync.Mutex
	stage  int
	evid   int
	sink   authority.EvidenceSink
	counts map[string]int
}

func (p *evidenceOnlyUsageLimitPort) SetEvidenceSink(sink authority.EvidenceSink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.evid++
	p.sink = sink
}

func (p *evidenceOnlyUsageLimitPort) attachedSink() authority.EvidenceSink {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sink
}

func (p *evidenceOnlyUsageLimitPort) attachCounts() (stage, evidence int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stage, p.evid
}

func (p *evidenceOnlyUsageLimitPort) Admit(context.Context, usagelimit.AdmissionInput) (usagelimit.AdmissionResult, error) {
	return usagelimit.AdmissionResult{Allowed: true, Outcome: usagelimit.DecisionOutcomeAllow}, nil
}

func (p *evidenceOnlyUsageLimitPort) Settle(context.Context, usagelimit.SettleInput) (usagelimit.SettleResult, error) {
	return usagelimit.SettleResult{}, nil
}

func (p *evidenceOnlyUsageLimitPort) Release(context.Context, usagelimit.ReleaseInput) (usagelimit.ReleaseResult, error) {
	return usagelimit.ReleaseResult{}, nil
}

func (p *evidenceOnlyUsageLimitPort) ApplyUsage(context.Context, usagelimit.ApplyUsageCommand) (usagelimit.ApplyUsageResult, error) {
	return usagelimit.ApplyUsageResult{}, nil
}

var _ usagelimit.Authority = (*evidenceOnlyUsageLimitPort)(nil)

// recordingEvidenceSink records evidence writes for the caller-owned sink
// lifetime assertions.
type recordingEvidenceSink struct {
	mu    sync.Mutex
	wrote int
}

func (s *recordingEvidenceSink) RecordPolicyDecision(context.Context, policydecision.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote++
	return nil
}

func (s *recordingEvidenceSink) RecordAccountingAuthority(context.Context, cp.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wrote++
	return nil
}

func (s *recordingEvidenceSink) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wrote
}

// TestUsageLimitPortOnlyAuthority drives one request and its terminal
// completion through a host built with rules configured but only an injected
// port (task 5.3 B): the runtime consumes the port, and a configured sqlite
// authority store path is never opened or migrated because no built-in
// authority composition exists any more.
func TestUsageLimitPortOnlyAuthority(t *testing.T) {
	t.Parallel()
	port := &recordingUsageLimitPort{}
	cfg := baseAuthorityConfig(true, "fail_closed")
	var empty yaml.Node
	if err := yaml.Unmarshal([]byte("{}"), &empty); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Backends = []config.PluginConfig{{Kind: "stub", ID: "stub", Enabled: true, Config: empty}}
	storePath := filepath.Join(t.TempDir(), "authority.db")
	cfg.Accounting.Authority.Store = "sqlite"
	cfg.Accounting.Authority.SQLitePath = storePath
	reg := pluginreg.NewRegistry()
	registerUsageLimitStubBackend(t, reg)
	opts := &runtimebundle.BuildOptions{
		PluginRegistry: reg,
		Production:     runtimebundle.ProductionOptions{UsageLimits: port},
	}
	_, cand := mustProcessAndCandidate(t, cfg, opts)

	stream, err := cand.Executor().Execute(context.Background(), &aipapi.Call{
		ID:       "usage-limit-port-only-1",
		Route:    aipapi.RouteIntent{Selector: "stub:model"},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("drive the injected port")}}},
	})
	if err != nil {
		t.Fatalf("Execute through the injected port: %v", err)
	}
	if _, err := aipapi.Collect(context.Background(), stream); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	admit, settle, release, apply := port.counts()
	if admit < 1 {
		t.Fatal("injected port never received Admit")
	}
	if settle < 1 {
		t.Fatalf("completed request did not settle through the injected port (admit=%d settle=%d release=%d apply=%d)", admit, settle, release, apply)
	}
	if _, err := os.Stat(storePath); err == nil {
		t.Fatalf("configured authority sqlite store %s was created: no built-in authority store may remain", storePath)
	}
	if port.isClosed() {
		t.Fatal("runtime closed the borrowed port")
	}
}

// TestUsageLimitPortCapturedOnceAcrossReload proves task 5.3 C: BuildHost
// captures one injected port instance, attaches the optional surfaces once at
// process build, and every reloaded generation consumes the same instance.
func TestUsageLimitPortCapturedOnceAcrossReload(t *testing.T) {
	t.Parallel()
	upstream := usageLimitUpstream(t)
	port := &recordingUsageLimitPort{full: true}
	configPath := writeUsageLimitHostConfig(t, usageLimitHostConfig(upstream.URL, "^alias-chat$"))

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:             configPath,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		LogWriter:              io.Discard,
		HandlerComposer:        stdhttp.ComposeStandardHTTP,
		EnterpriseCapabilities: config.CapabilitySet{config.CapabilityUsageLimits: true},
		Production: runtimebundle.ProductionOptions{
			UsageLimits:     port,
			PolicyObservers: []policydecision.Observer{&capturePolicyObserver{}},
		},
	})
	if err != nil {
		t.Fatalf("BuildHost with the injected port: %v", err)
	}
	t.Cleanup(func() { hostServeCleanup(t, host) })

	if got := hostActiveExecutor(t, host).UsageAuthority; got == nil {
		t.Fatal("compiled generation did not receive the injected port")
	} else if got != usagelimit.Authority(port) {
		t.Fatalf("generation port = %T, want the injected instance", got)
	}
	if stage, evidence := port.attachCounts(); stage != 1 || evidence != 1 {
		t.Fatalf("startup attach counts stage=%d evidence=%d, want exactly once each", stage, evidence)
	}

	serve := func(id string) {
		t.Helper()
		stream, err := host.ExecutorView().Execute(t.Context(), &aipapi.Call{
			ID:       id,
			Route:    aipapi.RouteIntent{Selector: "wire-backend:gpt-4o-mini"},
			Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}},
		})
		if err != nil {
			t.Fatalf("%s: Execute: %v", id, err)
		}
		if _, err := aipapi.Collect(t.Context(), stream); err != nil {
			t.Fatalf("%s: Collect: %v", id, err)
		}
	}
	serve("usage-limit-reload-1")
	_, settleBefore, _, _ := port.counts()
	if settleBefore < 1 {
		t.Fatalf("first served request did not settle through the injected port")
	}

	// Publish a real reload (a harmless candidate change) and serve again
	// against the new generation.
	if result := publishUsageLimitReload(t, host, configPath, usageLimitHostConfig(upstream.URL, "^alias-chat-v2$")); result.Category != sdkreload.ResultPublished {
		t.Fatalf("reload category=%q want published; reason=%q", result.Category, result.ReasonCategory)
	}
	serve("usage-limit-reload-2")

	if got := hostActiveExecutor(t, host).UsageAuthority; got != usagelimit.Authority(port) {
		t.Fatalf("reloaded generation port = %T, want the same captured instance", got)
	}
	if stage, evidence := port.attachCounts(); stage != 1 || evidence != 1 {
		t.Fatalf("reload re-attached surfaces: stage=%d evidence=%d, want exactly once each across the reload", stage, evidence)
	}
	admit, settle, _, _ := port.counts()
	if admit < 2 || settle < 2 {
		t.Fatalf("reloaded generation did not use the captured port: admit=%d settle=%d", admit, settle)
	}
}

// TestUsageLimitEvidenceFanOut proves task 5.3 F on the host boundary: a port
// implementing only SetEvidenceSink receives a non-nil host-built sink, and
// the sink fans a policy decision out to the production observer chain.
func TestUsageLimitEvidenceFanOut(t *testing.T) {
	t.Parallel()
	upstream := usageLimitUpstream(t)
	port := &evidenceOnlyUsageLimitPort{}
	observer := &capturePolicyObserver{}
	configPath := writeUsageLimitHostConfig(t, usageLimitHostConfig(upstream.URL, "^alias-chat$"))

	host, err := runtimebundle.BuildHost(t.Context(), runtimebundle.BuildHostInput{
		ConfigPath:             configPath,
		Mandatory:              aipsdk.StandardDistributionRequirements(),
		LogWriter:              io.Discard,
		HandlerComposer:        stdhttp.ComposeStandardHTTP,
		EnterpriseCapabilities: config.CapabilitySet{config.CapabilityUsageLimits: true},
		Production: runtimebundle.ProductionOptions{
			UsageLimits:     port,
			PolicyObservers: []policydecision.Observer{observer},
		},
	})
	if err != nil {
		t.Fatalf("BuildHost: %v", err)
	}
	t.Cleanup(func() { hostServeCleanup(t, host) })

	if stage, evidence := port.attachCounts(); stage != 0 || evidence != 1 {
		t.Fatalf("attach counts stage=%d evidence=%d, want only the evidence surface attached", stage, evidence)
	}
	sink := port.attachedSink()
	if sink == nil {
		t.Fatal("host did not attach an evidence sink to the SetEvidenceSink-only port")
	}
	if err := sink.RecordPolicyDecision(t.Context(), policydecision.Record{}); err != nil {
		t.Fatalf("RecordPolicyDecision: %v", err)
	}
	if got := len(observer.snapshot()); got != 1 {
		t.Fatalf("policy observer received %d records, want 1 through the host-built sink", got)
	}
}

// registerUsageLimitStubBackend registers a minimal in-process stub backend
// whose stream starts and finishes so the request lifecycle (admit → open →
// finish → settle) can run end to end without network I/O.
func registerUsageLimitStubBackend(t *testing.T, reg *pluginreg.Registry) {
	t.Helper()
	if err := reg.RegisterBackend("stub", func(yaml.Node, *http.Client, pluginreg.BackendFactoryDeps) (execbackend.Backend, error) {
		return execbackend.Backend{
			Caps:            aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			BackendPrefixes: []string{"stub"},
			ModelInventory:  testModelInventory(),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{{Kind: aipapi.EventResponseStarted}, {Kind: aipapi.EventResponseFinished}}), nil
			},
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
}

// usageLimitUpstream serves the minimal compatible-chat SSE stream the host
// request path needs.
func usageLimitUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"gpt-4o-mini"}]}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-ul\",\"object\":\"chat.completion.chunk\",\"model\":\"gpt-4o-mini\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server
}

// usageLimitHostConfig renders a standard-distribution configuration with the
// accounting-authority rules block and one local compatible backend.
func usageLimitHostConfig(upstreamURL, aliasPattern string) string {
	return fmt.Sprintf(`server:
  address: "127.0.0.1:0"
routing:
  max_attempts: 3
  default_route: "wire-backend:gpt-4o-mini"
model_aliases:
  - pattern: %q
    replacement: "wire-backend:gpt-4o-mini"
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
observability:
  metrics:
    enabled: true
hooks:
  tool_reactor_error_policy: fail_open
accounting:
  enabled: true
  mode: local_only
  authority:
    enabled: true
    mode: strict
    store: memory
    startup_posture: fail_closed
    rules:
      - id: "tenant.requests"
        kind: quota
        mode: strict
        unit: requests
        limit: 10
        basis: legacy_provider_preferred_attempt
plugins:
  frontends:
    - id: openai-responses
      enabled: true
      config: {}
    - id: openai-legacy
      enabled: true
      config: {}
    - id: anthropic
      enabled: true
      config: {}
    - id: gemini
      enabled: true
      config: {}
  backends:
    - id: openai-responses
      enabled: false
      config: {}
    - id: openai-legacy
      enabled: false
      config: {}
    - id: anthropic
      enabled: false
      config: {}
    - id: gemini
      enabled: false
      config: {}
    - id: bedrock
      enabled: false
      config: {}
    - id: wire-backend
      kind: custom-openai-legacy-compatible
      enabled: true
      config:
        backend_prefix: wire-backend
        base_url: %q
  features:
    - id: submit-noop
      enabled: true
      config: {}
    - id: parts-noop
      enabled: true
      config: {}
    - id: tool-reactor-noop
      enabled: true
      config: {}
`, aliasPattern, upstreamURL+"/v1")
}

func writeUsageLimitHostConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage-limit-host.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// publishUsageLimitReload writes the new body atomically and publishes it
// through the real host reload path.
func publishUsageLimitReload(t *testing.T, host *runtimebundle.Host, path, body string) sdkreload.Result {
	t.Helper()
	candidate := filepath.Join(t.TempDir(), "candidate.yaml")
	if err := os.WriteFile(candidate, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceTestFile(candidate, path); err != nil {
		t.Fatal(err)
	}
	return host.Reload(t.Context(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI, AcceptedAt: time.Now().UTC(), SafeActor: "usage-limit-test"})
}
