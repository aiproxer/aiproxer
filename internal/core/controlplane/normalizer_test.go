package controlplane_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

func newTestNormalizer(t *testing.T) *controlplane.Normalizer {
	t.Helper()
	return controlplane.NewNormalizer(
		fixedClock{t: time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)},
		cp.SourceRef{Name: "test-source", Version: "v1"},
		controlplane.NewScopeFlattener(),
	)
}

func knownScopeView() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind:    scope.SubjectHuman,
		PrincipalID:    scope.Known("principal-1"),
		CredentialID:   scope.Known("cred-1"),
		TenantID:       scope.Known("tenant-1"),
		OrganizationID: scope.Known("org-1"),
		Origin:         scope.OriginClient,
		Roles:          []string{"ops"},
		SafeClaims:     map[string]string{"team": "platform"},
		PolicyLabels:   map[string]string{"tier": "gold"},
	}
}

// ---- 3.2 auth / session / attempt ----

func TestNormalizeAuthDecisionPreservesCorrelationAndScope(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev := auth.AuthDecisionEvent{
		Time:          time.Date(2026, 7, 4, 0, 1, 0, 0, time.UTC),
		TraceID:       "trace-1",
		Frontend:      "openai-responses",
		Outcome:       auth.OutcomeAllow,
		ReasonCode:    "ok",
		PrincipalID:   "principal-1",
		HandlerKind:   auth.HandlerLocalAPIKey,
		RequiredLevel: auth.LevelAPIKey,
		Scope:         new(knownScopeView()),
	}
	got, err := n.FromAuthDecision(ev)
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	if got.Category != cp.CategoryAuth {
		t.Fatalf("category = %q, want auth", got.Category)
	}
	if got.Auth() == nil || got.Auth().Outcome != "allow" {
		t.Fatalf("auth detail lost: %#v", got.Auth())
	}
	if got.Correlation.TraceID != "trace-1" {
		t.Fatalf("trace correlation lost: %#v", got.Correlation)
	}
	if !got.Scope.PrincipalID.Equal(scope.Known("principal-1")) {
		t.Fatalf("scope principal_id lost: %#v", got.Scope.PrincipalID)
	}
	if got.EvidenceState != cp.EvidenceRecorded {
		t.Fatalf("evidence state = %q, want recorded", got.EvidenceState)
	}
	if got.Visibility != cp.VisibilityDefault {
		t.Fatalf("visibility = %q, want default", got.Visibility)
	}
	if got.Source.Name != "test-source" {
		t.Fatalf("source name lost: %q", got.Source.Name)
	}
	if got.OccurredAt.IsZero() || got.RecordedAt.Before(got.OccurredAt) {
		t.Fatalf("timing not normalized: occurred=%v recorded=%v", got.OccurredAt, got.RecordedAt)
	}
	if err := controlplane.ValidateEvent(got); err != nil {
		t.Fatalf("normalized event invalid: %v", err)
	}
}

func TestNormalizeAuthDecisionRejectsRawCredentialsInReason(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev := auth.AuthDecisionEvent{
		Time:       time.Now(),
		TraceID:    "trace-1",
		Outcome:    auth.OutcomeDeny,
		ReasonCode: "bearer abc123secret",
	}
	if _, err := n.FromAuthDecision(ev); err == nil {
		t.Fatalf("FromAuthDecision must reject credential-bearing reason code")
	}
}

func TestNormalizeSessionStartMapsActionAndCertainty(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev := auth.SessionStartEvent{
		Time:             time.Date(2026, 7, 4, 0, 2, 0, 0, time.UTC),
		TraceID:          "trace-2",
		Frontend:         "anthropic",
		SessionID:        "sess-2",
		ClientSessionRef: "client-ref-2",
		ALegID:           "aleg-2",
		Certainty:        auth.SessionCertaintyKnown,
		IsNew:            true,
		PrincipalID:      "principal-1",
	}
	got, err := n.FromSessionStart(ev)
	if err != nil {
		t.Fatalf("FromSessionStart: %v", err)
	}
	if got.Category != cp.CategorySession || got.Session() == nil {
		t.Fatalf("session detail lost: %#v", got)
	}
	if got.Session().SessionID != "sess-2" || got.Session().ALegID != "aleg-2" {
		t.Fatalf("session correlation lost: %#v", got.Session())
	}
	if got.Session().Action != cp.SessionActionCreated {
		t.Fatalf("action = %q, want created", got.Session().Action)
	}
	if got.Session().Certainty != "known" {
		t.Fatalf("certainty lost: %q", got.Session().Certainty)
	}
	if got.Correlation.SessionID != "sess-2" || got.Correlation.ALegID != "aleg-2" {
		t.Fatalf("correlation not projected: %#v", got.Correlation)
	}
}

func TestNormalizeSessionStartDeniedActionForUncertain(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev := auth.SessionStartEvent{
		Time:      time.Now(),
		TraceID:   "trace-d",
		SessionID: "sess-d",
		Certainty: auth.SessionCertaintyUnknown,
	}
	got, err := n.FromSessionStart(ev)
	if err != nil {
		t.Fatalf("FromSessionStart: %v", err)
	}
	if got.Session().Action != cp.SessionActionDenied {
		t.Fatalf("uncertain non-new session must map to denied, got %q", got.Session().Action)
	}
}

func TestNormalizeAttemptDistinguishesSurfacedFromLostRace(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.AttemptSourceRecord{
		TraceID:      "trace-a",
		SessionID:    "sess-a",
		ALegID:       "aleg-a",
		BLegID:       "bleg-a",
		AttemptSeq:   2,
		BackendID:    "openai",
		Model:        "gpt-4o",
		RouteOutcome: "primary",
		Surfaced:     cp.AttemptSurfacedSwallowed,
		Outcome:      cp.AttemptOutcomeLostRace,
		ErrorClass:   "lost_race",
		StartedAt:    time.Now().Add(-time.Minute),
		FinishedAt:   time.Now(),
		OccurredAt:   time.Now(),
		Scope:        new(knownScopeView()),
	}
	got, err := n.FromAttempt(rec)
	if err != nil {
		t.Fatalf("FromAttempt: %v", err)
	}
	if got.Attempt() == nil || got.Attempt().Surfaced != cp.AttemptSurfacedSwallowed || got.Attempt().Outcome != cp.AttemptOutcomeLostRace {
		t.Fatalf("attempt surfacing/outcome lost: %#v", got.Attempt())
	}
	if got.Correlation.BLegID != "bleg-a" || got.Correlation.AttemptSeq != 2 {
		t.Fatalf("attempt correlation lost: %#v", got.Correlation)
	}
	if got.Attempt().BackendID != "openai" || got.Attempt().Model != "gpt-4o" {
		t.Fatalf("backend/model attribution lost: %#v", got.Attempt())
	}
}

func TestNormalizeAttemptRejectsUnsafeErrorClass(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.AttemptSourceRecord{
		OccurredAt: time.Now(),
		TraceID:    "trace-x",
		ErrorClass: "Authorization: Bearer secrettoken",
		Surfaced:   cp.AttemptSurfacedSurfaced,
		Outcome:    cp.AttemptOutcomeFailed,
	}
	if _, err := n.FromAttempt(rec); err == nil {
		t.Fatalf("FromAttempt must reject credential-bearing error class")
	}
}

func TestNormalizeAttemptUnknownIDsStayUnknown(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.AttemptSourceRecord{
		OccurredAt: time.Now(),
		TraceID:    "trace-u",
		Surfaced:   cp.AttemptSurfacedUnknown,
		Outcome:    cp.AttemptOutcomeUnknown,
	}
	got, err := n.FromAttempt(rec)
	if err != nil {
		t.Fatalf("FromAttempt: %v", err)
	}
	if got.Correlation.SessionID != "" || got.Correlation.ALegID != "" || got.Correlation.BLegID != "" {
		t.Fatalf("unknown IDs must stay empty, not invented: %#v", got.Correlation)
	}
	if got.Attempt().Surfaced != cp.AttemptSurfacedUnknown || got.Attempt().Outcome != cp.AttemptOutcomeUnknown {
		t.Fatalf("unknown surfacing/outcome must be preserved: %#v", got.Attempt())
	}
}

// ---- 3.3 usage / policy / audit ----

func TestNormalizeUsageDropsRawUsageJSONAndPreservesPlane(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev := usage.Event{
		TraceID:       "trace-u",
		ALegID:        "aleg-u",
		BLegID:        "bleg-u",
		PrincipalID:   "principal-1",
		SessionID:     "sess-u",
		AttemptSeq:    1,
		BackendID:     "openai",
		FrontendID:    "openai-responses",
		Model:         "gpt-4o",
		Scope:         knownScopeView(),
		InputTokens:   100,
		OutputTokens:  50,
		TotalTokens:   150,
		CostNanoUnits: 1000,
		Currency:      "USD",
		CostSource:    "accounting",
		RawUsageJSON:  `{"secret":"should-not-leak"}`,
		RecordedAt:    time.Now(),
	}
	got, err := n.FromUsage(ev)
	if err != nil {
		t.Fatalf("FromUsage: %v", err)
	}
	if got.Usage() == nil {
		t.Fatalf("usage detail lost")
	}
	if got.Usage().Plane != cp.UsagePlaneObserved {
		t.Fatalf("plane = %q, want observed", got.Usage().Plane)
	}
	if got.Usage().Availability != cp.UsageAvailabilityObserved {
		t.Fatalf("availability = %q, want observed", got.Usage().Availability)
	}
	if got.Usage().InputTokens != 100 || got.Usage().TotalTokens != 150 {
		t.Fatalf("token dimensions lost: %#v", got.Usage())
	}
	for _, bad := range []string{"secret", "RawUsageJSON", `{"secret":`} {
		if strings.Contains(serializeEvent(t, got), bad) {
			t.Fatalf("normalized usage must not carry raw usage JSON; found %q", bad)
		}
	}
	if err := controlplane.ValidateEvent(got); err != nil {
		t.Fatalf("normalized usage invalid: %v", err)
	}
}

func TestNormalizePolicyDecisionPreservesOutcomeButDoesNotChangeIt(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := policydecision.Record{
		TraceID:    "trace-p",
		ALegID:     "aleg-p",
		BLegID:     "bleg-p",
		AttemptSeq: 1,
		Stage:      "pre_backend",
		Provider:   policydecision.ProviderRef{ID: "opa", Stage: "pre_backend"},
		Outcome:    policydecision.OutcomeDeny,
		Effect:     policydecision.EffectSwallow,
		ReasonCode: "policy_violation",
		Visibility: policydecision.EvidenceDefault,
		Scope:      knownScopeView(),
	}
	got, err := n.FromPolicyDecision(rec)
	if err != nil {
		t.Fatalf("FromPolicyDecision: %v", err)
	}
	if got.Policy() == nil || got.Policy().Outcome != "deny" || got.Policy().Effect != "swallow" {
		t.Fatalf("policy outcome/effect lost: %#v", got.Policy())
	}
	if got.Policy().Stage != "pre_backend" {
		t.Fatalf("policy stage lost: %q", got.Policy().Stage)
	}
	if got.Policy().ProviderID != "opa" {
		t.Fatalf("provider id lost: %q", got.Policy().ProviderID)
	}
	if got.Correlation.TraceID != "trace-p" {
		t.Fatalf("correlation lost: %#v", got.Correlation)
	}
}

func TestNormalizePolicyDecisionPrivilegedVisibilityMarksPrivileged(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := policydecision.Record{
		TraceID:    "trace-priv",
		Stage:      "post_backend",
		Provider:   policydecision.ProviderRef{ID: "opa"},
		Outcome:    policydecision.OutcomeAllow,
		Visibility: policydecision.EvidencePrivileged,
	}
	got, err := n.FromPolicyDecision(rec)
	if err != nil {
		t.Fatalf("FromPolicyDecision: %v", err)
	}
	if got.Visibility != cp.VisibilityPrivileged || got.RedactionState != cp.RedactionPrivileged {
		t.Fatalf("privileged policy evidence must be marked privileged, got vis=%q redaction=%q",
			got.Visibility, got.RedactionState)
	}
}

func TestNormalizeAuditMarksRedactedForPrivilegedContent(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.AuditSourceRecord{
		OccurredAt:     time.Now(),
		TraceID:        "trace-audit",
		SessionID:      "sess-audit",
		Action:         "transcript.view",
		Result:         "ok",
		ReasonCode:     "operator_access",
		Visibility:     cp.VisibilityPrivileged,
		RedactionState: cp.RedactionPrivileged,
	}
	got, err := n.FromAudit(rec)
	if err != nil {
		t.Fatalf("FromAudit: %v", err)
	}
	if got.Audit() == nil || got.Audit().Action != "transcript.view" {
		t.Fatalf("audit detail lost: %#v", got.Audit())
	}
	if got.Visibility != cp.VisibilityPrivileged || got.RedactionState != cp.RedactionPrivileged {
		t.Fatalf("privileged audit must keep privileged+privileged redaction state, got %q/%q",
			got.Visibility, got.RedactionState)
	}
	if got.EvidenceState != cp.EvidenceRedacted {
		t.Fatalf("privileged audit at default visibility must expose redacted evidence state, got %q",
			got.EvidenceState)
	}
}

func TestNormalizeAuditRejectsRawPayloadInResult(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.AuditSourceRecord{
		OccurredAt: time.Now(),
		TraceID:    "trace-audit-x",
		Action:     "request.body",
		Result:     "Bearer secrettoken payload",
	}
	if _, err := n.FromAudit(rec); err == nil {
		t.Fatalf("FromAudit must reject credential-bearing result text")
	}
}

func TestNormalizeEachCategoryProducesExactlyOneDetail(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	authEv, err := n.FromAuthDecision(auth.AuthDecisionEvent{Time: time.Now(), TraceID: "t", Outcome: auth.OutcomeAllow})
	if err != nil {
		t.Fatalf("auth: %v", err)
	}
	sessEv, err := n.FromSessionStart(auth.SessionStartEvent{Time: time.Now(), TraceID: "t", SessionID: "s", IsNew: true})
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	attemptEv, err := n.FromAttempt(controlplane.AttemptSourceRecord{OccurredAt: time.Now(), TraceID: "t", Surfaced: cp.AttemptSurfacedSurfaced, Outcome: cp.AttemptOutcomeSucceeded})
	if err != nil {
		t.Fatalf("attempt: %v", err)
	}
	usageEv, err := n.FromUsage(usage.Event{TraceID: "t", RecordedAt: time.Now()})
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	policyEv, err := n.FromPolicyDecision(policydecision.Record{TraceID: "t", Stage: "s", Outcome: policydecision.OutcomeAllow})
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	auditEv, err := n.FromAudit(controlplane.AuditSourceRecord{OccurredAt: time.Now(), TraceID: "t", Action: "a"})
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	cases := []struct {
		name string
		ev   cp.Event
	}{
		{"auth", authEv},
		{"session", sessEv},
		{"attempt", attemptEv},
		{"usage", usageEv},
		{"policy", policyEv},
		{"audit", auditEv},
	}
	for _, c := range cases {
		if err := c.ev.Validate(); err != nil {
			t.Fatalf("%s: normalized event failed SDK validation: %v", c.name, err)
		}
		if err := controlplane.ValidateEvent(c.ev); err != nil {
			t.Fatalf("%s: normalized event failed core validation: %v", c.name, err)
		}
	}
}

// ---- Phase 4 source-adapter support: session and usage source records ----

func TestNormalizeSessionRecordUsesExplicitSourceKeyAndAction(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.SessionSourceRecord{
		SourceEventKey: "secure-create:sess-1",
		OccurredAt:     time.Date(2026, 7, 4, 0, 5, 0, 0, time.UTC),
		TraceID:        "trace-s",
		SessionID:      "sess-1",
		ALegID:         "aleg-1",
		Action:         cp.SessionActionCreated,
		Certainty:      "known",
		Scope:          new(knownScopeView()),
	}
	got, err := n.FromSessionRecord(rec)
	if err != nil {
		t.Fatalf("FromSessionRecord: %v", err)
	}
	if got.SourceEventKey != "secure-create:sess-1" {
		t.Fatalf("source key lost: %q", got.SourceEventKey)
	}
	if got.Category != cp.CategorySession || got.Session() == nil || got.Session().Action != cp.SessionActionCreated {
		t.Fatalf("session detail lost: %#v", got.Session())
	}
	if got.Correlation.SessionID != "sess-1" || got.Correlation.ALegID != "aleg-1" {
		t.Fatalf("correlation lost: %#v", got.Correlation)
	}
	if err := controlplane.ValidateEvent(got); err != nil {
		t.Fatalf("normalized session record invalid: %v", err)
	}
}

func TestNormalizeSessionRecordDefaultsUnknownActionToUpdated(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.SessionSourceRecord{
		SourceEventKey: "secure-touch:sess-1:client_request:2026-07-04T00:05:00Z",
		OccurredAt:     time.Now(),
		SessionID:      "sess-1",
	}
	got, err := n.FromSessionRecord(rec)
	if err != nil {
		t.Fatalf("FromSessionRecord: %v", err)
	}
	if got.Session().Action != cp.SessionActionUpdated {
		t.Fatalf("unknown action must default to updated, got %q", got.Session().Action)
	}
}

func TestNormalizeSessionRecordRejectsUnsafeFreeText(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.SessionSourceRecord{
		SourceEventKey: "secure-create:sess-x",
		OccurredAt:     time.Now(),
		SessionID:      "Bearer secrettoken",
	}
	if _, err := n.FromSessionRecord(rec); err == nil {
		t.Fatalf("FromSessionRecord must reject credential-bearing session id")
	}
}

func TestNormalizeUsageRecordUsesExplicitSourceKeyAndDropsRawJSON(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	rec := controlplane.UsageSourceRecord{
		SourceEventKey: "secure-usage:sess-1:turn-1:bleg-1:2026-07-04T00:05:00Z",
		OccurredAt:     time.Now(),
		TraceID:        "trace-u",
		SessionID:      "sess-1",
		BLegID:         "bleg-1",
		AttemptSeq:     1,
		BackendID:      "openai",
		Model:          "gpt-4o",
		InputTokens:    100,
		OutputTokens:   50,
		TotalTokens:    150,
		CostNanoUnits:  1000,
		Currency:       "USD",
		CostSource:     "accounting",
		Scope:          new(knownScopeView()),
	}
	got, err := n.FromUsageRecord(rec)
	if err != nil {
		t.Fatalf("FromUsageRecord: %v", err)
	}
	if got.SourceEventKey != "secure-usage:sess-1:turn-1:bleg-1:2026-07-04T00:05:00Z" {
		t.Fatalf("source key lost: %q", got.SourceEventKey)
	}
	if got.Usage() == nil || got.Usage().InputTokens != 100 || got.Usage().TotalTokens != 150 {
		t.Fatalf("usage dimensions lost: %#v", got.Usage())
	}
	if got.Usage().Plane != cp.UsagePlaneObserved || got.Usage().Availability != cp.UsageAvailabilityObserved {
		t.Fatalf("plane/availability lost: %#v", got.Usage())
	}
	if err := controlplane.ValidateEvent(got); err != nil {
		t.Fatalf("normalized usage record invalid: %v", err)
	}
}

// ---- Zero-timestamp rejection: normalizer layer ----

// zeroClock returns the zero time.Time from Now(), so the normalizer fills
// OccurredAt and RecordedAt with the zero time and the resulting event must
// be rejected by ValidateEvent's explicit-guard check. This locks the
// zero-timestamp rejection at the normalizer layer (not just the SDK/core
// validator layers): a future change that weakens the normalizer's clock
// fallback or that bypasses the core validator would surface here.
type zeroClock struct{}

func (zeroClock) Now() time.Time { return time.Time{} }

// TestNormalizeRejectsZeroTimestamps regression-locks the zero-timestamp
// rejection at the normalizer layer. It exercises every From* entry point
// with a clock that returns the zero time, forcing the normalizer to fill in
// zero OccurredAt/RecordedAt. The resulting event must be rejected by the
// core ValidateEvent's explicit-guard check, and the error must mention
// "occurred_at" so the test cannot pass for the wrong reason (e.g., a
// different validator path masking the guard).
func TestNormalizeRejectsZeroTimestamps(t *testing.T) {
	t.Parallel()
	n := controlplane.NewNormalizer(
		zeroClock{},
		cp.SourceRef{Name: "test-source", Version: "v1"},
		controlplane.NewScopeFlattener(),
	)
	cases := []struct {
		name string
		call func() (cp.Event, error)
	}{
		{
			name: "auth_decision",
			call: func() (cp.Event, error) {
				return n.FromAuthDecision(auth.AuthDecisionEvent{
					TraceID: "trace-1",
					Outcome: auth.OutcomeAllow,
					Scope:   new(knownScopeView()),
				})
			},
		},
		{
			name: "session_start",
			call: func() (cp.Event, error) {
				return n.FromSessionStart(auth.SessionStartEvent{
					TraceID:   "trace-2",
					SessionID: "sess-2",
					IsNew:     true,
				})
			},
		},
		{
			name: "attempt",
			call: func() (cp.Event, error) {
				return n.FromAttempt(controlplane.AttemptSourceRecord{
					TraceID:  "trace-3",
					Surfaced: cp.AttemptSurfacedSurfaced,
					Outcome:  cp.AttemptOutcomeSucceeded,
				})
			},
		},
		{
			name: "usage",
			call: func() (cp.Event, error) {
				return n.FromUsage(usage.Event{TraceID: "trace-4"})
			},
		},
		{
			name: "policy_decision",
			call: func() (cp.Event, error) {
				return n.FromPolicyDecision(policydecision.Record{
					TraceID: "trace-5",
					Stage:   "pre_backend",
					Outcome: policydecision.OutcomeAllow,
				})
			},
		},
		{
			name: "audit",
			call: func() (cp.Event, error) {
				return n.FromAudit(controlplane.AuditSourceRecord{
					TraceID: "trace-6",
					Action:  "transcript.view",
				})
			},
		},
		{
			name: "session_record",
			call: func() (cp.Event, error) {
				return n.FromSessionRecord(controlplane.SessionSourceRecord{
					SourceEventKey: "secure-create:sess-1",
					SessionID:      "sess-1",
				})
			},
		},
		{
			name: "usage_record",
			call: func() (cp.Event, error) {
				return n.FromUsageRecord(controlplane.UsageSourceRecord{
					SourceEventKey: "secure-usage:sess-1:turn-1:bleg-1:2026-07-04T00:05:00Z",
				})
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := c.call()
			if err == nil {
				t.Fatalf("normalizer must reject zero-timestamp event from zeroClock; got nil error")
			}
			if !strings.Contains(err.Error(), "occurred_at is required") {
				t.Fatalf("error must mention explicit-guard 'occurred_at is required', got: %v", err)
			}
		})
	}
}

// ---- Multi-detail rejection: normalizer layer ----

// TestNormalizerEventWithMultipleDetailBlocksRejected regression-locks the
// explicit "exactly one detail block" guard at the normalizer layer. The
// normalizer's From* methods each produce exactly one detail, but a caller
// (or a future refactor) could mutate the result to add a second detail. The
// core ValidateEvent must reject such an event with the explicit-guard
// error, proving the normalizer → validator chain enforces the single-detail
// invariant at the normalizer layer (not just the SDK layer).
func TestNormalizerEventWithMultipleDetailBlocksRejected(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Now(),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
		Scope:   new(knownScopeView()),
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Replacing the auth detail with a session detail while category stays auth
	// must fail category/detail matching (single Detail field enforces one block).
	ev.Detail = &cp.SessionDetail{Action: cp.SessionActionCreated}
	err = controlplane.ValidateEvent(ev)
	if err == nil {
		t.Fatalf("ValidateEvent must reject category/detail mismatch")
	}
	if !strings.Contains(err.Error(), "requires auth detail") {
		t.Fatalf("error must mention category/detail mismatch, got: %v", err)
	}
}

// TestNormalizerEventWithUnsafeSummaryRejected regression-locks the
// unsafe-summary rejection path at the normalizer layer. The normalizer's
// From* methods don't set Summary (it's not in the source records), so a
// caller (or a future refactor) could mutate the result to add an unsafe
// summary. The core ValidateEvent must reject such an event with the
// explicit-guard error, proving the normalizer → validator chain enforces
// the unsafe-content invariant at the normalizer layer (not just the core
// validator layer).
func TestNormalizerEventWithUnsafeSummaryRejected(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Now(),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
		Scope:   new(knownScopeView()),
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate to add an unsafe summary (credential-like content).
	ev.Summary = "Bearer secrettoken"
	err = controlplane.ValidateEvent(ev)
	if err == nil {
		t.Fatalf("ValidateEvent must reject event with unsafe summary")
	}
	if !strings.Contains(err.Error(), "unsafe token-like content") {
		t.Fatalf("error must mention explicit-guard 'unsafe token-like content', got: %v", err)
	}
}

// TestNormalizerEventWithEmptySourceNameRejected regression-locks the
// empty-source-name rejection path at the normalizer layer. The normalizer
// sets Source from the constructor's cp.SourceRef, so the source name is
// always set. But a caller (or a future refactor) could mutate the result
// to set an empty source name. The core ValidateEvent must reject such an
// event with the explicit-guard error, proving the normalizer → validator
// chain enforces the source-name invariant at the normalizer layer.
func TestNormalizerEventWithEmptySourceNameRejected(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Now(),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
		Scope:   new(knownScopeView()),
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate to set an empty source name.
	ev.Source = cp.SourceRef{}
	err = controlplane.ValidateEvent(ev)
	if err == nil {
		t.Fatalf("ValidateEvent must reject event with empty source name")
	}
	if !strings.Contains(err.Error(), "source.name is required") {
		t.Fatalf("error must mention explicit-guard 'source.name is required', got: %v", err)
	}
}

// TestNormalizerEventWithPrivilegedVisibilityWithoutPrivilegedRedaction
// regression-locks the privileged-visibility guard at the normalizer layer.
// The normalizer's From* methods produce events with default visibility and
// redaction, but a caller (or a future refactor) could mutate the result to
// set privileged visibility without the matching privileged redaction state.
// The core ValidateEvent must reject such an event with the explicit-guard
// error, proving the normalizer → validator chain enforces the
// privileged-visibility invariant at the normalizer layer (not just the SDK
// and core validator layers).
func TestNormalizerEventWithPrivilegedVisibilityWithoutPrivilegedRedaction(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Now(),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
		Scope:   new(knownScopeView()),
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate the result to set privileged visibility without privileged redaction.
	ev.Visibility = cp.VisibilityPrivileged
	ev.RedactionState = cp.RedactionNone // explicitly NOT privileged
	err = controlplane.ValidateEvent(ev)
	if err == nil {
		t.Fatalf("ValidateEvent must reject privileged visibility without privileged redaction state")
	}
	if !strings.Contains(err.Error(), "privileged visibility requires privileged redaction state") {
		t.Fatalf("error must mention explicit-guard 'privileged visibility requires privileged redaction state', got: %v", err)
	}
}

// TestNormalizerEventWithRecordedAtBeforeOccurredAt regression-locks the
// ordering guard at the normalizer layer. The normalizer fills in
// OccurredAt from the source record and RecordedAt from the clock, so a
// normalizer output never has RecordedAt before OccurredAt. But a caller (or
// a future refactor) could mutate the result to set RecordedAt earlier than
// OccurredAt. The core ValidateEvent must reject such an event with the
// explicit-guard error, proving the normalizer → validator chain enforces
// the ordering invariant at the normalizer layer (not just the SDK and core
// validator layers).
func TestNormalizerEventWithRecordedAtBeforeOccurredAt(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Now(),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
		Scope:   new(knownScopeView()),
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate the result so RecordedAt is earlier than OccurredAt (both non-zero).
	ev.OccurredAt = ev.RecordedAt.Add(time.Minute) // OccurredAt is later; RecordedAt stays as the normalizer's clock value (earlier)
	err = controlplane.ValidateEvent(ev)
	if err == nil {
		t.Fatalf("ValidateEvent must reject RecordedAt before OccurredAt")
	}
	if !strings.Contains(err.Error(), "recorded_at precedes occurred_at") {
		t.Fatalf("error must mention explicit-guard 'recorded_at precedes occurred_at', got: %v", err)
	}
}

// TestNormalizerEventWithOversizedSummaryRejected regression-locks the
// summary-size rejection path at the normalizer layer (parallel to the
// core-level oversized_summary subtest in validate_test.go). The
// normalizer's From* methods don't set Summary (it's not in the source
// records), and the normalizer has no built-in summary-length cap. A
// caller (or a future refactor) could mutate the result to produce an
// oversized summary. The core ValidateEvent must reject such an event
// with the explicit-guard error, proving the normalizer → validator
// chain enforces the summary-size invariant at the normalizer layer (not
// just the core validator layer).
func TestNormalizerEventWithOversizedSummaryRejected(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Now(),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
		Scope:   new(knownScopeView()),
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate to add a summary that exceeds MaxSummaryBytes by one byte.
	ev.Summary = strings.Repeat("x", controlplane.MaxSummaryBytes+1)
	err = controlplane.ValidateEvent(ev)
	if err == nil {
		t.Fatalf("ValidateEvent must reject event with oversized summary")
	}
	if !strings.Contains(err.Error(), "summary exceeds") {
		t.Fatalf("error must mention explicit-guard 'summary exceeds', got: %v", err)
	}
}

// TestNormalizerEventWithOversizedScopeLabelsRejected regression-locks the
// scope-size rejection path at the normalizer layer (parallel to the
// core-level oversized_scope_labels subtest in validate_test.go). The
// normalizer projects scope from known safe fields, but a caller (or a
// future refactor) could mutate the result to add an oversized PolicyLabels
// map. The core ValidateEvent must reject such an event with the
// explicit-guard error, proving the normalizer → validator chain enforces
// the scope-size invariant at the normalizer layer (not just the core
// validator layer). Uses the shared itoa helper from validate_test.go
// (same controlplane_test package).
func TestNormalizerEventWithOversizedScopeLabelsRejected(t *testing.T) {
	t.Parallel()
	n := newTestNormalizer(t)
	ev, err := n.FromAuthDecision(auth.AuthDecisionEvent{
		Time:    time.Now(),
		TraceID: "trace-1",
		Outcome: auth.OutcomeAllow,
		Scope:   new(knownScopeView()),
	})
	if err != nil {
		t.Fatalf("FromAuthDecision: %v", err)
	}
	// Mutate Scope to add MaxScopeMapEntries+1 policy labels.
	labels := make(map[string]string, controlplane.MaxScopeMapEntries+1)
	for i := range controlplane.MaxScopeMapEntries + 1 {
		labels["k"+itoa(i)] = "v"
	}
	ev.Scope = cp.ScopeSnapshot{
		Principal: scope.PrincipalScopeView{PolicyLabels: labels},
	}
	err = controlplane.ValidateEvent(ev)
	if err == nil {
		t.Fatalf("ValidateEvent must reject event with oversized scope")
	}
	if !strings.Contains(err.Error(), "scope.policy_labels exceeds") {
		t.Fatalf("error must mention explicit-guard 'scope.policy_labels exceeds', got: %v", err)
	}
}

// helpers

//go:fix inline
func ptrScope(v scope.PrincipalScopeView) *scope.PrincipalScopeView { return new(v) }

func serializeEvent(t *testing.T, ev cp.Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
