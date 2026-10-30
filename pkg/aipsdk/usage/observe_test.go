package usage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

type seqObs struct {
	id    string
	calls *[]string
	err   error
}

func (o seqObs) OnUsage(_ context.Context, _ usage.Event) error {
	if o.calls != nil {
		*o.calls = append(*o.calls, o.id)
	}
	return o.err
}

func TestChainObservers_dropsNilObservers(t *testing.T) {
	t.Parallel()
	var calls []string
	ch := usage.ChainObservers(nil, seqObs{id: "a", calls: &calls}, nil, seqObs{id: "b", calls: &calls})
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "a" || calls[1] != "b" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestChainObservers_invokesInRegistrationOrder(t *testing.T) {
	t.Parallel()
	var calls []string
	ch := usage.ChainObservers(seqObs{id: "first", calls: &calls}, seqObs{id: "second", calls: &calls})
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0] != "first" || calls[1] != "second" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestChainObservers_firstObserverErrorShortCircuits(t *testing.T) {
	t.Parallel()
	var calls []string
	wantErr := errors.New("stop")
	ch := usage.ChainObservers(seqObs{id: "fail", calls: &calls, err: wantErr}, seqObs{id: "skipped", calls: &calls})
	err := ch.OnUsage(context.Background(), usage.Event{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("got %v", err)
	}
	if len(calls) != 1 || calls[0] != "fail" {
		t.Fatalf("calls %#v", calls)
	}
}

func TestChainObservers_emptyChainIsNoOp(t *testing.T) {
	t.Parallel()
	ch := usage.ChainObservers()
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
}

func TestChainObservers_allNilBehavesAsNoOp(t *testing.T) {
	t.Parallel()
	ch := usage.ChainObservers(nil, nil)
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
}

// mutatingObs corrupts the digest backing array it receives, simulating a
// hostile or buggy observer. A correct fan-out boundary isolates later
// observers from the mutation.
type mutatingObs struct{}

func (mutatingObs) OnUsage(_ context.Context, ev usage.Event) error {
	for i := range ev.SeamDigests {
		ev.SeamDigests[i] = [32]byte{0xFF}
	}
	return nil
}

type captureObs struct{ got usage.Event }

func (c *captureObs) OnUsage(_ context.Context, ev usage.Event) error {
	c.got = ev
	return nil
}

// TestChainObservers_isolatesSeamDigestsPerObserver proves the fan-out
// boundary: a mutating observer ahead of a recording observer cannot alter
// the digests the later observer receives.
func TestChainObservers_isolatesSeamDigestsPerObserver(t *testing.T) {
	t.Parallel()
	var first, second [32]byte
	first[0], second[0] = 0x01, 0x02
	late := &captureObs{}
	ch := usage.ChainObservers(mutatingObs{}, late)
	if err := ch.OnUsage(context.Background(), usage.Event{SeamDigests: [][32]byte{first, second}}); err != nil {
		t.Fatal(err)
	}
	if len(late.got.SeamDigests) != 2 || late.got.SeamDigests[0] != first || late.got.SeamDigests[1] != second {
		t.Fatalf("late observer saw mutated digests: %x", late.got.SeamDigests)
	}
}

// TestChainObservers_emptyDigestsPassThrough proves the disabled feed pays
// no isolation cost and stays nil end to end.
func TestChainObservers_emptyDigestsPassThrough(t *testing.T) {
	t.Parallel()
	late := &captureObs{}
	ch := usage.ChainObservers(mutatingObs{}, late)
	if err := ch.OnUsage(context.Background(), usage.Event{}); err != nil {
		t.Fatal(err)
	}
	if len(late.got.SeamDigests) != 0 {
		t.Fatalf("empty digests grew a vector: %d", len(late.got.SeamDigests))
	}
}
