package ledgerstore

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/ledgerstore/contract"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// memoryFactory implements contract.Factory for the in-memory store. It builds
// a fresh store per test so cases remain independent.
type memoryFactory struct{}

func (memoryFactory) Build(t *testing.T) controlplane.Store {
	t.Helper()
	store, err := NewMemoryStore(MemoryConfig{StoreID: "mem-test"})
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// memoryFactoryUnsupported builds a memory store configured to reject a known
// filter field, so the contract's unsupported-filter reporting path is
// exercised for the memory adapter (task 2.4).
type memoryFactoryUnsupported struct {
	fields []string
}

func (f memoryFactoryUnsupported) Build(t *testing.T) controlplane.Store {
	t.Helper()
	store, err := NewMemoryStore(MemoryConfig{
		StoreID:            "mem-unsup",
		UnsupportedFilters: f.fields,
	})
	if err != nil {
		t.Fatalf("NewMemoryStore(unsupported) error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func (f memoryFactoryUnsupported) UnsupportedConfig() contract.UnsupportedConfig {
	return contract.UnsupportedConfig{Fields: f.fields}
}

// TestMemoryStore_Contract runs the shared store contract against the in-memory
// adapter (tasks 2.1, 2.4, 2.5).
func TestMemoryStore_Contract(t *testing.T) {
	t.Parallel()
	contract.RunSuite(t, memoryFactory{})
}

// TestMemoryStore_ContractUnsupportedFilters exercises unsupported-filter
// reporting through the shared contract for the memory adapter (task 2.4).
func TestMemoryStore_ContractUnsupportedFilters(t *testing.T) {
	t.Parallel()
	f := memoryFactoryUnsupported{fields: []string{contract.FieldBackendID, contract.FieldScopeTenantID}}
	contract.RunSuite(t, f)
}

func TestMemoryStore_appendAssignsMonotonicIdentity(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	c := context.Background()
	ev := contractEvent()
	first, err := s.Append(c, ev)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if first.ID.StoreID != "mem-test" || first.ID.Sequence == 0 {
		t.Fatalf("first identity = %#v", first.ID)
	}
	ev2 := contractEvent()
	ev2.SourceEventKey = "auth:second:1"
	second, err := s.Append(c, ev2)
	if err != nil {
		t.Fatalf("Append() second error = %v", err)
	}
	if second.ID.Sequence <= first.ID.Sequence {
		t.Fatalf("sequence not monotonic: first=%d second=%d", first.ID.Sequence, second.ID.Sequence)
	}
	if !first.RecordedAt.Equal(ev.RecordedAt) && first.RecordedAt.IsZero() {
		t.Fatalf("first recorded_at = %v, want non-zero", first.RecordedAt)
	}
}

func TestMemoryStore_sourceKeyDedupeIsIdempotent(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	c := context.Background()
	ev := contractEvent()
	if _, err := s.Append(c, ev); err != nil {
		t.Fatalf("Append() first error = %v", err)
	}
	res, err := s.Append(c, ev)
	if err != nil {
		t.Fatalf("Append() dup error = %v", err)
	}
	if res.Dedupe != cp.DedupeDuplicate {
		t.Fatalf("dup dedupe = %q, want duplicate", res.Dedupe)
	}
	page, err := s.Events(c, cp.EventQuery{Limit: 10})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Events() len = %d, want 1 after dedupe", len(page.Items))
	}
}

func TestMemoryStore_cancelledContextPropagates(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Append(cctx, contractEvent()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Append(canceled) error = %v, want context.Canceled", err)
	}
	if _, err := s.Events(cctx, cp.EventQuery{Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Events(canceled) error = %v, want context.Canceled", err)
	}
}

func TestMemoryStore_tooBroadQueryRejected(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	c := context.Background()
	// A limit over MaxPageSize (default 500) must be rejected as too broad.
	_, err := s.Events(c, cp.EventQuery{Limit: 1000})
	if err == nil {
		t.Fatalf("Events(too broad) must fail; got nil")
	}
	if !errors.Is(err, controlplane.ErrTooBroad) {
		t.Fatalf("Events(too broad) error = %v, want ErrTooBroad", err)
	}
}

func TestMemoryStore_defaultPageSizeAppliedWhenLimitZero(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	c := context.Background()
	for i := 1; i <= 3; i++ {
		ev := contractEvent()
		ev.SourceEventKey = "auth:default:" + strconv.Itoa(i)
		ev.OccurredAt = contract.FixedTime.Add(time.Duration(i) * time.Second)
		ev.RecordedAt = ev.OccurredAt.Add(time.Millisecond)
		if _, err := s.Append(c, ev); err != nil {
			t.Fatalf("Append() error = %v", err)
		}
	}
	page, err := s.Events(c, cp.EventQuery{Limit: 0})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("Events(limit=0) len = %d, want 3 (default page size should not truncate small sets)", len(page.Items))
	}
}

func TestMemoryStore_scopeKnownValueAndEmptyFilter(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	c := context.Background()

	evA := contractEvent()
	evA.SourceEventKey = "auth:scope:a"
	evA.Scope.ProjectID = scope.Known("proj-1")
	if _, err := s.Append(c, evA); err != nil {
		t.Fatalf("Append() a error = %v", err)
	}

	evB := contractEvent()
	evB.SourceEventKey = "auth:scope:b"
	evB.Scope.ProjectID = scope.Known("proj-2")
	if _, err := s.Append(c, evB); err != nil {
		t.Fatalf("Append() b error = %v", err)
	}

	evEmpty := contractEvent()
	evEmpty.SourceEventKey = "auth:scope:empty"
	evEmpty.Scope.ProjectID = scope.Known("")
	if _, err := s.Append(c, evEmpty); err != nil {
		t.Fatalf("Append() empty error = %v", err)
	}

	page, err := s.Events(c, cp.EventQuery{Limit: 10, Common: cp.CommonFilters{Scope: cp.ScopeFilters{ProjectID: scope.Known("proj-1")}}})
	if err != nil {
		t.Fatalf("Events(proj-1) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceEventKey != "auth:scope:a" {
		t.Fatalf("Events(proj-1) = %d items, want only a", len(page.Items))
	}

	page, err = s.Events(c, cp.EventQuery{Limit: 10, Common: cp.CommonFilters{Scope: cp.ScopeFilters{ProjectID: scope.Known("")}}})
	if err != nil {
		t.Fatalf("Events(empty) error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceEventKey != "auth:scope:empty" {
		t.Fatalf("Events(empty) = %d items, want only empty", len(page.Items))
	}
}

func TestMemoryStore_redactionProfileStrictClearsDetails(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	c := context.Background()
	ev := contractEvent()
	if _, err := s.Append(c, ev); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if _, err := s.ApplyRetention(c, controlplane.RetentionCommand{
		Cutoff:     contract.FixedTime.Add(time.Hour),
		Profile:    controlplane.RetentionProfileStrict,
		Visibility: cp.VisibilityDefault,
	}); err != nil {
		t.Fatalf("ApplyRetention(strict) error = %v", err)
	}
	page, err := s.Events(c, cp.EventQuery{Limit: 10, Visibility: cp.VisibilityDefault})
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Events() len = %d, want 1", len(page.Items))
	}
	got := page.Items[0]
	if got.EvidenceState != cp.EvidenceRedacted {
		t.Fatalf("strict redaction state = %q, want redacted", got.EvidenceState)
	}
	if got.Auth() != nil {
		t.Fatalf("strict redaction must clear auth detail, got %#v", got.Auth())
	}
}

// TestMemoryStore_concurrentAppendQueryAndRetention proves the RWMutex makes
// the store safe under concurrent writers, readers, and retention passes: no
// deadlock, no panic, and the store remains queryable after the storm. Each
// Append uses a distinct source event key so dedupe does not serialize writers.
func TestMemoryStore_concurrentAppendQueryAndRetention(t *testing.T) {
	t.Parallel()
	s := newMemoryStoreForTest(t)
	c := context.Background()

	const writers, readers, retentionRuns = 8, 8, 2
	const writesPerGoroutine = 20

	var wg sync.WaitGroup
	wg.Add(writers + readers + retentionRuns)

	for i := range writers {
		go func(n int) {
			defer wg.Done()
			for j := range writesPerGoroutine {
				ev := contractEvent()
				ev.SourceEventKey = "auth:conc:w" + strconv.Itoa(n) + ":" + strconv.Itoa(j)
				_, _ = s.Append(c, ev)
			}
		}(i)
	}
	for range readers {
		go func() {
			defer wg.Done()
			for range writesPerGoroutine {
				_, _ = s.Events(c, cp.EventQuery{Limit: 10, Visibility: cp.VisibilityDefault})
			}
		}()
	}
	for range retentionRuns {
		go func() {
			defer wg.Done()
			_, _ = s.ApplyRetention(c, controlplane.RetentionCommand{
				Cutoff:     contract.FixedTime.Add(time.Hour),
				Profile:    controlplane.RetentionProfileStandard,
				Visibility: cp.VisibilityDefault,
			})
		}()
	}
	wg.Wait()

	// After concurrent writers finish, the store must still serve a bounded
	// query containing every inserted event.
	page, err := s.Events(c, cp.EventQuery{Limit: writers*writesPerGoroutine + 1})
	if err != nil {
		t.Fatalf("final Events: %v", err)
	}
	if got, want := len(page.Items), writers*writesPerGoroutine; got != want {
		t.Fatalf("final Events len = %d, want %d (some appends lost or duplicated)", got, want)
	}
}

func newMemoryStoreForTest(t *testing.T) *MemoryStore {
	t.Helper()
	s, err := NewMemoryStore(MemoryConfig{StoreID: "mem-test"})
	if err != nil {
		t.Fatalf("NewMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func contractEvent() cp.Event {
	ev := contractEventAt(1)
	return ev
}

func contractEventAt(seq int) cp.Event {
	occurred := contract.FixedTime.Add(time.Duration(seq) * time.Second)
	return cp.Event{
		SourceEventKey: "auth:fixture:" + strconv.Itoa(seq),
		Category:       cp.CategoryAuth,
		OccurredAt:     occurred,
		RecordedAt:     occurred.Add(time.Millisecond),
		Correlation: cp.Correlation{
			TraceID:    "trace-fixture",
			SessionID:  "sess-fixture",
			ALegID:     "aleg-fixture",
			FrontendID: "openai-responses",
		},
		Scope: cp.ScopeSnapshot{
			Principal: scope.PrincipalScopeView{
				SubjectKind:  scope.SubjectHuman,
				PrincipalID:  scope.Known("p1"),
				TenantID:     scope.Known("tenant-a"),
				WorkspaceID:  scope.Known("ws-1"),
				Origin:       scope.OriginClient,
				Roles:        []string{"analyst"},
				SafeClaims:   map[string]string{"team": "data"},
				PolicyLabels: map[string]string{"tier": "standard"},
			},
			PrincipalID:    scope.Known("p1"),
			TenantID:       scope.Known("tenant-a"),
			WorkspaceID:    scope.Known("ws-1"),
			OrganizationID: scope.Unknown(),
			ProjectID:      scope.Known("proj-1"),
			DepartmentID:   scope.Unknown(),
			CostCenterID:   scope.Unknown(),
			CredentialID:   scope.Unknown(),
		},
		Source:         cp.SourceRef{Name: "test", Version: "v1"},
		Visibility:     cp.VisibilityDefault,
		EvidenceState:  cp.EvidenceRecorded,
		RedactionState: cp.RedactionNone,
		Detail: &cp.AuthDetail{
			Outcome:    "allowed",
			ReasonCode: "ok",
			Frontend:   "openai-responses",
			AuthMethod: "api_key",
		},
	}
}
