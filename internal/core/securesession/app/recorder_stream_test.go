package app_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

func TestRecorder_RecordPostHookStreamEvent_orderAndUsage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-stream")
	fp := domain.TokenFingerprint{}
	fp[2] = 7
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "owner-x"},
		Workspace:         domain.WorkspaceRef{ID: "ws-x"},
		Policy: domain.PolicyMetadata{
			TranscriptEnabled: true,
			RedactionProfile:  "standard",
		},
		ALegID: "aleg", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	pol := domain.PolicyMetadata{TranscriptEnabled: true, RedactionProfile: "standard"}
	t0 := time.Unix(100, 0)
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: t0, SessionID: sid, TurnID: "t1", BLegID: "b1", BackendID: "be",
		Policy: pol, EventKind: "text_delta", EventPayloadJSON: `{"kind":"text_delta"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: t0.Add(time.Second), SessionID: sid, TurnID: "t1", BLegID: "b1", BackendID: "be",
		Policy: pol, EventKind: "usage_delta", IsUsageEvent: true,
		InputTokens: 3, OutputTokens: 2,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadByID(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastActivitySource != domain.ActivityRemoteEvent {
		t.Fatalf("activity source: %s", got.LastActivitySource)
	}
	tx, err := st.Transcript(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != 2 {
		t.Fatalf("want 2 transcript rows, got %d", len(tx))
	}
	if tx[0].Seq >= tx[1].Seq {
		t.Fatalf("seq order %d %d", tx[0].Seq, tx[1].Seq)
	}
}

func TestRecorder_RecordPostHookStreamEvent_usageUnavailableMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-usage-unavail")
	fp := domain.TokenFingerprint{}
	fp[3] = 1
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp, Owner: domain.PrincipalRef{ID: "o"},
		Policy: domain.PolicyMetadata{TranscriptEnabled: false},
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
		Now: time.Unix(2, 0), SessionID: sid, TurnID: "t1", BLegID: "b9", BackendID: "bk",
		Policy: domain.PolicyMetadata{}, EventKind: "usage_delta", IsUsageEvent: true,
		BillingUnavailable: true,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.Audit(ctx, sid, domain.ReadOptions{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var sawUsage bool
	for _, a := range rows {
		if a.Action != "stream_post_hook" {
			continue
		}
		var env map[string]any
		if json.Unmarshal([]byte(a.Result), &env) != nil {
			continue
		}
		if env["usage"] == true {
			sawUsage = true
		}
	}
	if !sawUsage {
		t.Fatalf("expected usage in audit trail, got %#v", rows)
	}
}
