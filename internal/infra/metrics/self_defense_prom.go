package metrics

import (
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/ingressdefense"
	"github.com/prometheus/client_golang/prometheus"
)

// unknownSelfDefenseReason is the single finite bucket every reason outside the
// closed vocabulary collapses into. It is a constant: an unrecognized or
// attacker-influenced reason can never widen the label set.
const unknownSelfDefenseReason = "unknown"

// SelfDefenseProm exposes the three bounded ingress self-defense process
// metrics: denial count by finite reason, quarantine-transition count by finite
// reason, and the current adaptive-state entry count.
//
// Label safety is structural, not conventional, and it covers the label VALUE as
// well as the label NAME. Both counters declare exactly one variable label,
// "reason", and the entry gauge declares none; the value of that label is a switch
// over the closed [ingressdefense.AllReasons] vocabulary whose only other outcome
// is the constant "unknown" bucket, so a source address, CIDR text, request path,
// query, header, User-Agent, credential, principal, country, or any other
// attacker-controlled string cannot become a series dimension on any code path
// (requirement 9.4). The invariant is enforced on the REAL process bundle
// registry, not only through this file: selfDefenseBundleLabelFindings reads every
// family published under the aip_self_defense_ prefix through registry.Gather and
// rejects both a label name outside {"reason"} and a "reason" value outside the
// closed vocabulary, so a sibling file that bypasses selfDefenseReasonLabel cannot
// smuggle a value through. The known boundary of that guard is its name prefix: a
// self-defense series renamed out of aip_self_defense_ is outside its scope.
//
// Every method is total: a nil collector, an unbound entry-count source, and any
// reason string are no-ops that cannot panic and cannot return an error. Metrics
// are therefore non-authoritative — an observability absence can never change a
// security decision or a state mutation (design "Error Handling").
//
// The entry-count gauge is scrape-driven, exactly like the process-owned
// PostgreSQL pool collector: it reads the adaptive state live on every scrape, so
// it reports the current entry count including successful-auth clears, bounded
// eviction and lazy hostile-inactivity expiry, and it adds no work to the request
// path. The transition-time entry count is therefore intentionally not stored.
type SelfDefenseProm struct {
	denials     *prometheus.CounterVec
	transitions *prometheus.CounterVec
	entries     prometheus.GaugeFunc
	entrySource atomic.Pointer[func() int]
}

// RegisterSelfDefenseProm registers the aip_self_defense_* collectors on reg.
func RegisterSelfDefenseProm(reg prometheus.Registerer) *SelfDefenseProm {
	m := &SelfDefenseProm{
		denials: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "self_defense_denials_total",
			Help:      "Ingress self-defense generic denials by finite reason.",
		}, []string{"reason"}),
		transitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "self_defense_quarantine_transitions_total",
			Help:      "Ingress self-defense quarantine transitions by finite reason.",
		}, []string{"reason"}),
	}
	m.entries = prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: namespace,
		Name:      "self_defense_state_entries",
		Help: "Allocated ingress self-defense adaptive state entries. This counts entries " +
			"currently held by the bounded state, including entries whose hostile inactivity " +
			"has already reached state_ttl: expiry is lazy and happens on the next state " +
			"lookup or mutation, so a source that is never touched again stays counted " +
			"until then. Treat this as allocated capacity in use, not as live sources.",
	}, func() float64 {
		source := m.entrySource.Load()
		if source == nil || *source == nil {
			return 0
		}
		return float64(max((*source)(), 0))
	})
	reg.MustRegister(m.denials, m.transitions, m.entries)
	return m
}

// SetEntryCountSource binds the process-owned adaptive-state entry-count source
// the gauge reads on every scrape. A nil source clears the binding and leaves the
// gauge at zero. Binding is done once at process construction; the gauge never
// writes back into the state it observes.
func (m *SelfDefenseProm) SetEntryCountSource(source func() int) {
	if m == nil {
		return
	}
	if source == nil {
		m.entrySource.Store(nil)
		return
	}
	m.entrySource.Store(&source)
}

// Denial records one generic refusal for a finite reason.
//
// The deferred recover states the invariant of design "Error Handling" that
// metrics are non-authoritative under failure, not only under absence. The gate
// (internal/stdhttp/selfdefense.Middleware) calls the observer AFTER it has
// chosen the generic status and BEFORE it writes the response — recordDenial at
// middleware.go:101, :112 and :125 each precede forbidden/notFound/tooManyRequests
// — and the 403 resolution-failure path at middleware.go:101 mutates no adaptive
// state at all, so a panicking label lookup (a prometheus client panics when a
// vec's declared label arity does not match the supplied values) may cost only
// one observation. Letting it escape would be caught by the outer recovery
// middleware and rewrite a deliberate generic refusal as a 500, so the wire
// answer would change because of observability.
func (m *SelfDefenseProm) Denial(reason ingressdefense.Reason) {
	if m == nil || m.denials == nil {
		return
	}
	defer func() { _ = recover() }()
	m.denials.WithLabelValues(selfDefenseReasonLabel(reason)).Inc()
}

// QuarantineTransition records one started or escalated quarantine for a finite
// reason. The transition-time entry count of tracked source entries is
// deliberately not stored: the entry-count gauge reads the adaptive state live, so
// it also reflects a successful-auth clear and lazy hostile-inactivity expiry.
//
// The deferred recover is the same invariant as in [SelfDefenseProm.Denial]: a
// metrics fault must not turn the caller's generic refusal into a 500.
func (m *SelfDefenseProm) QuarantineTransition(reason ingressdefense.Reason, _ int) {
	if m == nil || m.transitions == nil {
		return
	}
	defer func() { _ = recover() }()
	m.transitions.WithLabelValues(selfDefenseReasonLabel(reason)).Inc()
}

// selfDefenseReasonLabel maps a reason onto the closed metric vocabulary. Only
// the four members of the closed set are named; everything else, including any
// string that leaked in from request data, collapses into the constant
// "unknown" bucket, so the label set is bounded by construction.
func selfDefenseReasonLabel(reason ingressdefense.Reason) string {
	switch reason {
	case ingressdefense.ReasonImpossiblePath,
		ingressdefense.ReasonAuthFailureThreshold,
		ingressdefense.ReasonActiveQuarantine,
		ingressdefense.ReasonClientIPError:
		return string(reason)
	default:
		return unknownSelfDefenseReason
	}
}
