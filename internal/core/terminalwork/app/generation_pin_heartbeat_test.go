package app_test

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
)

func TestGenerationPin_Heartbeat_LeaseSetReleaseRetainsPin(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "genpin-hb"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "21", pin: pin})
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	if err := svc.AcceptLeaseSetRelease(ctx, app.LeaseSetReleaseInput{
		RequestID:  "req-hb",
		AttemptID:  "a",
		LeaseSetID: "set-1",
		Reason:     "renew_fail_closed",
		Versions:   terminalwork.BoundVersions{GenerationID: "4", ProviderID: "concurrency"},
	}); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 1 {
		t.Fatalf("pins=%d want 1 after heartbeat durable handoff", pins.Len())
	}
	page, err := store.List(context.Background(), workstore.Query{RequestID: "req-hb", Limit: 10})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("list: %v n=%d", err, len(page.Records))
	}
	if page.Records[0].Versions.RuntimeGenerationID != "21" {
		t.Fatalf("runtime gen=%q", page.Records[0].Versions.RuntimeGenerationID)
	}
	if page.Records[0].Versions.ExecutableGenerationID() != "4" {
		t.Fatalf("executable gen=%q", page.Records[0].Versions.ExecutableGenerationID())
	}
	pins.Release(page.Records[0].WorkID)
	if pin.releases.Load() != 1 || pins.Len() != 0 {
		t.Fatalf("releases=%d pins=%d", pin.releases.Load(), pins.Len())
	}
}
