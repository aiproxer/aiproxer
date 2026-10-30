package leasestore

import (
	"context"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
)

func TestDurableStore_CheckReadiness_NilReceiver(t *testing.T) {
	t.Parallel()
	var store *DurableStore
	got, err := store.CheckReadiness(context.Background())
	if err != nil {
		t.Fatalf("error=%v", err)
	}
	if got.State != domain.ReadinessStateUnavailable {
		t.Fatalf("state=%v want unavailable", got.State)
	}
	store = &DurableStore{}
	got, err = store.CheckReadiness(context.Background())
	if err != nil {
		t.Fatalf("error=%v", err)
	}
	if got.State != domain.ReadinessStateUnavailable {
		t.Fatalf("nil db state=%v want unavailable", got.State)
	}
}

func TestDurableStore_Release_NilReceiver(t *testing.T) {
	t.Parallel()
	var store *DurableStore
	_, err := store.Release(context.Background(), app.ReleaseCommand{})
	if err == nil || !strings.Contains(err.Error(), "nil store") {
		t.Fatalf("error=%v want nil store", err)
	}
}
