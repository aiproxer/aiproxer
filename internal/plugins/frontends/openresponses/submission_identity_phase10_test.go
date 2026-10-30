package openresponses

import (
	"context"
	"testing"

	sdkauth "github.com/aiproxer/aiproxer/pkg/aipsdk/auth"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/submission"
)

type phase10Auth struct {
	decision sdkauth.Decision
}

func (a phase10Auth) Authenticate(context.Context, sdkauth.InboundCallMeta) (sdkauth.Decision, error) {
	return a.decision, nil
}

func TestAuthDecisionContextCarriesOnlyTrustedSubmissionAuthority(t *testing.T) {
	t.Parallel()

	authority := submission.Authority{
		Kind:          submission.KindTransportRetry,
		Source:        submission.SourceAuthenticatedContinuation,
		SubmissionID:  "submission-http",
		BillingCallID: "bc_0123456789abcdef0123456789abcdef",
	}
	ctx := contextWithAuthDecision(context.Background(), sdkauth.Decision{SubmissionAuthority: &authority})
	got, ok := submission.AuthorityFromContext(ctx)
	if !ok {
		t.Fatal("trusted submission authority was not attached to request context")
	}
	if got != authority {
		t.Fatalf("authority = %+v, want %+v", got, authority)
	}
	authority.SubmissionID = "mutated-after-context"
	got, _ = submission.AuthorityFromContext(ctx)
	if got.SubmissionID != "submission-http" {
		t.Fatalf("context authority changed through adapter pointer: %q", got.SubmissionID)
	}
}

func TestOpenResponsesPayloadMetadataCannotOverrideSubmissionAuthority(t *testing.T) {
	t.Parallel()

	authority := submission.Authority{
		Kind:         submission.KindNewSubmission,
		Source:       submission.SourceAuthenticatedCurrentTurn,
		SubmissionID: "submission-authenticated",
	}
	auth := phase10Auth{decision: sdkauth.Decision{Outcome: sdkauth.OutcomeAllow, SubmissionAuthority: &authority}}
	decoded, err := AuthenticateAndDecodeCreate(context.Background(), []byte(`{
		"model":"gpt-test",
		"input":"hello",
		"metadata":{"submission_id":"submission-spoofed"}
	}`), DecodeCreateOptions{Auth: auth, Method: "POST", Path: "/v1/responses"})
	if err != nil {
		t.Fatalf("AuthenticateAndDecodeCreate() error = %v", err)
	}
	if decoded.AuthDecision.SubmissionAuthority == nil {
		t.Fatal("decoded request lost authenticated submission authority")
	}
	if decoded.AuthDecision.SubmissionAuthority.SubmissionID != authority.SubmissionID {
		t.Fatalf("decoded authority = %q, want authenticated %q", decoded.AuthDecision.SubmissionAuthority.SubmissionID, authority.SubmissionID)
	}
	if decoded.Call.Session.Metadata["submission_id"] != "submission-spoofed" {
		t.Fatal("test fixture did not retain spoofed metadata for the trust-boundary assertion")
	}
}
