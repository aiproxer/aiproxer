package adapter

import (
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

// maxPreBindConnectorClaims bounds the pre-bind queue of connector claims. A
// claim that arrives before the runtime bound the attempt is queued with its
// original host receipt time; the queue drains exactly once, before later
// frames are processed.
const maxPreBindConnectorClaims = 1024

// preBindConnectorClaim is one queued connector claim and its original host
// receipt time.
type preBindConnectorClaim struct {
	evidence   backendplugin.AccountingEvidenceV2
	receivedAt time.Time
}

// connectorClaimState owns the host-binding state of one managed attempt. It
// holds the bounded pre-bind queue, the attempt-lifetime reference map, the
// admitted bound observations and the closed rejection records.
type connectorClaimState struct {
	mu sync.Mutex

	policy execbackend.ConnectorEconomicEvidencePolicy
	// optedIn reports that the peer negotiated the host-bound economic
	// evidence contract. A peer that did not opt in is never rewritten.
	optedIn bool

	trusted execbackend.ProviderBindingIdentity
	bound   bool

	preBind []preBindConnectorClaim
	refs    *execbackend.SourceReferenceMap

	admitted  []metering.Observation
	rejected  []execbackend.EconomicEvidence
	hostBytes uint64
	// pendingReleased holds the closed rejection reasons of the claims the
	// last bind released and rejected. They are recorded as capture losses of
	// their own and reported to the policy owner exactly once.
	pendingReleased []execbackend.ConnectorEvidenceRejection
	// coverages retains the transport coverage disposition of the first
	// binding per claim key. A repeated claim whose disposition changed is a
	// payload conflict: the first binding is preserved.
	coverages map[string]string
}

// newConnectorClaimState creates the attempt-lifetime claim state.
func newConnectorClaimState(policy execbackend.ConnectorEconomicEvidencePolicy, optedIn bool) *connectorClaimState {
	if policy == "" {
		policy = execbackend.ConnectorEconomicEvidenceDegrade
	}
	return &connectorClaimState{
		policy:    policy,
		optedIn:   optedIn,
		refs:      execbackend.NewSourceReferenceMap(execbackend.DefaultMaxHostBoundLogicalItems),
		coverages: make(map[string]string),
	}
}

// bindTrusted installs the trusted host facts and drains the pre-bind queue
// exactly once, before any later frame is processed.
//
// It returns the closed rejection tokens of the claims the drain rejected, in
// arrival order. The first one is the reason of the strict refusal.
func (c *connectorClaimState) bindTrusted(identity coremetering.ObservationIdentity, legPseudonym string, receivedAt time.Time) []execbackend.ConnectorEvidenceRejection {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bound {
		return nil
	}
	c.bound = true
	c.trusted = execbackend.ProviderBindingIdentity{
		StoreID:               identity.StoreID,
		RequestID:             identity.RequestID,
		CallID:                identity.CallID,
		BillingCallID:         identity.BillingCallID,
		ALegID:                identity.ALegID,
		BLegID:                identity.BLegID,
		AttemptID:             identity.AttemptID,
		AttemptSeq:            identity.AttemptSeq,
		ConnectorLegPseudonym: legPseudonym,
		AccountID:             identity.Provider.AccountID,
		Scope:                 identity.Provider.Scope.Clone(),
		ScopePresent:          identity.Provider.ScopePresent,
		ReceivedAt:            receivedAt.UTC(),
	}
	queued := c.preBind
	c.preBind = nil
	var reasons []execbackend.ConnectorEvidenceRejection
	for _, claim := range queued {
		if reason := c.bindLocked(claim.evidence, claim.receivedAt); reason != "" {
			reasons = append(reasons, reason)
		}
	}
	// The claims the drain released and rejected are reported after the claims
	// that released them: the first reason is the strict refusal's reason.
	reasons = append(reasons, c.pendingReleased...)
	c.pendingReleased = nil
	return reasons
}

// accept processes one connector economic evidence frame.
//
// It returns the closed rejection reason of a claim the host could not admit,
// or an empty reason when the claim was admitted, replayed or held. A claim
// that is still waiting for its predecessor is bound but not retained as an
// observation yet.
func (c *connectorClaimState) accept(evidence backendplugin.AccountingEvidenceV2, receivedAt time.Time) execbackend.ConnectorEvidenceRejection {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.optedIn {
		// A peer that did not opt in is never rewritten and its claim is never
		// retained. The explicit capture loss is the only trace.
		c.recordRejectionLocked(execbackend.ConnectorEvidenceRejectedPeerNotHostBound)
		return execbackend.ConnectorEvidenceRejectedPeerNotHostBound
	}
	if !c.bound {
		if len(c.preBind) >= maxPreBindConnectorClaims {
			c.recordRejectionLocked(execbackend.ConnectorEvidenceRejectedCapacity)
			return execbackend.ConnectorEvidenceRejectedCapacity
		}
		c.preBind = append(c.preBind, preBindConnectorClaim{evidence: evidence, receivedAt: receivedAt.UTC()})
		return ""
	}
	return c.bindLocked(evidence, receivedAt.UTC())
}

// bindLocked binds one claim against the trusted host facts. Callers hold the
// claim-state lock.
func (c *connectorClaimState) bindLocked(evidence backendplugin.AccountingEvidenceV2, receivedAt time.Time) execbackend.ConnectorEvidenceRejection {
	trusted := c.trusted
	trusted.ReceivedAt = receivedAt
	outcome := c.refs.Bind(evidence.Observation, trusted)
	// A rejection decided inside the release path is a rejection of the
	// attempt's economic evidence. Every rejected claim is recorded as its own
	// capture loss and reported to the policy owner: the claim that released
	// it is not the claim that was rejected.
	for _, rejection := range outcome.ReleasedRejections {
		c.recordRejectionLocked(rejection.Reason)
		c.pendingReleased = append(c.pendingReleased, rejection.Reason)
	}
	if outcome.Reason != "" {
		c.recordRejectionLocked(outcome.Reason)
		return outcome.Reason
	}
	coverage := string(evidence.Coverage)
	if coverage == "" {
		coverage = string(backendplugin.EvidenceCoverageComplete)
	}
	if outcome.Replay {
		if prior, ok := c.coverages[outcome.Bound.ClaimKey]; ok && prior != coverage {
			// The transport coverage disposition of the first binding is
			// immutable. A repeated claim with a different disposition is a
			// payload conflict and the first binding is preserved.
			c.recordRejectionLocked(execbackend.ConnectorEvidenceRejectedPayloadConflict)
			return execbackend.ConnectorEvidenceRejectedPayloadConflict
		}
		// An exact replay reuses the first bound observation and its first
		// receipt. Nothing is admitted twice.
		return ""
	}
	c.coverages[outcome.Bound.ClaimKey] = coverage
	if outcome.Held {
		// The claim is bound and counted once, but it awaits its predecessor.
		return ""
	}
	c.admitted = append(c.admitted, outcome.Bound.Observation)
	for _, released := range outcome.Released {
		c.admitted = append(c.admitted, released.Observation)
	}
	return ""
}

// takeReleasedRejections returns and clears the closed rejection reasons of
// the claims the last bind released and rejected. The policy owner sees every
// rejected claim exactly once.
func (c *connectorClaimState) takeReleasedRejections() []execbackend.ConnectorEvidenceRejection {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pendingReleased) == 0 {
		return nil
	}
	reasons := c.pendingReleased
	c.pendingReleased = nil
	return reasons
}

// releaseHeld rejects every claim that is still waiting for its predecessor at
// the terminal drain. The whole unresolved claim is rejected.
func (c *connectorClaimState) releaseHeld() []execbackend.ConnectorEvidenceRejection {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	outcomes := c.refs.ReleaseUnresolvedHeld()
	if len(outcomes) == 0 {
		return nil
	}
	reasons := make([]execbackend.ConnectorEvidenceRejection, 0, len(outcomes))
	for _, outcome := range outcomes {
		c.recordRejectionLocked(outcome.Reason)
		reasons = append(reasons, outcome.Reason)
	}
	return reasons
}

// recordRejectionLocked retains one closed rejection record. Only the reason
// token survives: no source hash, no payload, no connector text.
func (c *connectorClaimState) recordRejectionLocked(reason execbackend.ConnectorEvidenceRejection) {
	if reason == "" {
		return
	}
	c.rejected = append(c.rejected, execbackend.EconomicEvidence{RejectionReason: string(reason)})
}

// drain returns the admitted bound observations and the closed rejection
// records of the attempt.
func (c *connectorClaimState) drain() ([]metering.Observation, []execbackend.EconomicEvidence) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	admitted := c.admitted
	c.admitted = nil
	rejected := c.rejected
	c.rejected = nil
	return admitted, rejected
}
