package extensions

import (
	"context"
	"errors"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

// Policy error reason codes used by the conversion helpers. They are bounded safe
// tokens suitable for evidence and frontend classification.
const (
	PolicyReasonMalformed       = "policy_malformed"
	PolicyReasonProviderFailure = "policy_provider_failure"
	PolicyReasonTimeout         = "policy_timeout"
	PolicyReasonFailClosed      = "policy_fail_closed"
	PolicyReasonDenied          = "policy_denied"
)

// PolicyErrorFromMalformed converts a malformed-decision validation error into a
// stable [aipapi.PolicyDecisionError] of kind malformed (requirements 1.5, 6.6).
// cause is preserved for diagnostics only.
func PolicyErrorFromMalformed(stage, providerID string, cause error) error {
	return aipapi.NewPolicyMalformedError(
		stage,
		providerID,
		PolicyReasonMalformed,
		policydecision.CategoryMalformed,
		"policy decision was malformed",
		cause,
	)
}

// PolicyErrorFromProviderFailure converts a provider failure into a stable policy
// failure error when the configured failure behavior is fail-closed
// (requirements 6.1, 6.5). Parent context cancellation is never converted here; the
// caller must check cancellation first and pass a non-cancellation cause.
func PolicyErrorFromProviderFailure(stage, providerID string, behavior policydecision.FailureBehavior, cause error) error {
	if behavior == policydecision.FailureBehaviorFailOpen {
		return nil
	}
	return aipapi.NewPolicyFailureError(
		stage,
		providerID,
		PolicyReasonProviderFailure,
		policydecision.CategoryFailure,
		"policy decision provider failed",
		cause,
	)
}

// PolicyErrorFromTimeout converts a provider evaluation timeout into a stable policy
// failure error when the configured failure behavior is fail-closed
// (requirements 6.1, 6.3). Fail-open timeouts return nil so the caller records a
// skipped record instead of surfacing a denial.
func PolicyErrorFromTimeout(stage, providerID string, behavior policydecision.FailureBehavior) error {
	if behavior == policydecision.FailureBehaviorFailOpen {
		return nil
	}
	return aipapi.NewPolicyFailureError(
		stage,
		providerID,
		PolicyReasonTimeout,
		policydecision.CategoryFailure,
		"policy decision timed out",
		nil,
	)
}

// PolicyErrorFromFailClosed converts an explicit fail-closed outcome into a stable
// policy denial error (requirements 6.1, 6.6). reasonCode and clientMessage must be
// client-safe bounded values.
func PolicyErrorFromFailClosed(stage, providerID, reasonCode, clientMessage string) error {
	return aipapi.NewPolicyDeniedError(
		stage,
		providerID,
		reasonCode,
		policydecision.CategoryDenied,
		clientMessage,
		nil,
	)
}

// IsContextCancellation reports whether err is the parent request context being
// canceled or expired. The policy error conversion helpers must preserve parent
// cancellation as cancellation and never convert it into policy denial, failure, or
// malformed errors (requirement 6.4).
//
// A bare context.DeadlineExceeded is only treated as parent cancellation when the
// supplied parent context has actually expired, so a child evaluation-deadline error
// is not misclassified as parent cancellation while the parent remains active.
func IsContextCancellation(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ctx != nil && ctx.Err() != nil
	}
	return ctx != nil && ctx.Err() != nil
}
