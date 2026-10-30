package b2bua_test

import (
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride"
	"github.com/aiproxer/aiproxer/internal/core/routeoverride/storecontract"
)

type memoryTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *memoryTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *memoryTestClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var memoryTestClocks sync.Map // *b2bua.MemoryStore -> *memoryTestClock

func TestMemoryStoreImplementsRouteOverrideStore(t *testing.T) {
	t.Parallel()
	mem, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := routeoverride.AsStore(mem); !ok {
		t.Fatal("b2bua.MemoryStore does not implement routeoverride.Store")
	}
	storecontract.RunAll(t, storecontract.ContractEnv{
		New:             newMemoryContractPair,
		SeedRevision:    seedMemoryRevision,
		PeekLastSeenAt:  peekMemoryLastSeenAt,
		AdvanceClock:    advanceMemoryClock,
		SeedStoredState: seedMemoryStoredState,
		Spawn:           func(fn func()) { go fn() },
	})
}

func newMemoryContractPair(t *testing.T) storecontract.ContractPair {
	t.Helper()
	clock := &memoryTestClock{t: time.Unix(1_800_000_000, 0).UTC()}
	s, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	memoryTestClocks.Store(s, clock)
	t.Cleanup(func() { memoryTestClocks.Delete(s) })
	ov, ok := routeoverride.AsStore(s)
	if !ok {
		t.Fatal("b2bua.MemoryStore does not implement routeoverride.Store")
	}
	return storecontract.ContractPair{Override: ov, Legs: s}
}

func memoryFromPair(t *testing.T, pair storecontract.ContractPair) *b2bua.MemoryStore {
	t.Helper()
	s, ok := pair.Legs.(*b2bua.MemoryStore)
	if !ok {
		t.Fatalf("Legs is %T, want *b2bua.MemoryStore", pair.Legs)
	}
	return s
}

func seedMemoryRevision(t *testing.T, pair storecontract.ContractPair, aLegID string, revision int64) {
	t.Helper()
	seedMemoryStoredState(t, pair, aLegID, routeoverride.State{
		ALegID:    aLegID,
		Active:    true,
		Selector:  "seed:overflow",
		Revision:  revision,
		UpdatedAt: time.Unix(1, 0).UTC(),
	})
}

func seedMemoryStoredState(t *testing.T, pair storecontract.ContractPair, aLegID string, st routeoverride.State) {
	t.Helper()
	if !memoryFromPair(t, pair).SeedRouteOverrideForTest(aLegID, st) {
		t.Fatalf("seed: missing a-leg %s", aLegID)
	}
}

func peekMemoryLastSeenAt(t *testing.T, pair storecontract.ContractPair, aLegID string) time.Time {
	t.Helper()
	got, ok := memoryFromPair(t, pair).PeekLastSeenAtForTest(aLegID)
	if !ok {
		t.Fatalf("peek: missing a-leg %s", aLegID)
	}
	return got
}

func advanceMemoryClock(t *testing.T, pair storecontract.ContractPair, aLegID string) {
	t.Helper()
	_ = aLegID
	s := memoryFromPair(t, pair)
	v, ok := memoryTestClocks.Load(s)
	if !ok {
		t.Fatal("advance: missing test clock")
	}
	clock, ok := v.(*memoryTestClock)
	if !ok {
		t.Fatalf("advance: clock is %T", v)
	}
	clock.Advance(time.Second)
}
