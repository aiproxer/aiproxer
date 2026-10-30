package runtime_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/stream"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
)

func newTestClock(t0 time.Time) (func() time.Time, func(d time.Duration)) {
	var mu sync.Mutex
	now := t0
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(d)
		}
}

func parallelStore(t *testing.T) b2bua.Store {
	t.Helper()
	st, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func parallelBackend(events []aipapi.Event) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return aipapi.NewFixedEventStream(events), nil
		},
	}
}

func delayedBackend(delay time.Duration, events []aipapi.Event) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &delayedStream{delay: delay, events: events, ctx: ctx}, nil
		},
	}
}

func delayedTailBackend(tailDelay time.Duration, text string) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return &tailDelayedStream{
				delay: tailDelay,
				events: []aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: text},
					{Kind: aipapi.EventResponseFinished},
				},
			}, nil
		},
	}
}

type delayedStream struct {
	delay  time.Duration
	events []aipapi.Event
	idx    int
	ctx    context.Context
}

func (d *delayedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if d.idx == 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if d.idx >= len(d.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := d.events[d.idx]
	d.idx++
	return ev, nil
}

func (d *delayedStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (d *delayedStream) Close() error { return nil }

type tailDelayedStream struct {
	delay  time.Duration
	events []aipapi.Event
	idx    int
}

func (t *tailDelayedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if t.idx == len(t.events)-1 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if t.idx >= len(t.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := t.events[t.idx]
	t.idx++
	return ev, nil
}

func (t *tailDelayedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (t *tailDelayedStream) Close() error { return nil }

func errorBackend(err error) execbackend.Backend {
	return execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			return nil, err
		},
	}
}

func parallelCall(selector string) *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
}

func completionEvents(text string) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	}
}

func TestParallelRace_FirstNonWhitespaceTokenWins(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"slow": delayedBackend(200*time.Millisecond, completionEvents("slow-response")),
		"fast": parallelBackend(completionEvents("fast-response")),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("slow:model!fast:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fast-response" {
		t.Fatalf("winner text: %q want fast-response", col.Text.String())
	}
}

func TestParallelRace_WhitespaceIgnoredForWinnerElection(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	wsEvents := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "   "},
		{Kind: aipapi.EventTextDelta, Delta: "  \n\t  "},
	}
	realEvents := completionEvents("real-answer")
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"ws":   parallelBackend(wsEvents),
		"real": parallelBackend(realEvents),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("ws:model!real:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "real-answer" {
		t.Fatalf("winner text: %q want real-answer", col.Text.String())
	}
}

func TestParallelRace_HandicapSchedulingStartsHighFirst(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var openOrder []string
	var mu sync.Mutex
	trackingBackend := func(name string, events []aipapi.Event) execbackend.Backend {
		return execbackend.Backend{
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				mu.Lock()
				openOrder = append(openOrder, name)
				mu.Unlock()
				return aipapi.NewFixedEventStream(events), nil
			},
		}
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"a": trackingBackend("a", completionEvents("a-resp")),
		"b": trackingBackend("b", completionEvents("b-resp")),
		"c": trackingBackend("c", completionEvents("c-resp")),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("[handicap=3]a:model![handicap=1]b:model!c:model"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aipapi.Collect(t.Context(), s)

	mu.Lock()
	defer mu.Unlock()
	if len(openOrder) == 0 {
		t.Fatal("expected at least one backend open")
	}
	if openOrder[0] != "a" {
		t.Fatalf("highest handicap (a) should start first, got order: %v", openOrder)
	}
}

func TestParallelRace_HandicapShortCircuitOnEarlyWinner(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var opens atomic.Int32
	fastBackend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream(completionEvents("fast")), nil
		},
	}
	slowBackend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream(completionEvents("slow")), nil
		},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"fast": fastBackend,
		"slow": slowBackend,
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("[handicap=10]fast:model!slow:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fast" {
		t.Fatalf("text: %q want fast", col.Text.String())
	}
	if opens.Load() == 0 {
		t.Fatalf("expected at least 1 open, got %d", opens.Load())
	}
	if opens.Load() > 2 {
		t.Fatalf("expected at most 2 opens, got %d", opens.Load())
	}
}

func TestParallelRace_MaxAttemptsBoundsParallelOpensDeterministically(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var opensA atomic.Int32
	var opensB atomic.Int32
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.MaxAttempts = 1
	ex.Backends = map[string]execbackend.Backend{
		"a": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensA.Add(1)
				return aipapi.NewFixedEventStream(completionEvents("a")), nil
			},
		},
		"b": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				opensB.Add(1)
				return aipapi.NewFixedEventStream(completionEvents("b")), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("a:model!b:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "a" {
		t.Fatalf("winner text: %q want a", got)
	}
	if opensA.Load() != 1 {
		t.Fatalf("backend a opens: got %d want 1", opensA.Load())
	}
	if opensB.Load() != 0 {
		t.Fatalf("backend b opens: got %d want 0", opensB.Load())
	}
}

func TestParallelRace_HandicapFastForwardOnTerminalFailure(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var opens atomic.Int32
	failBackend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return nil, errors.New("terminal failure")
		},
	}
	okBackend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			opens.Add(1)
			return aipapi.NewFixedEventStream(completionEvents("ok")), nil
		},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"fail": failBackend,
		"ok":   okBackend,
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("[handicap=10]fail:model!ok:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q want ok (fast-forward after handicapped fail)", col.Text.String())
	}
}

func TestParallelRace_PerLegTTFTTimeoutElimination(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	slowEvents := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"slow": delayedBackend(60*time.Second, slowEvents),
		"fast": parallelBackend(completionEvents("fast")),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("[ttft_timeout=1]slow:model!fast:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fast" {
		t.Fatalf("text: %q want fast (slow should be TTFT-eliminated)", col.Text.String())
	}
}

func TestParallelRace_TTFTTimeoutActuallyKillsLeg(t *testing.T) {
	// Both backends are slow, but one has a 1s TTFT timeout and the other has a
	// 200ms first-token delay. Without TTFT enforcement the slow leg blocks forever;
	// with it the slow leg's context is cancelled and the slightly-delayed backend wins.
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"stuck": delayedBackend(60*time.Second, completionEvents("stuck")),
		"ok":    delayedBackend(50*time.Millisecond, completionEvents("ok")),
	}
	ex.Rand = routing.NewSeededRng(1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s, err := ex.Execute(ctx, parallelCall("[ttft_timeout=1]stuck:model!ok:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "ok" {
		t.Fatalf("text: %q want ok (stuck leg should be killed by TTFT timeout)", col.Text.String())
	}
}

func TestParallelRace_KeepaliveEmittedWhileWaiting(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	// 400ms tail delay gives the 50ms keepalive timer ~8 chances to fire while
	// the winner stream idles before its final event; one emission suffices.
	ex.Backends = map[string]execbackend.Backend{
		"slow": delayedTailBackend(400*time.Millisecond, "ok"),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("slow:model!slow:model2"))
	if err != nil {
		t.Fatal(err)
	}
	ka, err := stream.NewKeepalive(s, stream.KeepaliveConfig{Interval: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ka.Close() }()
	var sawKeepalive bool
	for {
		ev, err := ka.Recv(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventWarning && ev.WarningCode == stream.KeepaliveEventCode {
			sawKeepalive = true
		}
		if ev.Kind == aipapi.EventResponseFinished {
			break
		}
	}
	if !sawKeepalive {
		t.Fatal("expected keepalive warning while waiting for winner stream")
	}
}

func TestParallelRace_CancelLosersBeforeClose(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var cancelCalled atomic.Int32
	var loserOpened atomic.Int32
	loserOpenedCh := make(chan struct{}, 8)
	cancelNotified := make(chan struct{}, 8)
	cancelableBackend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			loserOpened.Add(1)
			select {
			case loserOpenedCh <- struct{}{}:
			default:
			}
			return &cancelTrackingStream{
				events: completionEvents("loser"),
				ctx:    ctx,
				onCancel: func() {
					cancelCalled.Add(1)
					select {
					case cancelNotified <- struct{}{}:
					default:
					}
				},
				delay: 2 * time.Second,
			}, nil
		},
	}
	slowWinnerBackend := execbackend.Backend{
		Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
		Open: func(ctx context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
			for range 2 {
				select {
				case <-loserOpenedCh:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return aipapi.NewFixedEventStream(completionEvents("winner")), nil
		},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"winner": slowWinnerBackend,
		"loser1": cancelableBackend,
		"loser2": cancelableBackend,
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("winner:model!loser1:model!loser2:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "winner" {
		t.Fatalf("text: %q want winner", col.Text.String())
	}
	for i := range 2 {
		select {
		case <-cancelNotified:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected loser cancel notification %d/2", i+1)
		}
	}
	if loserOpened.Load() < 2 {
		t.Fatalf("expected both loser legs opened, got %d", loserOpened.Load())
	}
	if cancelCalled.Load() < 2 {
		t.Fatalf("expected Cancel on both losers, got %d", cancelCalled.Load())
	}
}

func TestParallelRace_WinnerPathLoserCleanupExactOnce(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var winnerStream, loserStream *parallelRaceCleanupStream
	slowReady := make(chan struct{}, 1)
	slowRelease := make(chan struct{})
	defer close(slowRelease)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"winner": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				winnerStream = &parallelRaceCleanupStream{
					waitReady: slowReady,
					events:    completionEvents("winner"),
				}
				return winnerStream, nil
			},
		},
		"loser": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				loserStream = &parallelRaceCleanupStream{
					blockNotify:  slowReady,
					blockRelease: slowRelease,
					events:       completionEvents("loser"),
				}
				return loserStream, nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("winner:model!loser:model"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if winnerStream == nil || loserStream == nil {
		t.Fatal("parallel streams must open")
	}
	assertParallelRaceCleanupOnce(t, loserStream, "loser")
	loserCancels := loserStream.cancelCount.Load()
	loserCloses := loserStream.closeCount.Load()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if loserStream.cancelCount.Load() != loserCancels {
		t.Fatalf("second outer close must not re-cancel loser: got %d want %d", loserStream.cancelCount.Load(), loserCancels)
	}
	if loserStream.closeCount.Load() != loserCloses {
		t.Fatalf("second outer close must not re-close loser: got %d want %d", loserStream.closeCount.Load(), loserCloses)
	}
}

func TestParallelRace_CloseWhileRecvBlockedIsRaceSafe(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	releaseTail := make(chan struct{})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"winner": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return &blockingTailStream{
					events: []aipapi.Event{
						{Kind: aipapi.EventResponseStarted},
						{Kind: aipapi.EventMessageStarted},
						{Kind: aipapi.EventTextDelta, Delta: "winner"},
						{Kind: aipapi.EventResponseFinished},
					},
					releaseTail: releaseTail,
				}, nil
			},
		},
		"loser": delayedBackend(2*time.Second, completionEvents("loser")),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("winner:model!loser:model"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ev, err := s.Recv(t.Context()); err != nil || ev.Kind != aipapi.EventTextDelta {
		t.Fatalf("winner text event = %+v, %v", ev, err)
	}
	recvDone := make(chan error, 1)
	go func() {
		_, err := s.Recv(t.Context())
		recvDone <- err
	}()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	close(releaseTail)
	select {
	case err := <-recvDone:
		if err != nil && !errors.Is(err, io.EOF) && !strings.Contains(err.Error(), "a-leg canceled") {
			t.Fatalf("blocked recv returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked recv did not finish")
	}
}

type cancelTrackingStream struct {
	events   []aipapi.Event
	idx      int
	ctx      context.Context
	onCancel func()
	delay    time.Duration
}

func (c *cancelTrackingStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if c.idx == 0 && c.delay > 0 {
		select {
		case <-time.After(c.delay):
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if c.idx >= len(c.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := c.events[c.idx]
	c.idx++
	return ev, nil
}

func (c *cancelTrackingStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	if c.onCancel != nil {
		c.onCancel()
	}
	return aipapi.CancelResult{}
}

func (c *cancelTrackingStream) Close() error { return nil }

type blockingTailStream struct {
	events      []aipapi.Event
	idx         int
	releaseTail <-chan struct{}
}

func (s *blockingTailStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if s.idx == len(s.events)-1 {
		select {
		case <-s.releaseTail:
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *blockingTailStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

func (s *blockingTailStream) Close() error { return nil }

func TestParallelRace_FailoverToNextArmWhenNoWinner(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"fail1": errorBackend(errors.New("fail1")),
		"fail2": errorBackend(errors.New("fail2")),
		"ok":    parallelBackend(completionEvents("fallback")),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("fail1:model!fail2:model|ok:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if col.Text.String() != "fallback" {
		t.Fatalf("text: %q want fallback", col.Text.String())
	}
}

func TestParallelRace_AllLegFailuresSurfaceJoinedError(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"fail1": errorBackend(errors.New("fail one")),
		"fail2": errorBackend(errors.New("fail two")),
	}
	ex.Rand = routing.NewSeededRng(1)
	_, err := ex.Execute(t.Context(), parallelCall("fail1:model!fail2:model"))
	if err == nil {
		t.Fatal("expected execute error for all-failing parallel arm")
	}
	if !strings.Contains(err.Error(), "parallel race arm failed") {
		t.Fatalf("expected parallel aggregation context, got %v", err)
	}
	if !strings.Contains(err.Error(), "fail1:model") || !strings.Contains(err.Error(), "fail2:model") {
		t.Fatalf("expected candidate keys in joined error, got %v", err)
	}
}

func TestParallelRace_NoFailoverAfterWinnerOutputCommitted(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var fallbackOpens atomic.Int32
	fallbackOpenedCh := make(chan struct{}, 8)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"a": parallelBackend(completionEvents("winner")),
		"b": delayedBackend(250*time.Millisecond, completionEvents("other")),
		"fallback": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				fallbackOpens.Add(1)
				select {
				case fallbackOpenedCh <- struct{}{}:
				default:
				}
				return aipapi.NewFixedEventStream(completionEvents("fallback")), nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("a:model!b:model|fallback:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "winner" {
		t.Fatalf("winner text: %q want winner", got)
	}
	select {
	case <-fallbackOpenedCh:
		t.Fatal("unexpected failover backend open after winner output committed")
	case <-time.After(300 * time.Millisecond):
	}
	if fallbackOpens.Load() != 0 {
		t.Fatalf("fallback backend opened %d times; want 0", fallbackOpens.Load())
	}
}

func TestParallelRace_ReasoningDeltaWins(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	reasoningEvents := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventReasoningDelta, Delta: "thinking..."},
		{Kind: aipapi.EventTextDelta, Delta: "after-reasoning"},
		{Kind: aipapi.EventResponseFinished},
	}
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"reason": parallelBackend(reasoningEvents),
		"slow":   delayedBackend(500*time.Millisecond, completionEvents("slow")),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("reason:model!slow:model"))
	if err != nil {
		t.Fatal(err)
	}
	var sawReasoning bool
	for {
		ev, err := s.Recv(t.Context())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventReasoningDelta {
			sawReasoning = true
		}
		if ev.Kind == aipapi.EventResponseFinished {
			break
		}
	}
	if !sawReasoning {
		t.Fatal("expected reasoning_delta from winning stream")
	}
}

func TestParallelRace_FailoverArmsAreNotFlattenedIntoSingleRace(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		// First failover arm (parallel): should race only these two. Any positive
		// delay discriminates a broken flattened race (instant c/d would win).
		"a": delayedBackend(30*time.Millisecond, completionEvents("first-arm-a")),
		"b": delayedBackend(40*time.Millisecond, completionEvents("first-arm-b")),
		// Second failover arm (parallel): must not participate unless first arm fully fails.
		"c": parallelBackend(completionEvents("second-arm-c")),
		"d": parallelBackend(completionEvents("second-arm-d")),
	}
	ex.Rand = routing.NewSeededRng(1)
	s, err := ex.Execute(t.Context(), parallelCall("a:model!b:model|c:model!d:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	got := col.Text.String()
	if got != "first-arm-a" && got != "first-arm-b" {
		t.Fatalf("winner text %q must come from first failover arm", got)
	}
}

func TestParallelRace_RecordsLoserAttemptLineage(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	var loserOpened atomic.Int32
	openGate := make(chan struct{})
	ex := runtime.TestExecutor()
	ex.Store = st
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = map[string]execbackend.Backend{
		"winner": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				<-openGate
				return aipapi.NewFixedEventStream(completionEvents("winner")), nil
			},
		},
		"loser": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(_ context.Context, _ aipapi.Call, _ routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				loserOpened.Add(1)
				return &cancelTrackingStream{
					events: completionEvents("loser"),
					delay:  2 * time.Second,
				}, nil
			},
		},
	}
	ex.Rand = routing.NewSeededRng(1)
	go func() {
		for {
			if loserOpened.Load() > 0 {
				close(openGate)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	call := &aipapi.Call{
		Session: aipapi.SessionRef{ContinuityKey: "parallel-lineage"},
		Route:   aipapi.RouteIntent{Selector: "winner:model!loser:model"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello")},
		}},
	}
	s, err := ex.Execute(t.Context(), call)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aipapi.Collect(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	leg, err := st.FetchALeg(t.Context(), call.Session.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	atts, err := st.LoadAttempts(t.Context(), leg.ALegID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) < 2 {
		t.Fatalf("expected at least 2 attempts (winner + loser), got %d", len(atts))
	}
	var sawSuccess, sawLoser bool
	for _, a := range atts {
		if a.Outcome == aipapi.AttemptSuccess {
			sawSuccess = true
		}
		if a.Outcome == aipapi.AttemptCancelled || a.Outcome == aipapi.AttemptSwallowedFailure {
			sawLoser = true
		}
	}
	if !sawSuccess || !sawLoser {
		t.Fatalf("expected success and loser attempt outcomes, got %#v", atts)
	}
}

type slogWrapper struct {
	slog.Handler
	onLog func(string)
}

func (w *slogWrapper) Handle(ctx context.Context, r slog.Record) error {
	w.onLog(r.Message)
	return w.Handler.Handle(ctx, r)
}

type mockRequestPartHook struct {
	id          string
	order       int
	failureMode sdkhooks.FailureMode
	handle      func(ctx context.Context, call *aipapi.Call, meta sdkhooks.PartMeta) error
}

func (m *mockRequestPartHook) ID() string                        { return m.id }
func (m *mockRequestPartHook) Order() int                        { return m.order }
func (m *mockRequestPartHook) FailureMode() sdkhooks.FailureMode { return m.failureMode }
func (m *mockRequestPartHook) HandleRequestParts(ctx context.Context, call *aipapi.Call, meta sdkhooks.PartMeta) error {
	return m.handle(ctx, call, meta)
}

func TestParallelRace_PostRequestHookExclusionRollback(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st

	var panicObserved atomic.Bool
	ex.Log = slog.New(&slogWrapper{
		Handler: slog.NewTextHandler(os.Stdout, nil),
		onLog: func(msg string) {
			if strings.Contains(strings.ToLower(msg), "panic") {
				panicObserved.Store(true)
			}
		},
	})

	ex.Backends = map[string]execbackend.Backend{
		"exclude": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				t.Error("open should not be called on excluded candidate")
				return nil, errors.New("should not be called")
			},
		},
		"winner": parallelBackend(completionEvents("winner-response")),
	}

	ex.Bus = hooks.New(hooks.Config{
		RequestPartHooks: []sdkhooks.RequestPartHook{
			&mockRequestPartHook{
				id:          "exclude_hook",
				order:       1,
				failureMode: sdkhooks.FailClosed,
				handle: func(ctx context.Context, call *aipapi.Call, meta sdkhooks.PartMeta) error {
					if meta.BackendID == "exclude" {
						call.Tools = []aipapi.ToolDef{{Name: "unsupported_tool"}}
					}
					return nil
				},
			},
		},
	})
	ex.Rand = routing.NewSeededRng(1)

	s, err := ex.Execute(t.Context(), parallelCall("exclude:model!winner:model"))
	if err != nil {
		t.Fatal(err)
	}
	col, err := aipapi.Collect(t.Context(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got := col.Text.String(); got != "winner-response" {
		t.Fatalf("got %q want winner-response", got)
	}

	if panicObserved.Load() {
		t.Fatal("detected panic in parallel race leg")
	}
}

func TestParallelRace_PostRequestHookRejectionNotLostWhenBudgetNil(t *testing.T) {
	t.Parallel()
	st := parallelStore(t)
	ex := runtime.TestExecutor()
	ex.Store = st

	ex.Backends = map[string]execbackend.Backend{
		"exclude1": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("should not be called")
			},
		},
		"exclude2": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, errors.New("should not be called")
			},
		},
	}

	ex.Bus = hooks.New(hooks.Config{
		RequestPartHooks: []sdkhooks.RequestPartHook{
			&mockRequestPartHook{
				id:          "exclude_hook",
				order:       1,
				failureMode: sdkhooks.FailClosed,
				handle: func(ctx context.Context, call *aipapi.Call, meta sdkhooks.PartMeta) error {
					call.Tools = []aipapi.ToolDef{{Name: "unsupported_tool"}}
					return nil
				},
			},
		},
	})
	ex.Rand = routing.NewSeededRng(1)

	_, err := ex.Execute(t.Context(), parallelCall("exclude1:model!exclude2:model"))
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "missing required capabilities") {
		t.Fatalf("expected capability negotiation reject error, got: %v", err)
	}
}
