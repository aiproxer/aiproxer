// Package main is a separate-module enterprise compile/execution fixture
// (requirements 12.6, 12.9, 17.7). It imports only public OSS packages.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipruntime"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

const (
	enterpriseBackendID = "enterprise-local"
	enterpriseModelID   = "enterprise/stub-default"
	enterpriseRoute     = enterpriseBackendID + ":" + enterpriseModelID
	enterpriseStubText  = "[enterprise] essential stub"
)

type enterpriseMeter struct{}

func (enterpriseMeter) Append(context.Context, metering.Fact) error { return nil }

type enterpriseQuerier struct{}

func (enterpriseQuerier) List(context.Context, metering.Query) (metering.Page, error) {
	return metering.Page{}, nil
}

type enterpriseEvidence struct {
	policyN int
}

func (e *enterpriseEvidence) RecordPolicyDecision(context.Context, policydecision.Record) error {
	e.policyN++
	return nil
}

func (e *enterpriseEvidence) RecordAccountingAuthority(context.Context, controlplane.Event) error {
	return nil
}

type enterpriseRequestProvider struct{}

func (enterpriseRequestProvider) AdmitRequest(_ context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{
		Kind:       authority.DecisionAllow,
		ProviderID: "enterprise-request",
		Stage:      authority.StageRequestAdmit,
	}, nil
}

func (enterpriseRequestProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (enterpriseRequestProvider) ReleaseRequest(context.Context, authority.RequestRelease) error {
	return nil
}

type enterpriseRuleSource struct{}

func (enterpriseRuleSource) Snapshot(context.Context) (economics.Snapshot[economics.PolicyRulesView], error) {
	now := time.Now().UTC()
	return economics.Snapshot[economics.PolicyRulesView]{
		ID: "usage_authority", Version: "enterprise-fixture-v1", EffectiveAt: now, FetchedAt: now,
		State: economics.SnapshotReady,
		Value: economics.PolicyRulesView{Kind: economics.PolicyKindUsageAuthority},
	}, nil
}

// startEssentialStub serves a minimal OpenAI-compatible chat completions SSE
// endpoint so the fixture can exercise a public essential backend
// (custom-openai-legacy-compatible) without external connector modules.
func startEssentialStub() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"stub-default","owned_by":"enterprise-fixture"}]}`))
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-enterprise\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":null}]}\n\n", enterpriseStubText)
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-enterprise\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":7,\"total_tokens\":10}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

func writeEssentialConfig(baseURL string) (path string, cleanup func(), err error) {
	if p := strings.TrimSpace(os.Getenv("AIP_ENTERPRISE_CONFIG")); p != "" {
		return filepath.Clean(p), nil, nil
	}
	dir, err := os.MkdirTemp("", "aip-enterprise-module-*")
	if err != nil {
		return "", nil, err
	}
	body := fmt.Sprintf(`server:
  address: "127.0.0.1:18080"
routing:
  max_attempts: 3
  default_route: %q
continuity:
  in_memory: true
  store: memory
logging:
  level: error
  format: text
diagnostics:
  enabled: false
hooks:
  tool_reactor_error_policy: fail_open
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
    - kind: custom-openai-legacy-compatible
      id: %s
      enabled: true
      config:
        backend_prefix: enterprise
        base_url: %q
        models:
          source: inline
          items:
            - canonical_id: %s
              native_id: stub-default
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
`, enterpriseRoute, enterpriseBackendID, strings.TrimRight(baseURL, "/")+"/v1", enterpriseModelID)
	path = filepath.Join(dir, "enterprise-essential.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", nil, err
	}
	return path, func() { _ = removeAllRetry(dir, 20, 50*time.Millisecond) }, nil
}

// removeAllRetry deletes path, retrying briefly so Windows releases the
// config file (antivirus/scan handle) before the fixture temp dir is reclaimed.
func removeAllRetry(path string, attempts int, delay time.Duration) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if attempts < 1 {
		attempts = 1
	}
	var last error
	for i := 0; i < attempts; i++ {
		last = os.RemoveAll(path)
		if last == nil {
			if _, err := os.Stat(path); os.IsNotExist(err) {
				return nil
			}
			last = fmt.Errorf("enterprise_module: %q still present after RemoveAll", path)
		}
		if i+1 < attempts && delay > 0 {
			time.Sleep(delay)
		}
	}
	return last
}

func run(ctx context.Context) error {
	stub := startEssentialStub()
	defer stub.Close()

	cfgPath, cleanupCfg, err := writeEssentialConfig(stub.URL)
	if err != nil {
		return err
	}
	if cleanupCfg != nil {
		defer cleanupCfg()
	}
	evidence := &enterpriseEvidence{}
	limits := &enterpriseUsageLimits{}
	querier := enterpriseQuerier{}
	// Canonical authority registrations are public API for external modules.
	rt, err := aipruntime.Build(ctx, aipruntime.Options{
		ConfigPath:       cfgPath,
		MeteringRecorder: enterpriseMeter{},
		MeteringQuerier:  querier,
		EvidenceSink:     evidence,
		// A custom public-only implementation of the usage-limit port: the
		// runtime captures it, attaches the evidence sink, and serves every
		// request through it (task 5.3).
		UsageLimits: limits,
		RequestRegistrations: []authority.RequestRegistration{{
			Descriptor: authority.ProviderDescriptor{
				ID:   "enterprise-request",
				Kind: authority.ProviderKindAuthority,
				Postures: []authority.StagePosture{{
					Stage:           authority.StageRequestAdmit,
					Strength:        authority.StrengthRequired,
					FailureBehavior: authority.FailureFailClosed,
				}},
			},
			Priority: authority.RequestPriorityQuotaBudgetRate,
			Provider: enterpriseRequestProvider{},
		}},
		UsageSnapshotSource: enterpriseRuleSource{},
	})
	if err != nil {
		return fmt.Errorf("aipruntime.Build: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()
	if !rt.Ready() || rt.ExecutorView() == nil {
		return fmt.Errorf("runtime not ready")
	}
	// Public reload/status facade is importable without internal types (req 16.1-16.2).
	// Build binds the same coordinator as aipstd; Reload is available on the facade.
	if rt.ReloadControl() == nil {
		return fmt.Errorf("expected coordinator-bound ReloadControl")
	}
	reloadRes := rt.Reload(ctx, aipruntime.ReloadTrigger{Kind: aipruntime.TriggerAPI, SafeActor: "enterprise-fixture"})
	if reloadRes.Category != aipruntime.ResultNoop && reloadRes.Category != aipruntime.ResultPublished {
		return fmt.Errorf("bound Reload category=%q want no-op or published", reloadRes.Category)
	}
	st := rt.ReloadStatus()
	if st.Busy {
		return fmt.Errorf("ReloadStatus must not report busy after terminal result")
	}
	if st.ActiveGeneration < 1 {
		return fmt.Errorf("expected active generation >= 1, got %d", st.ActiveGeneration)
	}
	_ = aipruntime.ResultPublished
	_ = aipruntime.ResultNoop
	_ = aipruntime.ResultBusy
	_ = aipruntime.ResultRestartRequired
	_ = aipruntime.ResultRetentionBlocked
	_ = aipruntime.ResultCanceled
	if rt.SnapshotGenerationID() == 0 {
		return fmt.Errorf("expected published generation")
	}
	if rt.ExecutableGenerationID() == 0 || rt.ExecutableEvidenceObjectID() == "" {
		return fmt.Errorf("expected executable generation evidence object id")
	}
	if !rt.HasProductionEvidenceSink() || !rt.HasProductionMeteringQuerier() {
		return fmt.Errorf("production evidence/rater/query mounts not wired")
	}
	if !rt.HasProductionMetering() {
		return fmt.Errorf("production metering must be wired")
	}
	if rt.MeteringQuerier() == nil {
		return fmt.Errorf("MeteringQuerier must be mounted")
	}
	if rt.ExecutableGenerationState() == "" {
		return fmt.Errorf("ExecutableGenerationState required")
	}
	if rt.ExecutableGenerationVersion() == "" {
		return fmt.Errorf("ExecutableGenerationVersion required")
	}
	desc := authority.ProviderDescriptor{
		ID: "enterprise-request",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageRequestAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	if err := (authority.Decision{Kind: authority.DecisionAllow, ProviderID: "enterprise-request"}).ValidateFor(desc, authority.StageRequestAdmit); err != nil {
		return fmt.Errorf("decision ValidateFor: %w", err)
	}
	if err := (authority.Settlement{Kind: authority.SettlementFinal}).ValidateFor(nil, metering.PerspectiveCustomer); err != nil {
		return fmt.Errorf("settlement ValidateFor: %w", err)
	}
	leaseDesc := authority.ProviderDescriptor{
		ID: "enterprise-concurrency",
		Postures: []authority.StagePosture{{
			Stage:           authority.StageLeaseAdmit,
			Strength:        authority.StrengthRequired,
			FailureBehavior: authority.FailureFailClosed,
		}},
	}
	if err := (authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 1, ExpiresAt: time.Now().UTC().Add(time.Minute),
	}).ValidateFor(authority.LeaseAdmission{RequestID: "req-1", TTL: time.Minute}, leaseDesc); err != nil {
		return fmt.Errorf("lease ValidateFor: %w", err)
	}
	if err := (authority.LeaseDecision{
		Kind: authority.LeaseAllow, LeaseID: "L1", Generation: 2, ExpiresAt: time.Now().UTC().Add(time.Minute), TTL: time.Minute,
	}).ValidateRenewalFor(authority.LeaseRenew{LeaseID: "L1", ExpectedGeneration: 1, TTL: time.Minute}, leaseDesc); err != nil {
		return fmt.Errorf("lease ValidateRenewalFor: %w", err)
	}
	if _, err := economics.ParseRequiredNanoRate("0"); err != nil {
		return fmt.Errorf("ParseRequiredNanoRate zero: %w", err)
	}
	if _, err := economics.ParseOptionalNanoRate(""); err != nil {
		return fmt.Errorf("ParseOptionalNanoRate absent: %w", err)
	}
	nano, err := economics.ParseDecimalToNano("1.25")
	if err != nil || nano != 1_250_000_000 {
		return fmt.Errorf("ParseDecimalToNano: %d %v", nano, err)
	}
	money := economics.Money{NanoUnits: 0, Currency: "USD", Present: true}
	if err := money.Validate(); err != nil {
		return fmt.Errorf("Money.Validate: %w", err)
	}
	if _, err := economics.RoundQuotient(5, 2, economics.RoundingHalfEven); err != nil {
		return fmt.Errorf("RoundQuotient: %w", err)
	}
	if _, err := economics.TokensFromMoneyPer1M(4_000_000_000, 4_000_000_000, economics.RoundingTowardZero); err != nil {
		return fmt.Errorf("TokensFromMoneyPer1M: %w", err)
	}
	_ = economics.NanoRate{NanoUnits: 0, Present: true}
	// Public-only EconomicControlReady evaluation (OSS technical posture, not billing).
	now := time.Now().UTC()
	report := controlplane.ReadinessReport{
		ExecutableGeneration: controlplane.ExecutableGenerationStatus{
			State: controlplane.CapabilityReady, ID: rt.ExecutableGenerationID(), LastUpdatedAt: now,
		},
		Components: []controlplane.ReadinessComponentStatus{
			{Component: controlplane.ReadinessComponentExecutableGeneration, State: controlplane.CapabilityReady},
			{Component: controlplane.ReadinessComponentMeteringJournal, State: controlplane.CapabilityReady},
			{Component: controlplane.ReadinessComponentUsageAuthority, State: controlplane.CapabilityReady, EnforcementScope: controlplane.EnforcementScopeAdvisorySingleProcess},
			{Component: controlplane.ReadinessComponentTerminalRecovery, State: controlplane.CapabilityDisabled},
		},
		Posture: controlplane.ProtectedTrafficPosture{State: controlplane.CapabilityReady, MayServeStrict: false, LastUpdatedAt: now},
	}
	if !controlplane.EconomicControlReady(report) {
		return fmt.Errorf("expected EconomicControlReady on public readiness shape")
	}
	if err := verifyPublicUsageLimits(ctx, limits); err != nil {
		return err
	}
	view := rt.ExecutorView()
	stream, err := view.Execute(ctx, &aipapi.Call{
		Route:    aipapi.RouteIntent{Selector: enterpriseRoute},
		Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("ping")}}},
	})
	if err != nil {
		return fmt.Errorf("ExecutorView.Execute: %w", err)
	}
	collected, err := aipapi.Collect(ctx, stream)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	if collected.Text.String() == "" {
		return fmt.Errorf("expected assistant text from essential stub")
	}
	if !strings.Contains(collected.Text.String(), enterpriseStubText) {
		return fmt.Errorf("unexpected assistant text %q", collected.Text.String())
	}
	if err := verifyPublicUsageLimitsConsumed(limits, 1); err != nil {
		return err
	}
	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "enterprise_module: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("enterprise_module: ok")
}
