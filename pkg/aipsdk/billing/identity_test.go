package billing_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/billing"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

func identityScope() scope.PrincipalScopeView {
	return scope.PrincipalScopeView{
		SubjectKind: scope.SubjectHuman,
		PrincipalID: scope.Known("user-1"),
		TenantID:    scope.Known("tenant-1"),
		Origin:      scope.OriginClient,
	}
}

func identityPolicy() economics.PolicySnapshotRef {
	return economics.PolicySnapshotRef{
		VersionRef: economics.VersionRef{ID: "policy-snap", Version: "v1"},
		PolicyID:   "policy-1",
	}
}

func identityTariff() economics.RatingSnapshotRef {
	return economics.RatingSnapshotRef{
		VersionRef: economics.VersionRef{ID: "tariff-snap", Version: "v1"},
	}
}

func identitySubject() metering.SubjectRef {
	return metering.SubjectRef{Kind: metering.SubjectBillingCall, StoreID: "store-test", BillingCallID: "call-1"}
}

func identityQuote() economics.ExposureQuote {
	return economics.ExposureQuote{
		ID:           "q-1",
		Version:      1,
		Subject:      identitySubject(),
		Policy:       identityPolicy(),
		Tariff:       identityTariff(),
		Completeness: economics.CompletenessComplete,
	}
}

func identityAdmissionInput() billing.ExposureAdmissionInput {
	return billing.ExposureAdmissionInput{
		Subject:         identitySubject(),
		Scope:           identityScope(),
		AccountID:       "acct-1",
		Quote:           identityQuote(),
		ExecutionLimits: []economics.Limit{{Name: "max-output", Value: 100, Unit: "token"}},
	}
}

// TestCreditResultMatchesInput locks the cheap-screen identity invariant: a
// result answers exactly the submitted store and account.
func TestCreditResultMatchesInput(t *testing.T) {
	t.Parallel()
	in := billing.CreditScreenInput{Scope: identityScope(), StoreID: "store-test", AccountID: "acct-1", Policy: identityPolicy()}
	ok := billing.CreditScreenResult{Decision: billing.CreditAllow, StoreID: "store-test", AccountID: "acct-1"}
	if !ok.MatchesInput(in) {
		t.Fatal("exact store/account result must match")
	}
	for name, other := range map[string]billing.CreditScreenResult{
		"wrong store":   {Decision: billing.CreditAllow, StoreID: "store-foreign", AccountID: "acct-1"},
		"wrong account": {Decision: billing.CreditDeny, StoreID: "store-test", AccountID: "acct-foreign"},
	} {
		if other.MatchesInput(in) {
			t.Fatalf("%s: foreign result must not match", name)
		}
	}
}

// TestCreditResultMismatchIsTyped ensures the mismatch surfaces through a
// stable sentinel suitable for errors.Is.
func TestCreditResultMismatchIsTyped(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []error{billing.ErrCreditResultMismatch, billing.ErrQuoteMismatch, billing.ErrAdmissionMismatch} {
		wrapped := fmt.Errorf("handoff: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("%v must survive wrapping for errors.Is", sentinel)
		}
	}
}

// TestAdmissionInputRequiresQuoteSubjectIdentity locks the atomic-admission
// boundary: the frozen quote must name exactly the requested call subject.
func TestAdmissionInputRequiresQuoteSubjectIdentity(t *testing.T) {
	t.Parallel()
	if err := identityAdmissionInput().Validate(); err != nil {
		t.Fatalf("fixture input: %v", err)
	}
	for name, mutate := range map[string]func(*economics.ExposureQuote){
		"foreign call":  func(q *economics.ExposureQuote) { q.Subject.BillingCallID = "call-foreign" },
		"foreign store": func(q *economics.ExposureQuote) { q.Subject.StoreID = "store-foreign" },
		"foreign kind": func(q *economics.ExposureQuote) {
			q.Subject.Kind = metering.SubjectBLeg
			q.Subject.BLegID = "bleg-foreign"
		},
	} {
		in := identityAdmissionInput()
		mutate(&in.Quote)
		err := in.Validate()
		if !errors.Is(err, billing.ErrInvalidAdmission) {
			t.Fatalf("%s: err=%v, want ErrInvalidAdmission", name, err)
		}
		if !errors.Is(err, billing.ErrQuoteMismatch) {
			t.Fatalf("%s: err=%v, want ErrQuoteMismatch", name, err)
		}
	}
}

// TestExposureHandleMatchesAdmission locks the handle identity invariant:
// store and call must equal the request, and a carried quote ID must equal
// the admitted quote. A quoteless ID check is skipped only when the quote
// itself carries no ID.
func TestExposureHandleMatchesAdmission(t *testing.T) {
	t.Parallel()
	in := identityAdmissionInput()
	handle := billing.ExposureHandle{StoreID: "store-test", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-1"}
	if !handle.MatchesAdmission(in) {
		t.Fatal("exact handle must match")
	}
	for name, other := range map[string]billing.ExposureHandle{
		"wrong store": {StoreID: "store-foreign", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-1"},
		"wrong call":  {StoreID: "store-test", BillingCallID: "call-foreign", ExposureID: "exp-1", QuoteID: "q-1"},
		"wrong quote": {StoreID: "store-test", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-foreign"},
	} {
		if other.MatchesAdmission(in) {
			t.Fatalf("%s: foreign handle must not match", name)
		}
	}
	idless := in
	idless.Quote.ID = ""
	if !(billing.ExposureHandle{StoreID: "store-test", BillingCallID: "call-1", ExposureID: "exp-1", QuoteID: "q-1"}).MatchesAdmission(idless) {
		t.Fatal("quote-ID check applies only when the quote carries an ID")
	}
}

// TestExposureAdmissionInputRequiresScopeAndAccount locks the per-customer
// admission boundary: the trusted scope must carry a known principal and
// the credited account must be present for allowance binding.
func TestExposureAdmissionInputRequiresScopeAndAccount(t *testing.T) {
	t.Parallel()
	if err := identityAdmissionInput().Validate(); err != nil {
		t.Fatalf("fixture input: %v", err)
	}
	anonymous := identityAdmissionInput()
	anonymous.Scope = scope.PrincipalScopeView{}
	if err := anonymous.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("anonymous scope err=%v, want ErrInvalidAdmission", err)
	}
	noAccount := identityAdmissionInput()
	noAccount.AccountID = "  "
	if err := noAccount.Validate(); !errors.Is(err, billing.ErrInvalidAdmission) {
		t.Fatalf("missing account err=%v, want ErrInvalidAdmission", err)
	}
}
