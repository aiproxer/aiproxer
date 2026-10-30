package featurehost

// Task 10.3 test-only opt-in consumer.
//
// Requirement 1.8 forbids every production consumer from gating on session
// classification until its own implementation opts in, so the capability proof
// for requirements 1.6 and 4.2 must come from a consumer that does not ship.
// This file is that consumer, and nothing else: the closed import and selector
// assertions in sessionclassification_optin_capability_test.go are what keep it
// that way, and they can only be closed because the consumer lives alone here.
//
// Three design choices are load-bearing.
//
//  1. optInCodingAgentDecision is the consumer's ENTIRE decision, and its only
//     parameter is the immutable session classification snapshot. Requirement 1.6
//     says a downstream feature must be able to decide from that snapshot alone,
//     without parsing a User-Agent, rescanning prompt content, re-running the
//     classifier, or consulting a vendor-specific result. Expressing the decision
//     as a one-argument function makes that claim checkable by an AST guard
//     instead of by a comment.
//
//  2. Handle discards the *aipapi.Call parameter instead of merely ignoring it,
//     and calls the decision function with meta.Session.Classification directly
//     rather than through a local. The handler signature hands a consumer the
//     whole request, so one that wanted to re-derive a classification could.
//     Discarding it, and asserting the exact set of names the handler touches,
//     turns "it does not need the request" from an inference into a fact.
//
//  3. The authoritative session ID is recorded but never reaches the decision.
//     It exists so an observation can be attributed to a session in the census,
//     which is what makes "this fired on the first turn" a measurable claim.
//
// The gate is registered on PlanePreRequestHandlers, which is a
// PlaneAccessCanonicalRequired plane: a consumer there statically blocks the
// large-payload wire lane. That is why the wire executor in the harness is always
// built from the unmodified plane set (task 7.3's finding), and why the harness
// serves one wire turn.

import (
	"context"
	"sync"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkhooks "github.com/aiproxer/aiproxer/pkg/aipsdk/hooks"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/prerequest"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
)

// optInGateReason is the pre-request rejection message. It is a test constant so
// the composed assertion can distinguish this consumer's own rejection from any
// other failure in the preparation pipeline.
const optInGateReason = "coding-agent turn gated by the opt-in consumer"

// optInCodingAgentDecision is the whole decision a consumer that opted into
// session classification makes on one turn.
//
// Its only input is the immutable snapshot. It cannot consult the request, the
// client identity, the prompt, the classifier, or any vendor result, because none
// of those is in scope: adding one changes this signature, which the AST guard in
// sessionclassification_optin_capability_test.go rejects.
func optInCodingAgentDecision(snapshot session.Classification) bool {
	return snapshot.IsCodingAgent()
}

// optInTurn is one recorded consumer observation: exactly the snapshot the
// consumer was handed, the session it belonged to, and whether the consumer's own
// decision function engaged.
type optInTurn struct {
	SessionID string
	Snapshot  session.Classification
	Gated     bool
}

// optInCodingAgentGate is the test-only downstream consumer. It is not a
// production consumer: no bundled feature gates on classification until its own
// implementation opts in (requirements 1.8 and 1.7).
type optInCodingAgentGate struct {
	id string

	mu    sync.Mutex
	turns []optInTurn
}

var _ prerequest.Handler = (*optInCodingAgentGate)(nil)

func newOptInCodingAgentGate(id string) *optInCodingAgentGate {
	return &optInCodingAgentGate{id: id}
}

func (g *optInCodingAgentGate) ID() string                        { return g.id }
func (g *optInCodingAgentGate) Order() int                        { return 0 }
func (g *optInCodingAgentGate) FailureMode() sdkhooks.FailureMode { return sdkhooks.FailOpen }

// Handle is the consumer. The call is deliberately discarded, and the decision
// function is handed meta.Session.Classification directly - never a local - so
// the decision provably starts from the projection.
func (g *optInCodingAgentGate) Handle(
	_ context.Context,
	_ *aipapi.Call,
	meta prerequest.Meta,
	_ prerequest.Services,
) (prerequest.Decision, error) {
	if optInCodingAgentDecision(meta.Session.Classification) {
		g.record(meta.Session.AuthoritativeSessionID, meta.Session.Classification, true)
		return prerequest.Deny(optInGateReason), nil
	}
	g.record(meta.Session.AuthoritativeSessionID, meta.Session.Classification, false)
	return prerequest.Allow(), nil
}

// record stores one observation. It is deliberately not in Handle's selector
// set that the AST guard asserts, so recording cannot smuggle an extra read into
// the decision path.
func (g *optInCodingAgentGate) record(sessionID string, snapshot session.Classification, gated bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.turns = append(g.turns, optInTurn{SessionID: sessionID, Snapshot: snapshot, Gated: gated})
}

// recorded returns every observation in turn order.
func (g *optInCodingAgentGate) recorded() []optInTurn {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]optInTurn(nil), g.turns...)
}

// total returns the number of turns the consumer observed.
func (g *optInCodingAgentGate) total() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.turns)
}

// gated returns the number of turns the consumer actually gated on.
func (g *optInCodingAgentGate) gated() int {
	count := 0
	for _, turn := range g.recorded() {
		if turn.Gated {
			count++
		}
	}
	return count
}
