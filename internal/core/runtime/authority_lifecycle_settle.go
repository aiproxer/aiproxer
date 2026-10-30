package runtime

import (
	"context"
	"strings"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func (l *authorityLifecycle) settlementInput(kind usagelimit.SettlementKind, usageEv aipapi.Event, clientCanceled bool, reservations []authorityReservationState) usagelimit.SettleInput {
	state := l.control.state
	measurementAuthority := measurementAuthorityForEvent(usageEv)
	input := usagelimit.SettleInput{
		Correlation:          state.admissionInput.Correlation,
		Scope:                state.admissionInput.Scope,
		Kind:                 kind,
		Authority:            measurementAuthority.Usage,
		MeasurementAuthority: measurementAuthority,
		Stage:                feature.StageIDAttemptLifecycle,
		BackendAttempted:     l.backendAttempted != nil && l.backendAttempted.Load(),
		OutputCommitted:      l.outputCommitted != nil && l.outputCommitted.Load(),
		ClientCanceled:       clientCanceled,
		FinalUsagePresent:    usageEventPresent(usageEv),
		// Pin settle to the admission-time snapshot (requirement 11.4).
		BoundVersion:       state.admissionResult.BoundVersion,
		BoundRatingVersion: state.admissionResult.BoundRatingVersion,
	}
	input.Reservations = make([]usagelimit.SettlementDescriptor, 0, len(reservations))
	for _, reservation := range reservations {
		descriptorAuthority := measurementAuthorityForUnit(usageEv, reservation.reservedAmount.Unit)
		input.Reservations = append(input.Reservations, usagelimit.SettlementDescriptor{
			Reservation: usagelimit.ReservationDescriptor{
				RuleID:         reservation.ruleID,
				Unit:           reservation.reservedAmount.Unit,
				Dimensions:     state.admissionInput.Dimensions,
				ReservationKey: reservation.reservationKey,
				ReservationID:  reservation.reservationID,
				Amount:         reservation.reservedAmount,
			},
			FinalUsage:           attemptAuthorityUsageAmount(usageEv, l.finalUsageEstimate(reservation)),
			EstimatedUsage:       reservation.reservedAmount,
			Authority:            descriptorAuthority.ForUnit(reservation.reservedAmount.Unit),
			MeasurementAuthority: descriptorAuthority,
		})
	}
	return input
}

func usageEventPresent(ev aipapi.Event) bool {
	return ev.Kind == aipapi.EventUsageDelta
}

func (l *authorityLifecycle) finalUsageEstimate(reservation authorityReservationState) usagelimit.Amount {
	return reservation.reservedAmount
}

// Settle atomically finalizes the reservation set with retryable compensation.
func (l *authorityLifecycle) Settle(ctx context.Context, kind usagelimit.SettlementKind, usageEv aipapi.Event, clientCanceled bool) bool {
	if l == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	if !l.isActiveLocked() {
		return false
	}
	if l.control.terminal == authorityTerminalReleased {
		return false
	}
	if l.control.terminal == authorityTerminalSettled {
		return l.reconcileAuthoritativeLocked(ctx, usageEv)
	}
	if l.control.state.viaCoordinator {
		return l.settleViaCoordinatorLocked(ctx, kind, usageEv, clientCanceled)
	}
	if l.svc == nil {
		return false
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	reservations := l.reservationStates()
	result, err := l.svc.Settle(cleanupCtx, usagelimit.DeriveSettleScalars(l.settlementInput(kind, usageEv, clientCanceled, reservations)))
	cancel()
	if result.Applied {
		l.control.terminal = authorityTerminalSettled
		for _, reservation := range reservations {
			incoming := measurementAuthorityForUnit(usageEv, reservation.reservedAmount.Unit)
			l.control.authority = mergeMeasurementAuthorityForAmount(l.control.authority, incoming, reservation.reservedAmount)
		}
		if err != nil && l.log != nil {
			l.log.DebugContext(ctx, "usage authority settle evidence failed after commit", "error", err, "candidate_key", l.control.cand.Key)
		}
		return true
	}
	if err != nil && l.log != nil {
		l.log.DebugContext(ctx, "usage authority settle failed", "error", err, "candidate_key", l.control.cand.Key)
	}
	// A pre-output settlement failure may safely release the complete set. Once
	// client output is committed, release would hide a real accounting failure;
	// retain the reservation and let degraded/unavailable evidence describe it.
	if l.outputCommitted == nil || !l.outputCommitted.Load() {
		releaseCtx, releaseCancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
		defer releaseCancel()
		if l.releaseReservationSet(releaseCtx, usagelimit.ReleaseKindLosing, reservations) {
			l.control.terminal = authorityTerminalReleased
		}
	}
	return false
}

// Stable BuildAuthorityCoordinators slot / Decision.ProviderID identities for the
// built-in usage-authority adapter (must match registration descriptor IDs).
const (
	usageAuthorityRequestProviderID = "usage-authority-request"
	usageAuthorityAttemptProviderID = "usage-authority-attempt"
)

func (l *authorityLifecycle) settleViaCoordinatorLocked(ctx context.Context, kind usagelimit.SettlementKind, usageEv aipapi.Event, clientCanceled bool) bool {
	if l.attemptCoord == nil {
		return false
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	if l.control.state.settledProviders == nil {
		l.control.state.settledProviders = make(map[string]struct{})
	}
	state := l.control.state
	outputCommitted := l.outputCommitted != nil && l.outputCommitted.Load()
	evidence := attemptSettlementEvidence(l, kind, usageEv, outputCommitted, clientCanceled)

	for _, providerID := range uniqueStackProviderIDs(state.stack) {
		if providerSettled(l.control.state.settledProviders, providerID) {
			continue
		}
		sub := filterStackByProvider(state.stack, providerID)
		if len(sub.Handles()) == 0 {
			continue
		}
		settleIn := evidence
		settleIn.Handles = sub.Handles()
		settleIn.Reservations = filterSDKReservations(evidence.Reservations, sub.Handles())
		if err := l.attemptCoord.Settle(cleanupCtx, sub, settleIn); err != nil {
			if l.log != nil {
				l.log.DebugContext(ctx, "attempt coordinator settle failed", "error", err, "provider_id", providerID, "candidate_key", l.control.cand.Key)
			}
			continue
		}
		markProviderSettled(&l.control.state, providerID)
		if providerID == usageAuthorityAttemptProviderID {
			for _, reservation := range filterReservationsByHandles(l.reservationStates(), sub.Handles()) {
				incoming := measurementAuthorityForUnit(usageEv, reservation.reservedAmount.Unit)
				l.control.authority = mergeMeasurementAuthorityForAmount(l.control.authority, incoming, reservation.reservedAmount)
			}
		}
	}

	unfinished := unfinishedStack(l.control.state.stack, l.control.state.settledProviders)
	if len(unfinished.Handles()) == 0 {
		l.control.terminal = authorityTerminalSettled
		return true
	}

	if outputCommitted {
		// Keep terminal open so only unfinished providers are retried (15.5).
		return false
	}

	// Before output is committed, release only providers that never settled.
	if l.releaseViaCoordinatorStackLocked(ctx, unfinished) {
		if len(l.control.state.settledProviders) > 0 {
			l.control.terminal = authorityTerminalSettled
			return true
		}
		l.control.terminal = authorityTerminalReleased
	}
	return false
}

func attemptSettlementEvidence(
	l *authorityLifecycle,
	kind usagelimit.SettlementKind,
	usageEv aipapi.Event,
	outputCommitted bool,
	clientCanceled bool,
) authority.AttemptSettlement {
	state := l.control.state
	outcome, surfaced := mapAttemptSettlementPosture(kind, outputCommitted, clientCanceled)
	qs := quantitiesFromUsageEvent(usageEv)
	presence := metering.PresenceUnknown
	if len(qs) > 0 {
		presence = metering.PresencePresent
	}
	authorityLevel := metering.AuthorityEstimated
	if eventCarriesAuthoritativeProviderUsage(usageEv) {
		authorityLevel = metering.AuthorityAuthoritative
	}
	fact := metering.Fact{
		FactID:      "attempt-settle:" + strings.TrimSpace(state.attemptID),
		StreamID:    "be-egress:" + strings.TrimSpace(state.bLegID),
		Sequence:    1,
		Kind:        metering.FactKindCumulative,
		Perspective: metering.PerspectiveOperator,
		Boundary:    metering.BoundaryBackendEgress,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Correlation: metering.Correlation{RequestID: state.requestID, AttemptID: state.attemptID, BLegID: state.bLegID},
		Quantities:  qs,
		// Money/Rated are intentionally omitted: stream CostNanoUnits are not
		// usage-authority financial input. Post-turn TUR rating owns money;
		// metering egress may still carry MoneyObservation for telemetry.
		Source:         metering.SourceObserved,
		Authority:      authorityLevel,
		Presence:       presence,
		AttemptOutcome: outcome,
		Surfaced:       surfaced,
		RecordedAt:     time.Now().UTC(),
	}
	var facts []metering.Fact
	if err := fact.Validate(); err == nil {
		facts = []metering.Fact{fact}
	}
	return authority.AttemptSettlement{
		RequestID:        state.requestID,
		AttemptID:        state.attemptID,
		BLegID:           state.bLegID,
		ALegID:           strings.TrimSpace(state.admissionInput.Correlation.ALegID),
		BackendID:        strings.TrimSpace(state.admissionInput.Dimensions.Backend.String()),
		Model:            strings.TrimSpace(state.admissionInput.Dimensions.Model.String()),
		Scope:            state.admissionInput.Scope,
		Reservations:     reservationsToSDK(l.reservationStates()),
		Facts:            facts,
		Outcome:          outcome,
		Surfaced:         surfaced,
		Kind:             string(kind),
		BackendAttempted: l.backendAttempted != nil && l.backendAttempted.Load(),
		OutputCommitted:  outputCommitted,
		ClientCanceled:   clientCanceled,
		BoundVersions:    append([]economics.PolicySnapshotRef(nil), state.boundVersions...),
	}
}

func reservationsToSDK(reservations []authorityReservationState) []authority.Reservation {
	out := make([]authority.Reservation, 0, len(reservations))
	for _, r := range reservations {
		id := strings.TrimSpace(r.reservationID)
		if id == "" {
			continue
		}
		out = append(out, mapAdmissionReservation(usagelimit.AdmissionReservation{
			ReservationID:  id,
			RuleID:         r.ruleID,
			ReservedAmount: r.reservedAmount,
		}))
	}
	return out
}

func filterSDKReservations(reservations []authority.Reservation, handles []string) []authority.Reservation {
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

func mapAttemptSettlementPosture(kind usagelimit.SettlementKind, outputCommitted, clientCanceled bool) (metering.AttemptOutcome, metering.SurfacedState) {
	switch kind {
	case usagelimit.SettlementKindCancellation:
		if clientCanceled {
			return metering.AttemptOutcomeCanceled, metering.SurfacedNo
		}
		return metering.AttemptOutcomeCanceled, metering.SurfacedUnknown
	case usagelimit.SettlementKindLosing, usagelimit.SettlementKindSwallowed:
		return metering.AttemptOutcomeLoser, metering.SurfacedNo
	case usagelimit.SettlementKindUnavailable, usagelimit.SettlementKindPartial:
		return metering.AttemptOutcomeFailed, metering.SurfacedNo
	default:
		if outputCommitted {
			return metering.AttemptOutcomeWinner, metering.SurfacedYes
		}
		return metering.AttemptOutcomeUnknown, metering.SurfacedUnknown
	}
}

func providerSettled(settled map[string]struct{}, providerID string) bool {
	if settled == nil {
		return false
	}
	_, ok := settled[strings.TrimSpace(providerID)]
	return ok
}

func markProviderSettled(state *attemptAuthorityState, providerID string) {
	if state == nil {
		return
	}
	id := strings.TrimSpace(providerID)
	if id == "" {
		return
	}
	if state.settledProviders == nil {
		state.settledProviders = make(map[string]struct{})
	}
	state.settledProviders[id] = struct{}{}
}

func uniqueStackProviderIDs(stack authoritycoord.CompensationStack) []string {
	seen := make(map[string]struct{})
	var out []string
	for _, e := range stack.Entries() {
		id := strings.TrimSpace(e.ProviderID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func filterStackByProvider(stack authoritycoord.CompensationStack, providerID string) authoritycoord.CompensationStack {
	want := strings.TrimSpace(providerID)
	var out authoritycoord.CompensationStack
	for _, e := range stack.Entries() {
		if strings.TrimSpace(e.ProviderID) == want {
			out.Push(e)
		}
	}
	return out
}

func unfinishedStack(stack authoritycoord.CompensationStack, settled map[string]struct{}) authoritycoord.CompensationStack {
	var out authoritycoord.CompensationStack
	for _, e := range stack.Entries() {
		if providerSettled(settled, e.ProviderID) {
			continue
		}
		out.Push(e)
	}
	return out
}

func filterReservationsByHandles(reservations []authorityReservationState, handles []string) []authorityReservationState {
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
	out := make([]authorityReservationState, 0, len(handles))
	for _, r := range reservations {
		if _, ok := allow[strings.TrimSpace(r.reservationID)]; ok {
			out = append(out, r)
		}
	}
	return out
}

// settlementKindForIncurredRelease maps losing/swallowed release postures to
// settlement kinds used when backend work was already incurred.
