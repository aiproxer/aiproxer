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

func TestRecorder_RecordClientTurnAfterGate_transcriptOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-ct")
	fp := domain.TokenFingerprint{}
	fp[0] = 9
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "u1"},
		Workspace:         domain.WorkspaceRef{ID: "w1"},
		Policy: domain.PolicyMetadata{
			PolicyVersion:     "v1",
			TranscriptEnabled: true,
			RedactionProfile:  "standard",
			AuditMode:         "best_effort",
		},
		ALegID: "a1", ResumeEligible: true, CreatedAt: time.Unix(10, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	in := app.ClientTurnRecordInput{
		Now:       time.Unix(20, 0),
		TraceID:   "tr1",
		SessionID: sid,
		TurnID:    "turn-1",
		Policy: domain.PolicyMetadata{
			TranscriptEnabled: true,
			RedactionProfile:  "standard",
		},
		Lines: []app.ClientInputLine{
			{Role: "user", Ordinal: 0, Parts: []string{"text"}},
			{Role: "tool", Ordinal: 1, Parts: []string{"tool_result"}},
		},
	}
	if err := rec.RecordClientTurnAfterGate(ctx, in); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Transcript(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != 2 {
		t.Fatalf("transcript len %d", len(tx))
	}
	if tx[0].Seq != 1 || tx[1].Seq != 2 {
		t.Fatalf("seq: %#v", tx)
	}
	if tx[0].EventKind != "client.input" || tx[1].EventKind != "client.input" {
		t.Fatalf("kind: %#v", tx)
	}
	var p0 map[string]any
	if err := json.Unmarshal([]byte(tx[0].PayloadRef), &p0); err != nil {
		t.Fatal(err)
	}
	if p0["role"] != "user" {
		t.Fatalf("payload: %#v", p0)
	}
}

func TestRecorder_RecordClientTurnAfterGate_transcriptDisabled_auditMeta(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-meta")
	fp := domain.TokenFingerprint{}
	fp[1] = 3
	_, err := st.Create(ctx, domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "u1"},
		Policy: domain.PolicyMetadata{
			TranscriptEnabled: false,
			RedactionProfile:  "strict",
			AuditMode:         "best_effort",
		},
		ALegID: "a2", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.RecordClientTurnAfterGate(ctx, app.ClientTurnRecordInput{
		Now:       time.Unix(2, 0),
		SessionID: sid,
		TurnID:    "t2",
		Policy: domain.PolicyMetadata{
			TranscriptEnabled: false,
			RedactionProfile:  "strict",
		},
		Lines: []app.ClientInputLine{{Role: "user", Ordinal: 0, Parts: []string{"text"}}},
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := st.Transcript(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != 0 {
		t.Fatalf("expected no transcript rows, got %d", len(tx))
	}
	aud, err := st.Audit(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(aud) != 1 || aud[0].Action != "client_turn_transcript_meta" {
		t.Fatalf("audit: %#v", aud)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(aud[0].Result), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["transcript_enabled"] != false {
		t.Fatalf("meta: %#v", meta)
	}
}
