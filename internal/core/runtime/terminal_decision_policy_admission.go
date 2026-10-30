package runtime

import (
	"context"
	"strconv"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/terminaldecision"
)

const terminalDecisionFeatureID = "terminal-decision"

// snapshotTerminalDecisionPolicy resolves the process policy exactly once at
// request admission. The returned values are copied into recvTurnFacts, so a
// later policy write cannot rebind an admitted request or its continuations.
func (e *Executor) snapshotTerminalDecisionPolicy(ctx context.Context, ibt *identityBoundTurn) (terminaldecision.PolicySnapshot, bool, error) {
	policy := terminaldecision.PolicySnapshot{Revision: "0"}
	if e == nil || e.RuntimeSnapshot == nil || e.RuntimeSnapshot.TerminalDecisionProvider() == nil {
		return policy, false, nil
	}
	if ibt == nil || !ibt.secureTurnOK {
		return policy, true, nil
	}

	if e.TerminalPolicyReader != nil {
		snapshot, err := e.TerminalPolicyReader.Effective(ctx, TerminalPolicyQuery{
			SecureSessionIncarnation: string(ibt.secureTurn.SessionID),
			ALegID:                   ibt.aLeg.ALegID,
			FeatureID:                terminalDecisionFeatureID,
			GenerationDefault:        true,
		})
		if err != nil {
			return policy, false, err
		}
		policy.Revision = strconv.FormatUint(snapshot.Revision, 10)
		return policy, snapshot.EffectiveEnabled, nil
	}

	// Minimal executors used by core tests may compose a provider without the
	// process root. Preserve their generation-default behavior; production
	// composition always supplies the process-owned reader.
	return policy, true, nil
}

// TerminalPolicyReader resolves session-scoped terminal decision policy overrides
// at request admission. The interface is consumer-owned: core execution never
// mutates policy and never observes actor-specific keys or store internals.
type TerminalPolicyReader interface {
	Effective(ctx context.Context, in TerminalPolicyQuery) (TerminalPolicySnapshot, error)
}

// TerminalPolicyQuery provides the scoped session identity and generation default
// needed to evaluate the effective terminal decision policy for an incoming turn.
type TerminalPolicyQuery struct {
	SecureSessionIncarnation string
	ALegID                   string
	FeatureID                string
	GenerationDefault        bool
}

// TerminalPolicySnapshot carries only the immutable effective decision and revision
// resolved at request admission.
type TerminalPolicySnapshot struct {
	EffectiveEnabled bool
	Revision         uint64
}
