package authoritycoord

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

type AttemptPriorityClass int

const (
	AttemptPriorityUnknown AttemptPriorityClass = iota
	AttemptPriorityHardSpend
	AttemptPriorityQuotaRate
	AttemptPriorityAdvisory
)

type AttemptSlot struct {
	ID              string
	Class           AttemptPriorityClass
	Provider        authority.AttemptProvider
	Strength        authority.Strength
	FailureBehavior authority.FailureBehavior
	Descriptor      *authority.ProviderDescriptor
	Stage           authority.Stage
}

type AttemptCoordinator struct {
	Slots          []AttemptSlot
	CleanupTimeout time.Duration
	Now            func() time.Time
}

func (c *AttemptCoordinator) Admit(ctx context.Context, in authority.AttemptAdmission) (CompositeDecision, error) {
	if err := in.Validate(); err != nil {
		return CompositeDecision{}, err
	}
	if c == nil {
		return CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessDisabled}, nil
	}
	slots := attemptStageSlots(c.Slots)
	if err := validateAttemptPriorityClasses(c.Slots); err != nil {
		return CompositeDecision{}, err
	}
	if err := validateStageSlots(slots, func(p authority.AttemptProvider) bool { return p == nil }); err != nil {
		return CompositeDecision{}, err
	}
	timeout := c.CleanupTimeout
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	return runStageAdmit(ctx, nil, slots, in, timeout, attemptAdmitConfig())
}

func (c *AttemptCoordinator) Settle(parent context.Context, stack CompensationStack, in authority.AttemptSettlement) error {
	if c == nil {
		return nil
	}
	slots := attemptStageSlotsUnsorted(c.Slots)
	if err := validateAttemptPriorityClasses(c.Slots); err != nil {
		return err
	}
	if err := validateStageSlots(slots, func(p authority.AttemptProvider) bool { return p == nil }); err != nil {
		return err
	}
	timeout := defaultCleanupTimeout
	if c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	return runStageSettle(parent, slots, stack, in, timeout, stageSettleConfig[authority.AttemptSettlement, authority.AttemptProvider]{
		observability: StageSettleRecordAllFailures,
		providerNil:   func(p authority.AttemptProvider) bool { return p == nil },
		settle:        invokeSettleAttempt,
		validate:      validateAttemptSettlement,
		withHandles: func(in authority.AttemptSettlement, handles []string) authority.AttemptSettlement {
			out := in
			out.Handles = append([]string(nil), handles...)
			out.Reservations = filterAttemptReservations(in.Reservations, handles)
			return out
		},
		resolvePosture: resolveStagePosture[authority.AttemptProvider],
	})
}

func (c *AttemptCoordinator) Release(ctx context.Context, stack CompensationStack) []CompensateFailed {
	timeout := defaultCleanupTimeout
	if c != nil && c.CleanupTimeout > 0 {
		timeout = c.CleanupTimeout
	}
	return stack.ReverseCompensate(ctx, timeout)
}

func validateAttemptPriorityClasses(slots []AttemptSlot) error {
	for _, slot := range slots {
		if slot.Provider != nil && slot.Class == AttemptPriorityUnknown {
			return fmt.Errorf("authoritycoord: unknown attempt priority class")
		}
	}
	return nil
}

func attemptStageSlot(slot AttemptSlot) stageProviderSlot[authority.AttemptProvider] {
	return stageProviderSlot[authority.AttemptProvider]{
		id: strings.TrimSpace(slot.ID), classOrd: int(slot.Class), provider: slot.Provider,
		strength: slot.Strength, failBeh: slot.FailureBehavior, descriptor: slot.Descriptor,
		stage: slot.Stage, advisoryClassOrd: int(AttemptPriorityAdvisory),
	}
}

func attemptStageSlots(slots []AttemptSlot) []stageProviderSlot[authority.AttemptProvider] {
	out := make([]stageProviderSlot[authority.AttemptProvider], len(slots))
	for i, slot := range slots {
		out[i] = attemptStageSlot(slot)
	}
	return out
}

func attemptStageSlotsUnsorted(slots []AttemptSlot) []stageProviderSlot[authority.AttemptProvider] {
	return attemptStageSlots(slots)
}

func filterAttemptReservations(reservations []authority.Reservation, handles []string) []authority.Reservation {
	if len(reservations) == 0 || len(handles) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(handles))
	for _, h := range handles {
		h = strings.TrimSpace(h)
		if h != "" {
			allow[h] = struct{}{}
		}
	}
	out := make([]authority.Reservation, 0, len(handles))
	for _, r := range reservations {
		if _, ok := allow[strings.TrimSpace(r.Handle)]; ok {
			out = append(out, r)
		}
	}
	return out
}

func pushAttemptHoldsFromAdmit(stack *CompensationStack, id string, prov authority.AttemptProvider, in authority.AttemptAdmission, d authority.Decision) {
	pushAttemptDecisionHolds(stack, id, prov, in.RequestID, in.AttemptID, in.BLegID, d)
}

func attemptAdmitConfig() stageAdmitConfig[authority.AttemptAdmission, authority.AttemptProvider] {
	return stageAdmitConfig[authority.AttemptAdmission, authority.AttemptProvider]{
		label:       "attempt",
		providerNil: func(p authority.AttemptProvider) bool { return p == nil },
		admit:       invokeAdmitAttempt,
		pushHolds:   pushAttemptHoldsFromAdmit,
		registration: func(slot stageProviderSlot[authority.AttemptProvider]) (authority.ProviderDescriptor, authority.Stage) {
			return stageSlotRegistration(slot, authority.StageAttemptAdmit)
		},
		resolvePosture: resolveStagePosture[authority.AttemptProvider],
	}
}
