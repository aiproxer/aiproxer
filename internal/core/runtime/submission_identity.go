package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/submission"
)

// bindSubmissionAuthority consumes only the authority projected by a trusted
// frontend/auth adapter. The canonical call is used solely to supply the
// runtime-owned scope; request metadata is deliberately not consulted.
func bindSubmissionAuthority(ctx context.Context, ibt *identityBoundTurn, call *aipapi.Call) (submission.BindingResult, error) {
	if ibt == nil || call == nil {
		return submission.BindingResult{}, fmt.Errorf("%w: runtime identity is required", submission.ErrInvalidAuthority)
	}
	input := submission.BindingInput{Scope: submission.Scope{
		TenantID:    ibt.scope.TenantID.String(),
		PrincipalID: ibt.scope.PrincipalID.String(),
		SessionID:   strings.TrimSpace(ibt.preSession.AuthoritativeSessionID),
		ALegID:      strings.TrimSpace(ibt.aLeg.ALegID),
		CallID:      strings.TrimSpace(call.ID),
	}}
	if authority, ok := submission.AuthorityFromContext(ctx); ok {
		return submission.Bind(input, &authority)
	}
	return submission.Bind(input, nil)
}

// completeSubmissionAuthority binds the store and BillingCallID dimensions
// after those runtime-owned facts have been frozen. It is intentionally a
// no-op for unsupported/incomplete attribution, allowing ordinary inference
// to proceed without a submission-priced offer.
func completeSubmissionAuthority(prep *preparedRequest, ibt *identityBoundTurn) error {
	if prep == nil || ibt == nil || !prep.submission.Trusted() || prep.submission.Authority.Kind == submission.KindLocalCommand {
		return nil
	}
	if prep.call == nil {
		return fmt.Errorf("%w: canonical call is required", submission.ErrInvalidAuthority)
	}
	if prep.billingCallID == "" {
		return fmt.Errorf("%w: billing call identity is required", submission.ErrIncomplete)
	}
	authority := prep.submission.Authority.Clone()
	if isSubmissionContinuation(authority.Kind) {
		if existing := strings.TrimSpace(authority.BillingCallID); existing != "" && existing != prep.billingCallID.String() {
			return fmt.Errorf("%w: billing call identity changed", submission.ErrScopeMismatch)
		}
		// Continuations reuse the already-open invocation. New submissions and
		// follow-ups intentionally keep this field empty: their fresh runtime
		// BillingCallID is not an authority claim and must not look like a
		// continuation to the binding contract.
		authority.BillingCallID = prep.billingCallID.String()
	}
	result, err := submission.Bind(submission.BindingInput{Scope: submission.Scope{
		TenantID:    ibt.scope.TenantID.String(),
		PrincipalID: ibt.scope.PrincipalID.String(),
		SessionID:   strings.TrimSpace(ibt.preSession.AuthoritativeSessionID),
		ALegID:      strings.TrimSpace(ibt.aLeg.ALegID),
		StoreID:     strings.TrimSpace(prep.billingStoreID),
		CallID:      strings.TrimSpace(prep.call.ID),
	}}, &authority)
	if err != nil {
		return err
	}
	if !result.Trusted() {
		return fmt.Errorf("%w: bound submission result is %s", submission.ErrIncomplete, result.Status)
	}
	prep.submission = result
	return nil
}

func submissionIDForBilling(result submission.BindingResult) string {
	if !result.Billable() {
		return ""
	}
	return strings.TrimSpace(result.Authority.SubmissionID)
}

func localSubmissionResult(sc scope.PrincipalScopeView, aLegID, callID string) submission.BindingResult {
	authority := submission.Authority{
		Kind:   submission.KindLocalCommand,
		Source: submission.SourceLocalCommand,
		Scope: submission.Scope{
			TenantID:    sc.TenantID.String(),
			PrincipalID: sc.PrincipalID.String(),
			ALegID:      strings.TrimSpace(aLegID),
			CallID:      strings.TrimSpace(callID),
		},
	}
	result, err := submission.Bind(submission.BindingInput{Scope: authority.Scope}, &authority)
	if err != nil {
		return submission.BindingResult{Status: submission.StatusIncomplete, ReasonCode: "local_submission_identity_invalid"}
	}
	return result
}

func isSubmissionContinuation(kind submission.Kind) bool {
	switch kind {
	case submission.KindToolContinuation, submission.KindHistoricalReplay, submission.KindTransportRetry:
		return true
	default:
		return false
	}
}
