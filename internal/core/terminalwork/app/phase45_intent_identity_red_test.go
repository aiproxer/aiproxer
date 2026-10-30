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

func TestPhase45_IntentIdentityUsesHashNotConcatenation(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 6, 0, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "intent-hash",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Clock: func() time.Time { return clock }})
	secret := "SECRET_HANDLE_VALUE"
	hostileReq := "req:with|delims"
	hostileProv := "prov:a|b"
	hostileHandle := "h1:" + secret + "|extra"
	if err := svc.AcceptSettleFailure(context.Background(), app.SettleFailureInput{
		RequestID:  hostileReq,
		ProviderID: hostileProv,
		Handles:    []string{hostileHandle, "h2"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: hostileProv},
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: hostileReq,
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("rows=%d want 1", len(page.Records))
	}
	rec := page.Records[0]
	idBlob := rec.WorkID + rec.SourceKey.Key + rec.SourceKey.String()
	for _, leak := range []string{hostileReq, hostileProv, hostileHandle, secret, "settle:"} {
		if strings.Contains(idBlob, leak) {
			t.Fatalf("identity leaked %q into work_id/source_key: work_id=%q source=%q", leak, rec.WorkID, rec.SourceKey.String())
		}
	}
	if !strings.HasPrefix(rec.WorkID, "tw_") {
		t.Fatalf("work_id=%q want tw_ hash prefix", rec.WorkID)
	}
	if rec.SourceKey.IdentityVersion != 1 || !strings.HasPrefix(rec.SourceKey.Key, "sk_") {
		t.Fatalf("source_key=%+v want v1 sk_ hash", rec.SourceKey)
	}
	// Replay must be idempotent under the same logical identity.
	if err := svc.AcceptSettleFailure(context.Background(), app.SettleFailureInput{
		RequestID:  hostileReq,
		ProviderID: hostileProv,
		Handles:    []string{hostileHandle, "h2"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: hostileProv},
	}); err != nil {
		t.Fatal(err)
	}
	page2, err := store.List(context.Background(), workstore.Query{
		RequestID: hostileReq,
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page2.Records) != 1 {
		t.Fatalf("idempotent replay rows=%d want 1", len(page2.Records))
	}
	if page2.Records[0].WorkID != rec.WorkID {
		t.Fatalf("work_id changed on replay: %q vs %q", page2.Records[0].WorkID, rec.WorkID)
	}
}

func TestPhase45_ReleaseIntentIdentityHashedAndIdempotent(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 6, 5, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "intent-release-hash",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Clock: func() time.Time { return clock }})
	reqID := "req|rel:1"
	provID := "quota:prov"
	handle := "lease:h|secret"
	in := app.ReleaseFailureInput{
		RequestID:  reqID,
		ProviderID: provID,
		Handle:     handle,
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: provID},
	}
	if err := svc.AcceptReleaseFailure(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if err := svc.AcceptReleaseFailure(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: reqID,
		States:    []sdk.WorkState{sdk.WorkStatePending, sdk.WorkStateIntent},
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) != 1 {
		t.Fatalf("rows=%d want 1", len(page.Records))
	}
	blob := page.Records[0].WorkID + page.Records[0].SourceKey.Key
	for _, leak := range []string{reqID, provID, handle, "release:"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("release identity leaked %q: %q", leak, blob)
		}
	}
}

func TestPhase45_MetricsSnapshotFailsLoudOnQueryError(t *testing.T) {
	t.Parallel()
	obs := app.NewMetricsObserver(failingQueryStore{}, app.MetricsConfig{})
	snap, err := obs.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected snapshot error on store failure")
	}
	if snap.Backlog != 0 || snap.Pending != 0 {
		t.Fatalf("failed snapshot must not invent ready-zero counts: %+v", snap)
	}
	if !errors.Is(err, errQueryBoom) {
		t.Fatalf("got %v want errQueryBoom", err)
	}
}

type failingQueryStore struct{}

var errQueryBoom = errors.New("query boom")

func (failingQueryStore) List(context.Context, terminalwork.ListQuery) (terminalwork.ListPage, error) {
	return terminalwork.ListPage{}, errQueryBoom
}
