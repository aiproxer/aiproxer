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

func TestRecorder_providerCorrelationDoesNotMutateSessionAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := memory.New(memory.Options{})
	sid := domain.SessionID("sess-corr")
	fp := domain.TokenFingerprint{}
	fp[6] = 6
	created, err := st.Create(ctx, domain.CreateRecord{
		SessionID:         sid,
		ResumeFingerprint: fp,
		Owner:             domain.PrincipalRef{ID: "p1"},
		Policy: domain.PolicyMetadata{
			TranscriptEnabled: true,
			RedactionProfile:  "standard",
		},
		ALegID: "a-corr", ResumeEligible: true, CreatedAt: time.Unix(1, 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.NewRecorder(st)
	if err != nil {
		t.Fatal(err)
	}
	corr := `{"vendor_conversation_id":"conv_upstream_123"}`
	if err := rec.RecordPostHookStreamEvent(ctx, app.StreamEventRecordInput{
		Now: time.Unix(2, 0), SessionID: sid, TurnID: "t1", BLegID: "b1", BackendID: "be",
		Policy: domain.PolicyMetadata{
			TranscriptEnabled: true,
			RedactionProfile:  "standard",
		},
		EventKind:               "response_started",
		EventPayloadJSON:        `{"kind":"response_started"}`,
		ProviderCorrelationJSON: corr,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := st.LoadByID(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if after.SessionID != created.SessionID || after.ALegID != created.ALegID {
		t.Fatalf("session authority drift: before=%+v after=%+v", created, after)
	}
	tx, err := st.Transcript(ctx, sid, domain.ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tx) != 1 {
		t.Fatalf("transcript: %d", len(tx))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(tx[0].PayloadRef), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["provider_correlation"]; !ok {
		t.Fatalf("missing provider_correlation: %#v", payload)
	}
}
