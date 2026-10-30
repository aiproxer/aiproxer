package runtime

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/submission"
)

func TestStampBillingCallIDReusesOnlyTrustedContinuationIdentity(t *testing.T) {
	t.Parallel()

	existing := usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	prep := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:          submission.KindTransportRetry,
			Source:        submission.SourceAuthenticatedContinuation,
			SubmissionID:  "submission-retry",
			BillingCallID: existing.String(),
		},
	}}
	if err := stampBillingCallID(prep); err != nil {
		t.Fatalf("stampBillingCallID() error = %v", err)
	}
	if prep.billingCallID != existing {
		t.Fatalf("billing call ID = %q, want trusted existing %q", prep.billingCallID, existing)
	}

	followUp := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindFollowUp,
			Source:       submission.SourceAuthenticatedCurrentTurn,
			SubmissionID: "submission-follow-up",
		},
	}}
	if err := stampBillingCallID(followUp); err != nil {
		t.Fatalf("follow-up stampBillingCallID() error = %v", err)
	}
	if followUp.billingCallID == "" || followUp.billingCallID == existing {
		t.Fatalf("follow-up billing call ID = %q, want a fresh ID", followUp.billingCallID)
	}

	toolContinuation := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindToolContinuation,
			Source:       submission.SourceAuthenticatedContinuation,
			SubmissionID: "submission-retry",
		},
	}}
	if err := stampBillingCallID(toolContinuation); err != nil {
		t.Fatalf("tool continuation stampBillingCallID() error = %v", err)
	}
	if toolContinuation.billingCallID == "" || toolContinuation.billingCallState.submissionIdentity() != "submission-retry" {
		t.Fatalf("tool continuation state = %+v, want fresh call with stable submission identity", toolContinuation.billingCallState)
	}

	firstSubmission := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindNewSubmission,
			Source:       submission.SourceAuthenticatedCurrentTurn,
			SubmissionID: "submission-1",
		},
	}}
	if err := stampBillingCallID(firstSubmission); err != nil {
		t.Fatalf("first submission stampBillingCallID() error = %v", err)
	}
	firstCallID := firstSubmission.billingCallID
	followUpSubmission := &preparedRequest{submission: submission.BindingResult{
		Status: submission.StatusTrusted,
		Authority: submission.Authority{
			Kind:         submission.KindFollowUp,
			Source:       submission.SourceAuthenticatedCurrentTurn,
			SubmissionID: "submission-2",
		},
	}}
	if err := stampBillingCallID(followUpSubmission); err != nil {
		t.Fatalf("resumed follow-up stampBillingCallID() error = %v", err)
	}
	if followUpSubmission.billingCallID == firstCallID || firstSubmission.billingCallState.submissionIdentity() != "submission-1" || followUpSubmission.billingCallState.submissionIdentity() != "submission-2" {
		t.Fatalf("follow-up state reopened prior submission: first=%q/%q follow-up=%q/%q", firstCallID, firstSubmission.billingCallState.submissionIdentity(), followUpSubmission.billingCallID, followUpSubmission.billingCallState.submissionIdentity())
	}
}

func TestStampBillingCallIDRejectsSubmissionIdentityChangeOnSameCall(t *testing.T) {
	t.Parallel()

	callID := usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	state := newBillingCallState(callID)
	state.submissionID = "submission-1"
	prep := &preparedRequest{
		billingCallID:    callID,
		billingCallState: state,
		submission: submission.BindingResult{
			Status: submission.StatusTrusted,
			Authority: submission.Authority{
				Kind:          submission.KindToolContinuation,
				Source:        submission.SourceAuthenticatedContinuation,
				SubmissionID:  "submission-2",
				BillingCallID: callID.String(),
			},
		},
	}
	if err := stampBillingCallID(prep); !errors.Is(err, submission.ErrScopeMismatch) {
		t.Fatalf("stampBillingCallID() error = %v, want ErrScopeMismatch", err)
	}
}

func TestCompleteSubmissionAuthorityKeepsFreshFollowUpCallIdentitySeparate(t *testing.T) {
	t.Parallel()

	callID := usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	prep := &preparedRequest{
		call:          &aipapi.Call{ID: "call-follow-up"},
		billingCallID: callID,
		recvTurnFacts: recvTurnFacts{billingStoreID: "store-1"},
		submission: submission.BindingResult{
			Status: submission.StatusTrusted,
			Authority: submission.Authority{
				Kind:         submission.KindFollowUp,
				Source:       submission.SourceAuthenticatedCurrentTurn,
				SubmissionID: "submission-follow-up",
			},
		},
	}
	ibt := &identityBoundTurn{
		aLeg:       b2bua.ALegRecord{ALegID: "a-leg-1"},
		preSession: session.SessionView{ALegID: "a-leg-1"},
	}
	if err := completeSubmissionAuthority(prep, ibt); err != nil {
		t.Fatalf("completeSubmissionAuthority() error = %v", err)
	}
	if !prep.submission.Trusted() || prep.submission.Authority.SubmissionID != "submission-follow-up" {
		t.Fatalf("submission result = %+v, want trusted follow-up", prep.submission)
	}
	if prep.submission.Authority.BillingCallID != "" {
		t.Fatalf("fresh follow-up authority reused BillingCallID %q", prep.submission.Authority.BillingCallID)
	}
}

func TestBillingLegRecordCarriesTrustedSubmissionIdentity(t *testing.T) {
	t.Parallel()

	callID := usageevidence.BillingCallID("bc_0123456789abcdef0123456789abcdef")
	record := billingLegRecord(billingLegDraft{
		callID: callID, submissionID: "submission-leg", aLegID: "a-leg", storeID: "store",
		bLegID: "b-leg", seq: 1,
	})
	if record.SubmissionID != "submission-leg" {
		t.Fatalf("record submission ID = %q, want %q", record.SubmissionID, "submission-leg")
	}
}

func TestSubmissionIdentityClaimCannotOverrideTrustedRecordLinkage(t *testing.T) {
	t.Parallel()

	incoming := []metering.Observation{{
		Subject:     metering.SubjectRef{SubmissionID: "submission-spoofed"},
		Correlation: metering.CorrelationV2{SubmissionID: "submission-spoofed"},
	}}
	got := retainSubmissionLinkedObservations(incoming, "submission-authenticated")
	if len(got) != 0 {
		t.Fatalf("spoofed observation claim was retained: %+v", got)
	}
	if incoming[0].Subject.SubmissionID != "submission-spoofed" || incoming[0].Correlation.SubmissionID != "submission-spoofed" {
		t.Fatal("submission filtering mutated caller-owned observation")
	}
	cleared := retainSubmissionLinkedObservations(incoming, "")
	if len(cleared) != 0 {
		t.Fatalf("unsupported adapter observation claim was retained: %+v", cleared)
	}
}
