package runtime

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/aiproxer/aiproxer/internal/compactionfacts"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
)

type compactionReleaseDispatch struct {
	meta    compaction.PreservationMeta
	enabled bool
}

// compactionObservers returns the frozen compaction observer slice bound to
// this executor's runtime snapshot. A nil snapshot yields nil (no-op).
func (e *Executor) compactionObservers() []compaction.Observer {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.RuntimeSnapshot.CompactionObservers()
}

func (e *Executor) compactionPreservers() []compaction.Preserver {
	if e == nil || e.RuntimeSnapshot == nil {
		return nil
	}
	return e.RuntimeSnapshot.CompactionPreservers()
}

func (e *Executor) compactionServices() compaction.Services {
	services := compaction.Services{}
	if e != nil && e.RuntimeSnapshot != nil {
		services.State = e.RuntimeSnapshot.State()
	}
	if e != nil {
		services.BackgroundAux = e.BackgroundAux
	}
	return services
}

// observeCompactionOpened runs the request-side compaction observation after
// the first upstream B-leg opened successfully for a logical request. Retry/
// failover replacement B-legs never reach this point, so starts and
// fingerprints are recorded exactly once per logical request (requirements
// 3.2, 3.5, 4.6). Observation is fail-open and never alters execution. The
// process-owned detector remains authoritative independently of metadata
// observer registration so later preservation consumers can use its state and
// pure previews.
func (e *Executor) preparedCompactionMeta(prep *preparedRequest, blegID string, seq int) compaction.PreservationMeta {
	meta := compaction.PreservationMeta{BLegID: blegID, AttemptSeq: seq}
	if prep.identity != nil {
		meta.TraceID, meta.ALegID = prep.identity.traceID, prep.identity.aLeg.ALegID
		if prep.identity.call != nil {
			meta.SessionID = prep.identity.call.Session.AuthoritativeSessionID
		}
	}
	if prep.recvTurnFacts.traceID != "" { //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
		meta.TraceID = prep.recvTurnFacts.traceID //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
	}
	if prep.recvTurnFacts.aLegID != "" { //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
		meta.ALegID = prep.recvTurnFacts.aLegID //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
	}
	if prep.recvTurnFacts.wirePayload != nil && prep.recvTurnFacts.wirePayload.sessionID != "" { //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
		meta.SessionID = prep.recvTurnFacts.wirePayload.sessionID //nolint:staticcheck // explicit recvTurnFacts qualification keeps request-attempt AST ratchet counts stable
	}
	return meta
}

func (e *Executor) applyCompactionEvents(prep *preparedRequest, out openedAttempt, emit func(compaction.PreservationMeta) []compaction.Event) (compaction.PreservationMeta, []compaction.Event, bool) {
	if e == nil || e.CompactionDetector == nil || prep == nil || out.ready == nil {
		return compaction.PreservationMeta{}, nil, false
	}
	bleg := out.ready.BLeg()
	if bleg.BLegID == "" {
		return compaction.PreservationMeta{}, nil, false
	}
	meta := e.preparedCompactionMeta(prep, bleg.BLegID, bleg.Seq)
	events := emit(meta)
	if len(events) > 0 {
		last := events[len(events)-1]
		meta.TransactionID = last.TransactionID
		meta.RuleID = string(last.RuleID)
		meta.Evidence = last.Evidence
	}
	return meta, events, true
}

func (e *Executor) observeCompactionOpened(ctx context.Context, prep *preparedRequest, out openedAttempt) compaction.PreservationMeta {
	meta, events, ok := e.applyCompactionEvents(prep, out, func(m compaction.PreservationMeta) []compaction.Event {
		return safeCompactionRequestOpened(ctx, e.Log, e.ExtensionMetrics, e.CompactionDetector, m, *prep.identity.call)
	})
	if !ok {
		return compaction.PreservationMeta{}
	}
	// The detector commits before content-bearing callbacks. RequestOpened gets
	// isolated callback-local copies because the primary request is already on
	// the wire and cannot be rolled back. Public metadata dispatch is last.
	// fail-open: preserver errors isolated inside with log/metrics, return is always nil
	_ = extensions.RunCompactionPreserverRequestOpened(
		ctx,
		e.Log,
		e.ExtensionMetrics,
		e.compactionPreservers(),
		*prep.identity.call,
		events,
		meta,
		e.compactionServices(),
	)
	compaction.Dispatch(ctx, e.compactionObservers(), events)
	return meta
}

func (e *Executor) observeCompactionOpenedWire(
	ctx context.Context,
	prep *preparedRequest,
	out openedAttempt,
	facts compactionfacts.RequestFacts,
) compaction.PreservationMeta {
	meta, events, ok := e.applyCompactionEvents(prep, out, func(m compaction.PreservationMeta) []compaction.Event {
		return safeCompactionRequestOpenedFacts(ctx, e.Log, e.ExtensionMetrics, e.CompactionDetector, m, facts)
	})
	if ok {
		compaction.Dispatch(ctx, e.compactionObservers(), events)
	}
	return meta
}

func (e *Executor) observeCompactionBeforeRequest(ctx context.Context, traceID, aLegID string, call *aipapi.Call) {
	if e == nil || e.CompactionDetector == nil || call == nil {
		return
	}
	if ctx != nil && execctx.AuxiliaryDepth(ctx) > 0 {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "compaction before-request skipped", "reason", "auxiliary_depth", "trace_id", traceID, "a_leg_id", aLegID)
		}
		return
	}
	if ctx != nil && ctx.Err() != nil {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "compaction before-request skipped", "reason", "context_canceled", "trace_id", traceID, "a_leg_id", aLegID)
		}
		return
	}
	// Note: no SessionModeDetached check here. prepareIdentity routes
	// detached-mode contexts to prepareSubmitAndALegDetached before secure
	// prepare runs, so this observer only ever sees secure turns.
	preservers := e.compactionPreservers()
	if len(preservers) == 0 {
		return
	}
	meta := compaction.PreservationMeta{
		TraceID:   traceID,
		ALegID:    aLegID,
		SessionID: call.Session.AuthoritativeSessionID,
	}
	if strings.TrimSpace(meta.ALegID) == "" {
		if e.Log != nil {
			e.Log.DebugContext(ctx, "compaction before-request skipped", "reason", "empty_a_leg_id", "trace_id", traceID)
		}
		return
	}
	preview := safeCompactionPreviewRequest(ctx, e.Log, e.ExtensionMetrics, e.CompactionDetector, meta, *call)
	meta.TransactionID = preview.TransactionID
	meta.RuleID = preview.RuleID
	meta.Evidence = preview.Evidence
	// fail-open: preserver errors isolated inside with log/metrics, return is always nil
	_ = extensions.RunCompactionPreserverBeforeRequest(
		ctx,
		e.Log,
		e.ExtensionMetrics,
		preservers,
		call,
		preview,
		meta,
		e.compactionServices(),
	)
}

func (e *Executor) notifyCompactionOpenFailed(ctx context.Context, prep *preparedRequest) {
	if e == nil || prep == nil {
		return
	}
	// fail-open: preserver errors isolated inside with log/metrics, return is always nil
	_ = extensions.RunCompactionPreserverRequestOpenFailed(
		ctx,
		e.Log,
		e.ExtensionMetrics,
		e.compactionPreservers(),
		e.preparedCompactionMeta(prep, "", 0),
		e.compactionServices(),
	)
}

// observeCompactionRelease runs the response-side compaction observation for
// every canonical event actually released by the retry stream. It is called
// from the single final release seam (emitTrafficPTC), so live, gated,
// tool-finalizer, and recovery drains are observed exactly once and the
// returned event is never altered (requirements 3.3, 8.4). The detector is
// committed even when no metadata observers are configured; dispatch is only
// the optional public side effect.
func (p *responsePipeline) observeCompactionRelease(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event) {
	// The caller ignores the possibly preserved event: this path is observation
	// only, and the released event it observes is rewritten by a preserver only
	// when one is registered.
	_, _ = p.observeCompactionReleaseFinal(ctx, facts, attempt, ev)
}

func (p *responsePipeline) observeCompactionReleaseFinal(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, ev aipapi.Event) (compactionReleaseDispatch, aipapi.Event) {
	return p.observeCompactionReleaseFinalEvidence(ctx, facts.responseEvidence(), attempt, ev)
}

func (p *responsePipeline) observeCompactionReleaseFinalEvidence(ctx context.Context, evidence responseRequestEvidence, attempt *attemptSession, ev aipapi.Event) (compactionReleaseDispatch, aipapi.Event) {
	var dispatch compactionReleaseDispatch
	if p == nil || p.detector == nil || attempt == nil {
		return dispatch, ev
	}
	observers := p.compactionObservers
	preservationMeta := compaction.PreservationMeta{
		TraceID:    evidence.traceID,
		SessionID:  evidence.sessionID,
		ALegID:     evidence.aLegID,
		BLegID:     attempt.bleg.BLegID,
		AttemptSeq: attempt.bleg.Seq,
	}
	preview := safeCompactionPreviewResponse(ctx, p.log, p.extensionMetrics, p.detector, preservationMeta, ev)
	preservationMeta.TransactionID = preview.TransactionID
	preservationMeta.RuleID = preview.RuleID
	preservationMeta.Evidence = preview.Evidence
	// Completion-only requests can have their committed transaction established
	// by RequestOpened while the later ordinary response has an empty pure
	// preview. Preserve response correlation and use only the request-side
	// transaction/rule/evidence as a fallback in that case.
	if strings.TrimSpace(preservationMeta.TransactionID) == "" {
		fallback := p.compactionOpenMeta
		preservationMeta.TransactionID = fallback.TransactionID
		if preservationMeta.RuleID == "" {
			preservationMeta.RuleID = fallback.RuleID
		}
		if preservationMeta.Evidence == "" {
			preservationMeta.Evidence = fallback.Evidence
		}
	}
	// Pure preview is deliberately before preservation. The callback runner
	// rolls back each failed/panicking/invalid mutation before committed detector
	// observation, so detector and client receive the same final event. The
	// addressable copy is paid only when a preserver is registered.
	if len(p.compactionPreservers) > 0 {
		ev = runCompactionReleasePreservers(ctx, p.log, p.extensionMetrics, p.compactionPreservers, ev, preview, preservationMeta, p.compactionServices)
	}
	events := safeCompactionResponseReleased(ctx, p.log, p.extensionMetrics, p.detector, preservationMeta, ev)
	dispatch = compactionReleaseDispatch{meta: preservationMeta, enabled: true}
	if len(events) == 0 {
		return dispatch, ev
	}
	compaction.Dispatch(ctx, observers, events)
	return dispatch, ev
}

// runCompactionReleasePreservers runs the response-release preserver chain on an
// addressable copy of the released event. It lives in its own function so the
// common release path never takes the event's address: doing that inline moved
// the 640-byte canonical event to the heap for every delivered event, while a
// preserver that may replace the event is the only reason the pointer exists.
// fail-open: preserver errors isolated inside with log/metrics, return is always nil
func runCompactionReleasePreservers(
	ctx context.Context,
	log *slog.Logger,
	obs extensions.StageMetrics,
	preservers []compaction.Preserver,
	ev aipapi.Event,
	preview compaction.ResponsePreview,
	meta compaction.PreservationMeta,
	services compaction.Services,
) aipapi.Event {
	_ = extensions.RunCompactionPreserverBeforeResponseRelease(ctx, log, obs, preservers, &ev, preview, meta, services)
	return ev
}

func (p *responsePipeline) notifyCompactionAfterRelease(ctx context.Context, ev aipapi.Event, dispatch compactionReleaseDispatch) {
	if p == nil || !dispatch.enabled {
		return
	}
	// fail-open: preserver errors isolated inside with log/metrics, return is always nil
	_ = extensions.RunCompactionPreserverAfterResponseRelease(
		ctx,
		p.log,
		p.extensionMetrics,
		p.compactionPreservers,
		ev,
		dispatch.meta,
		p.compactionServices,
	)
}

func isolateCompactionDetectorFailure(ctx context.Context, log *slog.Logger, obs extensions.StageMetrics, operation string, err error) {
	if err == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if pe, ok := errors.AsType[*safety.PanicError](err); ok {
		if log != nil {
			attrs := []slog.Attr{
				slog.String("operation", operation),
				slog.String("outcome", "panic"),
			}
			attrs = append(attrs, safety.PanicSlogFieldAttrs(pe)...)
			attrs = safety.AppendPanicStackAttr(attrs, pe)
			log.LogAttrs(ctx, slog.LevelWarn, "compaction detector failed (fail-open)", attrs...)
		}
	} else if log != nil {
		// Do not include detector error text: it may contain prompt or content.
		log.WarnContext(ctx, "compaction detector failed (fail-open)", "operation", operation, "outcome", "error")
	}
	if obs != nil {
		obs.IncFailOpenSkip(extensions.MetricsStageCompactionPreservation)
	}
}

func safeCompactionRequestOpened(ctx context.Context, log *slog.Logger, obs extensions.StageMetrics, d CompactionDetector, meta compaction.PreservationMeta, call aipapi.Call) (events []compaction.Event) {
	if d == nil {
		return nil
	}
	v, err := safety.CallValue(safety.BoundaryExtension, "compaction_detector_request_opened", func() ([]compaction.Event, error) {
		return d.RequestOpened(meta, call), nil
	})
	if err != nil {
		isolateCompactionDetectorFailure(ctx, log, obs, "request_opened", err)
		return nil
	}
	return v
}

func safeCompactionRequestOpenedFacts(ctx context.Context, log *slog.Logger, obs extensions.StageMetrics, d CompactionDetector, meta compaction.PreservationMeta, facts compactionfacts.RequestFacts) (events []compaction.Event) {
	if d == nil {
		return nil
	}
	v, err := safety.CallValue(safety.BoundaryExtension, "compaction_detector_request_opened_facts", func() ([]compaction.Event, error) {
		if wd, ok := d.(CompactionWireDetector); ok {
			return wd.RequestOpenedFacts(meta, facts), nil
		}
		return nil, nil
	})
	if err != nil {
		isolateCompactionDetectorFailure(ctx, log, obs, "request_opened_facts", err)
		return nil
	}
	return v
}

func safeCompactionResponseReleased(ctx context.Context, log *slog.Logger, obs extensions.StageMetrics, d CompactionDetector, meta compaction.PreservationMeta, ev aipapi.Event) (events []compaction.Event) {
	if d == nil {
		return nil
	}
	v, err := safety.CallValue(safety.BoundaryExtension, "compaction_detector_response_released", func() ([]compaction.Event, error) {
		return d.ResponseReleased(meta, ev), nil
	})
	if err != nil {
		isolateCompactionDetectorFailure(ctx, log, obs, "response_released", err)
		return nil
	}
	return v
}

func safeCompactionPreviewRequest(ctx context.Context, log *slog.Logger, obs extensions.StageMetrics, d CompactionDetector, meta compaction.PreservationMeta, call aipapi.Call) (preview compaction.RequestPreview) {
	if d == nil {
		return compaction.RequestPreview{Kind: compaction.PreviewNone}
	}
	v, err := safety.CallValue(safety.BoundaryExtension, "compaction_detector_preview_request", func() (compaction.RequestPreview, error) {
		return d.PreviewRequest(meta, call), nil
	})
	if err != nil {
		isolateCompactionDetectorFailure(ctx, log, obs, "preview_request", err)
		return compaction.RequestPreview{Kind: compaction.PreviewNone}
	}
	return v
}

func safeCompactionPreviewResponse(ctx context.Context, log *slog.Logger, obs extensions.StageMetrics, d CompactionDetector, meta compaction.PreservationMeta, ev aipapi.Event) (preview compaction.ResponsePreview) {
	if d == nil {
		return compaction.ResponsePreview{Kind: compaction.PreviewNone}
	}
	v, err := safety.CallValue(safety.BoundaryExtension, "compaction_detector_preview_response", func() (compaction.ResponsePreview, error) {
		return d.PreviewResponse(meta, ev), nil
	})
	if err != nil {
		isolateCompactionDetectorFailure(ctx, log, obs, "preview_response", err)
		return compaction.ResponsePreview{Kind: compaction.PreviewNone}
	}
	return v
}
