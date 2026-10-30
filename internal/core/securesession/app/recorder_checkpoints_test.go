package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
)

// Checkpoint: last-activity moves on remote canonical events.
func TestRecorder_checkpoint_lastActivityRemote(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-la")
	fp := domain.TokenFingerprint{}
	fp[4] = 2
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp, Owner: domain.PrincipalRef{ID: "o"},
		Policy: domain.PolicyMetadata{TranscriptEnabled: false},
		ALegID: "a", ResumeEligible: true, CreatedAt: time.Unix(50, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Unix(60, 0)
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: at, SessionID: sid, TurnID: "t1", BLegID: "b", BackendID: "x",
		Policy: domain.PolicyMetadata{}, EventKind: "message_started",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadByID(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastActivityAt.Equal(at) {
		t.Fatalf("last activity: %v want %v", got.LastActivityAt, at)
	}
	if got.LastActivitySource != domain.ActivityRemoteEvent {
		t.Fatalf("source: %s", got.LastActivitySource)
	}
}

// Checkpoint: usage row ties to B-leg id for rollup dimensions.
func TestRecorder_checkpoint_usageBLegDimension(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-ub")
	fp := domain.TokenFingerprint{}
	fp[5] = 5
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID: sid, ResumeFingerprint: fp, Owner: domain.PrincipalRef{ID: "owner-ub"},
		Workspace: domain.WorkspaceRef{ID: "ws-ub"},
		Policy:    domain.PolicyMetadata{TranscriptEnabled: false},
		ALegID:    "a", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: time.Unix(2, 0), SessionID: sid, TurnID: "t1", BLegID: "bleg-dim", BackendID: "openai",
		Policy: domain.PolicyMetadata{}, EventKind: "usage_delta", IsUsageEvent: true,
		InputTokens: 10, OutputTokens: 4,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadByID(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if got.LatestAttemptAccounting.BLegID != "bleg-dim" {
		t.Fatalf("accounting bleg: %#v", got.LatestAttemptAccounting)
	}
	if got.LatestAttemptAccounting.InputTokens != 10 || got.LatestAttemptAccounting.OutputTokens != 4 {
		t.Fatalf("accounting tokens: %#v", got.LatestAttemptAccounting)
	}
}
