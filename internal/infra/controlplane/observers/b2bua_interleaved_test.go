package observers_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/interleavedstate"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// interleavedB2BUAStore wraps fakeB2BUAStore with an observable
// b2bua.InterleavedStateStore implementation so the decorator can be asserted to
// delegate interleaved-thinking persistence to the underlying continuity store.
type interleavedB2BUAStore struct {
	*fakeB2BUAStore
	setCalls    int
	fetchCalls  int
	lastState   interleavedstate.State
	returnState interleavedstate.State
	fetchErr    error
}

func (s *interleavedB2BUAStore) SetInterleavedState(_ context.Context, _ string, state interleavedstate.State) error {
	s.setCalls++
	s.lastState = state
	return nil
}

func (s *interleavedB2BUAStore) FetchInterleavedState(_ context.Context, _ string) (interleavedstate.State, error) {
	s.fetchCalls++
	return s.returnState, s.fetchErr
}

func nonEmptyInterleavedState() interleavedstate.State {
	return interleavedstate.State{Cycle: interleavedstate.CycleState{Sequence: []interleavedstate.CycleEntry{{Key: "k", Role: interleavedstate.RoleThinker}}}}
}

// TestB2BUA_ImplementsInterleavedStateStoreAndDelegates proves the decorator
// preserves the optional b2bua.InterleavedStateStore capability: the executor
// type-asserts e.Store to that interface for interleaved-thinking persistence,
// so a decorator that only implements b2bua.Store would break persistence when
// control-plane is enabled. The decorator must implement the interface and
// delegate to the underlying store.
func TestB2BUA_ImplementsInterleavedStateStoreAndDelegates(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	want := nonEmptyInterleavedState()
	fake := &interleavedB2BUAStore{fakeB2BUAStore: &fakeB2BUAStore{}, returnState: want}
	dec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   fake,
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	var s b2bua.Store = dec
	is, ok := s.(b2bua.InterleavedStateStore)
	if !ok || is == nil {
		t.Fatalf("decorator must implement b2bua.InterleavedStateStore so control-plane does not break interleaved-thinking persistence")
	}
	if err := is.SetInterleavedState(context.Background(), "aleg-1", want); err != nil {
		t.Fatalf("SetInterleavedState: %v", err)
	}
	if fake.setCalls != 1 {
		t.Fatalf("expected delegate SetInterleavedState called once, got %d", fake.setCalls)
	}
	if !reflect.DeepEqual(fake.lastState, want) {
		t.Fatalf("delegated state mismatch: got %#v want %#v", fake.lastState, want)
	}
	got, err := is.FetchInterleavedState(context.Background(), "aleg-1")
	if err != nil {
		t.Fatalf("FetchInterleavedState: %v", err)
	}
	if fake.fetchCalls != 1 {
		t.Fatalf("expected delegate FetchInterleavedState called once, got %d", fake.fetchCalls)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fetched state mismatch: got %#v want %#v", got, want)
	}
}

// TestB2BUA_InterleavedStateUnsupportedWhenDelegateLacksIt proves the decorator
// mirrors the executor's graceful behavior when the underlying store does not
// implement b2bua.InterleavedStateStore: empty state is a no-op, non-empty state
// fails closed with ErrInterleavedStateUnsupported, and fetch returns a zero
// state. This keeps the decorator transparent relative to an unwrapped store.
func TestB2BUA_InterleavedStateUnsupportedWhenDelegateLacksIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	dec := observers.NewB2BUAStoreDecorator(observers.B2BUAStoreDecoratorConfig{
		Delegate:   &fakeB2BUAStore{},
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})
	var s b2bua.Store = dec
	is, ok := s.(b2bua.InterleavedStateStore)
	if !ok || is == nil {
		t.Fatalf("decorator must implement b2bua.InterleavedStateStore")
	}
	if err := is.SetInterleavedState(context.Background(), "aleg-1", nonEmptyInterleavedState()); !errors.Is(err, b2bua.ErrInterleavedStateUnsupported) {
		t.Fatalf("non-empty state on unsupported delegate must return ErrInterleavedStateUnsupported, got %v", err)
	}
	if err := is.SetInterleavedState(context.Background(), "aleg-1", interleavedstate.State{}); err != nil {
		t.Fatalf("empty state on unsupported delegate must be a no-op, got %v", err)
	}
	got, err := is.FetchInterleavedState(context.Background(), "aleg-1")
	if err != nil {
		t.Fatalf("fetch on unsupported delegate must be graceful, got %v", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("fetch on unsupported delegate must return zero state, got %#v", got)
	}
	if n := len(h.events()); n != 0 {
		t.Fatalf("interleaved-state ops on unsupported delegate must not record control-plane events, got %d", n)
	}
}
