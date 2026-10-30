package extensions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
)

const finalStreamCleanupTimeout = 5 * time.Second

type finalStreamObserverSlot struct {
	id   string
	mode sdkhooks.FailureMode
	obs  response.StreamObserver
}

type FinalStreamObservationSession struct {
	mu                                            sync.Mutex
	cond                                          *sync.Cond
	Log                                           *slog.Logger
	Metrics                                       StageMetrics
	slots                                         []finalStreamObserverSlot
	finished, opening, pendingFinish, hasDeferred bool
	// openingGen identifies the call that currently owns the opening gate, so a
	// deferred release can never clear a gate a later Open has taken.
	openingGen                      uint64
	observeInFlight                 int
	pendingOutcome, deferredOutcome response.StreamOutcome
	deferredSlots                   []finalStreamObserverSlot
}

func (s *FinalStreamObservationSession) condLocked() *sync.Cond {
	if s.cond == nil {
		s.cond = sync.NewCond(&s.mu)
	}
	return s.cond
}

func finishStreamObserverSlots(parentCtx context.Context, log *slog.Logger, metrics StageMetrics, slots []finalStreamObserverSlot, outcome response.StreamOutcome) {
	if len(slots) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), finalStreamCleanupTimeout)
	defer cancel()
	for _, slot := range slots {
		res := runBoundedProvider(ctx, nil, feature.StageIDFinalStreamObservation, slot.id, func(c context.Context) (struct{}, error) {
			return struct{}{}, safety.Call(safety.BoundaryExtension, "final_stream_finish", func() error {
				return slot.obs.Finish(c, outcome)
			})
		})
		isolateFinalStreamErr(ctx, log, metrics, slot.id, res.Err, "final_stream_finish")
	}
}

// waitOpeningLocked waits until no other Open owns the session's opening gate.
// The caller holds mu.
//
// The wait is admission, not execution: it belongs to the caller's context, so a
// canceled or expired caller withdraws instead of waiting for the in-flight Open
// to finish. sync.Cond has no select, so a context watcher broadcasts when the
// caller's context ends and the loop re-reads both the gate and the context.
func (s *FinalStreamObservationSession) waitOpeningLocked(ctx context.Context) error {
	if !s.opening {
		return nil
	}
	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.condLocked().Broadcast()
		s.mu.Unlock()
	})
	defer stop()
	for s.opening {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("extensions: final stream observation open abandoned while another open is in flight: %w", err)
		}
		s.condLocked().Wait()
	}
	return nil
}

// releaseOpeningLocked releases the opening gate only for the generation that
// owns it. The caller holds mu. The generation check makes the release idempotent
// and stops a finishing call from clearing a gate a later Open has already taken.
func (s *FinalStreamObservationSession) releaseOpeningLocked(gen uint64) {
	if !s.opening || s.openingGen != gen {
		return
	}
	s.opening = false
	s.condLocked().Broadcast()
}

// releaseOpening is the unwind-safe release of one call's ownership of the
// opening gate. The normal exits release the gate in the same critical section
// that publishes the new slots; this covers every other exit.
func (s *FinalStreamObservationSession) releaseOpening(gen uint64) {
	s.mu.Lock()
	s.releaseOpeningLocked(gen)
	s.mu.Unlock()
}

func (s *FinalStreamObservationSession) takeDeferredLocked() ([]finalStreamObserverSlot, response.StreamOutcome, bool) {
	if !s.hasDeferred || s.observeInFlight > 0 {
		return nil, "", false
	}
	slots, outcome := s.deferredSlots, s.deferredOutcome
	s.hasDeferred, s.deferredSlots = false, nil
	return slots, outcome, true
}

func (s *FinalStreamObservationSession) Open(ctx context.Context, factories []response.StreamObserverFactory, meta response.StreamMeta, svc response.Services) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	s.mu.Lock()
	if err := s.waitOpeningLocked(ctx); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.observeInFlight > 0 {
		s.mu.Unlock()
		return fmt.Errorf("extensions: final stream Open during Observe")
	}
	s.opening = true
	s.openingGen++
	gen := s.openingGen
	// This call alone owns the gate it just took: release it on every exit path,
	// including an unexpected unwind from provider code, so a contained panic can
	// never leave a later Open waiting on a gate whose owner is already gone.
	defer s.releaseOpening(gen)
	var prior []finalStreamObserverSlot
	var priorOutcome response.StreamOutcome
	if slots, outcome, ok := s.takeDeferredLocked(); ok {
		prior, priorOutcome = slots, outcome
	} else if !s.finished && len(s.slots) > 0 {
		prior, priorOutcome = slices.Clone(s.slots), response.OutcomeReplaced
	}
	s.slots, s.finished, s.pendingFinish, s.pendingOutcome = nil, false, false, ""
	s.hasDeferred, s.deferredSlots = false, nil
	log, metrics := s.Log, s.Metrics
	s.mu.Unlock()
	finishStreamObserverSlots(ctx, log, metrics, prior, priorOutcome)

	var opened []finalStreamObserverSlot
	var openErr error
	for _, f := range response.MaterializeSorted(factories) {
		if f == nil || execctx.IsSuppressedPluginID(ctx, f.ID()) {
			continue
		}
		mode := f.FailureMode()
		if mode == sdkhooks.FailureModeUnspecified {
			mode = sdkhooks.FailOpen
		}
		obs, oerr := safety.CallValue(safety.BoundaryExtension, "final_stream_open", func() (response.StreamObserver, error) {
			return f.Open(ctx, meta, svc)
		})
		if oerr != nil {
			isolateFinalStreamErr(ctx, s.Log, s.Metrics, f.ID(), oerr, "final_stream_open")
			if mode == sdkhooks.FailClosed {
				openErr = PolicyErrorFromProviderFailure(feature.StageIDFinalStreamObservation, f.ID(), failureBehaviorFromMode(mode), oerr)
				break
			}
			continue
		}
		if obs != nil {
			opened = append(opened, finalStreamObserverSlot{id: f.ID(), mode: mode, obs: obs})
		}
	}

	s.mu.Lock()
	finishOpened := openErr != nil || s.pendingFinish
	outcome := response.OutcomeFailed
	if s.pendingFinish {
		outcome = s.pendingOutcome
	}
	s.pendingFinish = false
	if finishOpened {
		s.finished, s.slots = true, nil
		log, metrics = s.Log, s.Metrics
		s.releaseOpeningLocked(gen)
		s.mu.Unlock()
		finishStreamObserverSlots(ctx, log, metrics, opened, outcome)
		return openErr
	}
	s.slots = opened
	s.releaseOpeningLocked(gen)
	s.mu.Unlock()
	return nil
}

func (s *FinalStreamObservationSession) Finish(parentCtx context.Context, outcome response.StreamOutcome) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.opening {
		if !s.pendingFinish {
			s.pendingFinish, s.pendingOutcome = true, outcome
		}
		s.mu.Unlock()
		return
	}
	if s.finished || len(s.slots) == 0 {
		s.finished = true
		s.mu.Unlock()
		return
	}
	s.finished = true
	run := slices.Clone(s.slots)
	s.slots = nil
	if s.observeInFlight > 0 {
		s.hasDeferred, s.deferredSlots, s.deferredOutcome = true, run, outcome
		s.mu.Unlock()
		return
	}
	log, metrics := s.Log, s.Metrics
	s.mu.Unlock()
	finishStreamObserverSlots(parentCtx, log, metrics, run, outcome)
}

func RunFinalStreamObservationStage(ctx context.Context, log *slog.Logger, obs StageMetrics, session *FinalStreamObservationSession, ev aipapi.Event, committed bool) error {
	if session == nil {
		return nil
	}
	if ctx == nil {
		return fmt.Errorf("extensions: %w", aipapi.ErrNilContext)
	}
	session.mu.Lock()
	if session.finished || session.opening || len(session.slots) == 0 {
		session.mu.Unlock()
		return nil
	}
	slots := slices.Clone(session.slots)
	session.observeInFlight++
	session.mu.Unlock()
	defer func() {
		session.mu.Lock()
		session.observeInFlight--
		flush, flushOutcome, ok := session.takeDeferredLocked()
		flog, fmetrics := session.Log, session.Metrics
		if session.observeInFlight == 0 {
			session.condLocked().Broadcast()
		}
		session.mu.Unlock()
		if ok {
			finishStreamObserverSlots(ctx, flog, fmetrics, flush, flushOutcome)
		}
	}()

	start, stageOutcome := time.Now(), "ok"
	ctx, span := otel.Tracer(otelScopeExtensions).Start(ctx, "aip.extension.final_stream_observe")
	defer func() {
		RecordStageObservation(obs, MetricsStageFinalStreamObservation, stageOutcome, time.Since(start).Seconds(), 1, SafeEventObserveBytes(ev))
		if stageOutcome == "error" {
			span.SetStatus(codes.Error, "observe error")
		} else {
			span.SetStatus(codes.Ok, "")
		}
		span.End()
	}()
	ev.UsageScopes = slices.Clone(ev.UsageScopes)
	evidence := DecisionEvidenceFromContext(ctx)
	for _, slot := range slots {
		res := observeFinalStreamSlot(ctx, evidence, slot, ev)
		if res.ParentCanceled {
			if committed {
				isolateFinalStreamErr(ctx, log, obs, slot.id, res.Err, "final_stream_observe")
				continue
			}
			stageOutcome = "error"
			return res.Err
		}
		if res.TimedOut {
			cont, terr := handleProviderTimeout(ctx, log, obs, evidence, finalStreamObserveFailureCfg, res.IterCtx, slot.id, res.Deadline, slot.mode)
			if cont {
				continue
			}
			if committed {
				isolateFinalStreamErr(ctx, log, obs, slot.id, terr, "final_stream_observe")
				continue
			}
			stageOutcome = "error"
			return terr
		}
		if res.Err != nil {
			if committed || slot.mode == sdkhooks.FailOpen {
				isolateFinalStreamErr(ctx, log, obs, slot.id, res.Err, "final_stream_observe")
				continue
			}
			stageOutcome = "error"
			return PolicyErrorFromProviderFailure(feature.StageIDFinalStreamObservation, slot.id, failureBehaviorFromMode(slot.mode), res.Err)
		}
	}
	return nil
}

// observeFinalStreamSlot runs one final-stream observer for one event. The
// observer closure captures the event, so the call lives in its own function:
// an escaping capture inside RunFinalStreamObservationStage moved the 640-byte
// canonical event to the heap on every delivered event, including the common
// attempt with no registered stream observers, where the stage returns before
// any observer runs.
func observeFinalStreamSlot(ctx context.Context, evidence *DecisionEvidence, slot finalStreamObserverSlot, ev aipapi.Event) boundedProviderResult[struct{}] {
	return runBoundedProvider(ctx, evidence, feature.StageIDFinalStreamObservation, slot.id, func(c context.Context) (struct{}, error) {
		return struct{}{}, safety.Call(safety.BoundaryExtension, "final_stream_observe", func() error {
			return slot.obs.Observe(c, ev)
		})
	})
}

func isolateFinalStreamErr(ctx context.Context, log *slog.Logger, obs StageMetrics, id string, err error, phase string) {
	if err == nil {
		return
	}
	if _, ok := errors.AsType[*safety.PanicError](err); ok {
		logFailOpenExtensionPanic(ctx, log, phase, id, err)
	} else if log != nil {
		log.WarnContext(ctx, phase+": observer error (isolated)", "factory", id, "error", err)
	}
	if obs != nil {
		obs.IncFailOpenSkip(MetricsStageFinalStreamObservation)
	}
}

var finalStreamObserveFailureCfg = stageFailureConfig{
	Stage: feature.StageIDFinalStreamObservation, MetricsStage: MetricsStageFinalStreamObservation,
	TimeoutMsg: "final_stream_observe: handler timed out (fail-open)", FailureMsg: "final_stream_observe: handler error (fail-open)",
	PanicStage: "final_stream_observe", ProviderAttr: "factory",
}
