package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func newCharacterizationStream(t *testing.T, inner aipapi.ManagedEventStream) *retryRecvStream {
	t.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bus := hooks.New(hooks.Config{})
	sel, err := routing.Parse("openai:gpt-4")
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = bus
	s := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			baseline: aipapi.Call{
				Route:    aipapi.RouteIntent{Selector: "openai:gpt-4"},
				Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("x")}}},
			},
			aLegID:  "a-characterization",
			traceID: "t-characterization",
		}),
		recovery: &recoveryController{budget: &attemptBudget{max: 3}, sel: sel, session: &routing.SessionRoutingState{}, excluded: map[string]struct{}{}, rng: routing.NewSeededRng(1)}, attempt: testAttemptSlot(b2bua.BLegRecord{BLegID: "b-characterization", Seq: 1}, routing.AttemptCandidate{
			Key:     "openai:gpt-4",
			Primary: routing.Primary{Backend: "openai", Model: "gpt-4"},
		}, authorityLifecycle{}),
		responsePipeline: newResponsePipeline(),
	}
	testStoreInner(s, inner)
	return s
}

func TestRetryRecvStreamCharacterization_NormalEventOrdering(t *testing.T) {
	t.Parallel()
	s := newCharacterizationStream(t, aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello"},
		{Kind: aipapi.EventResponseFinished},
	}))

	var got []aipapi.Event
	for range 4 {
		ev, err := s.Recv(context.Background())
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got = append(got, ev)
	}
	if len(got) != 4 {
		t.Fatalf("events=%d want 4: %#v", len(got), got)
	}
	wantKinds := []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
		aipapi.EventResponseFinished,
	}
	for i, want := range wantKinds {
		if got[i].Kind != want {
			t.Fatalf("event %d kind=%q want %q; all=%#v", i, got[i].Kind, want, got)
		}
	}
	if got[2].Delta != "hello" {
		t.Fatalf("text delta=%q want hello", got[2].Delta)
	}
	if _, err := s.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("post-finish Recv error=%v want io.EOF", err)
	}
	req, att := testTerminalOwners(s)
	for name, term := range map[string]*streamTerminal{"request": req, "attempt": att} {
		if term == nil {
			t.Fatalf("%s terminal is nil", name)
		}
		if !term.Owner().State().IsTerminal() {
			t.Fatalf("%s terminal state=%v", name, term.Owner().State())
		}
		out, ok := term.Owner().Outcome()
		if !ok || out.Command != sdkterminal.CommandNormalFinish {
			t.Fatalf("%s outcome=%+v ok=%v want normal_finish", name, out, ok)
		}
	}
}

func TestRetryRecvStreamCharacterization_EOFWithoutFinishIsTerminal(t *testing.T) {
	t.Parallel()
	s := newCharacterizationStream(t, aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "partial"},
	}))
	for i, want := range []aipapi.EventKind{
		aipapi.EventResponseStarted,
		aipapi.EventMessageStarted,
		aipapi.EventTextDelta,
	} {
		ev, err := s.Recv(context.Background())
		if err != nil || ev.Kind != want {
			t.Fatalf("event %d=%#v err=%v want kind %q", i, ev, err, want)
		}
	}
	ev, err := s.Recv(context.Background())
	if ev.Kind != "" || !errors.Is(err, io.EOF) {
		t.Fatalf("terminal Recv event=%#v err=%v want zero event/io.EOF", ev, err)
	}
	if _, err := s.Recv(context.Background()); !errors.Is(err, io.EOF) {
		t.Fatalf("post-EOF Recv error=%v want io.EOF", err)
	}
	req, att := testTerminalOwners(s)
	for name, term := range map[string]*streamTerminal{"request": req, "attempt": att} {
		out, ok := term.Owner().Outcome()
		if !ok || out.Command != sdkterminal.CommandEOF {
			t.Fatalf("%s outcome=%+v ok=%v want eof", name, out, ok)
		}
	}
}

type characterizationBlockingInner struct {
	recvEntered chan struct{}
	closeCalled chan struct{}
	release     chan struct{}
	closeOnce   sync.Once
	cancelCount atomic.Int32
	closeCount  atomic.Int32
}

func (s *characterizationBlockingInner) Recv(ctx context.Context) (aipapi.Event, error) {
	select {
	case s.recvEntered <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return aipapi.Event{}, ctx.Err()
	case <-s.release:
		return aipapi.Event{}, io.EOF
	}
}

func (s *characterizationBlockingInner) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCount.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (s *characterizationBlockingInner) Close() error {
	s.closeCount.Add(1)
	s.closeOnce.Do(func() {
		close(s.closeCalled)
		close(s.release)
	})
	return nil
}

func TestRetryRecvStreamCharacterization_BlockedRecvCloseOrdering(t *testing.T) {
	t.Parallel()
	inner := &characterizationBlockingInner{
		recvEntered: make(chan struct{}, 1),
		closeCalled: make(chan struct{}),
		release:     make(chan struct{}),
	}
	s := newCharacterizationStream(t, inner)

	type recvResult struct {
		ev  aipapi.Event
		err error
	}
	recvDone := make(chan recvResult, 1)
	go func() {
		ev, err := s.Recv(context.Background())
		recvDone <- recvResult{ev: ev, err: err}
	}()
	select {
	case <-inner.recvEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("Recv did not reach backend I/O")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- s.Close() }()
	select {
	case <-inner.closeCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not reach backend stream")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}
	select {
	case got := <-recvDone:
		if got.ev.Kind != "" || got.ev.Delta != "" || got.ev.ErrorCode != "" {
			t.Fatalf("Recv event=%#v want zero event after Close", got.ev)
		}
		if !errors.Is(got.err, io.EOF) {
			t.Fatalf("Recv error=%v want io.EOF after Close owns terminal", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Recv did not return after Close")
	}
	if got := inner.cancelCount.Load(); got != 1 {
		t.Fatalf("backend Cancel calls=%d want 1", got)
	}
	if got := inner.closeCount.Load(); got != 1 {
		t.Fatalf("backend Close calls=%d want 1", got)
	}
	req, att := testTerminalOwners(s)
	for name, term := range map[string]*streamTerminal{"request": req, "attempt": att} {
		out, ok := term.Owner().Outcome()
		if !ok || out.Command != sdkterminal.CommandClose {
			t.Fatalf("%s outcome=%+v ok=%v want close", name, out, ok)
		}
		if !term.Owner().State().IsTerminal() {
			t.Fatalf("%s state=%q want terminal", name, term.Owner().State())
		}
	}
}

func TestRetryRecvStreamCharacterization_CancelAndTimeoutTerminalVocabulary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		wantErr error
		wantCmd sdkterminal.Command
	}{
		{
			name: "caller cancellation",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
			wantCmd: sdkterminal.CommandCancel,
		},
		{
			name: "deadline timeout",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			wantErr: context.DeadlineExceeded,
			wantCmd: sdkterminal.CommandTimeout,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inner := &characterizationBlockingInner{
				recvEntered: make(chan struct{}, 1),
				closeCalled: make(chan struct{}),
				release:     make(chan struct{}),
			}
			s := newCharacterizationStream(t, inner)
			ctx, cancel := tt.ctx()
			defer cancel()
			ev, err := s.Recv(ctx)
			if ev.Kind != "" {
				t.Fatalf("event=%#v want zero event", ev)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Recv error=%v want %v", err, tt.wantErr)
			}
			if got := inner.cancelCount.Load(); got != 1 {
				t.Fatalf("backend Cancel calls=%d want 1", got)
			}
			if got := inner.closeCount.Load(); got != 1 {
				t.Fatalf("backend Close calls=%d want 1", got)
			}
			req, att := testTerminalOwners(s)
			for name, term := range map[string]*streamTerminal{"request": req, "attempt": att} {
				out, ok := term.Owner().Outcome()
				if !ok || out.Command != tt.wantCmd {
					t.Fatalf("%s outcome=%+v ok=%v want %s", name, out, ok, tt.wantCmd)
				}
			}
		})
	}
}

type characterizationErrorInner struct {
	err error
}

func (s characterizationErrorInner) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, s.err
}

func (characterizationErrorInner) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (characterizationErrorInner) Close() error { return nil }

func TestRetryRecvStreamCharacterization_PreCommitFatalError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("backend pre-commit failure")
	s := newCharacterizationStream(t, characterizationErrorInner{err: sentinel})
	ev, err := s.Recv(context.Background())
	if ev.Kind != "" {
		t.Fatalf("event=%#v want zero event", ev)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Recv error=%v want sentinel", err)
	}
	req, att := testTerminalOwners(s)
	for name, term := range map[string]*streamTerminal{"request": req, "attempt": att} {
		out, ok := term.Owner().Outcome()
		if !ok || out.Command != sdkterminal.CommandPartialError {
			t.Fatalf("%s outcome=%+v ok=%v want partial_error", name, out, ok)
		}
	}
}

type characterizationPanicInner struct {
	called atomic.Int32
}

func (s *characterizationPanicInner) Recv(context.Context) (aipapi.Event, error) {
	if s.called.Add(1) == 1 {
		return aipapi.Event{Kind: aipapi.EventTextDelta, Delta: "partial"}, nil
	}
	panic("backend recv panic")
}

func (*characterizationPanicInner) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

func (*characterizationPanicInner) Close() error { return nil }

func TestRetryRecvStreamCharacterization_PostCommitPanicIsFatal(t *testing.T) {
	t.Parallel()
	s := newCharacterizationStream(t, &characterizationPanicInner{})
	if ev, err := s.Recv(context.Background()); err != nil || ev.Kind != aipapi.EventTextDelta || ev.Delta != "partial" {
		t.Fatalf("first Recv event=%#v err=%v want partial text", ev, err)
	}
	_, err := s.Recv(context.Background())
	if err == nil {
		t.Fatal("second Recv must surface panic failure")
	}
	if aipapi.IsRecoverablePreOutput(err) {
		t.Fatalf("post-commit panic was recoverable: %v", err)
	}
	var upstream *aipapi.UpstreamFailureError
	if !errors.As(err, &upstream) || upstream.Phase != aipapi.PhasePostOutput || upstream.Recoverable {
		t.Fatalf("panic error=%v want non-recoverable post-output UpstreamFailureError", err)
	}
	req, att := testTerminalOwners(s)
	for name, term := range map[string]*streamTerminal{"request": req, "attempt": att} {
		out, ok := term.Owner().Outcome()
		if !ok || out.Command != sdkterminal.CommandPartialError {
			t.Fatalf("%s outcome=%+v ok=%v want partial_error", name, out, ok)
		}
	}
}

func TestRetryRecvStreamCharacterization_LosingTerminalClaimWaitsAndDoesNotRepeatEffects(t *testing.T) {
	t.Parallel()
	s := newCharacterizationStream(t, nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	effectContext := make(chan terminalContextObservation, 1)
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var effects atomic.Int32
	winnerDone := make(chan coreTerminalResult, 1)
	go func() {
		result := testTerminalizeRequest(parent, s, sdkterminal.CommandClose, func(ctx context.Context) error {
			effects.Add(1)
			deadline, hasDeadline := ctx.Deadline()
			cancel()
			effectContext <- terminalContextObservation{err: ctx.Err(), hasDeadline: hasDeadline, deadline: deadline}
			close(entered)
			<-release
			return nil
		})
		winnerDone <- coreTerminalResult{result: result}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("winning terminal effects did not start")
	}
	loserDone := make(chan coreTerminalResult, 1)
	go func() {
		result := testTerminalizeRequest(context.Background(), s, sdkterminal.CommandEOF, nil)
		loserDone <- coreTerminalResult{result: result}
	}()
	select {
	case <-loserDone:
		t.Fatal("losing terminal claim returned before winner effects completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	winner := <-winnerDone
	loser := <-loserDone
	if !winner.result.Won {
		t.Fatalf("winner result=%+v", winner.result)
	}
	if loser.result.Won || !errors.Is(loser.result.Err, sdkterminal.ErrConflict) {
		t.Fatalf("loser result=%+v want conflict", loser.result)
	}
	if got := effects.Load(); got != 1 {
		t.Fatalf("terminal effects=%d want exactly once", got)
	}
	select {
	case observation := <-effectContext:
		if observation.err != nil || !observation.hasDeadline || !observation.deadline.After(time.Now()) {
			t.Fatalf("terminal context=%+v want detached bounded context", observation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal effect context observation missing")
	}
	if loser.result.Outcome.Command != sdkterminal.CommandClose {
		t.Fatalf("loser outcome command=%q want close", loser.result.Outcome.Command)
	}
	if loser.result.State != sdkterminal.StateReleased {
		t.Fatalf("loser state=%q want released", loser.result.State)
	}
}

type coreTerminalResult struct {
	result coreterm.Result
}

type terminalContextObservation struct {
	err         error
	hasDeadline bool
	deadline    time.Time
}
