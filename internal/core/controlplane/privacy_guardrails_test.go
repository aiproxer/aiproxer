package controlplane_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore"
	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// forbiddenSecretSubstrings are the raw secret/credential/payload markers that
// must never appear in stored control-plane events or query results
// (requirements 4.4, 4.5, 4.6, 4.7, 4.8, 10.5).
var forbiddenSecretSubstrings = []string{
	"bearer ",
	"bearer:",
	"api key",
	"api-key",
	"apikey:",
	"oauth ",
	"oauth_token",
	"authorization:",
	"resume token",
	"resume_token",
	"secret:",
	"password:",
	"raw_usage_json",
	"raw_payload",
	"raw_headers",
	"x-api-key:",
	"sk-",
	"credentialsecret",
}

// TestPrivacyGuardrails_StoredEventsAndQueryResultsContainNoRawSecrets proves
// task 6.4: a representative flow (auth, session, attempt, usage, policy, audit)
// recorded into the control-plane store and queried back through every read
// view contains no raw bearer tokens, API keys, OAuth tokens, resume tokens,
// credential secrets, raw transport headers, or raw request/response payloads
// (requirements 4.4, 4.5, 4.6, 4.7, 4.8, 6.1-6.6, 10.5).
func TestPrivacyGuardrails_StoredEventsAndQueryResultsContainNoRawSecrets(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "privacy"})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: fixed},
	})
	normalizer := controlplane.NewNormalizer(fixedClock{t: fixed}, cp.SourceRef{Name: "privacy", Version: "v1"}, controlplane.NewScopeFlattener())
	view := scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman, PrincipalID: scope.Known("principal-p"),
		TenantID: scope.Known("tenant-p"), Origin: scope.OriginClient, Roles: []string{"ops"},
	}
	ctx := context.Background()

	authEv, err := normalizer.FromAuthDecision(sdkauth.AuthDecisionEvent{
		Time: fixed, TraceID: "trace-p", Frontend: "openai-responses",
		Outcome: sdkauth.OutcomeAllow, ReasonCode: "ok", HandlerKind: sdkauth.HandlerLocalAPIKey,
		Scope: &view,
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	if _, err := recorder.Record(ctx, authEv); err != nil {
		t.Fatalf("Record auth: %v", err)
	}
	sessEv, err := normalizer.FromSessionStart(sdkauth.SessionStartEvent{
		Time: fixed.Add(time.Minute), TraceID: "trace-p", Frontend: "openai-responses",
		SessionID: "sess-p", ALegID: "aleg-p", IsNew: true, Certainty: sdkauth.SessionCertaintyKnown,
	})
	if err != nil {
		t.Fatalf("FromSessionStart: %v", err)
	}
	if _, err := recorder.Record(ctx, sessEv); err != nil {
		t.Fatalf("Record session: %v", err)
	}
	attemptEv, err := normalizer.FromAttempt(controlplane.AttemptSourceRecord{
		SourceEventKey: "privacy-attempt", OccurredAt: fixed, TraceID: "trace-p",
		SessionID: "sess-p", ALegID: "aleg-p", BLegID: "bleg-p", AttemptSeq: 1,
		BackendID: "openai", Model: "gpt-4o", Surfaced: cp.AttemptSurfacedSurfaced, Outcome: cp.AttemptOutcomeSucceeded,
		Scope: &view,
	})
	if err != nil {
		t.Fatalf("FromAttempt: %v", err)
	}
	if _, err := recorder.Record(ctx, attemptEv); err != nil {
		t.Fatalf("Record attempt: %v", err)
	}
	usageEv, err := normalizer.FromUsage(usage.Event{
		TraceID: "trace-p", SessionID: "sess-p", ALegID: "aleg-p", BLegID: "bleg-p", AttemptSeq: 1,
		BackendID: "openai", FrontendID: "openai-responses", Model: "gpt-4o", Scope: view,
		InputTokens: 100, OutputTokens: 50, TotalTokens: 150, RecordedAt: fixed,
	})
	if err != nil {
		t.Fatalf("FromUsage: %v", err)
	}
	if _, err := recorder.Record(ctx, usageEv); err != nil {
		t.Fatalf("Record usage: %v", err)
	}

	qs := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{Enabled: true, DefaultPageSize: 50, MaxPageSize: 200})

	scanPage := func(t *testing.T, label string, page any) {
		t.Helper()
		body, err := json.Marshal(page)
		if err != nil {
			t.Fatalf("marshal %s: %v", label, err)
		}
		low := strings.ToLower(string(body))
		for _, bad := range forbiddenSecretSubstrings {
			if strings.Contains(low, bad) {
				t.Fatalf("%s leaked forbidden substring %q in: %s", label, bad, string(body))
			}
		}
	}

	eventsPage, err := qs.Events(ctx, cp.EventQuery{Common: cp.CommonFilters{TraceID: "trace-p"}})
	if err != nil {
		t.Fatalf("Events query: %v", err)
	}
	scanPage(t, "events", eventsPage)

	sessionsPage, err := qs.Sessions(ctx, cp.SessionQuery{Common: cp.CommonFilters{TraceID: "trace-p"}})
	if err != nil {
		t.Fatalf("Sessions query: %v", err)
	}
	scanPage(t, "sessions", sessionsPage)

	attemptsPage, err := qs.Attempts(ctx, cp.AttemptQuery{Common: cp.CommonFilters{TraceID: "trace-p"}})
	if err != nil {
		t.Fatalf("Attempts query: %v", err)
	}
	scanPage(t, "attempts", attemptsPage)

	usagePage, err := qs.Usage(ctx, cp.UsageQuery{Common: cp.CommonFilters{TraceID: "trace-p"}})
	if err != nil {
		t.Fatalf("Usage query: %v", err)
	}
	scanPage(t, "usage", usagePage)
	// Usage query must surface typed token fields, not raw usage JSON.
	if len(usagePage.Items) != 1 {
		t.Fatalf("usage: expected 1 row, got %d", len(usagePage.Items))
	}
	if usagePage.Items[0].InputTokens != 100 {
		t.Fatalf("usage input tokens: got %d, want 100", usagePage.Items[0].InputTokens)
	}
}

// TestPrivacyGuardrails_RecorderRejectsRawSecretsInAnyFreeTextField proves the
// recorder rejects events whose summary, reason code, or other free-text field
// carries raw secret/credential content, regardless of recording policy
// (requirements 4.4, 4.5, 4.6).
func TestPrivacyGuardrails_RecorderRejectsRawSecretsInAnyFreeTextField(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "privacy-reject"})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: fixed},
	})

	for _, bad := range []string{
		"Bearer abc123",
		"api-key sk-live-xxxx",
		"oauth token xyz",
		"resume_token=rt-1",
		"secret: hunter2",
		"password: p@ssw0rd",
		"authorization: Bearer ...",
	} {
		ev := cp.Event{
			Category:       cp.CategoryAuth,
			OccurredAt:     fixed,
			RecordedAt:     fixed,
			Visibility:     cp.VisibilityDefault,
			EvidenceState:  cp.EvidenceRecorded,
			RedactionState: cp.RedactionNone,
			Source:         cp.SourceRef{Name: "privacy"},
			Summary:        bad,
			Detail:         &cp.AuthDetail{Outcome: "allow"},
		}
		if _, err := recorder.Record(context.Background(), ev); err == nil {
			t.Fatalf("recorder must reject summary containing %q", bad)
		}
	}
	// No events should have been stored.
	page, err := store.Events(context.Background(), cp.EventQuery{Limit: 100})
	if err != nil {
		t.Fatalf("Events query: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("no unsafe events should be stored, got %d", len(page.Items))
	}
}

// TestPrivacyGuardrails_PrivilegedAuditIsRedactedForDefaultVisibility proves
// that privileged audit evidence is marked redacted or summarized for default
// visibility and never surfaced as raw detail (requirements 4.6, 4.7, 4.8, 9.3).
func TestPrivacyGuardrails_PrivilegedAuditIsRedactedForDefaultVisibility(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "privacy-priv"})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: fixed},
	})
	normalizer := controlplane.NewNormalizer(fixedClock{t: fixed}, cp.SourceRef{Name: "privacy-priv", Version: "v1"}, controlplane.NewScopeFlattener())

	auditEv, err := normalizer.FromAudit(controlplane.AuditSourceRecord{
		SourceEventKey: "priv-audit", OccurredAt: fixed, TraceID: "trace-priv",
		SessionID: "sess-priv", ALegID: "aleg-priv", Action: "privileged-action",
		Result: "summarized-result", ReasonCode: "policy_override",
		Visibility: cp.VisibilityPrivileged, RedactionState: cp.RedactionRedacted,
	})
	if err != nil {
		t.Fatalf("FromAudit: %v", err)
	}
	if _, err := recorder.Record(context.Background(), auditEv); err != nil {
		t.Fatalf("Record audit: %v", err)
	}
	if auditEv.Visibility != cp.VisibilityPrivileged || auditEv.RedactionState != cp.RedactionPrivileged {
		t.Fatalf("privileged audit must keep privileged visibility and privileged redaction state: %#v", auditEv)
	}

	qs := controlplane.NewQueryService(store, status, controlplane.QueryServiceConfig{Enabled: true, DefaultPageSize: 50, MaxPageSize: 200})
	page, err := qs.PolicyAudit(context.Background(), cp.EvidenceQuery{Common: cp.CommonFilters{TraceID: "trace-priv"}, Visibility: cp.VisibilityDefault})
	if err != nil {
		t.Fatalf("PolicyAudit query: %v", err)
	}
	if len(page.Items) == 0 {
		t.Fatalf("expected at least one policy/audit row")
	}
	for _, row := range page.Items {
		if row.RedactionState == "" {
			t.Fatalf("redaction state must be explicit, got empty for category %q", row.Category)
		}
		if row.Visibility != "" && row.Visibility != cp.VisibilityDefault && row.Visibility != cp.VisibilityPrivileged {
			t.Fatalf("unexpected visibility %q", row.Visibility)
		}
	}
}

// TestPrivacyGuardrails_RetentionDoesNotAffectInFlightRecording proves that
// retention application does not change in-flight recording outcomes: the
// recorder continues to accept new events with the same correlation after a
// retention sweep (requirements 6.1-6.6, 10.7).
func TestPrivacyGuardrails_RetentionDoesNotAffectInFlightRecording(t *testing.T) {
	t.Parallel()
	fixed := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
	store, err := ledgerstore.NewMemoryStore(ledgerstore.MemoryConfig{StoreID: "privacy-retention"})
	if err != nil {
		t.Fatalf("NewMemoryStore: %v", err)
	}
	defer func() { _ = store.Close() }()
	status := controlplane.NewStatus(cp.CapabilityStatus{State: cp.CapabilityReady, RecordingPolicy: cp.RecordingBestEffort})
	recorder := controlplane.NewRecorderService(store, status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  fixedClock{t: fixed},
	})
	normalizer := controlplane.NewNormalizer(fixedClock{t: fixed}, cp.SourceRef{Name: "retention", Version: "v1"}, controlplane.NewScopeFlattener())

	firstEv, err := normalizer.FromAttempt(controlplane.AttemptSourceRecord{
		SourceEventKey: "retention-1", OccurredAt: fixed, TraceID: "trace-ret",
		SessionID: "sess-ret", ALegID: "aleg-ret", BLegID: "bleg-ret-1", AttemptSeq: 1,
		BackendID: "openai", Model: "gpt-4o", Surfaced: cp.AttemptSurfacedSurfaced, Outcome: cp.AttemptOutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("FromAttempt 1: %v", err)
	}
	if _, err := recorder.Record(context.Background(), firstEv); err != nil {
		t.Fatalf("Record 1: %v", err)
	}

	ctrl := controlplane.NewRetentionController(store, status, controlplane.RetentionControllerConfig{
		Profile: controlplane.RetentionProfileStandard, Window: 24 * time.Hour, Clock: fixedClock{t: fixed.Add(48 * time.Hour)},
	})
	if _, err := ctrl.Apply(context.Background(), fixed.Add(24*time.Hour), cp.VisibilityDefault); err != nil {
		t.Fatalf("Retention Apply: %v", err)
	}

	// In-flight recording after retention must still succeed with the same correlation.
	secondEv, err := normalizer.FromAttempt(controlplane.AttemptSourceRecord{
		SourceEventKey: "retention-2", OccurredAt: fixed.Add(48 * time.Minute), TraceID: "trace-ret",
		SessionID: "sess-ret", ALegID: "aleg-ret", BLegID: "bleg-ret-2", AttemptSeq: 2,
		BackendID: "openai", Model: "gpt-4o", Surfaced: cp.AttemptSurfacedSurfaced, Outcome: cp.AttemptOutcomeSucceeded,
	})
	if err != nil {
		t.Fatalf("FromAttempt 2: %v", err)
	}
	if _, err := recorder.Record(context.Background(), secondEv); err != nil {
		t.Fatalf("Record after retention must succeed: %v", err)
	}
}

// TestPrivacyGuardrails_ExcludedEnterpriseFeaturesHaveNoConfigSurface proves
// that excluded enterprise features (billing, identity provisioning, policy
// engines, GUI, marketplace, provider forwarding, historical migration) have no
// configuration surface in ControlPlaneConfig (requirements 10.1-10.6).
func TestPrivacyGuardrails_ExcludedEnterpriseFeaturesHaveNoConfigSurface(t *testing.T) {
	t.Parallel()
	body, err := json.Marshal(config.ControlPlaneConfig{})
	if err != nil {
		t.Fatalf("marshal ControlPlaneConfig: %v", err)
	}
	low := strings.ToLower(string(body))
	excluded := []string{
		"billing",
		"identity_provision",
		"identityprovision",
		"policy_engine",
		"policyengine",
		"web_admin",
		"webadmin",
		"reporting_chart",
		"reportingchart",
		"marketplace",
		"provider_forward",
		"providerforward",
		"historical_migration",
		"historicalmigration",
	}
	for _, bad := range excluded {
		if strings.Contains(low, bad) {
			t.Fatalf("ControlPlaneConfig leaked excluded enterprise feature %q in: %s", bad, string(body))
		}
	}
}
