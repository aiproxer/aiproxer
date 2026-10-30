package runtime

import (
	"slices"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	coreterm "github.com/aiproxer/aiproxer/internal/core/terminal"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

type requestTerminalFacts struct {
	call                         aipapi.Call
	traceID                      string
	aLegID                       string
	billingCallID                usageevidence.BillingCallID
	submissionID                 string
	storeID                      string
	storeIDStamped               bool
	billingState                 *billingCallState
	accountID                    string
	sessionID                    string
	pricing                      usageevidence.VersionRef
	chargePolicy                 usageevidence.VersionRef
	identityStamped              bool
	requestAuth                  *requestAuthorityState
	secureTurn                   execctx.SecureSessionTurn
	secureTurnOK                 bool
	terminalDecisionPolicy       terminaldecision.PolicySnapshot
	terminalDecisionEnabled      bool
	replacementBlocked           bool
	routePrefs                   []string
	recvViews                    execctx.Views
	metering                     *checkpoint.RequestHolder
	conversationSnapshot         conversationprojection.Snapshot
	conversationProvenance       []conversationprojection.OverlayProvenance
	conversationFilteredBaseline aipapi.Call
	ingressCall                  aipapi.Call
	continuationIntent           continuationIntentFacts
	wirePayload                  *wireAttemptPayload
}

// attemptTerminalEvidence is a value snapshot of the current B-leg identity.
// It deliberately carries no attempt owner or authority pointer.
type attemptTerminalEvidence struct {
	bleg       b2bua.BLegRecord
	candidate  routing.AttemptCandidate
	startedAt  time.Time
	accounting attemptAccountingSnapshot
}

// responseTerminalSnapshot is the terminal-facing response evidence boundary.
// Mutable response state is copied before terminal effects begin.
type responseTerminalSnapshot struct {
	accumulator   coreterm.AccumulatorSnapshot
	operatorUsage aipapi.Event
	billingEv     aipapi.Event
	usageEv       aipapi.Event
	seenEvents    []aipapi.Event
	releasedText  string
}

type responseRequestEvidence struct {
	traceID      string
	aLegID       string
	sessionID    string
	secureTurn   execctx.SecureSessionTurn
	secureTurnOK bool
}

func (f recvTurnFacts) terminalFacts() requestTerminalFacts {
	return requestTerminalFacts{
		call:                         aipapi.CloneCall(f.baseline),
		traceID:                      f.traceID,
		aLegID:                       f.aLegID,
		billingCallID:                f.billingCallID,
		submissionID:                 strings.TrimSpace(f.submissionID),
		storeID:                      strings.TrimSpace(f.billingStoreID),
		storeIDStamped:               f.billingStoreIDStamped,
		billingState:                 f.billingCallState,
		accountID:                    f.billingAccountID,
		sessionID:                    f.baseline.Session.AuthoritativeSessionID,
		pricing:                      f.billingCustomerPricing,
		chargePolicy:                 f.billingChargePolicy,
		identityStamped:              f.billingIdentityStamped,
		requestAuth:                  f.requestAuth,
		secureTurn:                   f.secureTurn,
		secureTurnOK:                 f.secureTurnOK,
		terminalDecisionPolicy:       f.terminalDecisionPolicy,
		terminalDecisionEnabled:      f.terminalDecisionEnabled,
		routePrefs:                   slices.Clone(f.routePrefs),
		recvViews:                    f.recvViews,
		metering:                     f.metering,
		conversationSnapshot:         cloneSnapshot(f.conversationSnapshot),
		conversationProvenance:       slices.Clone(f.conversationProvenance),
		conversationFilteredBaseline: aipapi.CloneCall(f.conversationFilteredBaseline),
		ingressCall:                  aipapi.CloneCall(f.ingressCall),
		continuationIntent:           f.continuationIntent,
		wirePayload:                  f.wirePayload,
	}
}

func cloneSnapshot(s conversationprojection.Snapshot) conversationprojection.Snapshot {
	out := s
	if s.NeverBackend != nil {
		out.NeverBackend = slices.Clone(s.NeverBackend)
	}
	if s.Steering != nil {
		out.Steering = slices.Clone(s.Steering)
		// Deep copy anchors
		for i := range out.Steering {
			if out.Steering[i].Placement.Anchor != nil {
				cp := *out.Steering[i].Placement.Anchor
				out.Steering[i].Placement.Anchor = &cp
			}
		}
	}
	return out
}

func newResponseRequestEvidence(traceID, aLegID, canonicalSessionID string, wirePayload *wireAttemptPayload, secureTurn execctx.SecureSessionTurn, secureTurnOK bool) responseRequestEvidence {
	sessID := canonicalSessionID
	if sessID == "" && wirePayload != nil {
		sessID = wirePayload.sessionID
	}
	return responseRequestEvidence{
		traceID:      traceID,
		aLegID:       aLegID,
		sessionID:    sessID,
		secureTurn:   secureTurn,
		secureTurnOK: secureTurnOK,
	}
}

func (f requestTerminalFacts) responseEvidence() responseRequestEvidence {
	return newResponseRequestEvidence(f.traceID, f.aLegID, f.call.Session.AuthoritativeSessionID, f.wirePayload, f.secureTurn, f.secureTurnOK)
}

func (f recvTurnFacts) responseEvidence() responseRequestEvidence {
	return newResponseRequestEvidence(f.traceID, f.aLegID, f.baseline.Session.AuthoritativeSessionID, f.wirePayload, f.secureTurn, f.secureTurnOK)
}

// responseEvidenceFromWire constructs bounded response evidence directly from
// WireTurnFacts without requiring a full aipapi.Call (Requirements 13.7, 19.4).
func responseEvidenceFromWire(facts largebody.WireTurnFacts) responseRequestEvidence {
	sessID := facts.Session.Input.AuthoritativeSessionID
	if sessID == "" {
		sessID = facts.Identity.RequestID
	}
	alegID := facts.Session.Input.ALegID
	if alegID == "" {
		alegID = facts.Identity.RequestID
	}
	return responseRequestEvidence{
		traceID:      facts.Identity.TraceID,
		aLegID:       alegID,
		sessionID:    sessID,
		secureTurn:   execctx.SecureSessionTurn{},
		secureTurnOK: false,
	}
}

func (a *attemptSession) terminalEvidence() attemptTerminalEvidence {
	if a == nil {
		return attemptTerminalEvidence{}
	}
	return attemptTerminalEvidence{bleg: a.bleg, candidate: a.cand, startedAt: a.accountingStartedAt(), accounting: a.accountingSnapshot()}
}

func (p *responsePipeline) terminalEvidenceSnapshot() responseTerminalSnapshot {
	if p == nil {
		return responseTerminalSnapshot{}
	}
	return responseTerminalSnapshot{
		accumulator:   p.accumulatorSnapshot(),
		operatorUsage: p.operatorUsageForFinalize(),
		billingEv:     p.billingEvidenceFallback(),
		usageEv:       p.usageEvidenceOrEmpty(),
		seenEvents:    p.seenEventsCopy(),
		releasedText:  p.releasedOutputText(),
	}
}
