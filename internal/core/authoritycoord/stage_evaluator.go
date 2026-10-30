package authoritycoord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authorityattribution"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
)

// StageSettleObservability controls whether settle failures remain observable
// for reconciliation regardless of admission-time posture (requirement 15.5).
type StageSettleObservability int

const (
	StageSettleUnspecified StageSettleObservability = iota
	// StageSettleRecordAllFailures retains every provider settle failure (req 15.5).
	StageSettleRecordAllFailures
)

type stageProviderSlot[P any] struct {
	id               string
	classOrd         int
	provider         P
	strength         authority.Strength
	failBeh          authority.FailureBehavior
	descriptor       *authority.ProviderDescriptor
	stage            authority.Stage
	advisoryClassOrd int
}

type stageAdmitConfig[AdmitIn any, P any] struct {
	label          string
	providerNil    func(P) bool
	admit          func(context.Context, P, AdmitIn) (authority.Decision, error)
	pushHolds      func(*CompensationStack, string, P, AdmitIn, authority.Decision)
	registration   func(stageProviderSlot[P]) (authority.ProviderDescriptor, authority.Stage)
	resolvePosture func(stageProviderSlot[P]) (authority.Strength, authority.FailureBehavior)
}

type stageSettleConfig[SettleIn any, P any] struct {
	observability  StageSettleObservability
	providerNil    func(P) bool
	skipProvider   func(string) bool
	settle         func(context.Context, P, SettleIn) (authority.Settlement, error)
	validate       func(authority.Settlement, []string) error
	withHandles    func(SettleIn, []string) SettleIn
	resolvePosture func(stageProviderSlot[P]) (authority.Strength, authority.FailureBehavior)
}

func validateStageSlots[P any](slots []stageProviderSlot[P], isNil func(P) bool) error {
	seen := make(map[string]struct{}, len(slots))
	for _, slot := range slots {
		if isNil != nil && isNil(slot.provider) {
			continue
		}
		id := strings.TrimSpace(slot.id)
		if id == "" {
			return fmt.Errorf("authoritycoord: empty provider id rejected")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("authoritycoord: duplicate provider id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func sortStageSlots[P any](slots []stageProviderSlot[P]) {
	sort.SliceStable(slots, func(i, j int) bool {
		if slots[i].classOrd != slots[j].classOrd {
			return slots[i].classOrd < slots[j].classOrd
		}
		return slots[i].id < slots[j].id
	})
}

func runStageAdmit[AdmitIn any, P any](
	ctx context.Context,
	prior *CompensationStack,
	slots []stageProviderSlot[P],
	in AdmitIn,
	timeout time.Duration,
	cfg stageAdmitConfig[AdmitIn, P],
) (CompositeDecision, error) {
	stack := prior
	if stack == nil {
		stack = &CompensationStack{}
	}
	out := CompositeDecision{Kind: authority.DecisionAllow, Readiness: authority.ReadinessReady}
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	sortStageSlots(slots)

	for _, slot := range slots {
		if cfg.providerNil != nil && cfg.providerNil(slot.provider) {
			continue
		}
		id := strings.TrimSpace(slot.id)
		strength, failBeh := cfg.resolvePosture(slot)

		d, err := cfg.admit(ctx, slot.provider, in)
		if err != nil {
			recordStageAdmitErrorDecision(&out, d)
			fails := compensateCurrentThenPrior(ctx, timeout, stack, func(claimed *CompensationStack) {
				cfg.pushHolds(claimed, id, slot.provider, in, d)
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
			if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
				out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			out.Stack = *stack
			return out, &UnavailableError{ProviderID: id, Err: err, Decision: d}
		}

		reg, stage := cfg.registration(slot)
		if vErr := validateCoordinatorDecision(d, reg, stage); vErr != nil {
			ownsHolds := decisionHasHolds(d)
			if errors.Is(vErr, errNonAllowWithHolds) && d.Kind == authority.DecisionDeny && strength == authority.StrengthAdvisory {
				fails := compensateCurrentOnly(ctx, timeout, func(claimed *CompensationStack) {
					cfg.pushHolds(claimed, id, slot.provider, in, d)
				})
				out.CompensateFailures = append(out.CompensateFailures, fails...)
				d.Kind = authority.DecisionAdvisory
				d.Evidence = advisoryEvidenceFrom(d)
				out.ProviderDecisions = append(out.ProviderDecisions, d)
				out.Evidence = mergeAdvisoryEvidence(out.Evidence, d.Evidence)
				out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			if strength == authority.StrengthAdvisory {
				fails := compensateCurrentOnly(ctx, timeout, func(claimed *CompensationStack) {
					if ownsHolds {
						cfg.pushHolds(claimed, id, slot.provider, in, d)
					}
				})
				out.CompensateFailures = append(out.CompensateFailures, fails...)
				out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			fails := compensateCurrentThenPrior(ctx, timeout, stack, func(claimed *CompensationStack) {
				if ownsHolds {
					cfg.pushHolds(claimed, id, slot.provider, in, d)
				}
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			if errors.Is(vErr, errNonAllowWithHolds) && d.Kind == authority.DecisionDeny {
				out.Stack = *stack
				return out, &DeniedError{ProviderID: id, Decision: d}
			}
			out.Stack = *stack
			return out, &UnavailableError{ProviderID: id, Err: vErr, Decision: d}
		}

		out.ProviderDecisions = append(out.ProviderDecisions, d)
		out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, d.Readiness)
		merged, merr := mergeClampsNonWidening(out.Clamps, d.Clamps)
		if merr != nil {
			fails := compensateCurrentThenPrior(ctx, timeout, stack, func(claimed *CompensationStack) {
				cfg.pushHolds(claimed, id, slot.provider, in, d)
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			out.Stack = *stack
			return out, fmt.Errorf("authoritycoord: %s %s: %w", cfg.label, id, merr)
		}
		out.Clamps = merged
		if len(d.BoundVersions) > 0 {
			out.BoundVersions = append(out.BoundVersions, d.BoundVersions...)
		}

		switch d.Kind {
		case authority.DecisionDeny:
			if strength == authority.StrengthAdvisory {
				d.Kind = authority.DecisionAdvisory
				d.Evidence = advisoryEvidenceFrom(d)
				out.ProviderDecisions[len(out.ProviderDecisions)-1] = d
				out.Evidence = mergeAdvisoryEvidence(out.Evidence, d.Evidence)
				out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, authority.ReadinessDegraded)
				continue
			}
			fails := compensateCurrentThenPrior(ctx, timeout, stack, func(claimed *CompensationStack) {
				if decisionHasHolds(d) {
					cfg.pushHolds(claimed, id, slot.provider, in, d)
				}
			})
			out.CompensateFailures = append(out.CompensateFailures, fails...)
			out.Kind = authority.DecisionDeny
			out.DeniedBy = id
			out.Stack = *stack
			return out, &DeniedError{ProviderID: id, Decision: d}
		case authority.DecisionAllow, authority.DecisionAdvisory, "":
			cfg.pushHolds(stack, id, slot.provider, in, d)
			if d.Kind == authority.DecisionAdvisory {
				out.Evidence = mergeAdvisoryEvidence(out.Evidence, d.Evidence)
			}
		}
	}
	out.Stack = *stack
	return out, nil
}

func recordStageAdmitErrorDecision(out *CompositeDecision, d authority.Decision) {
	if out == nil || !stageAdmitDecisionPresent(d) {
		return
	}
	out.ProviderDecisions = append(out.ProviderDecisions, d)
	out.Readiness = authorityattribution.AggregateReadiness(out.Readiness, d.Readiness)
	if len(d.BoundVersions) > 0 {
		out.BoundVersions = append(out.BoundVersions, d.BoundVersions...)
	}
	if safeEvidencePresent(d.Evidence) {
		out.Evidence = mergeAdvisoryEvidence(out.Evidence, d.Evidence)
	}
}

func stageAdmitDecisionPresent(d authority.Decision) bool {
	return d.Kind != "" || strings.TrimSpace(d.ProviderID) != "" || d.Stage != "" || d.Readiness != "" ||
		len(d.Reservations) > 0 || len(d.Clamps) > 0 || strings.TrimSpace(d.CompensationHandle) != "" ||
		len(d.BoundVersions) > 0 || len(d.RatingVersions) > 0 || safeEvidencePresent(d.Evidence)
}

func safeEvidencePresent(ev authority.SafeEvidence) bool {
	return strings.TrimSpace(ev.Category) != "" || strings.TrimSpace(ev.Code) != "" ||
		strings.TrimSpace(ev.Message) != "" || strings.TrimSpace(ev.RuleID) != "" ||
		strings.TrimSpace(ev.ProviderID) != "" || len(ev.Attrs) > 0 || ev.QuotaPolicyRef != nil ||
		len(ev.EvidenceRefs) > 0
}

func runStageSettle[SettleIn any, P any](
	parent context.Context,
	slots []stageProviderSlot[P],
	stack CompensationStack,
	in SettleIn,
	timeout time.Duration,
	cfg stageSettleConfig[SettleIn, P],
) error {
	if timeout <= 0 {
		timeout = defaultCleanupTimeout
	}
	handlesByProvider := make(map[string][]string)
	for _, e := range stack.Entries() {
		id := strings.TrimSpace(e.ProviderID)
		h := strings.TrimSpace(e.Handle)
		if id == "" || h == "" {
			continue
		}
		if cfg.skipProvider != nil && cfg.skipProvider(id) {
			continue
		}
		handlesByProvider[id] = append(handlesByProvider[id], h)
	}
	tracker := stack.settlement()
	recordAll := cfg.observability == StageSettleRecordAllFailures
	var first error
	for _, slot := range slots {
		if cfg.providerNil != nil && cfg.providerNil(slot.provider) {
			continue
		}
		id := strings.TrimSpace(slot.id)
		if cfg.skipProvider != nil && cfg.skipProvider(id) {
			continue
		}
		handles := handlesByProvider[id]
		if len(handles) == 0 {
			continue
		}
		skip, wait, finish := tracker.beginSettle(id)
		if skip {
			continue
		}
		if wait != nil {
			<-wait
			if err := tracker.waitResult(id); err != nil {
				if !recordAll {
					strength, failBeh := cfg.resolvePosture(slot)
					if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
						continue
					}
				}
				if first == nil {
					first = &UnavailableError{ProviderID: id, Err: err}
				}
			}
			continue
		}
		settlement := cfg.withHandles(in, handles)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), timeout)
		result, err := cfg.settle(ctx, slot.provider, settlement)
		cancel()
		if err == nil {
			if vErr := cfg.validate(result, handles); vErr != nil {
				err = vErr
			}
		}
		finish(err)
		if err != nil {
			if !recordAll {
				strength, failBeh := cfg.resolvePosture(slot)
				if strength == authority.StrengthAdvisory || failBeh == authority.FailureFailOpen {
					continue
				}
			}
			if first == nil {
				first = &UnavailableError{ProviderID: id, Err: err}
			}
		}
	}
	return first
}

func resolveStagePosture[P any](slot stageProviderSlot[P]) (authority.Strength, authority.FailureBehavior) {
	strength := slot.strength
	if strength == "" {
		if slot.classOrd == slot.advisoryClassOrd {
			strength = authority.StrengthAdvisory
		} else {
			strength = authority.StrengthRequired
		}
	}
	failBeh := slot.failBeh
	if failBeh == "" {
		if strength == authority.StrengthAdvisory {
			failBeh = authority.FailureFailOpen
		} else {
			failBeh = authority.FailureFailClosed
		}
	}
	return strength, failBeh
}

func stageSlotRegistration[P any](slot stageProviderSlot[P], defaultStage authority.Stage) (authority.ProviderDescriptor, authority.Stage) {
	stage := slot.stage
	if stage == "" {
		stage = defaultStage
	}
	if slot.descriptor != nil {
		return *slot.descriptor, stage
	}
	strength, failBeh := resolveStagePosture(slot)
	return authority.ProviderDescriptor{
		ID: strings.TrimSpace(slot.id),
		Postures: []authority.StagePosture{{
			Stage: stage, Strength: strength, FailureBehavior: failBeh,
		}},
	}, stage
}

func compensateCurrentOnly(ctx context.Context, timeout time.Duration, claim func(*CompensationStack)) []CompensateFailed {
	var claimed CompensationStack
	if claim != nil {
		claim(&claimed)
	}
	return claimed.ReverseCompensate(ctx, timeout)
}

func compensateCurrentThenPrior(ctx context.Context, timeout time.Duration, prior *CompensationStack, claim func(*CompensationStack)) []CompensateFailed {
	claimedFails := compensateCurrentOnly(ctx, timeout, claim)
	var priorFails []CompensateFailed
	if prior != nil {
		priorFails = prior.ReverseCompensate(ctx, timeout)
	}
	return append(claimedFails, priorFails...)
}

func mergeAdvisoryEvidence(dst, add authority.SafeEvidence) authority.SafeEvidence {
	if strings.TrimSpace(dst.Category) == "" {
		return add
	}
	if strings.TrimSpace(add.Category) == "" {
		return dst
	}
	return add
}
