package runtime

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/feature"
)

func settlementKindForIncurredRelease(kind usagelimit.ReleaseKind) (usagelimit.SettlementKind, bool) {
	switch kind {
	case usagelimit.ReleaseKindLosing:
		return usagelimit.SettlementKindLosing, true
	case usagelimit.ReleaseKindSwallowed:
		return usagelimit.SettlementKindSwallowed, true
	default:
		return "", false
	}
}

// finalizeIncurredOrRelease settles operator liability when backend Open (or
// equivalent incurred work) already started; otherwise it releases the pre-work
// admission. AdmissionFailure and never-opened candidates stay on Release.
func (l *authorityLifecycle) finalizeIncurredOrRelease(ctx context.Context, kind usagelimit.ReleaseKind, usageEv aipapi.Event) {
	if l == nil {
		return
	}
	incurred := l.backendAttempted != nil && l.backendAttempted.Load()
	if settleKind, ok := settlementKindForIncurredRelease(kind); ok && incurred {
		_ = l.Settle(ctx, settleKind, usageEv, false)
		return
	}
	l.Release(ctx, kind)
}

// Release releases every reservation in the admission result once with the
// supplied kind. It marks settled only after the atomic release succeeds, so a
// failed cleanup can be retried. No-op when inactive or when svc is nil.
func (l *authorityLifecycle) Release(ctx context.Context, kind usagelimit.ReleaseKind) {
	if l == nil || l.control == nil {
		return
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	if !l.isActiveLocked() {
		return
	}
	if l.control.terminal != authorityTerminalOpen {
		return
	}
	if l.control.state.viaCoordinator {
		// Release never re-touches providers that already settled successfully.
		unfinished := unfinishedStack(l.control.state.stack, l.control.state.settledProviders)
		if len(unfinished.Handles()) == 0 {
			l.control.terminal = authorityTerminalSettled
			return
		}
		if l.releaseViaCoordinatorStackLocked(ctx, unfinished) {
			if len(l.control.state.settledProviders) > 0 {
				l.control.terminal = authorityTerminalSettled
			} else {
				l.control.terminal = authorityTerminalReleased
				l.control.authority = newSettlementAuthorityState()
			}
		}
		return
	}
	if l.svc == nil {
		return
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	if l.releaseReservationSet(cleanupCtx, kind, l.reservationStates()) {
		l.control.terminal = authorityTerminalReleased
		l.control.authority = newSettlementAuthorityState()
	}
}

func (l *authorityLifecycle) releaseViaCoordinatorLocked(ctx context.Context) bool {
	return l.releaseViaCoordinatorStackLocked(ctx, l.control.state.stack)
}

func (l *authorityLifecycle) releaseViaCoordinatorStackLocked(ctx context.Context, stack authoritycoord.CompensationStack) bool {
	if l.attemptCoord == nil {
		return false
	}
	if len(stack.Handles()) == 0 {
		return true
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	fails := l.attemptCoord.Release(cleanupCtx, stack)
	if len(fails) > 0 {
		if l.log != nil {
			l.log.DebugContext(ctx, "attempt coordinator release failures", "count", len(fails), "candidate_key", l.control.cand.Key)
		}
		return false
	}
	return true
}

func (l *authorityLifecycle) releaseReservationSet(ctx context.Context, kind usagelimit.ReleaseKind, reservations []authorityReservationState) bool {
	if l == nil || l.svc == nil || len(reservations) == 0 {
		return false
	}
	input := usagelimit.ReleaseInput{
		Correlation:      l.control.state.admissionInput.Correlation,
		Scope:            l.control.state.admissionInput.Scope,
		Kind:             kind,
		Authority:        usagelimit.AuthorityLevelEstimated,
		Stage:            feature.StageIDAttemptLifecycle,
		BackendAttempted: l.backendAttempted != nil && l.backendAttempted.Load(),
		OutputCommitted:  l.outputCommitted != nil && l.outputCommitted.Load(),
		Reservations:     make([]usagelimit.ReleaseDescriptor, 0, len(reservations)),
	}
	for _, reservation := range reservations {
		input.Reservations = append(input.Reservations, usagelimit.ReleaseDescriptor{Reservation: usagelimit.ReservationDescriptor{
			RuleID:         reservation.ruleID,
			Unit:           reservation.reservedAmount.Unit,
			Dimensions:     l.control.state.admissionInput.Dimensions,
			ReservationKey: reservation.reservationKey,
			ReservationID:  reservation.reservationID,
			Amount:         reservation.reservedAmount,
		}})
	}
	result, err := l.svc.Release(ctx, usagelimit.DeriveReleaseScalars(input))
	if err != nil && l.log != nil {
		l.log.DebugContext(ctx, "usage authority release failed", "error", err, "candidate_key", l.control.cand.Key)
	}
	return err == nil || result.Applied
}

// ReconcileAuthoritative adjusts a prior (typically estimated) settlement with
// later authoritative final usage (requirement 7.6, 8.4-8.6). It bypasses the
// settled guard — the prior settlement stays in evidence — and calls Settle
// once with the complete reservation set using authoritative Sequence values so
// the store applies authoritativeResettle instead of no-opping.
func (l *authorityLifecycle) ReconcileAuthoritative(ctx context.Context, usageEv aipapi.Event) bool {
	if l == nil || l.svc == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	if l.control.terminal != authorityTerminalSettled {
		return false
	}
	return l.reconcileAuthoritativeLocked(ctx, usageEv)
}

func (l *authorityLifecycle) reconcileAuthoritativeLocked(ctx context.Context, usageEv aipapi.Event) bool {
	if l.svc == nil {
		return false
	}
	prior := l.control.authority
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	reservations := l.reservationStates()
	input := l.settlementInput(usagelimit.SettlementKindFinal, usageEv, false, reservations)
	input.FinalUsagePresent = usageEventPresent(usageEv)
	filtered := input.Reservations[:0]
	for i := range input.Reservations {
		reservation := input.Reservations[i]
		incoming := measurementAuthorityForUnit(usageEv, reservation.Reservation.Amount.Unit)
		if !measurementAuthorityNeedsUpgradeForAmount(prior, incoming, reservation.Reservation.Amount) {
			continue
		}
		reservation.Authority = usagelimit.AuthorityLevelAuthoritative
		reservation.MeasurementAuthority = incoming
		reservation.Reservation.Authority = usagelimit.AuthorityLevelAuthoritative
		filtered = append(filtered, reservation)
	}
	input.Reservations = filtered
	if len(input.Reservations) == 0 {
		return false
	}
	for i := range input.Reservations {
		input.Reservations[i].SourceKey = ""
		input.Reservations[i].Reservation.SourceKey = ""
	}
	input.Authority = usagelimit.AuthorityLevelAuthoritative
	input.Sequence = usagelimit.SettlementSequence(usagelimit.SettlementKindFinal, usagelimit.AuthorityLevelAuthoritative)
	result, err := l.svc.Settle(cleanupCtx, usagelimit.DeriveSettleScalars(input))
	if err != nil && l.log != nil {
		l.log.DebugContext(ctx, "usage authority authoritative reconcile failed", "error", err, "candidate_key", l.control.cand.Key)
	}
	if result.Applied {
		for _, reservation := range input.Reservations {
			incoming := reservation.MeasurementAuthority
			l.control.authority = mergeMeasurementAuthorityForAmount(l.control.authority, incoming, reservation.Reservation.Amount)
		}
		if err != nil && l.log != nil {
			l.log.DebugContext(ctx, "usage authority authoritative reconcile evidence failed after commit", "error", err, "candidate_key", l.control.cand.Key)
		}
		return true
	}
	return false
}

// Reset replaces the reservation state for a replacement iteration (recv-phase
// failover or parallel winner handoff) and clears the settled guard so the new
// reservation can be settled/released independently.
func (l *authorityLifecycle) Reset(state attemptAuthorityState, cand routing.AttemptCandidate) {
	if l == nil {
		return
	}
	if l.control == nil {
		l.control = &authorityLifecycleControl{authority: newSettlementAuthorityState()}
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	l.control.state = state
	l.control.cand = cand
	if l.backendAttempted == nil {
		l.backendAttempted = &atomic.Bool{}
	}
	if l.outputCommitted == nil {
		l.outputCommitted = &atomic.Bool{}
	}
	l.control.terminal = authorityTerminalOpen
	l.control.authority = newSettlementAuthorityState()
	l.backendAttempted.Store(true)
	l.outputCommitted.Store(false)
}

// ApplyUnreservedUsage applies final or partial usage/cost to every matched rule
// that did not create a reservation. It is independent of reservation lifecycle
// state and is idempotent at the store fact/source boundary.
func (l *authorityLifecycle) ApplyUnreservedUsage(ctx context.Context, kind usagelimit.SettlementKind, usageEv aipapi.Event) bool {
	if l == nil || l.svc == nil {
		return false
	}
	if l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	ruleIDs := l.control.state.admissionResult.UnreservedRuleIDs
	if len(ruleIDs) == 0 {
		ruleIDs = l.control.state.admissionResult.AdvisoryRuleIDs
	}
	if len(ruleIDs) == 0 {
		return false
	}
	cleanupCtx, cancel := cleanupContext(ctx, l.control.state.cleanupTimeout)
	defer cancel()
	usagePresent := usageEventPresent(usageEv)
	usage := advisoryUsageBreakdown(usageEv)
	measurementAuthority := measurementAuthorityForEvent(usageEv)
	if !usagePresent {
		// A final/cancellation path can legitimately have no usage event (for
		// example, reconstruction was unavailable). Keep unreserved windows
		// observable with the preflight estimate rather than silently dropping
		// token/request accounting. Monetary spend stays empty: BillingAdmission
		// / TUR settlement own money.
		usage = l.control.state.admissionInput.PreflightUsage
	}
	cmd := usagelimit.ApplyUsageCommand{
		Correlation:          l.control.state.admissionInput.Correlation,
		Scope:                l.control.state.admissionInput.Scope,
		Dimensions:           l.control.state.admissionInput.Dimensions,
		RuleIDs:              append([]string(nil), ruleIDs...),
		Usage:                usage,
		RequestCount:         usagelimit.Amount{Unit: usagelimit.AmountUnitRequests, Value: 1},
		UsagePresent:         usagePresent,
		Authority:            measurementAuthority.Usage,
		MeasurementAuthority: measurementAuthority,
		Kind:                 kind,
		SourceKey:            l.advisorySourceKey(),
	}
	result, err := l.svc.ApplyUsage(cleanupCtx, cmd)
	if err != nil {
		if l.log != nil {
			l.log.DebugContext(ctx, "usage authority advisory apply failed", "error", err, "candidate_key", l.control.cand.Key)
		}
		return false
	}
	return result.Applied
}

// ApplyAdvisoryUsage preserves the legacy lifecycle call shape for callers that
// only have a final response event.
func (l *authorityLifecycle) ApplyAdvisoryUsage(ctx context.Context, usageEv aipapi.Event) bool {
	return l.ApplyUnreservedUsage(ctx, usagelimit.SettlementKindFinal, usageEv)
}

// advisorySourceKey derives a deterministic idempotency key for advisory usage
// from the logical request trace ID and B-leg ID. Replays for the same logical
// request and B-leg converge on the same key so the store treats them as
// no-ops (requirement 7.8).
func (l *authorityLifecycle) advisorySourceKey() string {
	if l == nil {
		return ""
	}
	return strings.TrimSpace(l.control.state.admissionInput.Correlation.TraceID) + "|" +
		strings.TrimSpace(l.control.state.admissionInput.Correlation.BLegID) + "|advisory_usage"
}

// advisoryUsageBreakdown extracts the final per-unit token usage from a
// reconstructed usage event so the store can select the right amount per
// matched advisory rule unit (input/output/cache/reasoning/total tokens).
func advisoryUsageBreakdown(ev aipapi.Event) usagelimit.PreflightUsage {
	totalPresent := attemptAuthorityEventHasUsageForUnit(ev, usagelimit.AmountUnitTotalTokens)
	return usagelimit.PreflightUsage{
		InputTokens:        int64(ev.InputTokens),
		OutputTokens:       int64(ev.OutputTokens),
		CacheReadTokens:    int64(ev.CacheReadTokens),
		CacheWriteTokens:   int64(ev.CacheWriteTokens),
		ReasoningTokens:    int64(ev.ReasoningTokens),
		TotalTokens:        int64(ev.TotalTokens),
		TotalTokensPresent: totalPresent,
	}
}
