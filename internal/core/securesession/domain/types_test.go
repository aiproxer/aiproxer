package domain

import (
	"testing"
	"time"
)

func TestConstructRecordAndNestedTypes_NoAnyOrUntypedMaps(t *testing.T) {
	t.Parallel()

	principal := PrincipalRef{ID: "user-1", Issuer: "iss", Tenant: "t1"}
	ws := WorkspaceRef{ID: "ws-a"}
	hints := ClientHints{ClientSessionID: "client-hint", AgentIdentityDigest: "sha256:abc"}
	policy := PolicyMetadata{
		PolicyVersion:            "v2",
		TranscriptEnabled:        true,
		EffectiveTreatment:       "standard",
		StricterPolicyResolution: "none",
		RouteHint:                "default",
		RedactionProfile:         "standard",
		AuditMode:                "best_effort",
	}
	trace := AttemptTrace{
		SessionID:       SessionID("sid-1"),
		TurnID:          TurnID("turn-1"),
		ALegID:          "aleg-1",
		BLegID:          "bleg-1",
		AttemptSeq:      1,
		RequestedModel:  "gpt-4",
		RequestedAlias:  "alias",
		ResolvedBackend: "openai-responses",
		ResolvedModel:   "gpt-4o",
		RouteSource:     "header",
		RouteReason:     "explicit",
		Settings:        AttemptSettings{Streaming: true, ReasoningEffort: "medium"},
		StartedAt:       time.Unix(1700, 0).UTC(),
	}
	outcome := AttemptOutcome{
		SessionID:      SessionID("sid-1"),
		TurnID:         TurnID("turn-1"),
		BLegID:         "bleg-1",
		Success:        true,
		SurfaceState:   SurfaceSurfaced,
		HTTPStatus:     200,
		ProviderStatus: "ok",
		ErrorCode:      "",
		TimeoutClass:   "",
		DebugReason:    "",
		EndedAt:        time.Unix(1701, 0).UTC(),
	}
	acct := AttemptAccounting{
		BLegID:             "bleg-1",
		InputTokens:        10,
		OutputTokens:       20,
		CacheReadTokens:    0,
		CacheWriteTokens:   0,
		CostMinorUnits:     0,
		Currency:           "USD",
		BillingUnavailable: false,
	}
	rec := Record{
		SessionID:               SessionID("sid-1"),
		ResumeFingerprint:       TokenFingerprint{0x01},
		Owner:                   principal,
		Workspace:               ws,
		ClientHints:             hints,
		Policy:                  policy,
		ALegID:                  "aleg-1",
		ResumeEligible:          true,
		LastActivityAt:          time.Unix(1600, 0).UTC(),
		LastActivitySource:      ActivityClientRequest,
		CreatedAt:               time.Unix(1500, 0).UTC(),
		LatestAttemptTrace:      trace,
		LatestAttemptOutcome:    outcome,
		LatestAttemptAccounting: acct,
	}
	if rec.SessionID != SessionID("sid-1") {
		t.Fatalf("session id")
	}
	if !rec.Policy.TranscriptEnabled {
		t.Fatalf("transcript flag")
	}
	if rec.LatestAttemptTrace.Settings.Streaming != true {
		t.Fatalf("attempt settings")
	}
}

func TestCreateRecord_FingerprintOnlyNoRawToken(t *testing.T) {
	t.Parallel()

	fp := TokenFingerprint{byte(0xab)}
	cr := CreateRecord{
		SessionID:         SessionID("new-sid"),
		ResumeFingerprint: fp,
		Owner:             PrincipalRef{ID: "u"},
		Workspace:         WorkspaceRef{ID: "w"},
		ClientHints:       ClientHints{},
		Policy:            PolicyMetadata{PolicyVersion: "1"},
		ALegID:            "a1",
		ResumeEligible:    true,
		CreatedAt:         time.Now().UTC(),
	}
	if cr.ResumeFingerprint != fp {
		t.Fatalf("fingerprint")
	}
}

func TestSummaryAndQueryOptions(t *testing.T) {
	t.Parallel()

	opts := ReadOptions{Limit: 50, AfterSeq: 10}
	q := SummaryQuery{OwnerID: "u1", WorkspaceID: "w1", Limit: 100}
	sum := Summary{
		SessionID:      SessionID("s1"),
		OwnerID:        "u1",
		WorkspaceID:    "w1",
		LastActivityAt: time.Unix(1, 0).UTC(),
		TurnCount:      3,
		AttemptCount:   5,
	}
	if opts.Limit != 50 || q.Limit != 100 {
		t.Fatalf("limits")
	}
	if sum.TurnCount != 3 {
		t.Fatalf("summary")
	}
}

func TestTranscriptAndAuditItems(t *testing.T) {
	t.Parallel()

	tr := TranscriptItem{
		SessionID:  SessionID("s"),
		TurnID:     TurnID("t"),
		Seq:        1,
		EventKind:  "message.delta",
		PayloadRef: "blob:1",
		CreatedAt:  time.Unix(2, 0).UTC(),
	}
	au := AuditItem{
		SessionID: SessionID("s"),
		TurnID:    TurnID("t"),
		Seq:       1,
		Action:    "append_transcript",
		Result:    "ok",
		CreatedAt: time.Unix(3, 0).UTC(),
	}
	ut := UsageTotals{
		SessionID:    SessionID("s"),
		InputTokens:  1,
		OutputTokens: 2,
		Attempts:     1,
	}
	if tr.EventKind != "message.delta" || au.Action != "append_transcript" || ut.OutputTokens != 2 {
		t.Fatalf("items")
	}
}
