package state

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

type coordinatorStore struct {
	last   persistedState
	found  bool
	failed bool
}

type contextAwareLoadStore struct {
	started chan struct{}
	once    sync.Once
}

func (s *contextAwareLoadStore) Get(ctx context.Context, _ aipstate.Scope, _, _ string, _ any) (bool, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return false, ctx.Err()
}

func (*contextAwareLoadStore) Put(context.Context, aipstate.Scope, string, string, any, time.Duration) error {
	return nil
}

func (*contextAwareLoadStore) Delete(context.Context, aipstate.Scope, string, string) error {
	return nil
}

func (*contextAwareLoadStore) InspectTTL(context.Context, aipstate.Scope, string, string) (time.Duration, bool, error) {
	return 0, false, nil
}

func TestBranchCoordinator_LoadUsesConstructorContext(t *testing.T) {
	t.Parallel()

	store := &contextAwareLoadStore{started: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := NewBranchCoordinator(ctx, Config{Store: store})
		result <- err
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("constructor did not reach state load")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("constructor error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("constructor did not honor cancellation")
	}
}

func (s *coordinatorStore) Get(_ context.Context, _ aipstate.Scope, _, _ string, out any) (bool, error) {
	if s.failed {
		return false, errors.New("store read failed")
	}
	if !s.found {
		return false, nil
	}
	b, err := json.Marshal(s.last)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, out)
}

func (s *coordinatorStore) Put(_ context.Context, _ aipstate.Scope, _, _ string, value any, _ time.Duration) error {
	if s.failed {
		return errors.New("store write failed")
	}
	state, ok := value.(persistedState)
	if !ok {
		return errors.New("unexpected persisted value")
	}
	s.last = state
	s.found = true
	return nil
}

func (s *coordinatorStore) Delete(context.Context, aipstate.Scope, string, string) error { return nil }

func (s *coordinatorStore) InspectTTL(context.Context, aipstate.Scope, string, string) (time.Duration, bool, error) {
	return 0, s.found, nil
}

func TestBranchCoordinator_CapturesParentBindingBeforePrivateChild(t *testing.T) {
	t.Parallel()

	parent, err := NewBranchKey("session-parent", "a-parent", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	child, err := NewBranchKey("session-parent", "a-child", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	parentBinding := parent.Binding()
	if parentBinding == "" || parentBinding == child.Binding() {
		t.Fatal("the private child A-leg must not replace the captured parent binding")
	}

	c, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Capture(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitCapsule(context.Background(), parent, 0, []byte(`{"base":true}`), [32]byte{7}, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RecordPendingJob(context.Background(), parent, auxiliary.JobID("job-parent"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ValidatePendingJob(context.Background(), child, auxiliary.JobID("job-parent")); !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("child branch validation error = %v, want ErrBranchMismatch", err)
	}
	if _, err := c.ValidatePendingJob(context.Background(), parent, auxiliary.JobID("job-parent")); err != nil {
		t.Fatalf("parent branch validation: %v", err)
	}
}

func TestBranchCoordinator_MutationsRequireExplicitCapture(t *testing.T) {
	t.Parallel()

	c, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := NewBranchKey("session-parent", "a-parent", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Capture(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	key, err := NewBranchKey("session-parent", "a-child", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RecordPendingJob(context.Background(), key, auxiliary.JobID("job-child"), 0); !errors.Is(err, ErrBranchNotFound) {
		t.Fatalf("uncaptured pending job error = %v, want ErrBranchNotFound", err)
	}
	if _, err := c.RecordPreviewIntent(context.Background(), key, PreviewIntent{Key: "preview-child"}); !errors.Is(err, ErrBranchNotFound) {
		t.Fatalf("uncaptured preview intent error = %v, want ErrBranchNotFound", err)
	}
	if _, err := c.SetPendingInjection(context.Background(), key, InjectionTarget{BoundaryKey: "boundary-child", CapsuleRevision: 1}); !errors.Is(err, ErrBranchNotFound) {
		t.Fatalf("uncaptured injection error = %v, want ErrBranchNotFound", err)
	}
	if _, found, err := c.Snapshot(context.Background(), key); err != nil || found {
		t.Fatalf("uncaptured child snapshot = found=%v err=%v, want absent", found, err)
	}
}

func TestBranchCoordinator_PersistsEntriesInStableBindingOrder(t *testing.T) {
	t.Parallel()

	store := &coordinatorStore{}
	c, err := NewBranchCoordinator(context.Background(), Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]BranchKey, 0, 3)
	for _, id := range []string{"s-3", "s-1", "s-2"} {
		key, err := NewBranchKey(id, "a-parent", "")
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		if _, err := c.Capture(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.last.Entries) != len(keys) {
		t.Fatalf("persisted entries=%d want %d", len(store.last.Entries), len(keys))
	}
	for i := 1; i < len(store.last.Entries); i++ {
		if store.last.Entries[i-1].Key.Binding() > store.last.Entries[i].Key.Binding() {
			t.Fatalf("persisted entries are not binding-sorted: %q then %q", store.last.Entries[i-1].Key.Binding(), store.last.Entries[i].Key.Binding())
		}
	}
}

func TestBranchCoordinator_PersistenceFailureLeavesStateUnchanged(t *testing.T) {
	t.Parallel()

	store := &coordinatorStore{}
	c, err := NewBranchCoordinator(context.Background(), Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewBranchKey("session-persist", "a-parent", "")
	if _, err := c.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	before, err := c.CommitCapsule(context.Background(), key, 0, []byte(`{"before":true}`), [32]byte{1}, "before")
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != 1 {
		t.Fatalf("initial revision=%d want 1", before.Revision)
	}
	store.failed = true
	if _, err := c.CommitCapsule(context.Background(), key, 1, []byte(`{"after":true}`), [32]byte{2}, "after"); err == nil {
		t.Fatal("expected persistence failure")
	}
	if _, err := c.CommitSource(context.Background(), key, 1, []byte(`{"source":true}`), "source-after"); err == nil {
		t.Fatal("expected source persistence failure")
	}
	if _, err := c.RecordPendingJob(context.Background(), key, auxiliary.JobID("job-failed"), 1); err == nil {
		t.Fatal("expected pending-job persistence failure")
	}
	if _, err := c.SetPendingInjection(context.Background(), key, InjectionTarget{BoundaryKey: "boundary-failed", CapsuleRevision: 1}); err == nil {
		t.Fatal("expected pending-injection persistence failure")
	}
	store.failed = false
	after, found, err := c.Snapshot(context.Background(), key)
	if err != nil || !found || string(after.CapsuleJSON) != `{"before":true}` || after.Revision != 1 || after.SourceHighWatermark != "before" || after.PendingJobID != "" || after.PendingInjection != nil {
		t.Fatalf("state after failed persist=%#v found=%v err=%v", after, found, err)
	}
}

func TestBranchCoordinator_SnapshotsDefensivelyCopyOpaqueAndPendingState(t *testing.T) {
	t.Parallel()

	c, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewBranchKey("session-copy", "a-parent", "")
	if _, err := c.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitCapsule(context.Background(), key, 0, []byte(`{"capsule":true}`), [32]byte{1}, "source-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitSource(context.Background(), key, 1, []byte(`{"source":true}`), "source-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RecordPendingJob(context.Background(), key, auxiliary.JobID("job-copy"), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPendingInjection(context.Background(), key, InjectionTarget{BoundaryKey: "boundary-copy", CapsuleRevision: 1}); err != nil {
		t.Fatal(err)
	}
	got, _, err := c.Snapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	got.CapsuleJSON[0] = 'X'
	got.SanitizedSourceJSON = append(got.SanitizedSourceJSON, 'X')
	got.PendingJobID = "mutated"
	got.PendingInjection.BoundaryKey = "mutated"
	want, _, err := c.Snapshot(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if string(want.CapsuleJSON) != `{"capsule":true}` || string(want.SanitizedSourceJSON) != `{"source":true}` || want.PendingJobID != "job-copy" || want.PendingInjection.BoundaryKey != "boundary-copy" {
		t.Fatalf("snapshot alias leaked into coordinator state: %#v", want)
	}
}

func TestBranchKey_SecureSessionDominatesPrincipalFallback(t *testing.T) {
	t.Parallel()

	secureA, err := NewBranchKey("session-1", "a-parent", "principal-a")
	if err != nil {
		t.Fatal(err)
	}
	secureB, err := NewBranchKey("session-1", "a-parent", "principal-b")
	if err != nil {
		t.Fatal(err)
	}
	if secureA.Binding() != secureB.Binding() {
		t.Fatal("principal fallback must not fork an authoritative secure session")
	}
	fallbackA, err := NewBranchKey("", "a-parent", "principal-a")
	if err != nil {
		t.Fatal(err)
	}
	fallbackB, err := NewBranchKey("", "a-parent", "principal-b")
	if err != nil {
		t.Fatal(err)
	}
	if fallbackA.Binding() == fallbackB.Binding() {
		t.Fatal("principal-isolated fallback branches must remain distinct")
	}
}

func TestBranchCoordinator_RevisionAndPendingJobAreCompareAndBindChecked(t *testing.T) {
	t.Parallel()

	c, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewBranchKey("session-1", "a-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	digest := [32]byte{1, 2, 3}
	state, err := c.CommitCapsule(context.Background(), key, 0, []byte(`{"schema_version":1}`), digest, "source-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 {
		t.Fatalf("revision = %d, want 1", state.Revision)
	}
	if _, err := c.CommitCapsule(context.Background(), key, 0, nil, digest, "stale"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale commit error = %v, want ErrRevisionConflict", err)
	}
	if _, err := c.RecordPendingJob(context.Background(), key, "job-1", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ValidatePendingJob(context.Background(), key, "other-job"); !errors.Is(err, ErrPendingJobMismatch) {
		t.Fatalf("wrong job error = %v, want ErrPendingJobMismatch", err)
	}
	if _, err := c.ValidatePendingJob(context.Background(), key, "job-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MergeCapsule(context.Background(), key, "job-1", key.Binding(), 1, []byte(`{"merged":true}`), [32]byte{4}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := c.Snapshot(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("Snapshot = %#v, %v, want found", got, err)
	}
	if got.Revision != 2 || got.PendingJobID != "" {
		t.Fatalf("merged state = %#v, want revision 2 and no pending job", got)
	}
}

func TestBranchCoordinator_PreviewIntentBindsOnlyAfterCommittedTransaction(t *testing.T) {
	t.Parallel()

	c, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewBranchKey("session-1", "a-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	intent := PreviewIntent{Key: "preview-1", TargetSourceRevision: 4}
	if _, err := c.RecordPreviewIntent(context.Background(), key, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.BindPreviewIntent(context.Background(), key, "other-preview", "txn-1"); !errors.Is(err, ErrPreviewIntentMismatch) {
		t.Fatalf("wrong preview binding error = %v, want ErrPreviewIntentMismatch", err)
	}
	if _, err := c.BindPreviewIntent(context.Background(), key, intent.Key, ""); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("empty transaction error = %v, want ErrInvalidTransaction", err)
	}
	state, err := c.BindPreviewIntent(context.Background(), key, intent.Key, "txn-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.PendingPreviewIntent != nil || state.LastCompactionTransaction != "txn-1" {
		t.Fatalf("bound state = %#v, want consumed intent and committed transaction", state)
	}
}

func TestBranchCoordinator_InjectionWatermarkIsBoundaryScopedAndCommitsOnRelease(t *testing.T) {
	t.Parallel()

	c, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	key, err := NewBranchKey("session-1", "a-1", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitCapsule(context.Background(), key, 0, []byte(`{"capsule":true}`), [32]byte{1}, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPendingInjection(context.Background(), key, InjectionTarget{BoundaryKey: "boundary-1", CapsuleRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitReleasedInjection(context.Background(), key, InjectionWatermark{BranchBinding: key.Binding(), BoundaryKey: "boundary-1", CapsuleRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetPendingInjection(context.Background(), key, InjectionTarget{BoundaryKey: "boundary-1", CapsuleRevision: 1}); !errors.Is(err, ErrInjectionAlreadyReleased) {
		t.Fatalf("duplicate same boundary error = %v, want ErrInjectionAlreadyReleased", err)
	}
	if _, err := c.SetPendingInjection(context.Background(), key, InjectionTarget{BoundaryKey: "boundary-2", CapsuleRevision: 1}); err != nil {
		t.Fatalf("same revision on later boundary: %v", err)
	}
	if _, err := c.CommitReleasedInjection(context.Background(), key, InjectionWatermark{BranchBinding: "wrong", BoundaryKey: "boundary-2", CapsuleRevision: 1}); !errors.Is(err, ErrBranchMismatch) {
		t.Fatalf("wrong branch release error = %v, want ErrBranchMismatch", err)
	}
}

func TestBranchCoordinator_BoundsAndLazyExpiry(t *testing.T) {
	t.Parallel()

	now := time.Unix(100, 0)
	c, err := NewBranchCoordinator(context.Background(), Config{MaxEntries: 1, TTL: time.Second, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := NewBranchKey("s-1", "a-1", "")
	k2, _ := NewBranchKey("s-2", "a-2", "")
	if _, err := c.Capture(context.Background(), k1); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Capture(context.Background(), k2); !errors.Is(err, ErrBranchLimit) {
		t.Fatalf("second branch error = %v, want ErrBranchLimit", err)
	}
	now = now.Add(2 * time.Second)
	if _, err := c.Capture(context.Background(), k2); err != nil {
		t.Fatalf("capture after lazy expiry: %v", err)
	}
}

func TestBranchCoordinator_SerializesConcurrentInjectionUpdates(t *testing.T) {
	t.Parallel()

	c, err := NewBranchCoordinator(context.Background(), Config{})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewBranchKey("session-concurrent", "a-parent", "")
	if _, err := c.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CommitCapsule(context.Background(), key, 0, []byte(`{"base":true}`), [32]byte{1}, "source"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = c.SetPendingInjection(context.Background(), key, InjectionTarget{BoundaryKey: "boundary-" + string(rune('a'+i)), CapsuleRevision: 1})
			_, _, _ = c.Snapshot(context.Background(), key)
		}(i)
	}
	wg.Wait()
	state, ok, err := c.Snapshot(context.Background(), key)
	if err != nil || !ok || state.PendingInjection == nil {
		t.Fatalf("concurrent state = %#v, found=%v, err=%v", state, ok, err)
	}
}

type memStore struct {
	mu      sync.Mutex
	entries map[string]any
}

func newMemStore() *memStore {
	return &memStore{entries: make(map[string]any)}
}

func (m *memStore) Get(_ context.Context, _ aipstate.Scope, ns, key string, out any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	val, ok := m.entries[ns+":"+key]
	if !ok {
		return false, nil
	}
	data, err := json.Marshal(val)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(data, out)
}

func (m *memStore) Put(_ context.Context, _ aipstate.Scope, ns, key string, val any, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[ns+":"+key] = val
	return nil
}

func (m *memStore) Delete(_ context.Context, _ aipstate.Scope, ns, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, ns+":"+key)
	return nil
}

func (m *memStore) InspectTTL(_ context.Context, _ aipstate.Scope, _, _ string) (time.Duration, bool, error) {
	return 0, false, nil
}

func TestBranchCoordinator_UsesOpaqueProcessExtensionStateAcrossCoordinatorReload(t *testing.T) {
	t.Parallel()

	store := newMemStore()
	c1, err := NewBranchCoordinator(context.Background(), Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := NewBranchKey("session-reload", "a-parent", "")
	if _, err := c1.Capture(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.CommitCapsule(context.Background(), key, 0, []byte(`{"persisted":true}`), [32]byte{9}, "source"); err != nil {
		t.Fatal(err)
	}
	if _, err := c1.RecordPendingJob(context.Background(), key, auxiliary.JobID("job-reload"), 1); err != nil {
		t.Fatal(err)
	}
	c2, err := NewBranchCoordinator(context.Background(), Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := c2.Snapshot(context.Background(), key)
	if err != nil || !ok || string(got.CapsuleJSON) != `{"persisted":true}` {
		t.Fatalf("reloaded snapshot = %#v, found=%v, err=%v", got, ok, err)
	}
	if _, err := c2.MergeCapsule(context.Background(), key, "job-reload", key.Binding(), 1, []byte(`{"merged":true}`), [32]byte{10}); err != nil {
		t.Fatalf("reloaded pending merge: %v", err)
	}
	if _, err := c2.ValidatePendingJob(context.Background(), key, "child-a-leg"); !errors.Is(err, ErrPendingJobMismatch) {
		// No pending job is intentionally a consistency failure, not a lookup
		// that could be satisfied by the private child A-leg.
		t.Fatalf("child job lookup error = %v, want ErrPendingJobMismatch", err)
	}
}
