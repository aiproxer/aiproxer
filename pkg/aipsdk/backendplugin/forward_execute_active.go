package backendplugin

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type controlCancelReq struct {
	reason     CancelReason
	deadlineMS int64
	outcome    bool
}

type controlObservation struct {
	cancel *controlCancelReq
	err    error
}

type upstreamObsKind int

const (
	upstreamObsEvent upstreamObsKind = iota
	upstreamObsEOF
	upstreamObsError
)

type upstreamObservation struct {
	kind  upstreamObsKind
	event aipapi.Event
	err   error
}

type cancelWorkerResult struct {
	res    aipapi.CancelResult
	reason CancelReason
}

type cancellationTiming struct {
	deadline time.Time
}

// cancellationLifecycle is the one source of truth for a negotiated cancel.
// The control reader records receipt before queueing the observation, so an
// already-received CANCEL cannot be lost when an upstream EOF wins a select.
// The coordinator remains the only owner of starting the lifecycle and sending
// its outcome; the worker owns only the physical adapter call and its forced
// close fallback.
type cancellationLifecycle struct {
	mu          sync.Mutex
	request     *controlCancelReq
	started     bool
	outcomeSent bool
	done        chan struct{}
	result      cancelWorkerResult
}

func newCancellationLifecycle() *cancellationLifecycle {
	return &cancellationLifecycle{done: make(chan struct{})}
}

func (c *cancellationLifecycle) observe(req controlCancelReq) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.request == nil {
		reqCopy := req
		c.request = &reqCopy
	}
}

func (c *cancellationLifecycle) requested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request != nil
}

func (c *cancellationLifecycle) requestValue() (controlCancelReq, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.request == nil {
		return controlCancelReq{}, false
	}
	return *c.request, true
}

func (c *cancellationLifecycle) needsOutcome() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request != nil && c.request.outcome
}

func (c *cancellationLifecycle) start(run func(controlCancelReq) cancelWorkerResult) bool {
	c.mu.Lock()
	if c.started || c.request == nil {
		c.mu.Unlock()
		return false
	}
	c.started = true
	req := *c.request
	c.mu.Unlock()

	go func() {
		result := run(req)
		c.mu.Lock()
		c.result = result
		close(c.done)
		c.mu.Unlock()
	}()
	return true
}

func (c *cancellationLifecycle) wait() cancelWorkerResult {
	<-c.done
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

func (c *cancellationLifecycle) doneCh() <-chan struct{} { return c.done }

func (c *cancellationLifecycle) sendOutcome(sequencer *frameSequencer) error {
	result := c.wait()
	c.mu.Lock()
	if c.outcomeSent {
		c.mu.Unlock()
		return nil
	}
	c.outcomeSent = true
	c.mu.Unlock()

	outcome := &CancelOutcome{
		Acknowledged: result.res.Err == nil,
		Reason:       result.reason,
		Mode:         result.res.Mode,
	}
	if result.res.Err != nil {
		outcome.Detail = genericCancelOutcomeDetail
	}
	return sequencer.Send(ServerFrame{
		Kind:          ServerFrameCancelOutcome,
		CancelOutcome: outcome,
	})
}

// runManagedCancellation establishes the termination contract for adapters:
// Cancel must return when its effective context is done or when Close is
// called. We force Close at the effective deadline and then join Cancel before
// reporting completion, so this coordinator never leaves an unjoinable worker.
func runManagedCancellation(streamCtx context.Context, ms aipapi.ManagedEventStream, closeManaged func(), req controlCancelReq, timing cancellationTiming) cancelWorkerResult {
	cancelCtx, cancelFn := context.WithDeadline(context.WithoutCancel(streamCtx), timing.deadline)
	defer cancelFn()

	resultCh := make(chan aipapi.CancelResult, 1)
	go func() {
		cause := CancelCauseFromCancelReason(req.reason)
		if !req.outcome {
			cause = aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "plugin_cancel"}
		}
		resultCh <- invokeManagedCancel(cancelCtx, ms, cause)
	}()

	var result aipapi.CancelResult
	select {
	case result = <-resultCh:
	case <-cancelCtx.Done():
		closeManaged()
		// ManagedEventStream.Cancel is required to become joinable after Close.
		result = <-resultCh
	}
	return cancelWorkerResult{res: result, reason: req.reason}
}

func invokeManagedCancel(ctx context.Context, ms aipapi.ManagedEventStream, cause aipapi.CancelCause) (result aipapi.CancelResult) {
	defer func() {
		if recover() != nil {
			result = aipapi.CancelResult{Mode: aipapi.CancelModeTransport, Err: errors.New("backendplugin: managed stream cancel panicked")}
		}
	}()
	return ms.Cancel(ctx, cause)
}

func effectiveCancellationTiming(streamCtx context.Context, req controlCancelReq) cancellationTiming {
	grace := max(0, fallbackCancelGrace)
	deadline := time.Now().Add(grace)
	if req.deadlineMS > 0 {
		if peerDeadline := time.UnixMilli(req.deadlineMS); peerDeadline.Before(deadline) {
			deadline = peerDeadline
		}
	}
	if streamDeadline, ok := streamCtx.Deadline(); ok && streamDeadline.Before(deadline) {
		deadline = streamDeadline
	}
	return cancellationTiming{deadline: deadline}
}

// forwardActiveExecute coordinates bidirectional execution for a negotiated stream.
// The calling goroutine acts as coordinator and is the sole sender through sequencer.
func forwardActiveExecute(stream ExecuteStream, sequencer *frameSequencer, ms aipapi.ManagedEventStream, negotiation Negotiation) error {
	// Initial accounting evidence created while opening is sent before any canonical frames.
	if err := forwardAllAccountingEvidence(sequencer, ms, negotiation); err != nil {
		return err
	}

	var closeOnce sync.Once
	closeManaged := func() { closeOnce.Do(func() { _ = ms.Close() }) }
	defer closeManaged()

	streamCtx := stream.Context()
	execCtx, execCancel := context.WithCancel(streamCtx)
	defer execCancel()

	var closerDone chan struct{}
	if closer, ok := stream.(OptionalExecuteStreamCloser); ok {
		closerDone = make(chan struct{})
		go func() {
			defer close(closerDone)
			<-execCtx.Done()
			_ = closer.Close()
		}()
	}

	controlObsCh := make(chan controlObservation, 1)
	upstreamObsCh := make(chan upstreamObservation, 16)
	// upstreamStopped is closed when the coordinator leaves its receive loop. The
	// upstream reader's terminal observation is its last send, so it must be able
	// to abandon that send once nobody frees buffer space any more.
	upstreamStopped := make(chan struct{})
	cancellation := newCancellationLifecycle()

	var wg sync.WaitGroup
	var cancellationGraceTimer *time.Timer
	var cancellationGraceCh <-chan time.Time

	// 1. Client-control reader: owns ExecuteStream.Recv post-START.
	wg.Go(func() {
		for {
			if execCtx.Err() != nil {
				return
			}

			frame, recvErr := stream.Recv()
			if recvErr != nil {
				select {
				case controlObsCh <- controlObservation{err: recvErr}:
				case <-execCtx.Done():
					return
				}
				return
			}

			switch frame.Kind {
			case ClientFrameCancel:
				req := controlCancelReq{
					reason:     frame.CancelReason,
					deadlineMS: frame.CancelDeadlineUnixMS,
					outcome:    true,
				}
				// Record receipt before queueing. This is intentionally not
				// guarded by execCtx: cancellation is the terminal control fact.
				cancellation.observe(req)
				controlObsCh <- controlObservation{cancel: &req}
				return
			case ClientFrameCloseInput:
				// CLOSE_INPUT remains distinct from CANCEL: do not cancel upstream.
			default:
			}
		}
	})

	// 2. Upstream reader: owns ManagedEventStream.Recv.
	wg.Go(func() {
		for {
			if execCtx.Err() != nil {
				return
			}

			ev, recvErr := ms.Recv(execCtx)
			if recvErr != nil {
				obs := upstreamObservation{kind: upstreamObsEOF}
				if !errors.Is(recvErr, io.EOF) {
					obs = upstreamObservation{kind: upstreamObsError, err: recvErr}
				}
				// The terminal observation is the reader's last send, and it is the
				// only observation the coordinator may still need after execCtx is
				// canceled: the cancellation-grace path force-closes upstream and
				// keeps receiving so the terminal observation can finish frame
				// ordering. execCtx is therefore the wrong guard. upstreamStopped is
				// closed only when the coordinator has stopped receiving for good, so
				// this send can never wait for buffer space that will never be freed.
				select {
				case upstreamObsCh <- obs:
				case <-upstreamStopped:
				}
				return
			}

			select {
			case upstreamObsCh <- upstreamObservation{kind: upstreamObsEvent, event: ev}:
			case <-execCtx.Done():
				return
			}
		}
	})

	startCancellation := func() {
		req, ok := cancellation.requestValue()
		if !ok {
			return
		}
		timing := effectiveCancellationTiming(streamCtx, req)
		started := cancellation.start(func(req controlCancelReq) cancelWorkerResult {
			return runManagedCancellation(streamCtx, ms, closeManaged, req, timing)
		})
		if started && cancellation.needsOutcome() {
			cancellationGraceTimer = time.NewTimer(max(0, time.Until(timing.deadline)))
			cancellationGraceCh = cancellationGraceTimer.C
		}
	}
	startFallbackCancellation := func() {
		if cancellation.requested() {
			startCancellation()
			return
		}
		cancellation.observe(controlCancelReq{reason: CancelReasonUnspecified})
		startCancellation()
	}

	var execErr error
	committed := false
	sendCancelledTerminal := func() error {
		startCancellation()
		// Cancellation can resolve before the upstream reader reports its
		// terminal error. Drain any provider accounting evidence before the
		// terminal so an early forced close cannot strand it.
		if err := forwardAllAccountingEvidence(sequencer, ms, negotiation); err != nil {
			return err
		}
		if cancellation.needsOutcome() {
			if err := cancellation.sendOutcome(sequencer); err != nil {
				return err
			}
		}
		return sequencer.Send(ServerFrame{
			Kind:     ServerFrameTerminal,
			Terminal: &Terminal{Status: TerminalCancelled},
		})
	}

	streamCtxDoneCh := streamCtx.Done()
	cancellationDoneCh := cancellation.doneCh()
	// Coordinator select loop.
coordinatorLoop:
	for {
		select {
		case obs := <-controlObsCh:
			if obs.cancel != nil {
				// Receipt was recorded by the reader; observe again is harmless
				// and keeps this path correct for future control sources.
				cancellation.observe(*obs.cancel)
				startCancellation()
			}
			if obs.err != nil && streamCtx.Err() != nil {
				startFallbackCancellation()
				if cancellation.needsOutcome() {
					continue
				}
				execCancel()
				break coordinatorLoop
			}

		case <-cancellationDoneCh:
			cancellationDoneCh = nil
			if !cancellation.needsOutcome() {
				execCancel()
				break coordinatorLoop
			}
			// Acknowledgement is a distinct phase from upstream terminal
			// observation. Publish it promptly, but keep the B-leg reader alive
			// so final usage evidence and EOF can still be drained and ordered.
			_ = cancellation.sendOutcome(sequencer)

		case <-cancellationGraceCh:
			cancellationGraceCh = nil
			// If the B-leg has not produced its terminal observation by the
			// bounded grace, force-close it and let its terminal observation
			// complete the ordering. Cancel itself is already joined or will be
			// joined by the lifecycle worker.
			closeManaged()
			execCancel()

		case obs := <-upstreamObsCh:
			switch obs.kind {
			case upstreamObsEvent:
				if evidenceErr := forwardAllAccountingEvidence(sequencer, ms, negotiation); evidenceErr != nil {
					execErr = evidenceErr
					execCancel()
					break coordinatorLoop
				}
				if aipapi.OutputCommitted(obs.event) {
					committed = true
				}
				if sendErr := sequencer.Send(ServerFrame{
					Kind:  ServerFrameEvent,
					Event: CanonicalEventFromAipapi(obs.event),
				}); sendErr != nil {
					execCancel()
					break coordinatorLoop
				}

			case upstreamObsEOF:
				if evidenceErr := forwardAllAccountingEvidence(sequencer, ms, negotiation); evidenceErr != nil {
					execErr = evidenceErr
					execCancel()
					break coordinatorLoop
				}
				if !cancellation.requested() {
					_ = forwardPromptCacheObservations(sequencer, ms)
					_ = sequencer.Send(ServerFrame{
						Kind:     ServerFrameTerminal,
						Terminal: &Terminal{Status: TerminalSuccess},
					})
				} else {
					if terminalErr := sendCancelledTerminal(); terminalErr != nil {
						execErr = terminalErr
					}
				}
				execCancel()
				break coordinatorLoop

			case upstreamObsError:
				if evidenceErr := forwardAllAccountingEvidence(sequencer, ms, negotiation); evidenceErr != nil {
					execErr = evidenceErr
					execCancel()
					break coordinatorLoop
				}
				if cancellation.requested() || errors.Is(obs.err, context.Canceled) {
					if !cancellation.requested() {
						startFallbackCancellation()
					}
					if terminalErr := sendCancelledTerminal(); terminalErr != nil {
						execErr = terminalErr
					}
					execCancel()
					break coordinatorLoop
				}
				if streamCtx.Err() != nil {
					startFallbackCancellation()
					execCancel()
					break coordinatorLoop
				}

				// Non-cancellation upstream error: ensure terminal is sent before
				// unblocking the control reader via host CloseSend. Use a constant
				// non-sensitive bounded error for the public protocol; the original
				// error is preserved only as the internal return value.
				_ = sequencer.Send(ServerFrame{
					Kind: ServerFrameTerminal,
					Terminal: &Terminal{
						Status: TerminalFailure,
						Error:  failurePluginError(obs.err, committed),
					},
				})
				execErr = obs.err
				execCancel()
				break coordinatorLoop
			}

		case <-streamCtxDoneCh:
			streamCtxDoneCh = nil
			if cancellation.needsOutcome() {
				startCancellation()
				// The context cancellation will cause the upstream reader to
				// report its terminal observation; do not synthesize one here.
				continue
			}
			startFallbackCancellation()
			execCancel()
			break coordinatorLoop
		}
	}

	// The coordinator will never receive again: release an upstream reader whose
	// terminal observation is waiting on a full buffer.
	close(upstreamStopped)
	// A receipt can race the final upstream observation. Start it before
	// teardown so every cancellation request is physically processed.
	startCancellation()
	execCancel()
	if closerDone != nil {
		<-closerDone
	}
	wg.Wait()
	// A reader can record CANCEL while we join it, after the initial start
	// check. Readers are now quiescent: start that final receipt before waiting
	// for its worker, or teardown can wait forever on an unstarted lifecycle.
	startCancellation()
	if cancellation.requested() {
		_ = cancellation.wait()
	}
	if cancellationGraceTimer != nil {
		cancellationGraceTimer.Stop()
	}
	closeManaged()

	if sequencer.Err() != nil {
		return sequencer.Err()
	}
	if execErr != nil {
		return execErr
	}
	if streamCtx.Err() != nil {
		return streamCtx.Err()
	}
	return nil
}
