package submission

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func phase10BindingInput() BindingInput {
	return BindingInput{Scope: Scope{
		TenantID:    "tenant-1",
		PrincipalID: "principal-1",
		SessionID:   "session-1",
		ALegID:      "a-leg-1",
		StoreID:     "store-1",
		CallID:      "call-1",
	}}
}

func phase10Authority(kind Kind, source Source, submissionID, billingCallID string) *Authority {
	in := phase10BindingInput()
	return &Authority{
		Kind:          kind,
		Source:        source,
		SubmissionID:  submissionID,
		BillingCallID: billingCallID,
		Scope:         in.Scope,
	}
}

func TestBindSubmissionIdentityAcceptanceVectors(t *testing.T) {
	t.Parallel()

	const submissionID = "submission-1"
	const billingCallID = "bc_0123456789abcdef0123456789abcdef"
	cases := []struct {
		name          string
		kind          Kind
		source        Source
		submissionID  string
		billingCallID string
		wantStatus    Status
		wantBillable  bool
	}{
		{name: "new authenticated turn", kind: KindNewSubmission, source: SourceAuthenticatedCurrentTurn, submissionID: submissionID, wantStatus: StatusTrusted, wantBillable: true},
		{name: "tool continuation", kind: KindToolContinuation, source: SourceAuthenticatedContinuation, submissionID: submissionID, billingCallID: billingCallID, wantStatus: StatusTrusted, wantBillable: true},
		{name: "historical replay", kind: KindHistoricalReplay, source: SourceAuthenticatedContinuation, submissionID: submissionID, billingCallID: billingCallID, wantStatus: StatusTrusted, wantBillable: true},
		{name: "transport retry", kind: KindTransportRetry, source: SourceAuthenticatedContinuation, submissionID: submissionID, billingCallID: billingCallID, wantStatus: StatusTrusted, wantBillable: true},
		{name: "new follow-up after done", kind: KindFollowUp, source: SourceAuthenticatedCurrentTurn, submissionID: "submission-2", wantStatus: StatusTrusted, wantBillable: true},
		{name: "explicit local command", kind: KindLocalCommand, source: SourceLocalCommand, wantStatus: StatusTrusted, wantBillable: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := Bind(phase10BindingInput(), phase10Authority(tc.kind, tc.source, tc.submissionID, tc.billingCallID))
			if err != nil {
				t.Fatalf("Bind() error = %v", err)
			}
			if result.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (reason %q)", result.Status, tc.wantStatus, result.ReasonCode)
			}
			if got := result.Billable(); got != tc.wantBillable {
				t.Fatalf("billable = %v, want %v", got, tc.wantBillable)
			}
			if result.Authority.Kind != tc.kind {
				t.Fatalf("kind = %q, want %q", result.Authority.Kind, tc.kind)
			}
			if result.Authority.SubmissionID != tc.submissionID {
				t.Fatalf("submission ID = %q, want %q", result.Authority.SubmissionID, tc.submissionID)
			}
			if result.Authority.BillingCallID != tc.billingCallID {
				t.Fatalf("billing call ID = %q, want %q", result.Authority.BillingCallID, tc.billingCallID)
			}
		})
	}
}

func TestBindSubmissionIdentityKeepsOneToolLoopOnOneSubmission(t *testing.T) {
	t.Parallel()

	for _, kind := range []Kind{KindToolContinuation, KindHistoricalReplay, KindTransportRetry} {
		result, err := Bind(phase10BindingInput(), phase10Authority(kind, SourceAuthenticatedContinuation, "submission-1", ""))
		if err != nil {
			t.Fatalf("Bind(%q) error = %v", kind, err)
		}
		if !result.Billable() || result.Authority.SubmissionID != "submission-1" {
			t.Fatalf("Bind(%q) result = %+v, want one billable submission identity", kind, result)
		}
	}
}

func TestBindSubmissionIdentityRejectsUntrustedAndCrossScopeAuthorities(t *testing.T) {
	t.Parallel()

	t.Run("client override is rejected", func(t *testing.T) {
		result, err := Bind(phase10BindingInput(), phase10Authority(KindNewSubmission, SourceClient, "spoofed", ""))
		if !errors.Is(err, ErrUntrustedAuthority) {
			t.Fatalf("error = %v, want ErrUntrustedAuthority", err)
		}
		if result.Status != StatusRejected {
			t.Fatalf("status = %q, want %q", result.Status, StatusRejected)
		}
	})

	for _, field := range []string{"tenant", "principal", "session", "a-leg", "store", "call"} {
		t.Run("cross-"+field, func(t *testing.T) {
			t.Parallel()
			in := phase10BindingInput()
			authority := phase10Authority(KindToolContinuation, SourceAuthenticatedContinuation, "submission-1", "bc_0123456789abcdef0123456789abcdef")
			switch field {
			case "tenant":
				authority.Scope.TenantID = "tenant-foreign"
			case "principal":
				authority.Scope.PrincipalID = "principal-foreign"
			case "session":
				authority.Scope.SessionID = "session-foreign"
			case "a-leg":
				authority.Scope.ALegID = "a-leg-foreign"
			case "store":
				authority.Scope.StoreID = "store-foreign"
			case "call":
				authority.Scope.CallID = "call-foreign"
			}
			result, err := Bind(in, authority)
			if !errors.Is(err, ErrScopeMismatch) {
				t.Fatalf("error = %v, want ErrScopeMismatch", err)
			}
			if result.Status != StatusRejected {
				t.Fatalf("status = %q, want %q", result.Status, StatusRejected)
			}
		})
	}
}

func TestBindRejectsCrossScopeEvenWhenContinuationIsIncomplete(t *testing.T) {
	t.Parallel()

	authority := phase10Authority(KindToolContinuation, SourceAuthenticatedContinuation, "submission-1", "")
	authority.Scope.TenantID = "tenant-foreign"
	result, err := Bind(phase10BindingInput(), authority)
	if !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("error = %v, want ErrScopeMismatch", err)
	}
	if result.Status != StatusRejected {
		t.Fatalf("status = %q, want %q", result.Status, StatusRejected)
	}
}

func TestBindRejectsMalformedRuntimeScope(t *testing.T) {
	t.Parallel()

	input := phase10BindingInput()
	input.Scope.TenantID = "tenant\n1"
	result, err := Bind(input, phase10Authority(KindNewSubmission, SourceAuthenticatedCurrentTurn, "submission-1", ""))
	if !errors.Is(err, ErrInvalidAuthority) {
		t.Fatalf("error = %v, want ErrInvalidAuthority", err)
	}
	if result.Status != StatusRejected {
		t.Fatalf("status = %q, want %q", result.Status, StatusRejected)
	}
}

func TestBindSubmissionIdentityMakesMissingAndLifecycleStatesExplicit(t *testing.T) {
	t.Parallel()

	t.Run("tool continuation may receive a fresh runtime call identity", func(t *testing.T) {
		t.Parallel()
		result, err := Bind(phase10BindingInput(), phase10Authority(KindToolContinuation, SourceAuthenticatedContinuation, "submission-1", ""))
		if err != nil {
			t.Fatalf("Bind() error = %v, want trusted submission identity", err)
		}
		if result.Status != StatusTrusted || !result.Billable() {
			t.Fatalf("result = %+v, want trusted billable continuation", result)
		}
	})

	t.Run("missing submission identity is incomplete", func(t *testing.T) {
		t.Parallel()
		result, err := Bind(phase10BindingInput(), phase10Authority(KindNewSubmission, SourceAuthenticatedCurrentTurn, "", ""))
		if err != nil {
			t.Fatalf("Bind() error = %v, want explicit status without rejecting inference", err)
		}
		if result.Status != StatusIncomplete {
			t.Fatalf("status = %q, want %q", result.Status, StatusIncomplete)
		}
		if result.Billable() {
			t.Fatal("incomplete submission must not be billable")
		}
	})

	t.Run("absent authority is unsupported", func(t *testing.T) {
		t.Parallel()
		result, err := Bind(phase10BindingInput(), nil)
		if err != nil {
			t.Fatalf("Bind() error = %v, want explicit status", err)
		}
		if result.Status != StatusUnsupported {
			t.Fatalf("status = %q, want %q", result.Status, StatusUnsupported)
		}
		if result.Billable() {
			t.Fatal("unsupported attribution must not be billable")
		}
	})

	t.Run("follow-up cannot reopen a prior billing call", func(t *testing.T) {
		t.Parallel()
		result, err := Bind(phase10BindingInput(), phase10Authority(KindFollowUp, SourceAuthenticatedCurrentTurn, "submission-2", "bc_0123456789abcdef0123456789abcdef"))
		if !errors.Is(err, ErrLifecycleConflict) {
			t.Fatalf("error = %v, want ErrLifecycleConflict", err)
		}
		if result.Status != StatusRejected {
			t.Fatalf("status = %q, want %q", result.Status, StatusRejected)
		}
	})
}

func TestSubmissionAuthorityContextClonesAndConcurrentBindingIsDeterministic(t *testing.T) {
	t.Parallel()

	authority := phase10Authority(KindNewSubmission, SourceAuthenticatedCurrentTurn, "submission-1", "")
	ctx := WithAuthority(context.Background(), *authority)
	got, ok := AuthorityFromContext(ctx)
	if !ok || got != *authority {
		t.Fatalf("context authority = %+v, ok=%v, want %+v", got, ok, *authority)
	}
	got.SubmissionID = "mutated"
	again, _ := AuthorityFromContext(ctx)
	if again.SubmissionID != authority.SubmissionID {
		t.Fatalf("context value was mutable through returned copy: %q", again.SubmissionID)
	}

	const workers = 32
	results := make(chan BindingResult, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			result, err := Bind(phase10BindingInput(), phase10Authority(KindToolContinuation, SourceAuthenticatedContinuation, "submission-1", "bc_0123456789abcdef0123456789abcdef"))
			if err != nil {
				t.Errorf("Bind() error = %v", err)
				return
			}
			results <- result
		})
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.Status != StatusTrusted || result.Authority.SubmissionID != "submission-1" || result.Authority.BillingCallID != "bc_0123456789abcdef0123456789abcdef" {
			t.Fatalf("nondeterministic result: %+v", result)
		}
	}
}
