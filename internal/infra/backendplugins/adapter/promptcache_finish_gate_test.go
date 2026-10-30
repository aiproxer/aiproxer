package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// promptCacheGateSession scripts the connector frame order that exposes the
// prompt-cache finish/terminal boundary: the prompt-cache observation and the
// canonical response_finished event are delivered while the terminal frame is
// still withheld. The test releases the terminal explicitly, so the ordering is
// deterministic instead of relying on goroutine scheduling.
type promptCacheGateSession struct {
	emitObservation bool
	// observationCount overrides the single observation a session emits when it
	// is greater than zero.
	observationCount int
	// observationAfterFinish emits the observation after the finish event but
	// still before the terminal, matching connectors that forward the sideband
	// at end of stream rather than as soon as it is observed.
	observationAfterFinish bool
	skipTerminal           bool
	executeErr             error
	terminalStatus         backendplugin.TerminalStatus
	terminalError          *backendplugin.PluginError
	afterFinish            []*backendplugin.CanonicalEvent

	finishQueued    chan struct{}
	releaseTerminal chan struct{}
	releaseOnce     sync.Once
	executed        chan struct{}
}

func newPromptCacheGateSession(emitObservation bool, status backendplugin.TerminalStatus, afterFinish ...*backendplugin.CanonicalEvent) *promptCacheGateSession {
	return &promptCacheGateSession{
		emitObservation: emitObservation,
		terminalStatus:  status,
		afterFinish:     afterFinish,
		finishQueued:    make(chan struct{}),
		releaseTerminal: make(chan struct{}),
		executed:        make(chan struct{}),
	}
}

func (s *promptCacheGateSession) Resolve(context.Context, *string) (backendplugin.ResolvedProfile, error) {
	return backendplugin.ResolvedProfile{}, nil
}

func (s *promptCacheGateSession) ListModels(context.Context, uint32) (backendplugin.ListModelsResponse, error) {
	return backendplugin.ListModelsResponse{}, nil
}

func (s *promptCacheGateSession) Close(context.Context) error { return nil }

func (s *promptCacheGateSession) allowTerminal() {
	s.releaseOnce.Do(func() { close(s.releaseTerminal) })
}

func (s *promptCacheGateSession) Execute(stream backendplugin.ExecuteStream) error {
	defer close(s.executed)
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != backendplugin.ClientFrameStart || start.Invocation == nil {
		return errors.New("expected start frame")
	}
	inv := start.Invocation
	if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameAccepted}); err != nil {
		return err
	}
	seq := uint64(1)
	sendObservation := func() error {
		total := int64(1)
		count := s.observationCount
		if count <= 0 {
			count = 1
		}
		for i := range count {
			observation := promptcache.Observation{
				ALegID:            inv.ALegID,
				BLegID:            inv.BLegID,
				BackendInstanceID: "instance",
				TargetID:          promptcache.TargetID(fmt.Sprintf("target-%d", i+1)),
				GenerationID:      "generation",
				Lifecycle:         promptcache.LifecycleSlidingExpiry,
				Timing:            promptcache.Timing{ObservedAt: time.Unix(1, 0).UTC()},
				Renewable:         true,
				Handle:            promptcache.Handle(fmt.Sprintf("handle-%d", i+1)),
				Evidence:          promptcache.CacheEvidence{TotalTokens: &total},
			}
			if err := stream.Send(backendplugin.ServerFrame{
				Kind: backendplugin.ServerFramePromptCacheObservation, Sequence: seq,
				PromptCacheObservation: &observation,
			}); err != nil {
				return err
			}
			seq++
		}
		return nil
	}
	if s.emitObservation && !s.observationAfterFinish {
		if err := sendObservation(); err != nil {
			return err
		}
	}
	if err := stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameEvent, Sequence: seq,
		Event: &backendplugin.CanonicalEvent{Kind: backendplugin.EventResponseFinished},
	}); err != nil {
		return err
	}
	seq++
	close(s.finishQueued)
	if s.skipTerminal {
		return s.executeErr
	}
	select {
	case <-s.releaseTerminal:
	case <-stream.Context().Done():
		return stream.Context().Err()
	}
	if s.emitObservation && s.observationAfterFinish {
		if err := sendObservation(); err != nil {
			return err
		}
	}
	for _, event := range s.afterFinish {
		if err := stream.Send(backendplugin.ServerFrame{Kind: backendplugin.ServerFrameEvent, Sequence: seq, Event: event}); err != nil {
			return err
		}
		seq++
	}
	status := s.terminalStatus
	if status == "" {
		status = backendplugin.TerminalSuccess
	}
	return stream.Send(backendplugin.ServerFrame{
		Kind: backendplugin.ServerFrameTerminal, Sequence: seq,
		Terminal: &backendplugin.Terminal{Status: status, Error: s.terminalError},
	})
}

func openPromptCacheGateStream(t *testing.T, session *promptCacheGateSession, promptCache bool) *managedStream {
	t.Helper()
	neg := backendplugin.Negotiation{Compatible: true}
	if promptCache {
		neg.NegotiatedMinor = backendplugin.ProtocolMinorPromptCacheResidency
		neg.EnabledFeatures = []string{backendplugin.FeaturePromptCacheResidency}
	}
	ms, err := openStream(context.Background(), session, Options{
		InstanceID:       "instance",
		MaxPendingEvents: 8,
		Negotiation:      neg,
	}, testCallWithMessages("gate-request"), routing.AttemptCandidate{
		Key:     "fake:fake-model",
		Primary: routing.Primary{Backend: "fake", Model: "fake-model"},
	})
	if err != nil {
		t.Fatalf("openStream: %v", err)
	}
	stream, ok := ms.(*managedStream)
	if !ok {
		t.Fatalf("openStream returned %T, want *managedStream", ms)
	}
	t.Cleanup(func() {
		session.allowTerminal()
		_ = stream.Close()
		select {
		case <-session.executed:
		case <-time.After(5 * time.Second):
			t.Errorf("connector Execute did not return after Close")
		}
	})
	return stream
}

// TestManagedStream_PromptCacheFinishWaitsForTerminalCommit is the deterministic
// regression for the lost keep-warm target. The connector delivers its
// prompt-cache observation and response_finished before the terminal frame, and
// the terminal frame is what commits the observation. The runtime drains the
// observation sideband exactly once at the finish, so a finish released before
// the terminal commit is observed with an empty sideband and the maintenance
// target is silently discarded.
func TestManagedStream_PromptCacheFinishWaitsForTerminalCommit(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalSuccess)
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ev, err := stream.Recv(waitCtx)
	if err == nil {
		drained := stream.DrainPromptCacheObservations()
		t.Fatalf("response_finished %q was released before the terminal committed the prompt-cache observation; drained=%d observations, want the finish held until the terminal",
			ev.Kind, len(drained))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv before the terminal: err=%v, want deadline exceeded", err)
	}

	session.allowTerminal()
	ev, err = stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after the terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event after the terminal = %q, want response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 1 {
		t.Fatalf("drained %d observations at the finish boundary, want 1", len(got))
	}
}

// TestManagedStream_PromptCacheLateObservationKeptForFinishBoundary covers the
// connector forward path, which forwards the observation sideband at end of
// stream: the observation frame arrives after response_finished and before the
// terminal. The finish must still be withheld so the finish-boundary drain sees
// the committed observation.
func TestManagedStream_PromptCacheLateObservationKeptForFinishBoundary(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalSuccess)
	session.observationAfterFinish = true
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv before the late observation and terminal: err=%v, want the finish withheld", err)
	}

	session.allowTerminal()
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after the terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event = %q, want response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 1 {
		t.Fatalf("drained %d observations for a late sideband, want 1", len(got))
	}
}

// TestManagedStream_PromptCacheFinishKeepsEveryCommittedObservation proves the
// finish-boundary drain preserves the full bounded observation set, not just
// the first target.
func TestManagedStream_PromptCacheFinishKeepsEveryCommittedObservation(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalSuccess)
	session.observationCount = 2
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv before the terminal: err=%v, want the finish withheld", err)
	}

	session.allowTerminal()
	if _, err := stream.Recv(context.Background()); err != nil {
		t.Fatalf("Recv after the terminal: %v", err)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 2 {
		t.Fatalf("drained %d observations at the finish boundary, want 2", len(got))
	}
}

// TestManagedStream_PromptCacheTerminalFailureStillDropsObservations pins the
// failure half of the gate: the finish is still released after the terminal,
// but a failed terminal has discarded the observations, so the finish-boundary
// drain stays empty.
func TestManagedStream_PromptCacheTerminalFailureStillDropsObservations(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalFailure)
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv before a failed terminal: err=%v, want the finish withheld", err)
	}

	session.allowTerminal()
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after the failed terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event after the failed terminal = %q, want response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 0 {
		t.Fatalf("failed terminal committed %d observations, want 0", len(got))
	}
}

// TestManagedStream_PromptCacheTerminalErrorSurfacesAfterFinish proves the
// terminal error state is recorded before a withheld finish is released, so
// the finish never overtakes (or hides) the failure: the next receive returns
// the classified terminal error, not EOF.
func TestManagedStream_PromptCacheTerminalErrorSurfacesAfterFinish(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalFailure)
	session.terminalError = &backendplugin.PluginError{Code: backendplugin.ErrorCodeInternal, Message: "terminal failed after finish"}
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv before the failed terminal: err=%v, want the finish withheld", err)
	}

	session.allowTerminal()
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after the failed terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event after the failed terminal = %q, want response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 0 {
		t.Fatalf("failed terminal committed %d observations, want 0", len(got))
	}

	_, err = stream.Recv(context.Background())
	var classified *ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("post-finish failure = %T %v, want the classified terminal error", err, err)
	}
	if classified.Code != string(backendplugin.ErrorCodeInternal) {
		t.Fatalf("post-finish failure code = %q, want %q", classified.Code, backendplugin.ErrorCodeInternal)
	}
	if !strings.Contains(classified.Message, "terminal failed after finish") {
		t.Fatalf("post-finish failure message = %q, want the terminal error message", classified.Message)
	}
}

// TestManagedStream_FinishWithoutPromptCacheIsNotHeld pins the scope of the
// gate: a stream that did not negotiate prompt-cache residency must release
// response_finished without waiting for a terminal frame.
func TestManagedStream_FinishWithoutPromptCacheIsNotHeld(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(false, backendplugin.TerminalSuccess)
	stream := openPromptCacheGateStream(t, session, false)
	<-session.finishQueued

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ev, err := stream.Recv(waitCtx)
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event = %q, want response_finished", ev.Kind)
	}
}

// TestManagedStream_PromptCachePostFinishEventFailsWithoutDeadlock proves a
// post-finish canonical event is rejected explicitly instead of being queued
// behind the withheld finish, where a burst could fill the bounded event
// channel and block the Execute pump before the terminal frame. The finish is
// still delivered, the uncommitted observations are dropped, and the protocol
// failure surfaces on the next receive.
func TestManagedStream_PromptCachePostFinishEventFailsWithoutDeadlock(t *testing.T) {
	t.Parallel()
	delta := "after-finish"
	session := newPromptCacheGateSession(true, backendplugin.TerminalSuccess, &backendplugin.CanonicalEvent{
		Kind: backendplugin.EventTextDelta, Delta: &delta,
	})
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Recv before the terminal: err=%v, want deadline exceeded", err)
	}

	session.allowTerminal()
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after the terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event after the terminal = %q, want response_finished", ev.Kind)
	}

	_, err = stream.Recv(context.Background())
	if err == nil {
		t.Fatal("post-finish canonical event did not surface a protocol failure")
	}
	var classified *ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("post-finish failure = %T %v, want *ClassifiedError", err, err)
	}
	if classified.Code != "protocol" {
		t.Fatalf("post-finish failure code = %q, want protocol", classified.Code)
	}
	if !strings.Contains(classified.Message, "canonical event after response_finished") {
		t.Fatalf("post-finish failure message = %q, want the explicit post-finish reason", classified.Message)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 0 {
		t.Fatalf("protocol-failed stream committed %d observations, want 0", len(got))
	}
}

// TestManagedStream_PromptCacheFinishCancelKeepsPendingFinish proves a caller
// cancellation while the finish is withheld never loses the finish: a later
// receive returns the same event after the terminal settles it.
func TestManagedStream_PromptCacheFinishCancelKeepsPendingFinish(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalSuccess)
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Recv(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Recv: err=%v, want context canceled", err)
	}

	session.allowTerminal()
	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after the terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event after the terminal = %q, want response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 1 {
		t.Fatalf("drained %d observations, want 1", len(got))
	}
}

// TestManagedStream_PromptCacheCancelledStreamDeliversBufferedFinishPromptly
// pins the cancellation tail of Recv. A stream whose own context is canceled
// (Close or a forced cancel) still delivers one already-queued event before the
// cancellation error, and the prompt-cache finish gate must not turn that into
// a wait for a terminal that may never arrive. A canceled stream cannot commit
// a new observation, so it also never arms maintenance from the buffered
// finish.
func TestManagedStream_PromptCacheCancelledStreamDeliversBufferedFinishPromptly(t *testing.T) {
	t.Parallel()
	execCtx, cancelExec := context.WithCancel(context.Background())
	cancelExec()
	stream := &managedStream{
		ctx:    execCtx,
		events: make(chan aipapi.Event, 1),
		opt: Options{
			InstanceID: "instance",
			Negotiation: backendplugin.Negotiation{
				Compatible:      true,
				NegotiatedMinor: backendplugin.ProtocolMinorPromptCacheResidency,
				EnabledFeatures: []string{backendplugin.FeaturePromptCacheResidency},
			},
		},
		promptCacheOutcomeClosed: make(chan struct{}),
	}
	stream.events <- aipapi.Event{Kind: aipapi.EventResponseFinished}

	waitCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	ev, err := stream.recvAfterStreamCanceled(waitCtx)
	if err != nil {
		t.Fatalf("cancelled Recv: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event = %q, want the buffered response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 0 {
		t.Fatalf("cancelled stream committed %d observations, want 0", len(got))
	}
	if _, err := stream.Recv(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-finish Recv: err=%v, want the stream context cancellation and no duplicate event", err)
	}
}

// TestManagedStream_PromptCacheFinishReleasesWhenExecuteEndsWithoutTerminal
// bounds the gate by the connector lifecycle: if the Execute session returns
// without a terminal, the withheld finish is still released and the uncommitted
// observations are discarded instead of leaving the client waiting forever.
func TestManagedStream_PromptCacheFinishReleasesWhenExecuteEndsWithoutTerminal(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalSuccess)
	session.skipTerminal = true
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after Execute returned without a terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event = %q, want response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 0 {
		t.Fatalf("terminal-free execute committed %d observations, want 0", len(got))
	}
}

// TestManagedStream_PromptCacheMissingTerminalErrorSurfacesAfterFinish proves a
// missing terminal does not hide the Execute failure: the withheld finish is
// released, the uncommitted observations are dropped, and the next receive
// returns the classified connector failure rather than EOF.
func TestManagedStream_PromptCacheMissingTerminalErrorSurfacesAfterFinish(t *testing.T) {
	t.Parallel()
	session := newPromptCacheGateSession(true, backendplugin.TerminalSuccess)
	session.skipTerminal = true
	session.executeErr = errors.New("connector ended before terminal")
	stream := openPromptCacheGateStream(t, session, true)
	<-session.finishQueued

	ev, err := stream.Recv(context.Background())
	if err != nil {
		t.Fatalf("Recv after Execute ended without a terminal: %v", err)
	}
	if ev.Kind != aipapi.EventResponseFinished {
		t.Fatalf("event = %q, want response_finished", ev.Kind)
	}
	if got := stream.DrainPromptCacheObservations(); len(got) != 0 {
		t.Fatalf("terminal-free failed execute committed %d observations, want 0", len(got))
	}

	_, err = stream.Recv(context.Background())
	var classified *ClassifiedError
	if !errors.As(err, &classified) {
		t.Fatalf("post-finish failure = %T %v, want the classified connector failure", err, err)
	}
	if classified.Code != "transport" {
		t.Fatalf("post-finish failure code = %q, want transport", classified.Code)
	}
	if !strings.Contains(classified.Message, "connector ended before terminal") {
		t.Fatalf("post-finish failure message = %q, want the connector failure", classified.Message)
	}
}
