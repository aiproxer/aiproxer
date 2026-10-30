package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authorityattribution"
	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	compatibleadmission "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/compatible"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type attemptAuthorityState struct {
	admissionInput  usagelimit.AdmissionInput
	admissionResult usagelimit.AdmissionResult
	cleanupTimeout  time.Duration
	// viaCoordinator is set when admit ran through AttemptCoordinator so settle/
	// release must preserve stack provider identity (not flatten to UA-only handles).
	viaCoordinator bool
	stack          authoritycoord.CompensationStack
	boundVersions  []economics.PolicySnapshotRef
	admitClamps    []authority.Clamp
	requestID      string
	attemptID      string
	bLegID         string
	// settledProviders tracks AttemptCoordinator / built-in UA providers that
	// already completed Settle successfully so retries skip them and Release
	// never runs after a successful Settle (requirement 15.5).
	settledProviders map[string]struct{}
}

func (e *Executor) admitAttemptAuthority(
	ctx context.Context,
	traceID string,
	aLegID string,
	bleg b2bua.BLegRecord,
	call aipapi.Call,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
	estimateOnly bool,
) (attemptAuthorityState, error) {
	// External AttemptProviders must run even when built-in UsageAuthority is disabled.
	if !estimateOnly && e != nil {
		bound := requestAuthorityFrom(ctx)
		if e.AttemptCoordinator != nil || (bound != nil && bound.ExecutableGen != nil && bound.ExecutableGen.AttemptCoord != nil) {
			return e.admitAttemptViaCoordinator(ctx, traceID, aLegID, bleg, call, c, decision)
		}
	}
	svc := e.UsageAuthority
	if svc == nil {
		return attemptAuthorityState{}, nil
	}
	admissionInput := attemptAuthorityAdmissionInput(ctx, traceID, aLegID, bleg, call, c, decision, estimateOnly)
	result, err := svc.Admit(ctx, admissionInput)
	if err != nil {
		// Cancellation must not be converted into an unrelated accounting denial
		// (requirement 10.4): propagate the canceled error verbatim so the
		// runtime can distinguish it from enforcement outcomes.
		if errors.Is(err, context.Canceled) {
			return attemptAuthorityState{}, err
		}
		outcome := usagelimit.DecisionOutcomeUnavailable
		if errors.Is(err, usagelimit.ErrReservationConflict) {
			outcome = usagelimit.DecisionOutcomeDeny
		}
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(usagelimit.AdmissionResult{Outcome: outcome}, err)
	}
	if result.ReservedAmount.Unit != "" {
		admissionInput.Request = result.ReservedAmount
	}
	var boundGen *snapshotgen.ExecutableGeneration
	if st := requestAuthorityFrom(ctx); st != nil {
		boundGen = st.ExecutableGen
	}
	e.applyGenerationBoundVersionFrom(boundGen, &result)
	state := attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: result,
		cleanupTimeout:  e.UsageAuthorityCleanupTimeout,
	}
	if authErr := attemptAuthorityAdmissionError(result, nil); authErr != nil {
		return state, authErr
	}
	return state, nil
}

// attemptAuthorityAdmissionInput is the single usage-authority attempt input:
// attribution dimensions (including route and policy labels), correlation,
// preflight usage, exposure, and the reservation key. The estimate-only
// precheck, the direct admission, and the coordinator's built-in usage-authority
// slot all evaluate rules against this same input.
func attemptAuthorityAdmissionInput(
	ctx context.Context,
	traceID string,
	aLegID string,
	bleg b2bua.BLegRecord,
	call aipapi.Call,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
	estimateOnly bool,
) usagelimit.AdmissionInput {
	quantities := attemptRatingQuantities(decision)
	factRefs := []metering.FactRef(nil)
	if !estimateOnly {
		quantities = finalOperatorAttemptQuantities(ctx, bleg.BLegID, decision)
		factRefs = backendIngressFactRefs(ctx, bleg.BLegID)
	}
	admissionInput := usagelimit.AdmissionInput{
		Correlation:    attemptAuthorityCorrelation(traceID, call.ID, aLegID, call, bleg, c),
		Scope:          trustedFrontendIngressScope(ctx, scopeFromCtx(ctx)),
		Dimensions:     attemptAuthorityDimensions(ctx, call, c),
		Request:        attemptAuthorityRequestAmount(decision),
		RequestCount:   usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 1},
		PreflightUsage: attemptAuthorityPreflightUsage(decision),
		Authority:      usagelimit.AuthorityLevelEstimated,
		ReservationKey: attemptAuthorityReservationKey(call.ID, traceID, aLegID, bleg, c),
		EstimateOnly:   estimateOnly,
	}
	if len(quantities) > 0 || len(factRefs) > 0 {
		admissionInput.Exposure = attemptAuthorityExposure(quantities, factRefs)
	}
	// When request coordinator already reserved request-count, avoid double-charging
	// customer request quotas on each B-leg (requirements 4.5, 8.3).
	if requestAuthorityFrom(ctx) != nil {
		admissionInput.RequestCount = usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 0}
	}
	if !estimateOnly {
		admissionInput.LifecycleScope = metering.LifecycleBackendAttempt
	}
	return admissionInput
}

// backendIngressFactRefs references the frozen backend-ingress fact for bleg,
// when the metering holder captured one.
func backendIngressFactRefs(ctx context.Context, blegID string) []metering.FactRef {
	holder := meteringHolderFrom(ctx)
	if holder == nil {
		return nil
	}
	id := strings.TrimSpace(holder.BackendIngressFactID(blegID))
	if id == "" {
		return nil
	}
	streamID := "operator-attempt:" + strings.TrimSpace(blegID)
	if be := holder.BackendIngressFor(blegID); be != nil && strings.TrimSpace(be.Public.StreamID) != "" {
		streamID = be.Public.StreamID
	}
	return []metering.FactRef{{StreamID: streamID, FactID: id}}
}

func attemptAuthorityExposure(quantities []metering.Quantity, factRefs []metering.FactRef) economics.ExposureBasis {
	return economics.ExposureBasis{
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendIngress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Quantities:  quantities,
		FactRefs:    append([]metering.FactRef(nil), factRefs...),
	}
}

func (e *Executor) admitAttemptViaCoordinator(
	ctx context.Context,
	traceID string,
	aLegID string,
	bleg b2bua.BLegRecord,
	call aipapi.Call,
	c routing.AttemptCandidate,
	decision accountingpreflight.Decision,
) (attemptAuthorityState, error) {
	// The built-in usage-authority slot reserves against the same input the
	// precheck evaluated. The request stage owns request-count reservation.
	builtin := attemptAuthorityAdmissionInput(ctx, traceID, aLegID, bleg, call, c, decision, false)
	builtin.Request = exposureInputTokens(builtin.Exposure)
	builtin.RequestCount = usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 0}
	builtin.Perspective = metering.PerspectiveOperator
	boundGen := (*snapshotgen.ExecutableGeneration)(nil)
	if st := requestAuthorityFrom(ctx); st != nil {
		boundGen = st.ExecutableGen
	}
	in := authority.AttemptAdmission{
		RequestID:      strings.TrimSpace(call.ID),
		AttemptID:      strings.TrimSpace(bleg.BLegID),
		BLegID:         strings.TrimSpace(bleg.BLegID),
		ALegID:         strings.TrimSpace(aLegID),
		BackendID:      strings.TrimSpace(c.Primary.Backend),
		Model:          strings.TrimSpace(c.Primary.Model),
		Perspective:    metering.PerspectiveOperator,
		Lifecycle:      metering.LifecycleBackendAttempt,
		Scope:          builtin.Scope,
		IdempotencyKey: builtin.ReservationKey.String(),
		Exposure:       builtin.Exposure,
	}
	attemptCoord := e.AttemptCoordinator
	if boundGen != nil && boundGen.AttemptCoord != nil {
		attemptCoord = boundGen.AttemptCoord
	}
	if attemptCoord == nil {
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(
			usagelimit.AdmissionResult{Outcome: usagelimit.DecisionOutcomeUnavailable},
			errors.New("attempt coordinator unavailable"),
		)
	}
	d, err := attemptCoord.Admit(withBuiltinAttemptAdmission(ctx, builtin), in)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return attemptAuthorityState{}, err
		}
		if mapped := mapAttemptAuthorityCoordinatorError(err); mapped != nil {
			return attemptAuthorityState{}, mapped
		}
		outcome := usagelimit.DecisionOutcomeDeny
		if !authoritycoord.IsDenied(err) {
			outcome = usagelimit.DecisionOutcomeUnavailable
		}
		return attemptAuthorityState{}, attemptAuthorityAdmissionError(usagelimit.AdmissionResult{Outcome: outcome}, err)
	}
	entries := d.Stack.Entries()
	handles := d.Stack.Handles()
	res := usagelimit.AdmissionResult{Allowed: true, Outcome: usagelimit.DecisionOutcomeAllow}
	if len(handles) > 0 {
		res.Reserved = true
		res.ReservationID = handles[0]
		for _, entry := range entries {
			if strings.TrimSpace(entry.ProviderID) != usageAuthorityAttemptProviderID {
				continue
			}
			reservation := usagelimit.AdmissionReservation{
				ReservationID:  strings.TrimSpace(entry.Handle),
				RuleID:         strings.TrimSpace(entry.Reservation.RuleID),
				ReservedAmount: authorityAmountFromReservation(entry.Reservation),
			}
			res.Reservations = append(res.Reservations, reservation)
			if len(res.Reservations) == 1 {
				res.ReservationID = reservation.ReservationID
				res.ReservedAmount = reservation.ReservedAmount
				res.SelectedRuleID = reservation.RuleID
			}
			if reservation.RuleID != "" {
				res.RuleIDs = append(res.RuleIDs, reservation.RuleID)
			}
		}
	}
	if len(d.BoundVersions) > 0 {
		res.BoundVersion = d.BoundVersions[0]
	}
	e.applyGenerationBoundVersionFrom(boundGen, &res)
	admissionInput := builtin
	admissionInput.Scope = scopeFromCtx(ctx)
	admissionInput.Request = attemptAuthorityRequestAmount(decision)
	return attemptAuthorityState{
		admissionInput:  admissionInput,
		admissionResult: res,
		cleanupTimeout:  e.UsageAuthorityCleanupTimeout,
		viaCoordinator:  true,
		stack:           d.Stack,
		boundVersions:   append([]economics.PolicySnapshotRef(nil), d.BoundVersions...),
		admitClamps:     append([]authority.Clamp(nil), d.Clamps...),
		requestID:       in.RequestID,
		attemptID:       in.AttemptID,
		bLegID:          in.BLegID,
	}, nil
}

func authorityAmountFromReservation(in authority.Reservation) usagelimit.Amount {
	if in.Quantity == nil || !in.Quantity.Present {
		return usagelimit.Amount{}
	}
	var unit usagelimit.AmountUnit
	switch in.Quantity.Component {
	case metering.ComponentRequest:
		unit = usagelimit.AmountUnitRequests
	case metering.ComponentInputToken:
		unit = usagelimit.AmountUnitInputTokens
	case metering.ComponentOutputToken:
		unit = usagelimit.AmountUnitOutputTokens
	case metering.ComponentCacheReadInputToken:
		unit = usagelimit.AmountUnitCacheReadTokens
	case metering.ComponentCacheWriteInputToken:
		unit = usagelimit.AmountUnitCacheWriteTokens
	case metering.ComponentReasoningOutputToken:
		unit = usagelimit.AmountUnitReasoningTokens
	case metering.ComponentTotalToken:
		unit = usagelimit.AmountUnitTotalTokens
	default:
		return usagelimit.Amount{}
	}
	return usagelimit.Amount{Unit: unit, Value: in.Quantity.Value}
}

func attemptAuthorityCorrelation(traceID, requestID, aLegID string, call aipapi.Call, bleg b2bua.BLegRecord, c routing.AttemptCandidate) controlplane.Correlation {
	reqID := strings.TrimSpace(requestID)
	if reqID == "" {
		reqID = strings.TrimSpace(traceID)
	}
	return controlplane.Correlation{
		TraceID:    strings.TrimSpace(traceID),
		RequestID:  reqID,
		SessionID:  strings.TrimSpace(call.Session.CorrelationID()),
		ALegID:     strings.TrimSpace(aLegID),
		BLegID:     strings.TrimSpace(bleg.BLegID),
		AttemptSeq: bleg.Seq,
		BackendID:  strings.TrimSpace(c.Primary.Backend),
		Model:      strings.TrimSpace(c.Primary.Model),
	}
}

func attemptAuthorityDimensions(ctx context.Context, call aipapi.Call, c routing.AttemptCandidate) usagelimit.Dimensions {
	sc := scopeFromCtx(ctx)
	dims := usagelimit.Dimensions{
		Principal:    sc.PrincipalID,
		Credential:   sc.CredentialID,
		Tenant:       sc.TenantID,
		Organization: sc.OrganizationID,
		Workspace:    sc.WorkspaceID,
		Project:      sc.ProjectID,
		Department:   sc.DepartmentID,
		CostCenter:   sc.CostCenterID,
		Backend:      scope.Known(strings.TrimSpace(c.Primary.Backend)),
		Model:        scope.Known(strings.TrimSpace(c.Primary.Model)),
		Route:        scope.Known(strings.TrimSpace(call.Route.Selector)),
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

func attemptAuthorityRequestAmount(decision accountingpreflight.Decision) usagelimit.Amount {
	return usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: int64(decision.Count.InputTokens)}
}

func attemptAuthorityPreflightUsage(decision accountingpreflight.Decision) usagelimit.PreflightUsage {
	count := decision.Count
	output := max(int64(count.OutputTokens), 0)
	return usagelimit.PreflightUsage{
		InputTokens:        int64(count.InputTokens),
		OutputTokens:       output,
		CacheReadTokens:    int64(count.CacheReadTokens),
		CacheWriteTokens:   int64(count.CacheWriteTokens),
		ReasoningTokens:    int64(count.ReasoningTokens),
		TotalTokens:        int64(count.TotalTokens),
		TotalTokensPresent: count.TotalTokensPresent,
	}
}

// backendCanEnforceAuthorityClamp reports whether the selected backend can
// represent a MaxOutputTokens authority clamp on the wire.
func backendCanEnforceAuthorityClamp(be execbackend.Backend, call *aipapi.Call) bool {
	return be.CanEnforceAuthorityMaxOutputTokens(call)
}

func attemptAuthorityUsageAmount(ev aipapi.Event, estimate usagelimit.Amount) usagelimit.Amount {
	var amount usagelimit.Amount
	switch estimate.Unit {
	case usagelimit.AmountUnitRequests:
		return usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 1}
	case usagelimit.AmountUnitInputTokens:
		amount = usagelimit.Amount{Unit: usagelimit.AmountUnitInputTokens, Value: int64(ev.InputTokens)}
	case usagelimit.AmountUnitOutputTokens:
		amount = usagelimit.Amount{Unit: usagelimit.AmountUnitOutputTokens, Value: int64(ev.OutputTokens)}
	case usagelimit.AmountUnitCacheReadTokens:
		amount = usagelimit.Amount{Unit: usagelimit.AmountUnitCacheReadTokens, Value: int64(ev.CacheReadTokens)}
	case usagelimit.AmountUnitCacheWriteTokens:
		amount = usagelimit.Amount{Unit: usagelimit.AmountUnitCacheWriteTokens, Value: int64(ev.CacheWriteTokens)}
	case usagelimit.AmountUnitReasoningTokens:
		amount = usagelimit.Amount{Unit: usagelimit.AmountUnitReasoningTokens, Value: int64(ev.ReasoningTokens)}
	case usagelimit.AmountUnitTotalTokens:
		value := int64(ev.TotalTokens)
		if value == 0 && !attemptAuthorityEventHasUsageForUnit(ev, usagelimit.AmountUnitTotalTokens) {
			// Default inclusion schema: cache ⊂ input, reasoning ⊂ output.
			value = int64(ev.InputTokens + ev.OutputTokens)
		}
		amount = usagelimit.Amount{Unit: usagelimit.AmountUnitTotalTokens, Value: value}
	default:
		value := int64(ev.TotalTokens)
		if value == 0 && !attemptAuthorityEventHasUsageForUnit(ev, usagelimit.AmountUnitTotalTokens) {
			value = int64(ev.InputTokens + ev.OutputTokens)
		}
		if estimate.Unit == "" {
			amount = usagelimit.Amount{Unit: usagelimit.AmountUnitTotalTokens, Value: value}
		} else {
			amount = usagelimit.Amount{Unit: estimate.Unit, Value: value}
		}
	}
	// A zero amount is only reconciled to the preflight estimate when usage is
	// genuinely absent (no scoped usage delta was reported) or when usage was
	// reported for other units but not this one (partial reporting). A present
	// usage delta whose scoped readings are all zero is a legitimate zero-usage
	// or zero-cost completion and must settle at zero, not the reserved estimate.
	if amount.Value == 0 && !attemptAuthorityEventHasUsageForUnit(ev, estimate.Unit) {
		if len(ev.UsageScopes) == 0 || attemptAuthorityEventHasAnyUsage(ev) {
			return usagelimit.Amount{Unit: estimate.Unit, Value: estimate.Value}
		}
	}
	return amount
}

func attemptAuthorityEventHasUsageForUnit(ev aipapi.Event, unit usagelimit.AmountUnit) bool {
	if present, known := explicitUsagePresenceForUnit(ev, unit); known {
		return present
	}
	// Preserve legacy all-zero authoritative events when presence is unmarked.
	if ev.Kind == aipapi.EventUsageDelta && !attemptAuthorityEventHasAnyUsage(ev) {
		if authoritativeProviderAccounting(ev.Accounting) {
			return true
		}
		for _, usageScope := range ev.UsageScopes {
			if authoritativeProviderAccounting(usageScope.Accounting) {
				return true
			}
		}
	}
	if unit == usagelimit.AmountUnitRequests {
		return true
	}
	if usageCounterValue(unit, int64(ev.InputTokens), int64(ev.OutputTokens), int64(ev.CacheReadTokens), int64(ev.CacheWriteTokens), int64(ev.ReasoningTokens), int64(ev.TotalTokens)) > 0 {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if usageCounterValue(unit, int64(scope.InputTokens), int64(scope.OutputTokens), int64(scope.CacheReadTokens), int64(scope.CacheWriteTokens), int64(scope.ReasoningTokens), int64(scope.TotalTokens)) > 0 {
			return true
		}
	}
	return false
}

func usageCounterValue(unit usagelimit.AmountUnit, input, output, cacheRead, cacheWrite, reasoning, total int64) int64 {
	switch unit {
	case usagelimit.AmountUnitInputTokens:
		return input
	case usagelimit.AmountUnitOutputTokens:
		return output
	case usagelimit.AmountUnitCacheReadTokens:
		return cacheRead
	case usagelimit.AmountUnitCacheWriteTokens:
		return cacheWrite
	case usagelimit.AmountUnitReasoningTokens:
		return reasoning
	case usagelimit.AmountUnitTotalTokens:
		return total
	default:
		return total
	}
}

func explicitUsagePresenceForUnit(ev aipapi.Event, unit usagelimit.AmountUnit) (bool, bool) {
	if ev.Kind != aipapi.EventUsageDelta {
		return false, false
	}
	presence := ev.UsagePresence
	for _, scope := range ev.UsageScopes {
		presence = presence.Union(scope.UsagePresence)
	}
	if !presence.Any() {
		return false, false
	}
	switch unit {
	case usagelimit.AmountUnitInputTokens:
		return presence.InputTokens, true
	case usagelimit.AmountUnitOutputTokens:
		return presence.OutputTokens, true
	case usagelimit.AmountUnitCacheReadTokens:
		return presence.CacheReadTokens, true
	case usagelimit.AmountUnitCacheWriteTokens:
		return presence.CacheWriteTokens, true
	case usagelimit.AmountUnitReasoningTokens:
		return presence.ReasoningTokens, true
	case usagelimit.AmountUnitTotalTokens:
		return presence.TotalTokens, true
	default:
		return false, false
	}
}

// attemptAuthorityEventHasAnyUsage reports whether any legacy counter is non-zero.
func attemptAuthorityEventHasAnyUsage(ev aipapi.Event) bool {
	if ev.InputTokens > 0 || ev.OutputTokens > 0 || ev.CacheReadTokens > 0 ||
		ev.CacheWriteTokens > 0 || ev.ReasoningTokens > 0 || ev.TotalTokens > 0 {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if scope.InputTokens > 0 || scope.OutputTokens > 0 || scope.CacheReadTokens > 0 ||
			scope.CacheWriteTokens > 0 || scope.ReasoningTokens > 0 || scope.TotalTokens > 0 {
			return true
		}
	}
	return false
}

func attemptAuthorityReservationKey(requestID, traceID, aLegID string, bleg b2bua.BLegRecord, c routing.AttemptCandidate) usagelimit.ReservationKey {
	reqID := strings.TrimSpace(requestID)
	if reqID == "" {
		reqID = strings.TrimSpace(traceID)
	}
	return usagelimit.ReservationKey{
		LogicalRequestID: reqID,
		ALegID:           strings.TrimSpace(aLegID),
		BLegID:           strings.TrimSpace(bleg.BLegID),
		AttemptID:        strings.TrimSpace(bleg.BLegID),
		RuleID:           strings.TrimSpace(c.Key),
		Sequence:         1,
	}
}

func attemptAuthorityRuleID(state attemptAuthorityState) string {
	if len(state.admissionResult.RuleIDs) > 0 {
		return state.admissionResult.RuleIDs[0]
	}
	return state.admissionInput.ReservationKey.RuleID
}

func mapAttemptAuthorityCoordinatorError(err error) error {
	var denied *authoritycoord.DeniedError
	if !errors.As(err, &denied) || denied == nil {
		return nil
	}
	if denied.ProviderID == compatibleadmission.ProviderID || denied.Decision.Evidence.Category == "concurrency_limit" {
		return aipapi.NewPolicyDeniedError(
			"compatible_backend_attempt",
			"",
			"concurrency_limit",
			"concurrency_limit",
			"compatible backend concurrent request limit reached",
			nil,
		)
	}
	return nil
}

func attemptAuthorityAdmissionError(result usagelimit.AdmissionResult, err error) error {
	reasonCode := "usage_authority_denied"
	if result.PolicyRecord.ReasonCode != "" {
		reasonCode = result.PolicyRecord.ReasonCode
	}
	switch result.Outcome {
	case usagelimit.DecisionOutcomeUnavailable, usagelimit.DecisionOutcomeError:
		return aipapi.NewPolicyFailureError("usage_authority_admission", "", reasonCode, "accounting_authority", "usage authority unavailable", err)
	case usagelimit.DecisionOutcomeDeny:
		return aipapi.NewPolicyDeniedError("usage_authority_admission", "", reasonCode, "accounting_authority", "request denied by usage authority", err)
	default:
		if !result.Allowed {
			return aipapi.NewPolicyFailureError("usage_authority_admission", "", reasonCode, "accounting_authority", "usage authority unavailable", err)
		}
		return nil
	}
}

// applyGenerationBoundVersionFrom prefers the request-bound executable generation's
// evaluator object identity (requirements 9.3, 9.5, 9.9; design D10).
func (e *Executor) applyGenerationBoundVersionFrom(bound *snapshotgen.ExecutableGeneration, res *usagelimit.AdmissionResult) {
	if e == nil || res == nil {
		return
	}
	exec := bound
	if exec == nil && e.SnapshotGeneration != nil {
		// Compatibility only when no request binding exists yet.
		exec = e.SnapshotGeneration.CurrentExecutable()
	}
	if exec != nil {
		obj := exec.EvidenceObjectID()
		if obj != "" {
			res.BoundVersion = economics.PolicySnapshotRef{
				VersionRef: economics.VersionRef{
					ID: obj, Version: exec.Version, EffectiveAt: exec.PublishedAt,
				},
				PolicyID: obj,
			}
		}
		return
	}
	if e.SnapshotGeneration == nil {
		return
	}
	gen := e.SnapshotGeneration.Current()
	if gen == nil {
		return
	}
	if strings.TrimSpace(gen.Usage.Version) != "" {
		policyID := strings.TrimSpace(gen.Usage.ID)
		if policyID == "" {
			policyID = string(economics.PolicyKindUsageAuthority)
		}
		res.BoundVersion = gen.Usage.PolicyRef(policyID)
	}
}

// mergeGenerationBoundVersionsFrom appends bound executable generation evidence.
func (e *Executor) mergeGenerationBoundVersionsFrom(bound *snapshotgen.ExecutableGeneration, d *authoritycoord.CompositeDecision) {
	if e == nil || d == nil {
		return
	}
	if bound != nil {
		obj := bound.EvidenceObjectID()
		if obj != "" {
			ref := economics.PolicySnapshotRef{
				VersionRef: economics.VersionRef{
					ID: obj, Version: bound.Version, EffectiveAt: bound.PublishedAt,
				},
				PolicyID: obj,
			}
			d.BoundVersions = prependPolicyRef(d.BoundVersions, ref)
		}
		return
	}
	if e.SnapshotGeneration == nil {
		return
	}
	if exec := e.SnapshotGeneration.CurrentExecutable(); exec != nil {
		e.mergeGenerationBoundVersionsFrom(exec, d)
		return
	}
	gen := e.SnapshotGeneration.Current()
	if gen == nil {
		return
	}
	if strings.TrimSpace(gen.Usage.Version) != "" {
		policyID := strings.TrimSpace(gen.Usage.ID)
		if policyID == "" {
			policyID = string(economics.PolicyKindUsageAuthority)
		}
		ref := gen.Usage.PolicyRef(policyID)
		d.BoundVersions = prependPolicyRef(d.BoundVersions, ref)
	}
	if strings.TrimSpace(gen.Concurrency.Version) != "" {
		policyID := strings.TrimSpace(gen.Concurrency.ID)
		if policyID == "" {
			policyID = string(economics.PolicyKindConcurrency)
		}
		ref := gen.Concurrency.PolicyRef(policyID)
		d.BoundVersions = prependPolicyRef(d.BoundVersions, ref)
	}
}

func prependPolicyRef(existing []economics.PolicySnapshotRef, ref economics.PolicySnapshotRef) []economics.PolicySnapshotRef {
	out := make([]economics.PolicySnapshotRef, 0, len(existing)+1)
	out = append(out, ref)
	for _, cur := range existing {
		if cur.Version == ref.Version && cur.ID == ref.ID && cur.PolicyID == ref.PolicyID {
			continue
		}
		out = append(out, cur)
	}
	return out
}
