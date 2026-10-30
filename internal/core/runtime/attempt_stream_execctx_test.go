package runtime

import (
	"context"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/diag"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

func TestRetryRecvStream_recvExecContext_mergesViewsAndRoutePrefs(t *testing.T) {
	t.Parallel()
	want := execctx.Views{Session: session.SessionView{ClientSessionHint: "sid"}}
	s := &retryRecvStream{
		facts: testRecvTurnFacts(recvTurnFacts{
			traceID:      "tr1",
			aLegID:       "a1",
			recvViewsOK:  true,
			recvViews:    want,
			routePrefs:   []string{"cand-a", "cand-b"},
			secureTurnOK: true,
			secureTurn:   execctx.SecureSessionTurn{SessionID: domain.SessionID("ss"), TurnID: domain.TurnID("tt")},
		}),
		responsePipeline: newResponsePipeline(),
		terminal:         newTurnTerminal(),
	}
	ctx := s.responsePipeline.withDecisionEvidence(s.facts.projectContext(context.Background(), s.responsePipeline.log), s.terminal)
	if got := diag.TraceID(ctx); got != "tr1" {
		t.Fatalf("diag trace: got %q want tr1", got)
	}
	gotViews, ok := execctx.FromContext(ctx)
	if !ok || gotViews.Session.ClientSessionHint != "sid" {
		t.Fatalf("views: ok=%v %+v", ok, gotViews.Session)
	}
	prefs := execctx.RouteCandidatePreferences(ctx)
	if len(prefs) != 2 || prefs[0] != "cand-a" || prefs[1] != "cand-b" {
		t.Fatalf("prefs: %#v", prefs)
	}
	st, ok := execctx.SecureSessionTurnFromContext(ctx)
	if !ok || st.SessionID != "ss" || st.TurnID != "tt" {
		t.Fatalf("secure turn: ok=%v %+v", ok, st)
	}
}
