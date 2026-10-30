package backendplugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

const genericCancelOutcomeDetail = "cancel failed"

var fallbackCancelGrace = 2 * time.Second

// OpenManagedStream opens an upstream managed event stream for one Execute attempt.
// ctx is the incoming plugin Execute stream context and must be respected for cancellation.
type OpenManagedStream func(ctx context.Context, inv Invocation, call aipapi.Call) (aipapi.ManagedEventStream, error)

// ForwardExecute accepts the start frame, opens the upstream managed stream via open,
// and coordinates bidirectional execution:
//   - exactly one client-control reader owns ExecuteStream.Recv after START;
//   - exactly one upstream reader owns ManagedEventStream.Recv;
//   - the calling goroutine acts as coordinator and is the sole sender through frameSequencer;
//   - in-band CANCEL is consumed against the active upstream stream, calling upstream.Cancel
//     with effective deadline and emitting a sequenced CancelOutcome before the cancelled terminal;
//   - CLOSE_INPUT remains distinct from CANCEL (no upstream cancellation);
//   - all goroutines cleanly terminate and join when execution completes.
func ForwardExecute(stream ExecuteStream, open OpenManagedStream) error {
	if open == nil {
		return fmt.Errorf("backendplugin: open is required")
	}
	start, err := stream.Recv()
	if err != nil {
		return err
	}
	if start.Kind != ClientFrameStart || start.Invocation == nil {
		return fmt.Errorf("%w: expected start", ErrInvalidFrame)
	}
	if err := ValidateClientFrameBounds(start); err != nil {
		return err
	}
	if err := sendServerFrame(stream, ServerFrame{Kind: ServerFrameAccepted}); err != nil {
		return err
	}

	sequencer := newFrameSequencer(stream)
	negotiation := Negotiation{}
	if ns, ok := stream.(OptionalNegotiatedStream); ok {
		negotiation = ns.Negotiation()
	}
	call, err := CallFromInvocation(*start.Invocation)
	if err != nil {
		return err
	}

	ms, err := open(stream.Context(), *start.Invocation, call)
	if err != nil {
		if ms != nil {
			defer func() { _ = ms.Close() }()
			if evidenceErr := forwardAllAccountingEvidence(sequencer, ms, negotiation); evidenceErr != nil {
				return evidenceErr
			}
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			// A nested operation timeout is not caller withdrawal. When the
			// connector already classified the failure (for example a
			// transient refresh whose HTTP client hit its own deadline), the
			// classified terminal must win. An actual caller cancellation or
			// deadline leaves the stream context done and stays terminal-free.
			callerDone := stream.Context() != nil && stream.Context().Err() != nil
			if callerDone || !hasTerminalClassification(err) {
				return err
			}
		}
		// Pre-output open failure after ACCEPTED: emit one failure terminal
		// before returning the original error (owner precedent from the active
		// upstream-error path). Without it the bare error crosses gRPC as
		// Unknown and the host maps it pre-terminal to transport_death,
		// invalidating a healthy generation for an explicit rejection.
		_ = sequencer.Send(ServerFrame{
			Kind:     ServerFrameTerminal,
			Terminal: &Terminal{Status: TerminalFailure, Error: failurePluginError(err, false)},
		})
		return err
	}
	if ms == nil {
		return fmt.Errorf("backendplugin: open returned nil stream")
	}

	handshakeNegotiated := false
	if ns, ok := stream.(OptionalNegotiatedStream); ok {
		handshakeNegotiated = CancellationHandshakeNegotiated(ns.Negotiation())
	}
	if !handshakeNegotiated {
		return forwardLegacyExecute(stream, sequencer, ms, negotiation)
	}
	return forwardActiveExecute(stream, sequencer, ms, negotiation)
}

// hasTerminalClassification reports whether a connector supplied the safe
// host-visible classification for this failure.
func hasTerminalClassification(err error) bool {
	var terminal TerminalError
	return errors.As(err, &terminal)
}

// preOutputPluginError classifies a pre-output failure into a safe terminal
// PluginError. Messages are constants (bounded, no raw error text) so
// credentials/operator strings cannot leak. Capability rejects keep
// FailedPrecondition and never retry; all other pre-output failures use the
// established Internal owner precedent with no retry and no output commit.
// Cancellation never reaches here (callers return it without a terminal).
func failurePluginError(err error, outputCommitted bool) *PluginError {
	if terminal, ok := errors.AsType[TerminalError](err); ok {
		if mapped := terminal.TerminalPluginError(); mapped != nil {
			out := *mapped
			if out.Code == ErrorCodeUnspecified {
				out.Code = ErrorCodeInternal
			}
			if strings.TrimSpace(out.Message) == "" {
				out.Message = "upstream execution failed"
			}
			if outputCommitted {
				out.Retryable = false
				out.OutputCommitted = true
			}
			return &out
		}
	}
	out := preOutputPluginError(err)
	if outputCommitted {
		out.Retryable = false
		out.OutputCommitted = true
	}
	return out
}

func preOutputPluginError(err error) *PluginError {
	if errors.Is(err, aipapi.ErrCapabilityReject) {
		return &PluginError{
			Code:      ErrorCodeFailedPrecondition,
			Message:   "capability rejected",
			Retryable: false,
		}
	}
	return &PluginError{
		Code:      ErrorCodeInternal,
		Message:   "upstream execution failed",
		Retryable: false,
	}
}

func forwardLegacyExecute(stream ExecuteStream, sequencer *frameSequencer, ms aipapi.ManagedEventStream, negotiation Negotiation) error {
	ctx := stream.Context()
	var closeOnce sync.Once
	closeManaged := func() { closeOnce.Do(func() { _ = ms.Close() }) }
	defer closeManaged()

	committed := false
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			cancelCtx, cancelFn := fallbackCancelContext(ctx)
			defer cancelFn()
			_ = ms.Cancel(cancelCtx, aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "plugin_cancel"})
			closeManaged()
		case <-stopWatch:
		}
	}()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ev, err := ms.Recv(ctx)
		if errors.Is(err, io.EOF) {
			if err := forwardAllAccountingEvidence(sequencer, ms, negotiation); err != nil {
				return err
			}
			if err := forwardPromptCacheObservations(sequencer, ms); err != nil {
				return err
			}
			return sequencer.Send(ServerFrame{Kind: ServerFrameTerminal, Terminal: &Terminal{Status: TerminalSuccess}})
		}
		if err != nil {
			if evidenceErr := forwardAllAccountingEvidence(sequencer, ms, negotiation); evidenceErr != nil {
				return evidenceErr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// The caller side is checked above. A classified connector
				// failure may still wrap a nested operation deadline and must
				// publish its recovery terminal.
				if !hasTerminalClassification(err) {
					return err
				}
			}
			// Same owner fix as the open path: one pre-output failure terminal
			// before the original error. The adapter marks OutputCommitted when
			// events were already delivered, so post-output failures still
			// commit correctly.
			_ = sequencer.Send(ServerFrame{
				Kind:     ServerFrameTerminal,
				Terminal: &Terminal{Status: TerminalFailure, Error: failurePluginError(err, committed)},
			})
			return err
		}
		if err := forwardAllAccountingEvidence(sequencer, ms, negotiation); err != nil {
			return err
		}
		if aipapi.OutputCommitted(ev) {
			committed = true
		}
		if err := sequencer.Send(ServerFrame{Kind: ServerFrameEvent, Event: CanonicalEventFromAipapi(ev)}); err != nil {
			return err
		}
	}
}

// CancelCauseFromCancelReason maps wire CancelReason to canonical aipapi.CancelCause.
func CancelCauseFromCancelReason(reason CancelReason) aipapi.CancelCause {
	switch reason {
	case CancelReasonClient:
		return aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "client"}
	case CancelReasonHost:
		return aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "host"}
	case CancelReasonDeadline:
		return aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "deadline"}
	case CancelReasonShutdown:
		return aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "shutdown"}
	default:
		return aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: string(reason)}
	}
}

func fallbackCancelContext(streamCtx context.Context) (context.Context, context.CancelFunc) {
	grace := fallbackCancelGrace
	if d, ok := streamCtx.Deadline(); ok {
		grace = max(0, min(grace, time.Until(d)))
	}
	return context.WithTimeout(context.WithoutCancel(streamCtx), grace)
}

// frameSequencer coordinates server-frame sequence assignment and ExecuteStream.Send.
// In the negotiated active execution path, the coordinator is the sole sender;
// the mutex is retained for defense-in-depth and legacy stream support.
type frameSequencer struct {
	mu           sync.Mutex
	stream       ExecuteStream
	seq          uint64
	terminalSent bool
	err          error
}

func newFrameSequencer(stream ExecuteStream) *frameSequencer {
	return &frameSequencer{
		stream: stream,
		seq:    1,
	}
}

func (s *frameSequencer) Send(frame ServerFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	if s.terminalSent {
		return nil
	}
	if frame.Kind != ServerFrameAccepted {
		frame.Sequence = s.seq
		s.seq++
	}
	if err := sendServerFrame(s.stream, frame); err != nil {
		s.err = err
		return err
	}
	if frame.Kind == ServerFrameTerminal {
		s.terminalSent = true
	}
	return nil
}

func (s *frameSequencer) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *frameSequencer) TerminalSent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminalSent
}

func forwardAccountingEvidence(sequencer *frameSequencer, ms aipapi.ManagedEventStream) error {
	source, ok := ms.(AccountingEvidenceSource)
	if !ok {
		return nil
	}
	for _, evidence := range source.DrainAccountingEvidence() {
		evCopy := evidence
		if err := sequencer.Send(ServerFrame{
			Kind:       ServerFrameAccountingEvidence,
			Accounting: &evCopy,
		}); err != nil {
			return err
		}
	}
	return nil
}

// forwardEconomicEvidence drains typed V2 observations only when the exact
// negotiated capability is present. An optional empty V2 source remains
// compatible with an older host; an actual V2 payload without the capability
// fails closed rather than dropping provider evidence.
func forwardEconomicEvidence(sequencer *frameSequencer, ms aipapi.ManagedEventStream, negotiation Negotiation) error {
	var drain func() []AccountingEvidenceV2
	if source, ok := ms.(AccountingEvidenceV2Source); ok {
		drain = source.DrainAccountingEvidenceV2
	} else if source, ok := ms.(EconomicEvidenceSource); ok {
		drain = source.DrainEconomicEvidence
	}
	if drain == nil {
		return nil
	}
	evidence := drain()
	if len(evidence) == 0 {
		return nil
	}
	if err := RequireAccountingEvidenceV2(negotiation); err != nil {
		return err
	}
	for i := range evidence {
		if err := sequencer.Send(ServerFrame{Kind: ServerFrameAccountingEvidence, AccountingV2: &evidence[i]}); err != nil {
			return err
		}
	}
	return nil
}

func forwardAllAccountingEvidence(sequencer *frameSequencer, ms aipapi.ManagedEventStream, negotiation Negotiation) error {
	if err := forwardAccountingEvidence(sequencer, ms); err != nil {
		return err
	}
	return forwardEconomicEvidence(sequencer, ms, negotiation)
}

func forwardPromptCacheObservations(sequencer *frameSequencer, ms aipapi.ManagedEventStream) error {
	source, ok := ms.(promptcache.ObservationSource)
	if !ok {
		return nil
	}
	for _, observation := range source.DrainPromptCacheObservations() {
		obsCopy := observation
		if err := sequencer.Send(ServerFrame{
			Kind:                   ServerFramePromptCacheObservation,
			PromptCacheObservation: &obsCopy,
		}); err != nil {
			return err
		}
	}
	return nil
}

func sendServerFrame(stream ExecuteStream, frame ServerFrame) error {
	if err := ValidateServerFrameBounds(frame); err != nil {
		return err
	}
	return stream.Send(frame)
}
