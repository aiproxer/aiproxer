package continuation

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// TestStreamRecorderDeepCopiesItemAndDecisionPayloads proves the recorder
// retains no alias to the producer's arrays: mutating Item.Content and
// Decision.Answers after Observe must leave the recorded events unchanged.
func TestStreamRecorderDeepCopiesItemAndDecisionPayloads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := NewStreamRecorder(nil, ContinuationRecord{}, func() {})
	itemEvent := aipapi.Event{
		Kind: aipapi.EventItem,
		Item: &aipapi.Item{
			Kind: aipapi.ItemKindMessage, Role: aipapi.RoleUser, Status: aipapi.ItemStatusCompleted,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "original-item-text"}},
		},
	}
	conf := 0.7
	decisionEvent := aipapi.Event{
		Kind: aipapi.EventDecisionResult,
		Decision: &aipapi.DecisionResult{
			Model: "m",
			Answers: []aipapi.DecisionAnswer{{
				QuestionID: "q", Kind: aipapi.DecisionKindChoice, Selected: "original-selected",
				Probabilities: []float64{1.0}, Confidence: &conf,
			}},
		},
	}
	r.Observe(ctx, itemEvent)
	r.Observe(ctx, decisionEvent)
	itemEvent.Item.Content[0].Text = "mutated"
	decisionEvent.Decision.Answers[0].Selected = "mutated"
	decisionEvent.Decision.Answers[0].Probabilities[0] = 0
	*decisionEvent.Decision.Answers[0].Confidence = 0
	if len(r.events) != 2 {
		t.Fatalf("recorded %d events, want 2", len(r.events))
	}
	if got := r.events[0].Item.Content[0].Text; got != "original-item-text" {
		t.Fatalf("recorded item text=%q, want original", got)
	}
	if got := r.events[1].Decision.Answers[0].Selected; got != "original-selected" {
		t.Fatalf("recorded answer selected=%q, want original", got)
	}
	if got := r.events[1].Decision.Answers[0].Probabilities[0]; got != 1.0 {
		t.Fatalf("recorded probability=%v, want 1.0", got)
	}
	if got := *r.events[1].Decision.Answers[0].Confidence; got != 0.7 {
		t.Fatalf("recorded confidence=%v, want 0.7", got)
	}
}

// TestAuthorityParity exercises the SDK authority's complete mutable-state and
// recorder lifecycle surface. Core now delegates to this implementation.
func TestAuthorityParity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	scope := Scope{TenantID: "tenant", PrincipalID: "principal", SessionID: "session"}
	store := NewMemoryStoreWithLimits(StorageLimits{MaxRecords: 4, MaxRecordBytes: 4096, MaxBytes: 1 << 20, MaxChainDepth: 4})
	now := time.Unix(1700000000, 0)
	store.SetClock(func() time.Time { return now })
	policy := StoragePolicy{Mode: PersistencePersistent, TTL: time.Hour, AllowIncomplete: true}
	id, err := store.Reserve(ctx, scope, policy)
	if err != nil {
		t.Fatal(err)
	}
	record := ContinuationRecord{ID: id, Scope: scope, Policy: policy, Terminal: true, Status: RecordStatusCompleted, InputItems: []aipapi.Item{{Kind: aipapi.ItemKindMessage}}}
	if err := store.PutTerminal(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(ctx, store, Scope{TenantID: "other", PrincipalID: "principal", SessionID: "session"}, id); err != ErrPreviousResponseNotFound {
		t.Fatalf("scope isolation error=%v", err)
	}
	got, err := Lookup(ctx, store, scope, id)
	if err != nil || got.ID != id {
		t.Fatalf("lookup=%+v err=%v", got, err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := Lookup(ctx, store, scope, id); err != ErrPreviousResponseNotFound {
		t.Fatalf("expiry error=%v", err)
	}

	id, err = store.Reserve(ctx, scope, policy)
	if err != nil {
		t.Fatal(err)
	}
	cleanup := 0
	recorder := NewStreamRecorder(TerminalRecorder{Store: store}, ContinuationRecord{ID: id, Scope: scope, Policy: policy, InputItems: []aipapi.Item{{Kind: aipapi.ItemKindMessage}}}, func() { cleanup++ })
	recorder.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "ok"})
	recorder.Observe(ctx, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "stop"})
	recorder.Observe(ctx, aipapi.Event{Kind: aipapi.EventResponseFinished, FinishReason: "stop"})
	if cleanup != 0 || !recorder.ContinuationReservationCleanupConsumed() {
		t.Fatalf("terminal lifecycle cleanup=%d consumed=%v", cleanup, recorder.ContinuationReservationCleanupConsumed())
	}
	if _, err := Lookup(ctx, store, scope, id); err != nil {
		t.Fatal(err)
	}

	id, err = store.Reserve(ctx, scope, policy)
	if err != nil {
		t.Fatal(err)
	}
	incomplete := NewStreamRecorder(TerminalRecorder{Store: store}, ContinuationRecord{ID: id, Scope: scope, Policy: policy}, func() { cleanup++ })
	incomplete.Observe(ctx, aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "partial"})
	if err := incomplete.FinalizeIncomplete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := incomplete.Close(); err != nil || cleanup != 0 {
		t.Fatalf("incomplete lifecycle err=%v cleanup=%d", err, cleanup)
	}
	if err := store.Close(); err != nil || store.Close() != nil {
		t.Fatalf("close lifecycle err=%v", err)
	}
}
