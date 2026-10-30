package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestRetryRecvStream_Recv_nilContext(t *testing.T) {
	t.Parallel()
	s := &retryRecvStream{}
	_, err := s.Recv(nil) //nolint:staticcheck // deliberate nil ctx; expect aipapi.ErrNilContext
	if !errors.Is(err, aipapi.ErrNilContext) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryRecvStream_Recv_nilReceiver(t *testing.T) {
	t.Parallel()
	var s *retryRecvStream
	_, err := s.Recv(context.Background())
	if !errors.Is(err, errNilRetryRecvStream) {
		t.Fatalf("got %v", err)
	}
}

func TestRetryRecvStream_Close_nilReceiver(t *testing.T) {
	t.Parallel()
	var s *retryRecvStream
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// blockingUntilCloseInner blocks Recv until Close closes unblock.
type blockingUntilCloseInner struct {
	recvEntered chan struct{}
	unblock     chan struct{}
	closeOnce   sync.Once
}

func (b *blockingUntilCloseInner) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case b.recvEntered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-b.unblock:
		return aipapi.Event{}, io.EOF
	}
}

func (b *blockingUntilCloseInner) Close() error {
	b.closeOnce.Do(func() { close(b.unblock) })
	return nil
}

func (b *blockingUntilCloseInner) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func TestRetryRecvStream_Close_concurrentWhileRecvBlocked(t *testing.T) {
	t.Parallel()
	inner := &blockingUntilCloseInner{
		recvEntered: make(chan struct{}, 1),
		unblock:     make(chan struct{}),
	}
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	sel, err := routing.Parse("openai:gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = st
	ex.Bus = bus
	s := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
			},
			aLegID:  "a1",
			traceID: "t1",
		}),
		recovery: newRecoveryController(recoveryControllerInput{
			e:              ex,
			affinityStore:  ex.AffinityStore,
			log:            ex.Log,
			streamRecovery: ex.StreamRecovery,
			opener:         newReplacementOpener(ex, bus, nil),
			budget:         &attemptBudget{max: 3, used: 0},
			sel:            sel,
			session:        &routing.SessionRoutingState{},
			excluded:       map[string]struct{}{},
			rng:            routing.NewSeededRng(1),
		}),
		attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b1", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: &responsePipeline{bus: bus},
	}
	testStoreInner(s, inner)

	var wg sync.WaitGroup
	wg.Go(func() {
		_, err := s.Recv(context.Background())
		if err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("Recv: %v", err)
		}
	})

	waitTimer := time.NewTimer(2 * time.Second)
	defer waitTimer.Stop()
	select {
	case <-inner.recvEntered:
	case <-waitTimer.C:
		t.Fatal("Recv did not reach inner block")
	}

	const n = 32
	var closes sync.WaitGroup
	for range n {
		closes.Go(func() {
			_ = s.Close()
		})
	}
	closes.Wait()
	wg.Wait()

	req, att := testTerminalOwners(s)
	if req == nil || att == nil {
		t.Fatal("terminal owners must be initialized after concurrent Close")
	}
	if !req.Owner().State().IsTerminal() {
		t.Fatalf("request terminal state=%q want terminal", req.Owner().State())
	}
	if !att.Owner().State().IsTerminal() {
		t.Fatalf("attempt terminal state=%q want terminal", att.Owner().State())
	}
	out, ok := req.Owner().Outcome()
	if !ok || out.Command != sdkterminal.CommandClose {
		t.Fatalf("request outcome=%+v ok=%v want CommandClose", out, ok)
	}
	if !s.terminal.finished() {
		t.Fatal("stream must be finished after concurrent Close")
	}
}

func TestCancellationAttemptReason(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := cancellationAttemptReason(ctx, context.Canceled); got != "context canceled" {
		t.Fatalf("canceled recv: got %q", got)
	}
	ctx2, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel2()
	if got := cancellationAttemptReason(ctx2, errors.New("wrapped transport error")); got != "context deadline exceeded" {
		t.Fatalf("deadline ctx: got %q want context deadline exceeded", got)
	}
	if got := cancellationAttemptReason(context.Background(), nil); got != "cancelled" {
		t.Fatalf("no ctx err: got %q", got)
	}
}
