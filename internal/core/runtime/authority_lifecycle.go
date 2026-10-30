package runtime

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// authorityLifecycle owns one admission result's synchronized settle/release
// lifecycle and submits complete reservation-set mutations.
type authorityLifecycle struct {
	svc UsageAuthorityService
	log *slog.Logger
	// attemptCoord settles/releases coordinator-admitted holds through owning providers.
	attemptCoord *authoritycoord.AttemptCoordinator
	// Pointers keep the value-shaped lifecycle copyable for existing stream and
	// race-owner structs without copying sync/atomic noCopy values. The owner
	// created by newAuthorityLifecycle always initializes them.
	control          *authorityLifecycleControl
	backendAttempted *atomic.Bool
	outputCommitted  *atomic.Bool
}

type authorityReservationState struct {
	reservationID  string
	ruleID         string
	reservedAmount usagelimit.Amount
	reservationKey usagelimit.ReservationKey
}

type settlementAuthorityState struct {
	UsageByUnit map[usagelimit.AmountUnit]usagelimit.AuthorityLevel
}

type authorityTerminalState uint8

const (
	authorityTerminalOpen authorityTerminalState = iota
	authorityTerminalSettled
	authorityTerminalReleased
)

type authorityLifecycleControl struct {
	mu        sync.Mutex
	state     attemptAuthorityState
	cand      routing.AttemptCandidate
	terminal  authorityTerminalState
	authority settlementAuthorityState
}

const defaultAuthorityCleanupTimeout = 2 * time.Second

func cleanupContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultAuthorityCleanupTimeout
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// newAuthorityLifecycle builds an owner over the supplied authority service,
// logger, reservation state, and attempt candidate. The owner is inactive until
// IsActive reports true (correlated + reserved admission result).
func newAuthorityLifecycle(svc UsageAuthorityService, log *slog.Logger, state attemptAuthorityState, cand routing.AttemptCandidate) authorityLifecycle {
	lifecycle := authorityLifecycle{
		svc:              svc,
		log:              log,
		control:          &authorityLifecycleControl{state: state, cand: cand, authority: newSettlementAuthorityState()},
		backendAttempted: &atomic.Bool{},
		outputCommitted:  &atomic.Bool{},
	}
	// Most lifecycle owners are created from an opened attempt, so the default
	// is true. The pre-open cleanup owner in tryPlanOpenOnce explicitly resets
	// this until the actual backend Open call begins.
	lifecycle.backendAttempted.Store(true)
	return lifecycle
}

// newAttemptAuthorityLifecycle builds a lifecycle owner wired to both the
// built-in usage-authority service and the attempt coordinator (when present).
func (e *Executor) newAttemptAuthorityLifecycle(state attemptAuthorityState, cand routing.AttemptCandidate) authorityLifecycle {
	if e == nil {
		return newAuthorityLifecycle(nil, nil, state, cand)
	}
	lifecycle := newAuthorityLifecycle(e.UsageAuthority, e.Log, state, cand)
	lifecycle.attemptCoord = e.AttemptCoordinator
	return lifecycle
}

func (l *authorityLifecycle) settledLoad() bool {
	if l == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	return l.control.terminal != authorityTerminalOpen
}

func newSettlementAuthorityState() settlementAuthorityState {
	return settlementAuthorityState{UsageByUnit: make(map[usagelimit.AmountUnit]usagelimit.AuthorityLevel)}
}

func usageAuthorityForUnit(value settlementAuthorityState, unit usagelimit.AmountUnit) usagelimit.AuthorityLevel {
	if authority := value.UsageByUnit[unit]; authority != "" {
		return authority
	}
	return usagelimit.AuthorityLevelEstimated
}

func (l *authorityLifecycle) markOutputCommitted() {
	if l != nil && l.outputCommitted != nil {
		l.outputCommitted.Store(true)
	}
}

// IsActive reports whether this lifecycle owns a correlated live reservation.
func (l *authorityLifecycle) IsActive() bool {
	if l == nil || l.control == nil {
		return false
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	return l.isActiveLocked()
}

func (l *authorityLifecycle) isActiveLocked() bool {
	return l.control.state.admissionInput.Correlation.TraceID != "" &&
		l.control.state.admissionResult.Reserved
}

// Settled reports whether the reservation has been settled or released.
func (l *authorityLifecycle) Settled() bool {
	return l.settledLoad()
}

func (l *authorityLifecycle) stateSnapshot() attemptAuthorityState {
	if l == nil || l.control == nil {
		return attemptAuthorityState{}
	}
	l.control.mu.Lock()
	defer l.control.mu.Unlock()
	return l.control.state
}

func (l *authorityLifecycle) reservationStates() []authorityReservationState {
	if l == nil {
		return nil
	}
	state := l.control.state
	reservations := state.admissionResult.Reservations
	if len(reservations) == 0 {
		reservations = []usagelimit.AdmissionReservation{{
			ReservationID:  state.admissionResult.ReservationID,
			RuleID:         attemptAuthorityRuleID(state),
			ReservedAmount: state.admissionResult.ReservedAmount,
		}}
	}
	out := make([]authorityReservationState, 0, len(reservations))
	for _, reservation := range reservations {
		ruleID := strings.TrimSpace(reservation.RuleID)
		if ruleID == "" {
			ruleID = attemptAuthorityRuleID(state)
		}
		reservedAmount := reservation.ReservedAmount
		if reservedAmount.Unit == "" {
			reservedAmount = state.admissionResult.ReservedAmount
		}
		reservationKey := state.admissionInput.ReservationKey
		reservationKey.RuleID = ruleID
		out = append(out, authorityReservationState{
			reservationID:  strings.TrimSpace(reservation.ReservationID),
			ruleID:         ruleID,
			reservedAmount: reservedAmount,
			reservationKey: reservationKey,
		})
	}
	return out
}

// authorityForSettlement selects the authority level from the usage metadata,
// never from a non-zero token count alone (requirement 8.4-8.6). Only an event
// explicitly marked authoritative on the provider-billable plane is
// authoritative; local client-visible reconstruction remains estimated across
// final, partial, and cancellation paths.
func authorityForSettlement(_ usagelimit.SettlementKind, usageEv aipapi.Event) usagelimit.AuthorityLevel {
	if eventCarriesAuthoritativeProviderUsage(usageEv) {
		return usagelimit.AuthorityLevelAuthoritative
	}
	if eventCarriesUnavailableUsage(usageEv) {
		return usagelimit.AuthorityLevelUnavailable
	}
	return usagelimit.AuthorityLevelEstimated
}

func measurementAuthorityForEvent(ev aipapi.Event) usagelimit.MeasurementAuthority {
	return usagelimit.MeasurementAuthority{Usage: authorityForSettlement(usagelimit.SettlementKindFinal, ev)}
}

func measurementAuthorityForUnit(ev aipapi.Event, unit usagelimit.AmountUnit) usagelimit.MeasurementAuthority {
	authority := measurementAuthorityForEvent(ev)
	if !attemptAuthorityEventHasUsageForUnit(ev, unit) {
		authority.Usage = usagelimit.AuthorityLevelEstimated
	}
	return authority
}

func authorityRank(value usagelimit.AuthorityLevel) int {
	switch value {
	case usagelimit.AuthorityLevelAuthoritative:
		return 3
	case usagelimit.AuthorityLevelEstimated:
		return 2
	default:
		return 0
	}
}

func measurementAuthorityNeedsUpgradeForAmount(prior settlementAuthorityState, incoming usagelimit.MeasurementAuthority, amount usagelimit.Amount) bool {
	return authorityRank(incoming.Usage) > authorityRank(usageAuthorityForUnit(prior, amount.Unit))
}

func mergeMeasurementAuthorityForAmount(prior settlementAuthorityState, incoming usagelimit.MeasurementAuthority, amount usagelimit.Amount) settlementAuthorityState {
	if prior.UsageByUnit == nil {
		prior.UsageByUnit = make(map[usagelimit.AmountUnit]usagelimit.AuthorityLevel)
	}
	if authorityRank(incoming.Usage) > authorityRank(usageAuthorityForUnit(prior, amount.Unit)) {
		prior.UsageByUnit[amount.Unit] = incoming.Usage
	}
	return prior
}

func eventCarriesAuthoritativeProviderUsage(ev aipapi.Event) bool {
	if authoritativeProviderAccounting(ev.Accounting) {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if authoritativeProviderAccounting(scope.Accounting) {
			return true
		}
	}
	return false
}

func authoritativeProviderAccounting(accounting aipapi.UsageAccountingMetadata) bool {
	if accounting.Authority != aipapi.UsageAuthorityAuthoritative {
		return false
	}
	if accounting.Plane != aipapi.UsagePlaneProviderBillable {
		return false
	}
	return accounting.Source == aipapi.UsageSourceProviderReported ||
		accounting.Source == aipapi.UsageSourceProviderCountAPI
}

func eventCarriesUnavailableUsage(ev aipapi.Event) bool {
	if ev.Accounting.Authority == aipapi.UsageAuthorityUnavailable || ev.Accounting.Source == aipapi.UsageSourceUnavailable {
		return true
	}
	for _, scope := range ev.UsageScopes {
		if scope.Accounting.Authority == aipapi.UsageAuthorityUnavailable || scope.Accounting.Source == aipapi.UsageSourceUnavailable {
			return true
		}
	}
	return false
}
