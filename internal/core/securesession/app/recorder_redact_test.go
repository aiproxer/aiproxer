package app_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestRecorder_auditStrictRedactsCorrelation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-redact")
	fp := domain.TokenFingerprint{}
	fp[7] = 7
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp, Owner: domain.PrincipalRef{ID: "o"},
		Policy: domain.PolicyMetadata{TranscriptEnabled: false, RedactionProfile: "strict"},
		ALegID: "a", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: time.Unix(2, 0), SessionID: sid, TurnID: "t1", BLegID: "b", BackendID: "bk",
		Policy: domain.PolicyMetadata{
			TranscriptEnabled: false,
			RedactionProfile:  "strict",
		},
		EventKind:               "response_started",
		ProviderCorrelationJSON: `{"secret":"x"}`,
	}); err != nil {
		t.Fatal(err)
	}
	aud, err := st.Audit(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var sawRedacted bool
	for _, a := range aud {
		if a.Action != "stream_post_hook" {
			continue
		}
		if !strings.Contains(a.Result, "provider_correlation") {
			continue
		}
		if strings.Contains(a.Result, "secret") {
			t.Fatalf("leaked secret: %s", a.Result)
		}
		var env map[string]any
		_ = json.Unmarshal([]byte(a.Result), &env)
		pc, ok := env["provider_correlation"].(map[string]any)
		if ok && pc["redacted"] == true {
			sawRedacted = true
		}
	}
	if !sawRedacted {
		t.Fatalf("expected redacted provider correlation in audit: %#v", aud)
	}
}

func TestRecorder_auditRawDeniedByDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-raw-deny")
	fp := domain.TokenFingerprint{}
	fp[8] = 8
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp, Owner: domain.PrincipalRef{ID: "o"},
		Policy: domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "best_effort"},
		ALegID: "a", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: time.Unix(2, 0), SessionID: sid, TurnID: "t1", BLegID: "b", BackendID: "bk",
		Policy:           domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "best_effort"},
		EventKind:        "text_delta",
		EventPayloadJSON: `{"delta":"hello"}`,
	}); err != nil {
		t.Fatal(err)
	}
	aud, err := st.Audit(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range aud {
		if a.Action != "stream_post_hook" {
			continue
		}
		if !strings.Contains(a.Result, `"raw_payload_denied":true`) {
			t.Fatalf("expected raw denial: %s", a.Result)
		}
		if strings.Contains(a.Result, `"delta":"hello"`) {
			t.Fatalf("raw payload leaked: %s", a.Result)
		}
	}
}

func TestRecorder_auditFullModeEmbedsEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-full-audit")
	fp := domain.TokenFingerprint{}
	fp[9] = 9
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp, Owner: domain.PrincipalRef{ID: "o"},
		Policy: domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "full"},
		ALegID: "a", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: time.Unix(2, 0), SessionID: sid, TurnID: "t1", BLegID: "b", BackendID: "bk",
		Policy:           domain.PolicyMetadata{TranscriptEnabled: false, AuditMode: "full"},
		EventKind:        "text_delta",
		EventPayloadJSON: `{"delta":"visible"}`,
	}); err != nil {
		t.Fatal(err)
	}
	aud, err := st.Audit(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, a := range aud {
		if a.Action == "stream_post_hook" && strings.Contains(a.Result, "visible") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected full audit payload: %#v", aud)
	}
}
