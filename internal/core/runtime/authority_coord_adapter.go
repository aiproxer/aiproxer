package runtime

import (
	"context"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/authorityattribution"
	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

// usageAuthorityProviderAdapter bridges aipsdk RequestProvider / AttemptProvider
// onto the runtime's usage-limit Authority port with Phase 7 lifecycle/exposure
// forwarding.
type usageAuthorityProviderAdapter struct {
	svc UsageAuthorityService
}

func newUsageAuthorityProviderAdapter(svc UsageAuthorityService) *usageAuthorityProviderAdapter {
	if svc == nil {
		return nil
	}
	return &usageAuthorityProviderAdapter{svc: svc}
}

func (a *usageAuthorityProviderAdapter) AdmitRequest(ctx context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	if a == nil || a.svc == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	key := usagelimit.ReservationKey{
		LogicalRequestID: strings.TrimSpace(in.RequestID),
		ALegID:           strings.TrimSpace(in.ALegID),
		Sequence:         1,
	}
	input := usagelimit.AdmissionInput{
		Correlation: controlplane.Correlation{
			RequestID: strings.TrimSpace(in.RequestID),
			TraceID:   strings.TrimSpace(in.TraceID),
			ALegID:    strings.TrimSpace(in.ALegID),
		},
		Scope:          in.Scope,
		Dimensions:     scopeToDimensions(in.Scope),
		Request:        exposureInputTokens(in.Exposure),
		RequestCount:   usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 1},
		Authority:      usagelimit.AuthorityLevelEstimated,
		ReservationKey: key,
		LifecycleScope: metering.LifecycleLogicalRequest,
		Perspective:    in.Perspective,
		Exposure:       in.Exposure,
	}
	res, err := a.svc.Admit(ctx, input)
	if err != nil {
		return mapAdmissionDecisionOnError(res, usageAuthorityRequestProviderID, authority.StageRequestAdmit), err
	}
	return mapAdmissionDecision(res, usageAuthorityRequestProviderID, authority.StageRequestAdmit), nil
}

func (a *usageAuthorityProviderAdapter) SettleRequest(ctx context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	if a == nil || a.svc == nil {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	for _, h := range in.Handles {
		settleIn := usagelimit.SettleInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: usagelimit.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				Sequence:         1,
			},
			Correlation: controlplane.Correlation{RequestID: strings.TrimSpace(in.RequestID)},
			Kind:        usagelimit.SettlementKindFinal,
			Stage:       string(authority.StageRequestSettle),
			Facts:       in.Facts,
		}
		if len(in.BoundVersions) > 0 {
			settleIn.BoundVersion = in.BoundVersions[0]
		}
		_, err := a.svc.Settle(ctx, settleIn)
		if err != nil {
			return authority.Settlement{}, err
		}
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (a *usageAuthorityProviderAdapter) ReleaseRequest(ctx context.Context, in authority.RequestRelease) error {
	if a == nil || a.svc == nil {
		return nil
	}
	handles := in.Handles
	if len(handles) == 0 && strings.TrimSpace(in.CompensationHandle) != "" {
		handles = []string{in.CompensationHandle}
	}
	for _, h := range handles {
		_, err := a.svc.Release(ctx, usagelimit.ReleaseInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: usagelimit.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				Sequence:         1,
			},
			Correlation: controlplane.Correlation{RequestID: strings.TrimSpace(in.RequestID)},
			Kind:        usagelimit.ReleaseKindAdmissionFailure,
			Stage:       string(authority.StageRequestRelease),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func attemptAdmissionInput(in authority.AttemptAdmission, estimateOnly bool) usagelimit.AdmissionInput {
	key := usagelimit.ReservationKey{
		LogicalRequestID: strings.TrimSpace(in.RequestID),
		ALegID:           strings.TrimSpace(in.ALegID),
		BLegID:           strings.TrimSpace(in.BLegID),
		AttemptID:        strings.TrimSpace(in.AttemptID),
		Sequence:         1,
	}
	input := usagelimit.AdmissionInput{
		Correlation: controlplane.Correlation{
			RequestID: strings.TrimSpace(in.RequestID),
			ALegID:    strings.TrimSpace(in.ALegID),
			BLegID:    strings.TrimSpace(in.BLegID),
		},
		Scope:          in.Scope,
		Dimensions:     scopeToDimensions(in.Scope),
		Request:        exposureInputTokens(in.Exposure),
		RequestCount:   usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 0}, // request-count reserved at request stage
		Authority:      usagelimit.AuthorityLevelEstimated,
		ReservationKey: key,
		LifecycleScope: metering.LifecycleBackendAttempt,
		Perspective:    in.Perspective,
		Exposure:       in.Exposure,
		EstimateOnly:   estimateOnly,
	}
	if be := strings.TrimSpace(in.BackendID); be != "" {
		input.Dimensions.Backend = scope.Known(be)
	}
	if m := strings.TrimSpace(in.Model); m != "" {
		input.Dimensions.Model = scope.Known(m)
	}
	return input
}

// builtinAttemptAdmissionKey carries the runtime's full usage-authority attempt
// input to the built-in slot, so the coordinator path does not re-derive a
// thinner one from the public AttemptAdmission.
type builtinAttemptAdmissionKey struct{}

func withBuiltinAttemptAdmission(ctx context.Context, in usagelimit.AdmissionInput) context.Context {
	return context.WithValue(ctx, builtinAttemptAdmissionKey{}, in)
}

// builtinAttemptAdmissionInput returns the runtime-bound input for this B-leg,
// or the mapping from the public admission when none is bound.
func builtinAttemptAdmissionInput(ctx context.Context, in authority.AttemptAdmission, estimateOnly bool) usagelimit.AdmissionInput {
	if bound, ok := ctx.Value(builtinAttemptAdmissionKey{}).(usagelimit.AdmissionInput); ok &&
		bound.ReservationKey.BLegID == strings.TrimSpace(in.BLegID) {
		bound.EstimateOnly = estimateOnly
		return bound
	}
	return attemptAdmissionInput(in, estimateOnly)
}

func (a *usageAuthorityProviderAdapter) AdmitAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if a == nil || a.svc == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	res, err := a.svc.Admit(ctx, builtinAttemptAdmissionInput(ctx, in, false))
	if err != nil {
		return mapAdmissionDecisionOnError(res, usageAuthorityAttemptProviderID, authority.StageAttemptAdmit), err
	}
	return mapAdmissionDecision(res, usageAuthorityAttemptProviderID, authority.StageAttemptAdmit), nil
}

func (a *usageAuthorityProviderAdapter) PreviewAttempt(ctx context.Context, in authority.AttemptAdmission) (authority.Decision, error) {
	if a == nil || a.svc == nil {
		return authority.Decision{Kind: authority.DecisionAllow}, nil
	}
	// Clamp preview is side-effect free: EstimateOnly skips holds; SkipEvidence
	// skips durable policy/accounting projection (bounded loop may call this
	// up to four times per backend open).
	admitIn := builtinAttemptAdmissionInput(ctx, in, true)
	admitIn.SkipEvidence = true
	res, err := a.svc.Admit(ctx, admitIn)
	if err != nil {
		d := mapAdmissionDecisionOnError(res, usageAuthorityAttemptProviderID, authority.StageAttemptAdmit)
		d.Reservations = nil
		d.CompensationHandle = ""
		return d, err
	}
	d := mapAdmissionDecision(res, usageAuthorityAttemptProviderID, authority.StageAttemptAdmit)
	d.Reservations = nil
	d.CompensationHandle = ""
	return d, nil
}

func mapAdmissionDecisionOnError(res usagelimit.AdmissionResult, providerID string, stage authority.Stage) authority.Decision {
	if !admissionResultPresent(res) {
		return authority.Decision{}
	}
	return mapAdmissionDecision(res, providerID, stage)
}

func admissionResultPresent(res usagelimit.AdmissionResult) bool {
	return res.Allowed || res.Outcome != "" || len(res.RuleIDs) > 0 || len(res.AdvisoryRuleIDs) > 0 ||
		len(res.UnreservedRuleIDs) > 0 || strings.TrimSpace(res.SelectedRuleID) != "" || res.RuleKind != "" ||
		strings.TrimSpace(res.ReservationID) != "" || res.Reserved || res.ReservedAmount.Unit != "" ||
		len(res.Reservations) > 0 || res.PolicyRecord.ReasonCode != "" || res.PolicyRecord.ClientCategory != "" ||
		res.BoundVersion.ID != "" || res.BoundVersion.Version != "" || res.BoundVersion.PolicyID != "" ||
		res.BoundRatingVersion.ID != "" || res.BoundRatingVersion.Version != ""
}

func (a *usageAuthorityProviderAdapter) SettleAttempt(ctx context.Context, in authority.AttemptSettlement) (authority.Settlement, error) {
	if a == nil || a.svc == nil {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	settleIn := attemptSettleInput(in)
	if len(settleIn.Reservations) == 0 && strings.TrimSpace(settleIn.ReservationID) == "" {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	result, err := a.svc.Settle(ctx, usagelimit.DeriveSettleScalars(settleIn))
	if result.Applied {
		return authority.OwnedFinalSettlement(in.Handles), nil
	}
	if err != nil {
		return authority.Settlement{}, err
	}
	return authority.Settlement{}, nil
}

func attemptSettleInput(in authority.AttemptSettlement) usagelimit.SettleInput {
	kind := usagelimit.SettlementKind(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = settlementKindFromOutcome(in.Outcome, in.OutputCommitted, in.ClientCanceled)
	}
	dims := scopeToDimensions(in.Scope)
	if be := strings.TrimSpace(in.BackendID); be != "" {
		dims.Backend = scope.Known(be)
	}
	if m := strings.TrimSpace(in.Model); m != "" {
		dims.Model = scope.Known(m)
	}
	key := usagelimit.ReservationKey{
		LogicalRequestID: strings.TrimSpace(in.RequestID),
		ALegID:           strings.TrimSpace(in.ALegID),
		BLegID:           strings.TrimSpace(in.BLegID),
		AttemptID:        strings.TrimSpace(in.AttemptID),
		Sequence:         1,
	}
	settleIn := usagelimit.SettleInput{
		Correlation: controlplane.Correlation{
			RequestID: strings.TrimSpace(in.RequestID),
			ALegID:    strings.TrimSpace(in.ALegID),
			BLegID:    strings.TrimSpace(in.BLegID),
		},
		Scope:            in.Scope,
		Kind:             kind,
		Stage:            feature.StageIDAttemptLifecycle,
		BackendAttempted: in.BackendAttempted,
		OutputCommitted:  in.OutputCommitted,
		ClientCanceled:   in.ClientCanceled,
		Facts:            in.Facts,
		ReservationKey:   key,
	}
	if len(in.Facts) > 0 {
		settleIn.FinalUsagePresent = true
		settleIn.Authority = authorityFromFacts(in.Facts)
	}
	if len(in.BoundVersions) > 0 {
		settleIn.BoundVersion = in.BoundVersions[0]
	}
	if len(in.Reservations) > 0 {
		settleIn.Reservations = make([]usagelimit.SettlementDescriptor, 0, len(in.Reservations))
		for _, r := range in.Reservations {
			reserved := authorityAmountFromReservation(r)
			resKey := key
			resKey.RuleID = strings.TrimSpace(r.RuleID)
			finalUsage := finalUsageFromFacts(in.Facts, reserved)
			settleIn.Reservations = append(settleIn.Reservations, usagelimit.SettlementDescriptor{
				Reservation: usagelimit.ReservationDescriptor{
					RuleID:         strings.TrimSpace(r.RuleID),
					Unit:           reserved.Unit,
					Dimensions:     dims,
					ReservationKey: resKey,
					ReservationID:  strings.TrimSpace(r.Handle),
					Amount:         reserved,
				},
				FinalUsage:           finalUsage,
				EstimatedUsage:       reserved,
				Authority:            authorityFromFacts(in.Facts),
				MeasurementAuthority: usagelimit.MeasurementAuthority{Usage: authorityFromFacts(in.Facts)},
			})
		}
		return settleIn
	}
	for _, h := range in.Handles {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if settleIn.ReservationID == "" {
			settleIn.ReservationID = h
		}
		settleIn.Reservations = append(settleIn.Reservations, usagelimit.SettlementDescriptor{
			Reservation: usagelimit.ReservationDescriptor{
				ReservationID:  h,
				ReservationKey: settleIn.ReservationKey,
				Dimensions:     dims,
			},
			FinalUsage: finalUsageFromFacts(in.Facts, usagelimit.Amount{}),
			Authority:  authorityFromFacts(in.Facts),
		})
	}
	return settleIn
}

func settlementKindFromOutcome(outcome metering.AttemptOutcome, outputCommitted, clientCanceled bool) usagelimit.SettlementKind {
	switch outcome {
	case metering.AttemptOutcomeCanceled:
		return usagelimit.SettlementKindCancellation
	case metering.AttemptOutcomeLoser:
		return usagelimit.SettlementKindLosing
	case metering.AttemptOutcomeFailed:
		return usagelimit.SettlementKindUnavailable
	case metering.AttemptOutcomeWinner:
		return usagelimit.SettlementKindFinal
	default:
		if clientCanceled {
			return usagelimit.SettlementKindCancellation
		}
		if outputCommitted {
			return usagelimit.SettlementKindFinal
		}
		return usagelimit.SettlementKindFinal
	}
}

func finalUsageFromFacts(facts []metering.Fact, reserved usagelimit.Amount) usagelimit.Amount {
	unit := reserved.Unit
	for _, fact := range facts {
		for _, q := range fact.Quantities {
			if !q.Present {
				continue
			}
			got := amountFromMeteringQuantity(q)
			if unit == "" || got.Unit == unit {
				return got
			}
		}
	}
	if reserved.Unit != "" {
		return usagelimit.Amount{Unit: reserved.Unit}
	}
	return usagelimit.Amount{}
}

func authorityFromFacts(facts []metering.Fact) usagelimit.AuthorityLevel {
	for _, fact := range facts {
		switch fact.Authority {
		case metering.AuthorityAuthoritative:
			return usagelimit.AuthorityLevelAuthoritative
		case metering.AuthorityUnavailable:
			return usagelimit.AuthorityLevelUnavailable
		}
	}
	return usagelimit.AuthorityLevelEstimated
}

func amountFromMeteringQuantity(q metering.Quantity) usagelimit.Amount {
	switch q.Component {
	case metering.ComponentRequest:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: q.Value}
	case metering.ComponentInputToken:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: q.Value}
	case metering.ComponentOutputToken:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitOutputTokens, Value: q.Value}
	case metering.ComponentCacheReadInputToken:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitCacheReadTokens, Value: q.Value}
	case metering.ComponentCacheWriteInputToken:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitCacheWriteTokens, Value: q.Value}
	case metering.ComponentReasoningOutputToken:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitReasoningTokens, Value: q.Value}
	case metering.ComponentTotalToken:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitTotalTokens, Value: q.Value}
	default:
		return usagelimit.Amount{Value: q.Value}
	}
}

func (a *usageAuthorityProviderAdapter) ReleaseAttempt(ctx context.Context, in authority.AttemptRelease) error {
	if a == nil || a.svc == nil {
		return nil
	}
	handles := in.Handles
	if len(handles) == 0 && strings.TrimSpace(in.CompensationHandle) != "" {
		handles = []string{in.CompensationHandle}
	}
	for _, h := range handles {
		_, err := a.svc.Release(ctx, usagelimit.ReleaseInput{
			ReservationID: strings.TrimSpace(h),
			ReservationKey: usagelimit.ReservationKey{
				LogicalRequestID: strings.TrimSpace(in.RequestID),
				BLegID:           strings.TrimSpace(in.BLegID),
				AttemptID:        strings.TrimSpace(in.AttemptID),
				Sequence:         1,
			},
			Correlation: controlplane.Correlation{RequestID: in.RequestID, BLegID: in.BLegID},
			Kind:        usagelimit.ReleaseKindAdmissionFailure,
			Stage:       string(authority.StageAttemptRelease),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func mapAdmissionDecision(res usagelimit.AdmissionResult, providerID string, stage authority.Stage) authority.Decision {
	d := authority.Decision{ProviderID: providerID, Stage: stage}
	if !res.Allowed {
		d.Kind = authority.DecisionDeny
		if res.BoundVersion.Version != "" {
			d.BoundVersions = []economics.PolicySnapshotRef{res.BoundVersion}
		}
		return d
	}
	d.Kind = authority.DecisionAllow
	if res.BoundVersion.Version != "" {
		d.BoundVersions = []economics.PolicySnapshotRef{res.BoundVersion}
	}
	reservations := append([]usagelimit.AdmissionReservation(nil), res.Reservations...)
	if len(reservations) == 0 && res.Reserved && strings.TrimSpace(res.ReservationID) != "" {
		reservations = append(reservations, usagelimit.AdmissionReservation{
			ReservationID:  res.ReservationID,
			RuleID:         res.SelectedRuleID,
			ReservedAmount: res.ReservedAmount,
		})
	}
	seen := make(map[string]struct{}, len(reservations))
	for _, r := range reservations {
		if strings.TrimSpace(r.ReservationID) == "" {
			continue
		}
		if _, ok := seen[r.ReservationID]; ok {
			continue
		}
		seen[r.ReservationID] = struct{}{}
		d.Reservations = append(d.Reservations, mapAdmissionReservation(r))
		if d.CompensationHandle == "" {
			d.CompensationHandle = r.ReservationID
		}
	}
	// Monetary admission clamps are retired from the runtime authority adapter;
	// Cheap credit screen + exposure admission own the customer max-charge boundary.
	return d
}

func mapAdmissionReservation(in usagelimit.AdmissionReservation) authority.Reservation {
	out := authority.Reservation{
		Handle: strings.TrimSpace(in.ReservationID),
		Kind:   authority.ReservationQuota,
		RuleID: strings.TrimSpace(in.RuleID),
	}
	amount := in.ReservedAmount
	component, unit := meteringComponentForAuthorityAmount(amount.Unit)
	if component != "" {
		out.Quantity = &metering.Quantity{Component: component, Unit: unit, Value: amount.Value, Present: true}
	}
	return out
}

func meteringComponentForAuthorityAmount(unit usagelimit.AmountUnit) (string, string) {
	switch unit {
	case usagelimit.AmountUnitRequests:
		return metering.ComponentRequest, metering.UnitCount
	case usagelimit.AmountUnitInputTokens:
		return metering.ComponentInputToken, metering.UnitToken
	case usagelimit.AmountUnitOutputTokens:
		return metering.ComponentOutputToken, metering.UnitToken
	case usagelimit.AmountUnitCacheReadTokens:
		return metering.ComponentCacheReadInputToken, metering.UnitToken
	case usagelimit.AmountUnitCacheWriteTokens:
		return metering.ComponentCacheWriteInputToken, metering.UnitToken
	case usagelimit.AmountUnitReasoningTokens:
		return metering.ComponentReasoningOutputToken, metering.UnitToken
	case usagelimit.AmountUnitTotalTokens:
		return metering.ComponentTotalToken, metering.UnitToken
	default:
		return "", ""
	}
}

func scopeToDimensions(sc scope.PrincipalScopeView) usagelimit.Dimensions {
	dims := usagelimit.Dimensions{
		Principal:    sc.PrincipalID,
		Credential:   sc.CredentialID,
		Tenant:       sc.TenantID,
		Organization: sc.OrganizationID,
		Workspace:    sc.WorkspaceID,
		Project:      sc.ProjectID,
		Department:   sc.DepartmentID,
		CostCenter:   sc.CostCenterID,
	}
	for k, v := range sc.PolicyLabels {
		if !authorityattribution.IsSafeLabelKey(k) {
			continue
		}
		if dims.PolicyLabels == nil {
			dims.PolicyLabels = make(map[string]scope.Value, len(sc.PolicyLabels))
		}
		dims.PolicyLabels[k] = scope.Known(v)
	}
	return dims
}

func exposureInputTokens(exp economics.ExposureBasis) usagelimit.Amount {
	for _, q := range exp.Quantities {
		if q.Component == metering.ComponentInputToken && q.Present {
			return usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: q.Value}
		}
	}
	return usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: 0}
}

// BuildAuthorityCoordinators wires thin adapters into request/attempt coordinators
// when a usage-authority service and/or concurrency provider is present.
func BuildAuthorityCoordinators(svc UsageAuthorityService, concurrency authority.ConcurrencyProvider) (*authoritycoord.RequestCoordinator, *authoritycoord.AttemptCoordinator) {
	adapter := newUsageAuthorityProviderAdapter(svc)
	if adapter == nil && concurrency == nil {
		return nil, nil
	}
	req := &authoritycoord.RequestCoordinator{
		Concurrency: concurrency,
	}
	if adapter != nil {
		req.Slots = []authoritycoord.RequestSlot{{
			ID:       usageAuthorityRequestProviderID,
			Class:    authoritycoord.PriorityQuotaBudgetRate,
			Provider: adapter,
			Strength: authority.StrengthRequired,
		}}
	}
	var att *authoritycoord.AttemptCoordinator
	if adapter != nil {
		att = &authoritycoord.AttemptCoordinator{
			Slots: []authoritycoord.AttemptSlot{{
				ID:       usageAuthorityAttemptProviderID,
				Class:    authoritycoord.AttemptPriorityHardSpend,
				Provider: adapter,
				Strength: authority.StrengthRequired,
			}},
		}
	}
	return req, att
}
