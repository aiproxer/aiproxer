package extensions

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// Metrics stage labels for observability (distinct constant names from legal pipeline ids in failure_policy.go).
const (
	MetricsStageSessionOpen               = "session_open"
	MetricsStageWorkspaceResolve          = "workspace_resolve"
	MetricsStageSecretGuard               = "secret_guard"
	MetricsStageToolCatalog               = "tool_catalog"
	MetricsStageRequestTransform          = "request_transform"
	MetricsStagePreRequest                = "pre_request"
	MetricsStageCandidateAttemptTransform = "candidate_attempt_transform"
	MetricsStageFinalStreamObservation    = "final_stream_observation"
	MetricsStageCompactionPreservation    = "compaction_preservation"

	StageOutcomeOK          = "ok"
	StageOutcomeError       = "error"
	StageOutcomeFailOpen    = "fail_open"
	StageOutcomeExcluded    = "excluded"
	StageMetricLabelUnknown = "unknown"
)

// StageMetrics receives extension pipeline timing and fail-open skip counts (optional on [Executor]).
type StageMetrics interface {
	ObserveStage(stage, outcome string, seconds float64)
	IncFailOpenSkip(stage string)
}

// StageCountByteMetrics is an optional StageMetrics extension for content-safe counts/bytes.
type StageCountByteMetrics interface {
	AddStageCount(stage, outcome string, n int64)
	ObserveStageBytes(stage, outcome string, n int64)
}

func knownStageMetricLabel(stage string) bool {
	switch stage {
	case MetricsStageSessionOpen, MetricsStageWorkspaceResolve, MetricsStageSecretGuard,
		MetricsStageToolCatalog, MetricsStageRequestTransform, MetricsStagePreRequest,
		MetricsStageCandidateAttemptTransform, MetricsStageFinalStreamObservation,
		MetricsStageCompactionPreservation,
		StageToolEventReaction, StageMetricLabelUnknown:
		return true
	default:
		return false
	}
}

func knownStageMetricOutcome(outcome string) bool {
	switch outcome {
	case StageOutcomeOK, StageOutcomeError, StageOutcomeFailOpen, StageOutcomeExcluded, StageMetricLabelUnknown:
		return true
	default:
		return false
	}
}

// CanonicalStageMetricLabels collapses unbounded/sensitive stage or outcome label values to unknown
// so metric cardinality stays bounded (samples are kept under unknown, not dropped).
func CanonicalStageMetricLabels(stage, outcome string) (string, string) {
	if !knownStageMetricLabel(stage) {
		stage = StageMetricLabelUnknown
	}
	if !knownStageMetricOutcome(outcome) {
		outcome = StageMetricLabelUnknown
	}
	return stage, outcome
}

// RecordStageObservation records duration and optional safe counts/bytes with bounded labels.
// count<=0 and bytes<=0 are ignored so sinks never receive non-positive counter adds.
func RecordStageObservation(obs StageMetrics, stage, outcome string, seconds float64, count, bytes int64) {
	if obs == nil {
		return
	}
	stage, outcome = CanonicalStageMetricLabels(stage, outcome)
	obs.ObserveStage(stage, outcome, seconds)
	if cb, ok := obs.(StageCountByteMetrics); ok {
		if count > 0 {
			cb.AddStageCount(stage, outcome, count)
		}
		if bytes > 0 {
			cb.ObserveStageBytes(stage, outcome, bytes)
		}
	}
}

// SafeEventObserveBytes returns content-safe byte totals for an observed stream event.
func SafeEventObserveBytes(ev aipapi.Event) int64 {
	return aipapi.SaturatingAddInt64(int64(len(ev.Delta)), int64(len(ev.Signature)))
}

// SafeCallReasoningObserveBytes returns aggregate reasoning payload byte lengths for a call.
func SafeCallReasoningObserveBytes(call *aipapi.Call) int64 {
	return aipapi.CallReasoningPayloadBytes(call)
}
