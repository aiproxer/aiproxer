package usagelimit

// StageMetrics observes bounded authority stage latency and outcomes (req 16.5).
// Implementations must not retain request payloads or unbounded labels.
type StageMetrics interface {
	ObserveStage(stage, provider, outcome string, seconds float64)
}

const (
	StageAdmit   = "admit"
	StageSettle  = "settle"
	StageRelease = "release"
	StageQuery   = "query"

	ProviderUsageAuthority = "usage_authority"

	OutcomeOK          = "ok"
	OutcomeDeny        = "deny"
	OutcomeTimeout     = "timeout"
	OutcomeError       = "error"
	OutcomeCanceled    = "canceled"
	OutcomeDisabled    = "disabled"
	OutcomeUnavailable = "unavailable"
)
