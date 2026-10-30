package proxymemory

import "github.com/prometheus/client_golang/prometheus"

// Event label values of aip_proxy_memory_events_total. The set is closed: every
// value is a compile-time constant and callers pass these constants only, so
// the series cardinality is bounded and no project path, principal ID, A-leg
// ID, prompt or memory text can become a label (Requirements 1.6, 2.6).
const (
	// Injection (bounded first-turn request transform).

	// EventInjectMatched counts a frozen matched decision applied to a request.
	EventInjectMatched = "inject_matched"
	// EventInjectNoSummary counts a frozen decision with no published summary.
	EventInjectNoSummary = "inject_no_summary"
	// EventInjectIneligible counts a request that failed CheckEligible.
	EventInjectIneligible = "inject_ineligible"
	// EventInjectNoSession counts a request without an established session.
	EventInjectNoSession = "inject_no_session"
	// EventInjectNoProject counts a request whose project cannot be derived.
	EventInjectNoProject = "inject_no_project"
	// EventInjectError counts a memory admission or decision persistence failure.
	EventInjectError = "inject_error"
	// EventInjectDecisionUnavailable counts a request with no validated
	// first-turn snapshot.
	EventInjectDecisionUnavailable = "inject_decision_unavailable"
	// EventInjectDroppedQueue counts a decision the bounded writer queue refused.
	EventInjectDroppedQueue = "inject_dropped_queue"
	// EventInjectDecisionDropped counts a queued first-turn write rejected as stale.
	EventInjectDecisionDropped = "inject_decision_dropped"

	// Capture (request transform, stream observer, writer).

	// EventCaptureRecorded counts one captured session upsert.
	EventCaptureRecorded = "capture_recorded"
	// EventCaptureNoSession counts a dropped capture without an established session.
	EventCaptureNoSession = "capture_no_session"
	// EventCaptureDroppedQueue counts a capture dropped because the writer queue is full.
	EventCaptureDroppedQueue = "capture_dropped_queue"
	// EventCaptureDroppedPending counts a pending turn evicted from the bounded map.
	EventCaptureDroppedPending = "capture_dropped_pending"
	// EventCaptureTruncated counts a transcript cut to MaxTranscriptBytes.
	EventCaptureTruncated = "capture_truncated"
	// EventCaptureOwnerMismatch counts an upsert refused for another owner's A-leg.
	EventCaptureOwnerMismatch = "capture_owner_mismatch"
	// EventCaptureForgotten counts an upsert refused by a forget fence.
	EventCaptureForgotten = "capture_forgotten"
	// EventCaptureUnbound counts an upsert refused because the A-leg has no
	// durable first-turn binding for the capture's owner.
	EventCaptureUnbound = "capture_unbound"
	// EventCaptureStale counts a capture refused because stored state covers it.
	EventCaptureStale = "capture_stale"
	// EventCaptureError counts a failed capture write.
	EventCaptureError = "capture_error"

	// Phase 1 (background extraction).

	// EventPhase1Succeeded counts one stored extraction.
	EventPhase1Succeeded = "phase1_succeeded"
	// EventPhase1Failed counts one failed extraction attempt.
	EventPhase1Failed = "phase1_failed"
	// EventPhase1LeaseLost counts an extraction whose completion lost its lease (ErrLeaseLost).
	EventPhase1LeaseLost = "phase1_lease_lost"
	// EventPhase1InvalidResultUnlinked counts a completed invalid extraction
	// result that had no host operation receipt, so no linked fact was
	// possible and the legacy usage-only failure path ran.
	EventPhase1InvalidResultUnlinked = "phase1_invalid_result_unlinked"
	// EventPhase1Purged counts rows removed by the worker's purge step.
	EventPhase1Purged = "phase1_purged"

	// Phase 2 (background consolidation).

	// EventPhase2Published counts one published consolidation.
	EventPhase2Published = "phase2_published"
	// EventPhase2PublishedEmpty counts a bank published with empty documents.
	EventPhase2PublishedEmpty = "phase2_published_empty"
	// EventPhase2Truncated counts a consolidation whose input budget dropped items.
	EventPhase2Truncated = "phase2_truncated"
	// EventPhase2Failed counts one failed consolidation attempt.
	EventPhase2Failed = "phase2_failed"
	// EventPhase2LeaseLost counts a consolidation whose publish lost its lease (ErrLeaseLost).
	EventPhase2LeaseLost = "phase2_lease_lost"
	// EventPhase2InvalidResultUnlinked counts a completed invalid consolidation
	// result that had no host operation receipt, so no linked fact was
	// possible and the legacy usage-only failure path ran.
	EventPhase2InvalidResultUnlinked = "phase2_invalid_result_unlinked"

	// Model policy (the per-phase inventory, locks and deployment facts).

	// EventModelPolicyUnverifiedInstallation counts a phase served with a
	// target whose installation the registry publication does not prove.
	EventModelPolicyUnverifiedInstallation = "model_policy_unverified_installation"
	// EventModelPolicyUnverifiedEffort counts a phase served with a
	// reasoning effort whose per-value support the provider does not publish.
	EventModelPolicyUnverifiedEffort = "model_policy_unverified_effort"
	// EventModelPolicyRefusedReasoning counts a phase refused because the
	// actual target does not prove the reasoning capability: a false
	// capability and an unknown one are both refused.
	EventModelPolicyRefusedReasoning = "model_policy_refused_reasoning"
	// EventModelPolicyRefusedNegotiatedStrip counts a proxy-memory call whose
	// requested reasoning effort the negotiated downgrade would strip. The
	// strip is converted to an explicit refusal before the provider open.
	EventModelPolicyRefusedNegotiatedStrip = "model_policy_refused_negotiated_strip"

	// Settings (the owner-scoped per-phase overrides).

	// EventSettingsRefused counts one refused settings write: a locked field
	// or a value outside the phase's allowed inventory. The event name is the
	// whole label, so no owner, phase or field becomes a label value.
	EventSettingsRefused = "settings_refused"
	// EventSettingsDependencyMasked counts one projection or claim that masked
	// an owner effort because the operator target it depended on changed. The
	// stored override is unchanged; the event name is the whole label.
	EventSettingsDependencyMasked = "settings_dependency_masked"

	// Tool, control and worker events.

	// EventToolCall counts one successful memory-tool call.
	EventToolCall = "tool_call"
	// EventControlForget counts one forget control call.
	EventControlForget = "control_forget"
	// EventWorkerPanic counts one recovered worker panic.
	EventWorkerPanic = "worker_panic"
	// EventStoreUnavailable counts a memory-store failure on a read path: a
	// bootstrap store error (alongside inject_error) or an HTTP 500.
	EventStoreUnavailable = "store_unavailable"
)

// Metrics counts proxy-memory lifecycle events. It holds one bounded
// CounterVec and retains no owner, project, session or memory content.
//
// The collector lives with its feature owner (not in generic metrics
// infrastructure) and its registration lifetime stays with the metrics bundle
// owner, which registers it through a generic prometheus.Registerer without
// importing the feature.
type Metrics struct {
	events *prometheus.CounterVec
}

// NewPrometheusCollector constructs an unregistered collector. Registration is
// the metrics bundle owner's responsibility. With no registry the collector
// stays unregistered but usable.
func NewPrometheusCollector() *Metrics {
	return &Metrics{
		events: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "aip_proxy_memory_events_total",
				Help: "Proxy memory events by bounded event name.",
			},
			[]string{"event"},
		),
	}
}

// Inc counts one event. It is nil-safe, so callers may hold a nil *Metrics
// when metrics are not wired, and its argument is always one of the Event*
// constants.
func (m *Metrics) Inc(event string) {
	if m == nil {
		return
	}
	m.events.WithLabelValues(event).Inc()
}

// Describe implements prometheus.Collector.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	if m == nil {
		return
	}
	m.events.Describe(ch)
}

// Collect implements prometheus.Collector.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	if m == nil {
		return
	}
	m.events.Collect(ch)
}

var _ prometheus.Collector = (*Metrics)(nil)
