package compatible_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	compatibleadmission "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/compatible"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// errUnboundedReleaseContext marks a lease release that was handed a context
// with no cancellation bound. The coordinator always supplies a bounded,
// detached cleanup context for settle/release work, so an unbounded release is
// the defect this test pins: a slow or contended lease store would then block
// the compensation loop past every configured cleanup deadline.
var errUnboundedReleaseContext = errors.New("release received a context with no cancellation bound")

// boundedReleaseStore models a slow durable lease store. Release waits for the
// cancellation bound of the context it receives, so the only way it completes
// is when the caller's cleanup deadline is honored.
type boundedReleaseStore struct {
	*leasestore.MemoryStore

	mu       sync.Mutex
	contexts []context.Context
}

func newBoundedReleaseStore() *boundedReleaseStore {
	return &boundedReleaseStore{MemoryStore: leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "compatible-release-context"})}
}

func (s *boundedReleaseStore) Release(ctx context.Context, _ concurrencyapp.ReleaseCommand) (concurrencyapp.ReleaseResult, error) {
	s.mu.Lock()
	s.contexts = append(s.contexts, ctx)
	s.mu.Unlock()
	if ctx == nil || ctx.Done() == nil {
		return concurrencyapp.ReleaseResult{}, errUnboundedReleaseContext
	}
	<-ctx.Done()
	return concurrencyapp.ReleaseResult{}, ctx.Err()
}

func (s *boundedReleaseStore) releaseContexts() []context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]context.Context(nil), s.contexts...)
}

func newReleaseProbeCoordinator(t *testing.T, store *boundedReleaseStore) *authoritycoord.AttemptCoordinator {
	t.Helper()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"be": 1}, store)
	if err != nil {
		t.Fatal(err)
	}
	return &authoritycoord.AttemptCoordinator{
		CleanupTimeout: 25 * time.Millisecond,
		Slots: []authoritycoord.AttemptSlot{{
			ID: compatibleadmission.ProviderID, Provider: reg.Provider,
			Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
		}},
	}
}

func admitForReleaseProbe(t *testing.T, coord *authoritycoord.AttemptCoordinator, bleg string) authoritycoord.CompositeDecision {
	t.Helper()
	d, err := coord.Admit(context.Background(), attemptAdmission("be", bleg))
	if err != nil || d.Kind != authority.DecisionAllow {
		t.Fatalf("admit err=%v d=%+v", err, d)
	}
	return d
}

func assertAllReleaseContextsBounded(t *testing.T, store *boundedReleaseStore) {
	t.Helper()
	contexts := store.releaseContexts()
	if len(contexts) == 0 {
		t.Fatal("store never observed a lease release")
	}
	for i, ctx := range contexts {
		if ctx == nil {
			t.Fatalf("release %d received a nil context", i)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatalf("release %d received an unbounded context; the coordinator cleanup deadline must bound the store call", i)
		}
	}
}

func TestAttemptProvider_releaseHonorsCoordinatorCleanupDeadline(t *testing.T) {
	t.Parallel()
	store := newBoundedReleaseStore()
	coord := newReleaseProbeCoordinator(t, store)
	d := admitForReleaseProbe(t, coord, "b-release")

	// The caller context is already canceled: the coordinator supplies a
	// detached cleanup context, so the release must still be attempted and must
	// still be bounded by the coordinator cleanup deadline.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	fails := coord.Release(canceled, d.Stack)
	elapsed := time.Since(start)

	if len(fails) != 1 {
		t.Fatalf("release failures=%v want exactly one bounded-release failure", fails)
	}
	if !errors.Is(fails[0].Err, context.DeadlineExceeded) {
		t.Fatalf("release error=%v want context.DeadlineExceeded from the enforced cleanup bound", fails[0].Err)
	}
	if errors.Is(fails[0].Err, errUnboundedReleaseContext) {
		t.Fatal("release dropped the coordinator cleanup context and handed the store an unbounded context")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("release took %s; the coordinator cleanup deadline was not enforced", elapsed)
	}
	assertAllReleaseContextsBounded(t, store)
}

func TestAttemptProvider_settleHonorsCoordinatorCleanupDeadline(t *testing.T) {
	t.Parallel()
	store := newBoundedReleaseStore()
	coord := newReleaseProbeCoordinator(t, store)
	d := admitForReleaseProbe(t, coord, "b-settle")

	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := coord.Settle(canceled, d.Stack, attemptSettlement("b-settle", d.Stack.Handles()))
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("settle error=%v want context.DeadlineExceeded from the enforced cleanup bound", err)
	}
	if errors.Is(err, errUnboundedReleaseContext) {
		t.Fatal("settle dropped the coordinator cleanup context and handed the store an unbounded context")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("settle took %s; the coordinator cleanup deadline was not enforced", elapsed)
	}
	assertAllReleaseContextsBounded(t, store)
}
