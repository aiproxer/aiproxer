package app_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestPhase45_ReorderedHandlesAreIdempotentReplay(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 7, 10, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "intent-reorder",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Clock: func() time.Time { return clock }})
	in := app.SettleFailureInput{
		RequestID:  "req-reorder",
		AttemptID:  "att-1",
		TraceID:    "tr-1",
		ProviderID: "quota",
		Handles:    []string{"h-b", "h-a"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}
	if err := svc.AcceptSettleFailure(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	// Same handles, different order — must SameIntentReplay, not collision.
	in.Handles = []string{"h-a", "h-b"}
	if err := svc.AcceptSettleFailure(context.Background(), in); err != nil {
		t.Fatalf("reordered handles must be idempotent replay: %v", err)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-reorder",
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("rows=%d want 1", len(page.Records))
	}
	var payload struct {
		Handles []string `json:"handles"`
	}
	if err := json.Unmarshal(page.Records[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Handles) != 2 || payload.Handles[0] != "h-a" || payload.Handles[1] != "h-b" {
		t.Fatalf("payload handles=%v want sorted [h-a h-b]", payload.Handles)
	}
}

func TestPhase45_DistinctAttemptsDoNotCollide(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 7, 15, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "intent-attempt",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Clock: func() time.Time { return clock }})
	base := app.SettleFailureInput{
		RequestID:  "req-att",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}
	a := base
	a.AttemptID = "att-1"
	a.TraceID = "tr-1"
	b := base
	b.AttemptID = "att-2"
	b.TraceID = "tr-1"
	if err := svc.AcceptSettleFailure(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if err := svc.AcceptSettleFailure(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-att",
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 2 {
		t.Fatalf("rows=%d want 2 distinct attempt identities", len(page.Records))
	}
	if page.Records[0].WorkID == page.Records[1].WorkID {
		t.Fatal("distinct attempts must not share WorkID")
	}
}
