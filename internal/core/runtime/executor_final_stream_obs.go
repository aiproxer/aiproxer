package runtime

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/response"
)

func (p *responsePipeline) streamObserverMeta(facts recvTurnFacts, attempt *attemptSession, views execctx.Views, viewsOK bool) response.StreamMeta {
	backendID := strings.TrimSpace(attempt.cand.Primary.Backend)
	var prefixes []string
	if p != nil {
		if be, ok := p.backends[backendID]; ok {
			prefixes = execbackend.CloneBackendPrefixes(be)
		}
	}
	meta := response.StreamMeta{
		TraceID: facts.traceID, ALegID: facts.aLegID, BLegID: attempt.bleg.BLegID, CandidateKey: attempt.cand.Key,
		BackendID: backendID, BackendPrefixes: prefixes, Model: strings.TrimSpace(attempt.cand.Primary.Model),
		AttemptSeq: attempt.bleg.Seq,
	}
	if viewsOK {
		meta.Scope = views.Scope.Clone()
		meta.Session, meta.Workspace = cloneSessionView(views.Session), cloneWorkspaceView(views.Workspace)
		meta.Session.ALegID = facts.aLegID
	}
	return meta
}

func (p *responsePipeline) openFinalStreamObservation(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, views execctx.Views, viewsOK bool, committed bool) error {
	if p == nil || p.runtimeSnapshot == nil {
		return nil
	}
	factories := p.runtimeSnapshot.StreamObserverFactories()
	if len(factories) == 0 {
		return nil
	}
	if attempt == nil || attempt.finalStreamObs == nil {
		return nil
	}
	if err := attempt.finalStreamObs.Open(ctx, factories, p.streamObserverMeta(facts, attempt, views, viewsOK), response.Services{}); err != nil && !committed {
		return err
	}
	return nil
}

func (p *responsePipeline) finishFinalStreamObservation(ctx context.Context, attempt *attemptSession, outcome response.StreamOutcome) {
	if p == nil {
		return
	}
	if attempt != nil && attempt.finalStreamObs != nil {
		attempt.finalStreamObs.Finish(ctx, outcome)
	}
}

// preflightFinalStreamObservation runs exactly one existing fail-closed
// final-stream observation for an event that a private publication has already
// recorded but not yet delivered.
//
// It is the preflight half only: it never marks the observer finished and never
// runs again at delivery. The staged batch keeps its already-completed
// observation, so physical delivery skips exactly this step and nothing else.
func (p *responsePipeline) preflightFinalStreamObservation(
	ctx context.Context,
	attempt *attemptSession,
	ev aipapi.Event,
	committed bool,
) error {
	if p == nil || attempt == nil {
		return nil
	}
	return extensions.RunFinalStreamObservationStage(ctx, p.log, p.extensionMetrics, attempt.finalStreamObs, ev, committed)
}

func (p *responsePipeline) cycleFinalStreamObservation(ctx context.Context, facts recvTurnFacts, attempt *attemptSession, views execctx.Views, viewsOK bool, outcome response.StreamOutcome, committed bool) error {
	p.finishFinalStreamObservation(ctx, attempt, outcome)
	return p.openFinalStreamObservation(ctx, facts, attempt, views, viewsOK, committed)
}
