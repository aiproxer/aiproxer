package ledgerstore

import (
	"context"
	"testing"
	"time"

	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

func TestMemoryStore_UsageAppliesDualPlaneFilters(t *testing.T) {
	t.Parallel()
	store, err := NewMemoryStore(MemoryConfig{StoreID: "dual-plane-usage"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Unix(100, 0).UTC()
	mustAppendUsage(t, store, usageEvent("u1", cp.UsagePerspectiveOperator, cp.UsageBoundaryBackendEgress, now))
	mustAppendUsage(t, store, usageEvent("u2", cp.UsagePerspectiveCustomer, cp.UsageBoundaryFrontendIngress, now.Add(time.Second)))

	page, err := store.Usage(context.Background(), cp.UsageQuery{
		Common:      cp.CommonFilters{ALegID: "a-1"},
		Perspective: cp.UsagePerspectiveCustomer,
		Boundary:    cp.UsageBoundaryFrontendIngress,
		Limit:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items=%d unsupported=%v", len(page.Items), page.Unsupported)
	}
	if page.Items[0].Perspective != cp.UsagePerspectiveCustomer {
		t.Fatalf("%#v", page.Items[0])
	}
	for _, u := range page.Unsupported {
		if u.Field == "usage.perspective" || u.Field == "usage.boundary" || u.Field == "usage.lifecycle_scope" {
			t.Fatalf("applied filters must not be reported unsupported: %+v", page.Unsupported)
		}
	}
}

func TestMemoryStore_UsageRejectsRuleIDAsUnsupported(t *testing.T) {
	t.Parallel()
	store, err := NewMemoryStore(MemoryConfig{StoreID: "usage-rule"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	mustAppendUsage(t, store, usageEvent("u1", cp.UsagePerspectiveOperator, cp.UsageBoundaryBackendEgress, time.Unix(1, 0).UTC()))

	page, err := store.Usage(context.Background(), cp.UsageQuery{
		Common: cp.CommonFilters{ALegID: "a-1"},
		RuleID: "rule-x",
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, u := range page.Unsupported {
		if u.Field == "usage.rule_id" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected usage.rule_id unsupported, got %#v", page.Unsupported)
	}
	if len(page.Items) != 0 {
		t.Fatalf("unsupported rule_id must not silently widen; items=%d", len(page.Items))
	}
}

func usageEvent(key string, perspective cp.UsagePerspective, boundary cp.UsageBoundary, at time.Time) cp.Event {
	return cp.Event{
		SourceEventKey: key,
		Category:       cp.CategoryUsage,
		OccurredAt:     at,
		RecordedAt:     at,
		Correlation:    cp.Correlation{ALegID: "a-1", RequestID: "req-1"},
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionSummarized,
		Detail: &cp.UsageDetail{
			Plane:          cp.UsagePlaneObserved,
			Availability:   cp.UsageAvailabilityObserved,
			Perspective:    perspective,
			Boundary:       boundary,
			LifecycleScope: cp.UsageLifecycleBackendAttempt,
			InputTokens:    1,
		},
	}
}

func mustAppendUsage(t *testing.T, store *MemoryStore, ev cp.Event) {
	t.Helper()
	if _, err := store.Append(context.Background(), ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
}
