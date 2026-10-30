package controlplane_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func TestQueryServiceRejectsTooBroadUsageBeforeStoreAccess(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	_, err := qs.Usage(context.Background(), cp.UsageQuery{Limit: 10})
	if !errors.Is(err, controlplane.ErrTooBroad) {
		t.Fatalf("got %v", err)
	}
	if store.usageCalled {
		t.Fatal("too-broad usage query must not reach store")
	}
}

func TestQueryServiceAcceptsBoundedUsageWithScopeFilter(t *testing.T) {
	t.Parallel()
	store := &queryProbeStore{}
	qs, _ := newQueryService(store, true, 0)
	_, err := qs.Usage(context.Background(), cp.UsageQuery{
		Common: cp.CommonFilters{Scope: cp.ScopeFilters{TenantID: scope.Known("ten-1")}},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("got %v", err)
	}
	if !store.usageCalled {
		t.Fatal("bounded usage query must reach store")
	}
}
