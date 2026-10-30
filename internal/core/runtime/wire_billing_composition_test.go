package runtime

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// isEmptyCallExposure reports whether e is the zero CallExposure. Direct ==
// comparison is unavailable because CallExposure carries the RouteTariffs
// slice; DeepEqual preserves the field-wise emptiness intent including it.
func isEmptyCallExposure(e billingport.CallExposure) bool {
	return reflect.DeepEqual(e, billingport.CallExposure{})
}

// capturingTerminalUsageSink records sealed Call and Leg records for testing.
type capturingTerminalUsageSink struct {
	mu    sync.Mutex
	calls []usageevidence.CallUsageRecord
	legs  []usageevidence.CallLegUsageRecord
}

func (s *capturingTerminalUsageSink) AppendCall(_ context.Context, record usageevidence.CallUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, record)
	return nil
}

func (s *capturingTerminalUsageSink) AppendLeg(_ context.Context, record usageevidence.CallLegUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.legs = append(s.legs, record)
	return nil
}

// TestWireBilling_CheckCheapCredit_Parity proves that checkWireCheapCredit preserves
// exact canonical credit screen error classes and behavior while operating on bounded facts
// without materializing or inspecting a aipapi.Call (Requirements 15.6, 19).
func TestWireBilling_CheckCheapCredit_Parity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	principalScope := scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-wire-001"),
	}

	stockIdentity := BillingIdentity{
		WireBounded: true,
		WireAccountID: func(_ context.Context, sc scope.PrincipalScopeView) string {
			if sc.PrincipalID.IsKnown() {
				return strings.TrimSpace(sc.PrincipalID.String())
			}
			return ""
		},
		AccountID: func(_ context.Context, _ aipapi.Call) string {
			return "principal-wire-001"
		},
	}

	t.Run("nil gate is no-op", func(t *testing.T) {
		t.Parallel()
		ex := TestExecutor()
		ex.BillingCreditGate = nil
		ex.BillingIdentity = stockIdentity

		err := ex.checkWireCheapCredit(ctx, WireBillingCreditArgs{
			Scope: principalScope,
		})
		if err != nil {
			t.Fatalf("expected nil for unconfigured credit gate, got %v", err)
		}
	})

	t.Run("successful credit screen on stock bounded identity", func(t *testing.T) {
		t.Parallel()
		var checkedAccount string
		ex := TestExecutor()
		ex.BillingCreditGate = creditGateFunc(func(_ context.Context, acct string) error {
			checkedAccount = acct
			return nil
		})
		ex.BillingIdentity = stockIdentity

		err := ex.checkWireCheapCredit(ctx, WireBillingCreditArgs{
			Scope: principalScope,
		})
		if err != nil {
			t.Fatalf("unexpected credit check error: %v", err)
		}
		if checkedAccount != "principal-wire-001" {
			t.Fatalf("checkedAccount = %q, want %q", checkedAccount, "principal-wire-001")
		}
	})

	t.Run("denied credit screen preserves exact error class", func(t *testing.T) {
		t.Parallel()
		ex := TestExecutor()
		ex.BillingCreditGate = creditGateFunc(func(_ context.Context, _ string) error {
			return billingport.ErrCreditScreenDenied
		})
		ex.BillingIdentity = stockIdentity

		err := ex.checkWireCheapCredit(ctx, WireBillingCreditArgs{
			Scope: principalScope,
		})
		if err == nil {
			t.Fatal("expected credit denied error, got nil")
		}
		if !errors.Is(err, ErrBillingAdmissionDenied) {
			t.Fatalf("expected ErrBillingAdmissionDenied, got %v", err)
		}
		if !errors.Is(err, ErrBillingCreditScreenDenied) {
			t.Fatalf("expected ErrBillingCreditScreenDenied, got %v", err)
		}
	})

	t.Run("unavailable credit screen preserves exact error class", func(t *testing.T) {
		t.Parallel()
		ex := TestExecutor()
		ex.BillingCreditGate = creditGateFunc(func(_ context.Context, _ string) error {
			return errors.New("transient db error")
		})
		ex.BillingIdentity = stockIdentity

		err := ex.checkWireCheapCredit(ctx, WireBillingCreditArgs{
			Scope: principalScope,
		})
		if err == nil {
			t.Fatal("expected credit unavailable error, got nil")
		}
		if !errors.Is(err, ErrBillingAdmissionDenied) {
			t.Fatalf("expected ErrBillingAdmissionDenied, got %v", err)
		}
		if !errors.Is(err, ErrBillingCreditScreenUnavailable) {
			t.Fatalf("expected ErrBillingCreditScreenUnavailable, got %v", err)
		}
	})

	t.Run("empty account identity fails closed", func(t *testing.T) {
		t.Parallel()
		ex := TestExecutor()
		ex.BillingCreditGate = creditGateFunc(func(_ context.Context, _ string) error { return nil })
		ex.BillingIdentity = BillingIdentity{
			WireBounded: true,
			WireAccountID: func(_ context.Context, _ scope.PrincipalScopeView) string {
				return ""
			},
		}

		err := ex.checkWireCheapCredit(ctx, WireBillingCreditArgs{
			Scope: scope.PrincipalScopeView{},
		})
		if err == nil {
			t.Fatal("expected error for empty account, got nil")
		}
		if !errors.Is(err, ErrBillingAdmissionDenied) || !errors.Is(err, ErrBillingCreditScreenDenied) {
			t.Fatalf("expected ErrBillingAdmissionDenied + ErrBillingCreditScreenDenied, got %v", err)
		}
		if !strings.Contains(err.Error(), "account identity is empty") {
			t.Fatalf("error %q should mention empty account identity", err.Error())
		}
	})
}

// TestWireBilling_CustomBillingIdentity_DeclinesToCanonical proves that arbitrary
// custom BillingIdentity Call callbacks remain canonical blockers by default (Requirements 15.6, 19).
func TestWireBilling_CustomBillingIdentity_DeclinesToCanonical(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	principalScope := scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-wire-002"),
	}

	customIdentity := BillingIdentity{
		WireBounded: false, // custom callback not certified as wire-bounded
		AccountID: func(_ context.Context, call aipapi.Call) string {
			if call.ID == "" {
				return ""
			}
			return "custom-" + call.ID
		},
		CustomerPricingRef: func(_ context.Context, call aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "custom-pricing"}
		},
		ChargePolicyRef: func(_ context.Context, call aipapi.Call) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "custom-policy"}
		},
	}

	if !customIdentity.HasCustomCallCallbacks() {
		t.Fatal("custom BillingIdentity must report HasCustomCallCallbacks() == true")
	}

	ex := TestExecutor()
	ex.BillingCreditGate = creditGateFunc(func(_ context.Context, _ string) error { return nil })
	ex.BillingIdentity = customIdentity

	t.Run("credit check fails closed on custom identity callback", func(t *testing.T) {
		err := ex.checkWireCheapCredit(ctx, WireBillingCreditArgs{
			Scope: principalScope,
		})
		if err == nil {
			t.Fatal("expected blocker error for custom AccountID callback, got nil")
		}
		if !errors.Is(err, ErrBillingAdmissionDenied) {
			t.Fatalf("expected ErrBillingAdmissionDenied, got %v", err)
		}
		if !strings.Contains(err.Error(), "custom BillingIdentity") {
			t.Fatalf("error %q should mention custom BillingIdentity", err.Error())
		}
	})

	t.Run("exposure authorization fails closed on custom identity callback", func(t *testing.T) {
		callID, err := usageevidence.NewBillingCallID()
		if err != nil {
			t.Fatal(err)
		}
		primary := routing.Primary{Backend: "backend-a", Model: "model-a"}
		_, authErr := ex.AuthorizeWireBilling(ctx, WireBillingExposureArgs{
			BillingCallID: callID,
			TraceID:       "trace-001",
			ALegID:        "aleg-001",
			Scope:         principalScope,
			Route:         &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}},
			RequestSize:   routing.RequestSizeEstimate{Available: true, Tokens: 100},
		})
		if authErr == nil {
			t.Fatal("expected blocker error for custom callback on exposure authorization, got nil")
		}
		if !errors.Is(authErr, ErrBillingAdmissionDenied) {
			t.Fatalf("expected ErrBillingAdmissionDenied, got %v", authErr)
		}
	})

	t.Run("assessment declines before commit when custom identity callback present", func(t *testing.T) {
		decision, reason, err := ex.assessWireBilling(ctx, WireBillingAssessmentArgs{
			Scope: principalScope,
		})
		if decision != largebody.AssessmentDecisionDecline {
			t.Fatalf("decision = %v, want AssessmentDecisionDecline", decision)
		}
		if reason != largebody.DeclineReasonAuthorityBlocker {
			t.Fatalf("reason = %v, want DeclineReasonAuthorityBlocker", reason)
		}
		if err == nil {
			t.Fatal("expected non-nil error describing the authority blocker")
		}
	})

	t.Run("stock bounded identity succeeds assessment without decline", func(t *testing.T) {
		stockEx := TestExecutor()
		stockEx.BillingCreditGate = creditGateFunc(func(_ context.Context, _ string) error { return nil })
		stockEx.BillingIdentity = BillingIdentity{
			WireBounded: true,
			WireAccountID: func(_ context.Context, sc scope.PrincipalScopeView) string {
				return sc.PrincipalID.String()
			},
		}
		decision, reason, err := stockEx.assessWireBilling(ctx, WireBillingAssessmentArgs{
			Scope: principalScope,
		})
		if decision != largebody.AssessmentDecisionAccept {
			t.Fatalf("decision = %v, want AssessmentDecisionAccept", decision)
		}
		if reason != largebody.DeclineReasonNone {
			t.Fatalf("reason = %v, want DeclineReasonNone", reason)
		}
		if err != nil {
			t.Fatalf("unexpected assessment error: %v", err)
		}
	})
}

// TestWireBilling_AuthorizeExposure_Parity proves that AuthorizeWireBilling admits exposure
// from bounded facts and shares exact identity stamping logic with the canonical path (Requirements 15.6, 19).
func TestWireBilling_AuthorizeExposure_Parity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	principalScope := scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-wire-003"),
	}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	pricingRef := usageevidence.VersionRef{ID: "price-snap-01", Version: "v1"}
	policyRef := usageevidence.VersionRef{ID: "policy-snap-01", Version: "v1"}

	var capturedInput BillingExposureAdmissionInput
	admitFunc := exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		capturedInput = in
		return billingport.CallExposure{
			AccountID:       "acct-admitted",
			PricingRef:      pricingRef,
			ChargePolicyRef: policyRef,
		}, nil
	})

	stockIdentity := BillingIdentity{
		WireBounded: true,
		WireAccountID: func(_ context.Context, sc scope.PrincipalScopeView) string {
			return sc.PrincipalID.String()
		},
		WireCustomerPricingRef: func(_ context.Context) usageevidence.VersionRef {
			return pricingRef
		},
		WireChargePolicyRef: func(_ context.Context) usageevidence.VersionRef {
			return policyRef
		},
	}

	ex := TestExecutor()
	ex.BillingExposureAdmission = admitFunc
	ex.BillingIdentity = stockIdentity

	primary := routing.Primary{Backend: "backend-wire", Model: "model-wire"}
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}}
	maxOut := 2048

	exposure, err := ex.AuthorizeWireBilling(ctx, WireBillingExposureArgs{
		BillingCallID:   callID,
		TraceID:         "trace-wire-003",
		ALegID:          "aleg-wire-003",
		SessionID:       "sess-wire-003",
		Scope:           principalScope,
		Route:           sel,
		RequestSize:     routing.RequestSizeEstimate{Available: true, Tokens: 512},
		MaxOutputTokens: &maxOut,
	})
	if err != nil {
		t.Fatalf("AuthorizeWireBilling failed: %v", err)
	}

	// Verify exposure results
	if exposure.AccountID != "acct-admitted" {
		t.Fatalf("exposure.AccountID = %q, want acct-admitted", exposure.AccountID)
	}
	if exposure.PricingRef != pricingRef {
		t.Fatalf("exposure.PricingRef = %+v, want %+v", exposure.PricingRef, pricingRef)
	}
	if exposure.ChargePolicyRef != policyRef {
		t.Fatalf("exposure.ChargePolicyRef = %+v, want %+v", exposure.ChargePolicyRef, policyRef)
	}

	// Verify admission input received bounded facts with zero aipapi.Call
	if capturedInput.CallID != callID.String() {
		t.Fatalf("captured CallID = %q, want %q", capturedInput.CallID, callID.String())
	}
	if capturedInput.TraceID != "trace-wire-003" {
		t.Fatalf("captured TraceID = %q, want trace-wire-003", capturedInput.TraceID)
	}
	if capturedInput.ALegID != "aleg-wire-003" {
		t.Fatalf("captured ALegID = %q, want aleg-wire-003", capturedInput.ALegID)
	}
	if capturedInput.SessionID != "sess-wire-003" {
		t.Fatalf("captured SessionID = %q, want sess-wire-003", capturedInput.SessionID)
	}
	if capturedInput.MaxOutputTokens == nil || *capturedInput.MaxOutputTokens != 2048 {
		t.Fatalf("captured MaxOutputTokens = %v, want 2048", capturedInput.MaxOutputTokens)
	}
	if capturedInput.RequestSize.Tokens != 512 || !capturedInput.RequestSize.Available {
		t.Fatalf("captured RequestSize = %+v, want 512 tokens available", capturedInput.RequestSize)
	}
	// Verify Call is zero
	if capturedInput.Call.ID != "" || len(capturedInput.Call.Items) != 0 || len(capturedInput.Call.Messages) != 0 {
		t.Fatalf("captured Call must be empty on wire path, got %+v", capturedInput.Call)
	}
}

// TestWireBilling_AuthorizeExposure_NilAdmission_CanonicalParity proves that when
// BillingExposureAdmission is nil, both wire AuthorizeWireBilling and canonical authorizeBillingOnce
// return nil error, empty CallExposure, and skip identity stamping (billingIdentityStamped == false).
func TestWireBilling_AuthorizeExposure_NilAdmission_CanonicalParity(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	principalScope := scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-wire-nil-admit"),
	}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	ex := TestExecutor()
	ex.BillingExposureAdmission = nil
	ex.BillingIdentity = BillingIdentity{
		WireBounded: true,
		WireAccountID: func(_ context.Context, sc scope.PrincipalScopeView) string {
			return sc.PrincipalID.String()
		},
		AccountID: func(_ context.Context, _ aipapi.Call) string {
			return "principal-wire-nil-admit"
		},
	}

	primary := routing.Primary{Backend: "backend-wire", Model: "model-wire"}
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}}
	maxOut := 2048

	// 1. Wire path
	wireExposure, err := ex.AuthorizeWireBilling(ctx, WireBillingExposureArgs{
		BillingCallID:   callID,
		TraceID:         "trace-nil-001",
		ALegID:          "aleg-nil-001",
		SessionID:       "sess-nil-001",
		Scope:           principalScope,
		Route:           sel,
		RequestSize:     routing.RequestSizeEstimate{Available: true, Tokens: 512},
		MaxOutputTokens: &maxOut,
	})
	if err != nil {
		t.Fatalf("wire AuthorizeWireBilling failed: %v", err)
	}
	if !isEmptyCallExposure(wireExposure) {
		t.Fatalf("expected empty CallExposure for nil BillingExposureAdmission, got %+v", wireExposure)
	}
	wireIdentityStamped := strings.TrimSpace(wireExposure.AccountID) != ""
	if wireIdentityStamped {
		t.Fatalf("expected wireIdentityStamped to be false for nil BillingExposureAdmission")
	}

	// 2. Canonical path
	call := aipapi.Call{ID: "req-nil-001"}
	prep := &preparedRequest{
		call:          &call,
		billingCallID: callID,
	}
	plan := &routePlanState{
		routeFacts: routeFacts{
			sel: sel,
		},
	}
	if err := ex.authorizeBillingOnce(ctx, prep, plan); err != nil {
		t.Fatalf("canonical authorizeBillingOnce failed: %v", err)
	}
	if !isEmptyCallExposure(prep.billingExposure) {
		t.Fatalf("expected canonical billingExposure to be empty, got %+v", prep.billingExposure)
	}
	if prep.billingIdentityStamped {
		t.Fatalf("expected canonical billingIdentityStamped to be false for nil BillingExposureAdmission")
	}

	// 3. Differential assertions
	if !reflect.DeepEqual(wireExposure, prep.billingExposure) {
		t.Fatalf("exposure mismatch: wire=%+v, canonical=%+v", wireExposure, prep.billingExposure)
	}
	if wireIdentityStamped != prep.billingIdentityStamped {
		t.Fatalf("identity stamped mismatch: wire=%v, canonical=%v", wireIdentityStamped, prep.billingIdentityStamped)
	}
}

// TestWireBilling_AbortClosure_ExactlyOnce proves that wire exposure abort closure
// appends exactly one sealed CallUsageRecord on post-admission failure (Requirements 15.6, 19).
func TestWireBilling_AbortClosure_ExactlyOnce(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	now := time.Unix(1715625000, 0).UTC()
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}

	sink := &capturingTerminalUsageSink{}
	ex := TestExecutor()
	ex.TerminalUsageSink = sink
	ex.Now = func() time.Time { return now }

	exposure := billingport.CallExposure{
		AccountID:       "acct-wire-abort",
		PricingRef:      usageevidence.VersionRef{ID: "price-abort", Version: "v1"},
		ChargePolicyRef: usageevidence.VersionRef{ID: "policy-abort", Version: "v1"},
	}

	args := WireExposureAbortArgs{
		BillingCallID:   callID,
		Exposure:        exposure,
		ALegID:          "aleg-abort-001",
		SessionID:       "sess-abort-001",
		ExpectedBLegIDs: []string{"bleg-001"},
		Now:             now,
	}

	err = ex.appendWireExposureAbort(ctx, args)
	if err != nil {
		t.Fatalf("appendWireExposureAbort failed: %v", err)
	}

	sink.mu.Lock()
	callCount := len(sink.calls)
	sink.mu.Unlock()

	if callCount != 1 {
		t.Fatalf("expected 1 call closure record, got %d", callCount)
	}

	record := sink.calls[0]
	if record.CallID != callID {
		t.Fatalf("record.CallID = %v, want %v", record.CallID, callID)
	}
	if record.AccountID != "acct-wire-abort" {
		t.Fatalf("record.AccountID = %q, want acct-wire-abort", record.AccountID)
	}
	if record.ALegID != "aleg-abort-001" {
		t.Fatalf("record.ALegID = %q, want aleg-abort-001", record.ALegID)
	}
	if record.SessionID != "sess-abort-001" {
		t.Fatalf("record.SessionID = %q, want sess-abort-001", record.SessionID)
	}
	if record.Outcome != usageevidence.TurnOutcomeFailed {
		t.Fatalf("record.Outcome = %q, want TurnOutcomeFailed", record.Outcome)
	}
	if record.CustomerPricingRef != exposure.PricingRef {
		t.Fatalf("record.CustomerPricingRef = %+v, want %+v", record.CustomerPricingRef, exposure.PricingRef)
	}
	if record.ChargePolicyRef != exposure.ChargePolicyRef {
		t.Fatalf("record.ChargePolicyRef = %+v, want %+v", record.ChargePolicyRef, exposure.ChargePolicyRef)
	}
	if len(record.ExpectedBLegIDs) != 1 || record.ExpectedBLegIDs[0] != "bleg-001" {
		t.Fatalf("record.ExpectedBLegIDs = %v, want [bleg-001]", record.ExpectedBLegIDs)
	}
}

// TestWireBilling_FullFastPathComposition_ReachesWireModeAndCompletesLifecycle proves
// that a production-like composition with stock billing (credit gate + exposure admission +
// terminal sink + stock BillingIdentity) reaches wire mode and executes the full two-seam
// billing lifecycle with zero Call retention (Requirements 15.6, 19).
func TestWireBilling_FullFastPathComposition_ReachesWireModeAndCompletesLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715626000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})

	factRecorder := &memoryFactRecorder{}
	turnRecorder := &capturingTurnRecorder{}
	usageSink := &capturingTerminalUsageSink{}

	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	pricingRef := usageevidence.VersionRef{ID: "price-comp-01", Version: "v1"}
	policyRef := usageevidence.VersionRef{ID: "policy-comp-01", Version: "v1"}

	var creditScreenCalls int
	creditGate := creditGateFunc(func(_ context.Context, acct string) error {
		creditScreenCalls++
		if acct != "principal-wire-comp" {
			return errors.New("unexpected account")
		}
		return nil
	})

	var exposureAdmissionCalls int
	exposureAdmission := exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		exposureAdmissionCalls++
		return billingport.CallExposure{
			AccountID:       in.AccountID,
			PricingRef:      pricingRef,
			ChargePolicyRef: policyRef,
		}, nil
	})

	stockIdentity := BillingIdentity{
		WireBounded: true,
		WireAccountID: func(_ context.Context, sc scope.PrincipalScopeView) string {
			return sc.PrincipalID.String()
		},
		WireCustomerPricingRef: func(_ context.Context) usageevidence.VersionRef {
			return pricingRef
		},
		WireChargePolicyRef: func(_ context.Context) usageevidence.VersionRef {
			return policyRef
		},
		OperatorRateRef: func(_ context.Context, _, _ string) usageevidence.VersionRef {
			return usageevidence.VersionRef{ID: "op-rate-comp", Version: "v1"}
		},
	}

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionRecorder = turnRecorder
	ex.MeteringRecorder = factRecorder
	ex.TerminalUsageSink = usageSink
	ex.BillingCreditGate = creditGate
	ex.BillingExposureAdmission = exposureAdmission
	ex.BillingIdentity = stockIdentity
	ex.Now = func() time.Time { return now }

	ctx := context.Background()
	reqScope := scope.PrincipalScopeView{
		PrincipalID: scope.Known("principal-wire-comp"),
	}

	// 1. Pre-commit assessment: verify billing is wire-accepted
	decision, reason, err := ex.assessWireBilling(ctx, WireBillingAssessmentArgs{
		Scope: reqScope,
	})
	if decision != largebody.AssessmentDecisionAccept || reason != largebody.DeclineReasonNone || err != nil {
		t.Fatalf("assessWireBilling failed: decision=%v, reason=%v, err=%v", decision, reason, err)
	}

	// 2. Post-commit Seam 1: cheap pre-route credit screen
	err = ex.checkWireCheapCredit(ctx, WireBillingCreditArgs{
		Scope: reqScope,
	})
	if err != nil {
		t.Fatalf("checkWireCheapCredit failed: %v", err)
	}
	if creditScreenCalls != 1 {
		t.Fatalf("creditScreenCalls = %d, want 1", creditScreenCalls)
	}

	// 3. Post-commit Seam 2: post-quote atomic exposure admission
	primary := routing.Primary{Backend: "backend-wire", Model: "model-wire"}
	sel := &routing.Selector{Alternatives: []routing.FailoverAlt{{Primary: &primary}}}
	maxOut := 4096

	exposure, err := ex.AuthorizeWireBilling(ctx, WireBillingExposureArgs{
		BillingCallID:   callID,
		TraceID:         "trace-comp-001",
		ALegID:          "aleg-comp-001",
		SessionID:       "sess-comp-001",
		Scope:           reqScope,
		Route:           sel,
		RequestSize:     routing.RequestSizeEstimate{Available: true, Tokens: 256},
		MaxOutputTokens: &maxOut,
	})
	if err != nil {
		t.Fatalf("AuthorizeWireBilling failed: %v", err)
	}
	if exposureAdmissionCalls != 1 {
		t.Fatalf("exposureAdmissionCalls = %d, want 1", exposureAdmissionCalls)
	}
	if exposure.AccountID != "principal-wire-comp" {
		t.Fatalf("exposure.AccountID = %q, want principal-wire-comp", exposure.AccountID)
	}

	// 4. Terminal handoff: append call closure exactly once
	term := newTurnTerminal()
	bindTurnTerminalRuntime(term, ex)
	billingState := newBillingCallState(callID)
	blegID := "bleg-comp-001"
	billingState.noteAllocatedBLeg(blegID, 1)

	facts := requestTerminalFacts{
		billingCallID:   callID,
		billingState:    billingState,
		accountID:       exposure.AccountID,
		aLegID:          "aleg-comp-001",
		sessionID:       "sess-comp-001",
		pricing:         exposure.PricingRef,
		chargePolicy:    exposure.ChargePolicyRef,
		identityStamped: true,
	}

	if err := term.handoffBillingTurn(ctx, facts, sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}

	// Idempotency: second handoff call is a no-op
	if err := term.handoffBillingTurn(ctx, facts, sdkterminal.CommandNormalFinish); err != nil {
		t.Fatalf("handoffBillingTurn: %v", err)
	}

	usageSink.mu.Lock()
	callCount := len(usageSink.calls)
	usageSink.mu.Unlock()

	if callCount != 1 {
		t.Fatalf("expected exactly 1 call usage record appended to sink, got %d", callCount)
	}
	record := usageSink.calls[0]
	if record.CallID != callID {
		t.Fatalf("record.CallID = %v, want %v", record.CallID, callID)
	}
	if record.AccountID != "principal-wire-comp" {
		t.Fatalf("record.AccountID = %q, want principal-wire-comp", record.AccountID)
	}
	if record.Outcome != usageevidence.TurnOutcomeCompleted {
		t.Fatalf("record.Outcome = %q, want TurnOutcomeCompleted", record.Outcome)
	}
	if len(record.ExpectedBLegIDs) != 1 || record.ExpectedBLegIDs[0] != blegID {
		t.Fatalf("record.ExpectedBLegIDs = %v, want [%s]", record.ExpectedBLegIDs, blegID)
	}
}
