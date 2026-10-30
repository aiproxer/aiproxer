package keepwarm

import (
	"context"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/promptcache"
)

// Orchestrator is the runtime-facing adapter. Call BeginRealTurn immediately
// after A-leg authority correlation and before route planning; call
// ArmCommittedTurn exactly once after the committed successful terminal.
type Orchestrator struct {
	manager *Manager
	policy  *PolicyStore
}

// NewOrchestrator accepts an optional process policy store so session-end
// cleanup can remove both generation-local state and process-owned disables.
func NewOrchestrator(manager *Manager, policy ...*PolicyStore) *Orchestrator {
	var store *PolicyStore
	if len(policy) > 0 {
		store = policy[0]
	}
	return &Orchestrator{manager: manager, policy: store}
}

func (o *Orchestrator) BeginRealTurn(aLegID string) {
	if o != nil && o.manager != nil {
		o.manager.BeginForegroundTurn(aLegID)
	}
}

// EndSession releases the A-leg's maintenance state without invoking provider
// control synchronously and forgets the process-owned administrative policy.
func (o *Orchestrator) EndSession(aLegID string) {
	if o == nil {
		return
	}
	if o.manager != nil {
		o.manager.EndSession(aLegID)
	}
	if o.policy != nil {
		o.policy.Forget(aLegID)
	}
}

func (o *Orchestrator) ArmCommittedTurn(input ArmInput) ArmResult {
	if o == nil || o.manager == nil {
		return ArmResult{Reason: "manager_unavailable"}
	}
	return o.manager.ArmFromCommittedTurn(input)
}

func (o *Orchestrator) RunDue(ctx context.Context) {
	if o != nil && o.manager != nil {
		o.manager.RunDue(ctx)
	}
}

func (o *Orchestrator) Quiesce(ctx context.Context) error {
	if o == nil || o.manager == nil {
		return nil
	}
	return o.manager.Quiesce(ctx)
}

// CommittedTurn is a convenience constructor that keeps the arm adapter tied
// to canonical tool events and the residency sideband only.
func CommittedTurn(aLegID, bLegID, backendInstanceID, canonicalModelID string, tools []aipapi.ToolEvent, observations []promptcache.Observation, controller promptcache.Controller) ArmInput {
	return ArmInput{ALegID: aLegID, BLegID: bLegID, BackendInstanceID: backendInstanceID, CanonicalModelID: canonicalModelID, ToolEvents: tools, Observations: observations, Controller: controller, CommittedSuccessful: true}
}
