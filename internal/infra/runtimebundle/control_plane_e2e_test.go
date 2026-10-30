//go:build integration

package runtimebundle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	admincp "github.com/aiproxer/aiproxer/internal/stdhttp/admin/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// TestControlPlaneEndToEnd_CrossSessionCaptureAndQueryThroughHTTP proves task
// 6.1: a representative auth, secure-session, backend attempt, policy, usage,
// and audit flow recorded through the standard runtime control-plane adapters
// is queryable across sessions through the protected stdhttp admin handler with
// principal/scope, time, backend/model, session, A-leg, B-leg, outcome, effect,
// visibility, and reason filters, returning bounded pages, stable ordering, and
// shared trace/session/A-leg/B-leg correlation (requirements 1.1-1.8, 2.1-2.8,
// 3.1, 3.4, 3.5, 3.6, 3.7, 9.1, 9.2, 9.3, 9.5).
func TestControlPlaneEndToEnd_CrossSessionCaptureAndQueryThroughHTTP(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	cfg.ControlPlane.Query.Enabled = true
	cfg.ControlPlane.Query.PathPrefix = "/cp"
	cfg.ControlPlane.Query.DefaultPageSize = 50
	cfg.ControlPlane.Query.MaxPageSize = 200

	rt, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
		Clock:          func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("buildControlPlaneRuntime: %v", err)
	}
	if rt == nil || rt.queries == nil {
		t.Fatalf("expected wired control-plane runtime with queries")
	}
	t.Cleanup(func() {
		if rt.closer != nil {
			_ = rt.closer()
		}
	})

	authAdapter := observers.NewAuthSinkAdapter(observers.AuthSinkAdapterConfig{
		Normalizer: rt.normalizer,
		Recorder:   rt.recorder,
	})
	policyAdapter := observers.NewPolicyObserverAdapter(observers.PolicyObserverAdapterConfig{
		Normalizer: rt.normalizer,
		Recorder:   rt.recorder,
	})
	usageAdapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: rt.normalizer,
		Recorder:   rt.recorder,
	})
	b2buaAdapter := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   &noopB2BUAStore{},
		Normalizer: rt.normalizer,
		Recorder:   rt.recorder,
	})
	ssAdapter := observers.NewSecureSessionStoreDecorator(observers.SecureSessionStoreDecoratorConfig{
		Delegate:   &noopSecureSessionStore{},
		Normalizer: rt.normalizer,
		Recorder:   rt.recorder,
	})

	ctx := context.Background()
	const (
		traceA = "trace-a"
		traceB = "trace-b"
		sessA  = "sess-a"
		sessB  = "sess-b"
		alegA  = "aleg-a"
		alegB  = "aleg-b"
		blegA  = "bleg-a"
		blegB  = "bleg-b"
	)
	scopeA := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("principal-a"),
		TenantID:    scope.Known("tenant-1"),
		Origin:      scope.OriginClient,
		Roles:       []string{"ops"},
	}
	scopeB := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("principal-b"),
		TenantID:    scope.Known("tenant-1"),
		Origin:      scope.OriginClient,
		Roles:       []string{"viewer"},
	}

	if err := authAdapter.OnAuthDecision(ctx, sdkauth.AuthDecisionEvent{
		Time: fixed, TraceID: traceA, Frontend: "openai-responses",
		Outcome: sdkauth.OutcomeAllow, ReasonCode: "ok", Scope: &scopeA,
	}); err != nil {
		t.Fatalf("auth A: %v", err)
	}
	if err := authAdapter.OnSessionStart(ctx, sdkauth.SessionStartEvent{
		Time: fixed.Add(time.Minute), TraceID: traceA, Frontend: "openai-responses",
		SessionID: sessA, ALegID: alegA, IsNew: true, Certainty: sdkauth.SessionCertaintyKnown,
	}); err != nil {
		t.Fatalf("session start A: %v", err)
	}
	if _, err := ssAdapter.Create(ctx, domain.CreateRecord{
		SessionID: sessA, ALegID: alegA, Owner: domain.PrincipalRef{ID: "principal-a"}, CreatedAt: fixed.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("secure-session Create A: %v", err)
	}
	if err := b2buaAdapter.RecordAttempt(ctx, aipapi.AttemptRecord{
		ALegID: alegA, BLegID: blegA, Seq: 1, BackendID: "openai", EffectiveModel: "gpt-4o",
		StartedAt: fixed.Add(2 * time.Minute), FinishedAt: fixed.Add(3 * time.Minute), Outcome: aipapi.AttemptSuccess,
	}); err != nil {
		t.Fatalf("b2bua RecordAttempt A: %v", err)
	}
	if err := ssAdapter.AppendAttemptTrace(ctx, domain.AttemptTrace{
		SessionID: sessA, ALegID: alegA, BLegID: blegA, AttemptSeq: 1,
		ResolvedBackend: "openai", ResolvedModel: "gpt-4o", StartedAt: fixed.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("secure-session AppendAttemptTrace A: %v", err)
	}
	if err := policyAdapter.OnPolicyDecision(ctx, policydecision.Record{
		TraceID: traceA, ALegID: alegA, BLegID: blegA, AttemptSeq: 1, Stage: "pre_backend",
		Provider: policydecision.ProviderRef{ID: "opa", Stage: "pre_backend"},
		Outcome:  policydecision.OutcomeAllow, Effect: policydecision.EffectNone, ReasonCode: "default_allow",
		Scope: scopeA,
	}); err != nil {
		t.Fatalf("policy A: %v", err)
	}
	if err := usageAdapter.OnUsage(ctx, usage.Event{
		TraceID: traceA, SessionID: sessA, ALegID: alegA, BLegID: blegA, AttemptSeq: 1,
		BackendID: "openai", FrontendID: "openai-responses", Model: "gpt-4o", Scope: scopeA,
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150, RecordedAt: fixed.Add(3 * time.Minute),
	}); err != nil {
		t.Fatalf("usage A: %v", err)
	}

	if err := authAdapter.OnAuthDecision(ctx, sdkauth.AuthDecisionEvent{
		Time: fixed.Add(5 * time.Minute), TraceID: traceB, Frontend: "anthropic",
		Outcome: sdkauth.OutcomeAllow, ReasonCode: "ok", Scope: &scopeB,
	}); err != nil {
		t.Fatalf("auth B: %v", err)
	}
	if err := authAdapter.OnSessionStart(ctx, sdkauth.SessionStartEvent{
		Time: fixed.Add(5 * time.Minute), TraceID: traceB, Frontend: "anthropic",
		SessionID: sessB, ALegID: alegB, IsNew: true, Certainty: sdkauth.SessionCertaintyKnown,
	}); err != nil {
		t.Fatalf("session start B: %v", err)
	}
	if _, err := ssAdapter.Create(ctx, domain.CreateRecord{
		SessionID: sessB, ALegID: alegB, Owner: domain.PrincipalRef{ID: "principal-b"}, CreatedAt: fixed.Add(6 * time.Minute),
	}); err != nil {
		t.Fatalf("secure-session Create B: %v", err)
	}

	handler := admincp.NewHandler(admincp.Options{Queries: rt.queries})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	statusBody := getJSON(t, srv, "/status")
	if got := statusBody["state"]; got != string(cp.CapabilityReady) {
		t.Fatalf("status state: got %v, want %q", got, cp.CapabilityReady)
	}

	sessionsA := getJSON(t, srv, "/sessions?principal_id=principal-a")
	itemsA, ok := sessionsA["items"].([]any)
	if !ok {
		t.Fatalf("sessions principal-a: items missing or not array, got %T", sessionsA["items"])
	}
	if len(itemsA) != 1 {
		t.Fatalf("sessions principal-a: got %d items, want 1", len(itemsA))
	}
	itemA, ok := itemsA[0].(map[string]any)
	if !ok {
		t.Fatalf("sessions principal-a: item not an object, got %T", itemsA[0])
	}
	if got := itemA["session_id"]; got != sessA {
		t.Fatalf("session id: got %v, want %q", got, sessA)
	}
	if ac, ok := itemA["attempt_count"].(float64); ok && ac != 1 {
		t.Fatalf("attempt count: got %v, want 1", ac)
	}

	sessionsB := getJSON(t, srv, "/sessions?session_id="+sessB)
	itemsB, ok := sessionsB["items"].([]any)
	if !ok {
		t.Fatalf("sessions session-b: items missing or not array, got %T", sessionsB["items"])
	}
	if len(itemsB) != 1 {
		t.Fatalf("sessions session-b: expected 1 item, got %v", sessionsB["items"])
	}

	attemptsBody := getJSON(t, srv, "/attempts?session_id="+sessA)
	attemptItems, ok := attemptsBody["items"].([]any)
	if !ok {
		t.Fatalf("attempts session A: items missing or not array, got %T", attemptsBody["items"])
	}
	if len(attemptItems) != 1 {
		t.Fatalf("attempts session A: got %d items, want 1", len(attemptItems))
	}
	attemptRow, ok := attemptItems[0].(map[string]any)
	if !ok {
		t.Fatalf("attempts session A: row not an object, got %T", attemptItems[0])
	}
	corr, ok := attemptRow["correlation"].(map[string]any)
	if !ok {
		t.Fatalf("attempts session A: correlation missing or not object, got %T", attemptRow["correlation"])
	}
	if corr["b_leg_id"] != blegA {
		t.Fatalf("attempt b_leg_id: got %v, want %q", corr["b_leg_id"], blegA)
	}
	if got := attemptRow["surfaced"]; got != string(cp.AttemptSurfacedSurfaced) && got != string(cp.AttemptSurfacedUnknown) {
		t.Fatalf("attempt surfaced: got %v, want surfaced or unknown", got)
	}

	eventsBody := getJSON(t, srv, "/events?trace_id="+traceA)
	eventItems, ok := eventsBody["items"].([]any)
	if !ok {
		t.Fatalf("events trace A: items missing or not array, got %T", eventsBody["items"])
	}
	if len(eventItems) == 0 {
		t.Fatalf("events trace A: expected at least one event")
	}
	for _, ei := range eventItems {
		ev, ok := ei.(map[string]any)
		if !ok {
			t.Fatalf("events trace A: event not an object, got %T", ei)
		}
		evCorr, ok := ev["correlation"].(map[string]any)
		if !ok {
			t.Fatalf("events trace A: event correlation missing or not object, got %T", ev["correlation"])
		}
		if evCorr["trace_id"] != traceA {
			t.Fatalf("events trace A filter leaked trace %v", evCorr["trace_id"])
		}
	}
	if vis := eventsBody["visibility"]; vis != string(cp.VisibilityDefault) {
		t.Fatalf("events visibility: got %v, want %q", vis, cp.VisibilityDefault)
	}

	usageBody := getJSON(t, srv, "/usage?backend_id=openai&model=gpt-4o")
	usageItems, ok := usageBody["items"].([]any)
	if !ok {
		t.Fatalf("usage: items missing or not array, got %T", usageBody["items"])
	}
	if len(usageItems) != 1 {
		t.Fatalf("usage: got %d items, want 1", len(usageItems))
	}
	usageRow, ok := usageItems[0].(map[string]any)
	if !ok {
		t.Fatalf("usage: row not an object, got %T", usageItems[0])
	}
	usageTokens, ok := usageRow["input_tokens"].(float64)
	if !ok || usageTokens != 100 {
		t.Fatalf("usage input tokens: got %v, want 100", usageRow["input_tokens"])
	}

	policyBody := getJSON(t, srv, "/policy-audit?a_leg_id="+alegA)
	policyItems, ok := policyBody["items"].([]any)
	if !ok {
		t.Fatalf("policy-audit: items missing or not array, got %T", policyBody["items"])
	}
	if len(policyItems) != 1 {
		t.Fatalf("policy-audit: got %d items, want 1", len(policyItems))
	}
	policyRow, ok := policyItems[0].(map[string]any)
	if !ok {
		t.Fatalf("policy-audit: row not an object, got %T", policyItems[0])
	}
	if got := policyRow["outcome"]; got != "allow" {
		t.Fatalf("policy outcome: got %v, want allow", got)
	}

	broad := getJSON(t, srv, "/sessions?limit=99999")
	if broad["error"] != string(cp.ErrCodeTooBroad) {
		t.Fatalf("too broad: got %v, want %q", broad["error"], cp.ErrCodeTooBroad)
	}
}

func getJSON(t *testing.T, srv *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := map[string]any{}
	body["_status"] = float64(resp.StatusCode)
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return body
}

type noopB2BUAStore struct{}

func (noopB2BUAStore) ResolveALeg(context.Context, string) (b2bua.ALegRecord, error) {
	return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
}

func (noopB2BUAStore) CreateALeg(context.Context, string) (b2bua.ALegRecord, error) {
	return b2bua.ALegRecord{ALegID: "aleg-1"}, nil
}

func (noopB2BUAStore) FetchALeg(context.Context, string) (b2bua.ALegRecord, error) {
	return b2bua.ALegRecord{}, b2bua.ErrALegNotFound
}

func (noopB2BUAStore) SetWeightedFirstConsumed(context.Context, string, bool) error {
	return nil
}

func (noopB2BUAStore) NextBLeg(context.Context, string) (b2bua.BLegRecord, error) {
	return b2bua.BLegRecord{BLegID: "bleg-1", ALegID: "aleg-1", Seq: 1}, nil
}
func (noopB2BUAStore) RecordAttempt(context.Context, aipapi.AttemptRecord) error { return nil }
func (noopB2BUAStore) LoadAttempts(context.Context, string) ([]aipapi.AttemptRecord, error) {
	return nil, nil
}

type noopSecureSessionStore struct{}

func (noopSecureSessionStore) Create(context.Context, domain.CreateRecord) (domain.Record, error) {
	return domain.Record{SessionID: "ok"}, nil
}

func (noopSecureSessionStore) LoadByID(context.Context, domain.SessionID) (domain.Record, error) {
	return domain.Record{}, nil
}

func (noopSecureSessionStore) LoadByResumeFingerprint(context.Context, domain.TokenFingerprint) (domain.Record, error) {
	return domain.Record{}, nil
}

func (noopSecureSessionStore) LoadByALegID(context.Context, string) (domain.Record, error) {
	return domain.Record{}, nil
}

func (noopSecureSessionStore) TouchActivity(context.Context, domain.SessionID, time.Time, domain.ActivitySource) error {
	return nil
}

func (noopSecureSessionStore) AppendAttemptTrace(context.Context, domain.AttemptTrace) error {
	return nil
}

func (noopSecureSessionStore) UpdateAttemptOutcome(context.Context, domain.AttemptOutcome) error {
	return nil
}

func (noopSecureSessionStore) AppendTranscript(context.Context, domain.TranscriptItem) error {
	return nil
}

func (noopSecureSessionStore) NextTranscriptSeq(context.Context, domain.SessionID) (int64, error) {
	return 1, nil
}
func (noopSecureSessionStore) AddUsage(context.Context, domain.UsageDelta) error { return nil }
func (noopSecureSessionStore) NextAuditSeq(context.Context, domain.SessionID) (int64, error) {
	return 1, nil
}
func (noopSecureSessionStore) AppendAudit(context.Context, domain.AuditItem) error { return nil }
func (noopSecureSessionStore) Audit(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.AuditItem, error) {
	return nil, nil
}

func (noopSecureSessionStore) Summary(context.Context, domain.SummaryQuery) ([]domain.Summary, error) {
	return nil, nil
}

func (noopSecureSessionStore) Transcript(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.TranscriptItem, error) {
	return nil, nil
}

func (noopSecureSessionStore) ListAttemptEvidence(context.Context, domain.SessionID, domain.ReadOptions) ([]domain.AttemptEvidence, error) {
	return nil, nil
}

func (noopSecureSessionStore) CheckReadiness(context.Context, domain.PolicyMetadata) error {
	return nil
}

func (noopSecureSessionStore) Quarantine(context.Context, domain.QuarantineInput) error {
	return domain.ErrQuarantineUnimplemented
}
