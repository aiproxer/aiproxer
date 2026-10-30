package runtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/modelregistry"
	"github.com/aiproxer/aiproxer/internal/core/modelview"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

type recvFactsTestResolver struct{}

func (recvFactsTestResolver) ResolveModelBinding(string, string) routing.ModelBinding {
	return routing.ModelBinding{Kind: routing.ModelBindingExactCanonical, Native: "native-pinned"}
}

func TestRecvTurnFacts_ClonesMutableRequestFacts(t *testing.T) {
	t.Parallel()

	rawPart := json.RawMessage(`{"part":"original"}`)
	rawTool := json.RawMessage(`{"type":"object"}`)
	rawExtension := json.RawMessage(`{"extension":"original"}`)
	call := aipapi.Call{
		ID: "request-facts",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{{Kind: aipapi.PartJSON, Content: rawPart}},
		}},
		Tools:      []aipapi.ToolDef{{Name: "tool", Parameters: rawTool}},
		ToolChoice: aipapi.ToolChoice{AllowedTools: []string{"tool-original"}},
		Extensions: map[string]json.RawMessage{"ext": rawExtension},
		Session:    aipapi.SessionRef{Metadata: map[string]string{"session": "original"}},
	}
	views := execctx.Views{Session: session.SessionView{ClientSessionHint: "session-original", Labels: map[string]string{"label": "original"}}}
	prefs := []string{"backend-original:model"}
	facts := newRecvTurnFacts(context.Background(), recvTurnFactsInput{
		baseline:     call,
		traceID:      "trace-original",
		aLegID:       "a-leg-original",
		recvViews:    views,
		recvViewsOK:  true,
		routePrefs:   prefs,
		secureTurn:   execctx.SecureSessionTurn{SessionID: domain.SessionID("secure"), TurnID: domain.TurnID("turn")},
		secureTurnOK: true,
	})

	rawPart[0] = 'X'
	rawTool[0] = 'X'
	rawExtension[0] = 'X'
	call.Messages[0].Parts[0].Content[0] = 'X'
	call.Tools[0].Parameters[0] = 'X'
	call.Extensions["ext"][0] = 'X'
	call.Session.Metadata["session"] = "changed"
	call.ToolChoice.AllowedTools[0] = "tool-changed"
	prefs[0] = "backend-changed:model"
	views.Session.Labels["label"] = "changed"

	if got := string(facts.baseline.Messages[0].Parts[0].Content); got != `{"part":"original"}` {
		t.Fatalf("baseline message content = %q, want original clone", got)
	}
	if got := string(facts.baseline.Tools[0].Parameters); got != `{"type":"object"}` {
		t.Fatalf("baseline tool parameters = %q, want original clone", got)
	}
	if got := string(facts.baseline.Extensions["ext"]); got != `{"extension":"original"}` {
		t.Fatalf("baseline extension = %q, want original clone", got)
	}
	if got := facts.baseline.Session.Metadata["session"]; got != "original" {
		t.Fatalf("baseline session metadata = %q, want original clone", got)
	}
	if got := facts.baseline.ToolChoice.AllowedTools[0]; got != "tool-original" {
		t.Fatalf("baseline allowed tools = %q, want original clone", got)
	}
	if got := facts.routePrefs[0]; got != "backend-original:model" {
		t.Fatalf("route preferences = %q, want original clone", got)
	}
	if got := facts.recvViews.Session.Labels["label"]; got != "original" {
		t.Fatalf("execution-view labels = %q, want original clone", got)
	}
}

func TestRequestTerminalFacts_ToRecvTurnFactsDeepCopiesEveryCall(t *testing.T) {
	t.Parallel()

	call := aipapi.Call{Messages: []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("original")}}}}
	facts := requestTerminalFacts{call: call, ingressCall: call, conversationFilteredBaseline: call}
	converted := facts.toRecvTurnFacts(context.Background())

	converted.baseline.Messages[0].Parts[0].Text = "baseline"
	converted.ingressCall.Messages[0].Parts[0].Text = "ingress"
	converted.conversationFilteredBaseline.Messages[0].Parts[0].Text = "filtered"
	if facts.call.Messages[0].Parts[0].Text != "original" ||
		facts.ingressCall.Messages[0].Parts[0].Text != "original" ||
		facts.conversationFilteredBaseline.Messages[0].Parts[0].Text != "original" {
		t.Fatal("converted facts mutated their frozen inputs")
	}
	if converted.baseline.Messages[0].Parts[0].Text != "baseline" ||
		converted.ingressCall.Messages[0].Parts[0].Text != "ingress" ||
		converted.conversationFilteredBaseline.Messages[0].Parts[0].Text != "filtered" {
		t.Fatal("converted calls share storage")
	}
}

func TestRecvTurnFacts_ProjectContextRetainsPinnedFactsOnBareContext(t *testing.T) {
	t.Parallel()

	holder := &checkpoint.RequestHolder{}
	authority := &requestAuthorityState{}
	callID := "bc_0123456789abcdef0123456789abcdef"
	callState := newBillingCallState(usageevidence.BillingCallID(callID))
	pricing := usageevidence.VersionRef{ID: "pricing-original", Version: "1"}
	policy := usageevidence.VersionRef{ID: "policy-original", Version: "1"}
	identity := modelview.Derive(7, "config-original", "registry-original", "catalog-original")
	facts := newRecvTurnFacts(context.Background(), recvTurnFactsInput{
		baseline: aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "session-original"}},
		traceID:  "trace-original",
		aLegID:   "a-leg-original",
		recvViews: execctx.Views{
			Session:     session.SessionView{ClientSessionHint: "session-view-original"},
			Annotations: map[string]string{"annotation": "original"},
		},
		recvViewsOK:            true,
		routePrefs:             []string{"backend-original:model"},
		secureTurn:             execctx.SecureSessionTurn{SessionID: domain.SessionID("secure-original"), TurnID: domain.TurnID("turn-original")},
		secureTurnOK:           true,
		boundRegistry:          modelregistry.EmptyBoundView(),
		boundRegistryOK:        true,
		boundCatalog:           modelcatalog.EmptyBoundView(),
		boundCatalogOK:         true,
		nativeResolver:         recvFactsTestResolver{},
		modelViewID:            identity,
		modelViewIDOK:          true,
		metering:               holder,
		requestAuth:            authority,
		billingCallID:          usageevidence.BillingCallID(callID),
		billingCallState:       callState,
		billingAccountID:       "account-original",
		billingCustomerPricing: pricing,
		billingChargePolicy:    policy,
		billingIdentityStamped: true,
	})
	if facts.metering != holder || facts.requestAuth != authority || facts.billingCallState != callState {
		t.Fatal("facts did not retain stable owner references")
	}
	if facts.billingCallID != usageevidence.BillingCallID(callID) || facts.billingAccountID != "account-original" || facts.billingCustomerPricing != pricing || facts.billingChargePolicy != policy || !facts.billingIdentityStamped {
		t.Fatalf("billing facts = %+v", facts)
	}

	ctx := facts.projectContext(context.Background(), slog.Default())
	if got := diagTraceIDForTurnRecvPinning(ctx); got != "trace-original" {
		t.Fatalf("trace id = %q, want trace-original", got)
	}
	gotViews, ok := execctx.FromContext(ctx)
	if !ok || gotViews.Session.ClientSessionHint != "session-view-original" {
		t.Fatalf("views = ok:%v %+v", ok, gotViews)
	}
	if got := execctx.RouteCandidatePreferences(ctx); len(got) != 1 || got[0] != "backend-original:model" {
		t.Fatalf("route preferences = %#v", got)
	}
	gotTurn, ok := execctx.SecureSessionTurnFromContext(ctx)
	if !ok || gotTurn.SessionID != domain.SessionID("secure-original") || gotTurn.TurnID != domain.TurnID("turn-original") {
		t.Fatalf("secure turn = ok:%v %+v", ok, gotTurn)
	}
	if meteringHolderFrom(ctx) != holder {
		t.Fatal("bare context lost metering holder")
	}
	if requestAuthorityFrom(ctx) != authority {
		t.Fatal("bare context lost request authority")
	}
	if got, ok := modelregistry.BoundViewFromContext(ctx); !ok || got != modelregistry.EmptyBoundView() {
		t.Fatalf("registry view = ok:%v %+v", ok, got)
	}
	if got, ok := modelcatalog.BoundViewFromContext(ctx); !ok || got != modelcatalog.EmptyBoundView() {
		t.Fatalf("catalog view = ok:%v %+v", ok, got)
	}
	if got, ok := routing.NativeModelResolverFromContext(ctx); !ok || got != (recvFactsTestResolver{}) {
		t.Fatalf("native resolver = ok:%v %#v", ok, got)
	}
	if got, ok := modelview.FromContext(ctx); !ok || got != identity {
		t.Fatalf("model view identity = ok:%v %+v", ok, got)
	}
}

func TestRecvTurnFacts_ProjectContextDefensiveProjection(t *testing.T) {
	t.Parallel()

	facts := newRecvTurnFacts(context.Background(), recvTurnFactsInput{
		recvViews: execctx.Views{
			Session:     session.SessionView{Labels: map[string]string{"session": "stored"}},
			Annotations: map[string]string{"annotation": "stored"},
		},
		recvViewsOK: true,
		routePrefs:  []string{"backend-stored:model"},
	})
	ctx := facts.projectContext(context.Background(), nil)
	projected, ok := execctx.FromContext(ctx)
	if !ok {
		t.Fatal("projected execution views missing")
	}
	projected.Session.Labels["session"] = "mutated"
	projected.Annotations["annotation"] = "mutated"
	prefs := execctx.RouteCandidatePreferences(ctx)
	prefs[0] = "backend-mutated:model"

	if got := facts.recvViews.Session.Labels["session"]; got != "stored" {
		t.Fatalf("stored session label mutated through projection: %q", got)
	}
	if got := facts.recvViews.Annotations["annotation"]; got != "stored" {
		t.Fatalf("stored annotation mutated through projection: %q", got)
	}
	if got := facts.routePrefs[0]; got != "backend-stored:model" {
		t.Fatalf("stored route preference mutated through projection: %q", got)
	}
}

func TestNewRecvTurnFacts_InitializesStableBillingState(t *testing.T) {
	t.Parallel()

	callID := usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	facts := newRecvTurnFacts(context.Background(), recvTurnFactsInput{billingCallID: callID})
	if facts.billingCallState == nil {
		t.Fatal("facts must own a stable billing call-state reference")
	}
	if facts.billingCallState.callID != callID {
		t.Fatalf("billing state call id = %q, want %q", facts.billingCallState.callID, callID)
	}
}
