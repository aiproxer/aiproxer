package app_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// Phase 4.5 RED/GREEN: bounded terminal-work queries, backlog/oldest-age metrics,
// operator-safe rows (requirements 8.9, 12.5–12.8; design D14).

func TestPhase45_QueryServiceRejectsTooBroadAndUnsupported(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "q-too-broad"})
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewQueryService(store)
	_, err = svc.List(context.Background(), app.WorkQuery{Limit: 10})
	if !errors.Is(err, app.ErrQueryTooBroad) {
		t.Fatalf("got %v want ErrQueryTooBroad", err)
	}
	_, err = svc.List(context.Background(), app.WorkQuery{
		RequestID: "req-1",
		Class:     app.QueryClassFinancialProjection,
		Limit:     10,
	})
	if !errors.Is(err, app.ErrQueryUnsupported) {
		t.Fatalf("got %v want ErrQueryUnsupported", err)
	}
}

func TestPhase45_QueryServiceReturnsSafeRowsWithoutRawContent(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "q-safe",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	secret := "RAW_PROMPT_DO_NOT_LEAK"
	rec := terminalwork.WorkRecord{
		WorkID:         "w-safe",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk-safe"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStateIntent,
		ProviderID:     "prov-a",
		Lifecycle:      terminalwork.LifecycleCorrelation{RequestID: "req-safe", AttemptID: "att-1", TraceID: "tr-1"},
		Versions:       terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "prov-a", RatingID: "r1"},
		Payload:        []byte(`{"note":"` + secret + `"}`),
		Error:          terminalwork.BoundedError{Code: "outage", Message: "provider unavailable"},
	}
	if err := store.AppendIntent(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: clock}); err != nil {
		t.Fatal(err)
	}
	svc := app.NewQueryService(store)
	page, err := svc.List(context.Background(), app.WorkQuery{
		RequestID: "req-safe",
		Class:     app.QueryClassPendingTerminalWork,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 1 {
		t.Fatalf("rows=%d want 1", len(page.Rows))
	}
	row := page.Rows[0]
	if row.WorkID != "w-safe" || row.State != sdk.WorkStatePending || row.ProviderID != "prov-a" {
		t.Fatalf("row=%+v", row)
	}
	if row.ErrorCode != "outage" {
		t.Fatalf("error_code=%q", row.ErrorCode)
	}
	encoded := row.WorkID + row.SourceKey + row.ProviderID + row.ErrorCode + row.Kind.String()
	if strings.Contains(encoded, secret) {
		t.Fatalf("raw content leaked into query row: %+v", row)
	}
	// Operator rows must not expose raw payload bytes.
	if len(row.Payload) != 0 {
		t.Fatalf("payload must be omitted from operator query rows, got %q", row.Payload)
	}
}

func TestPhase45_MetricsSnapshotBacklogAndOldestAge(t *testing.T) {
	t.Parallel()
	old := time.Date(2026, 7, 18, 4, 0, 0, 0, time.UTC)
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "m-backlog",
		Now:     func() time.Time { return old },
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"w1", "w2"} {
		rec := terminalwork.WorkRecord{
			WorkID:         id,
			SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk-" + id},
			PayloadVersion: 1,
			Kind:           sdk.WorkKindSettleRequestProvider,
			State:          sdk.WorkStateIntent,
			ProviderID:     "prov-a",
			Lifecycle:      terminalwork.LifecycleCorrelation{RequestID: "req-m", AttemptID: "a", TraceID: "t"},
			Versions:       terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "prov-a"},
			Payload:        []byte(`{}`),
		}
		if err := store.AppendIntent(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		if err := store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: old.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	obs := app.NewMetricsObserver(store, app.MetricsConfig{Clock: func() time.Time { return now }})
	snap, err := obs.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Backlog != 2 {
		t.Fatalf("backlog=%d want 2", snap.Backlog)
	}
	if snap.OldestAge < time.Hour-time.Second || snap.OldestAge > time.Hour+time.Second {
		t.Fatalf("oldest_age=%s want ~1h", snap.OldestAge)
	}
	if snap.Pending != 2 || snap.Retrying != 0 || snap.Quarantined != 0 || snap.Completed != 0 {
		t.Fatalf("counts=%+v", snap)
	}
}
