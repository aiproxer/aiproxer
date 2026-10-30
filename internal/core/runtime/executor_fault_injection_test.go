package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type errorAffinityStore struct {
	err error
}

func (s *errorAffinityStore) Get(ctx context.Context, key affinity.Key) (affinity.Binding, bool, error) {
	return affinity.Binding{}, false, s.err
}

func (s *errorAffinityStore) Set(ctx context.Context, binding affinity.Binding) error {
	return nil
}

func (s *errorAffinityStore) Delete(ctx context.Context, key affinity.Key) error {
	return nil
}

type errorStore struct {
	b2bua.Store
	setWeightedFirstConsumedErr error
}

func (s *errorStore) SetWeightedFirstConsumed(ctx context.Context, aLegID string, consumed bool) error {
	if s.setWeightedFirstConsumedErr != nil {
		return s.setWeightedFirstConsumedErr
	}
	return s.Store.SetWeightedFirstConsumed(ctx, aLegID, consumed)
}

func TestExecutorFault_AffinityStoreError(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.AffinityStore = &errorAffinityStore{err: errors.New("injected affinity lookup error")}

	sel := &routing.Selector{
		Affinity: routing.AffinitySession,
	}

	budget := &attemptBudget{max: 10}
	req := openNextRequest{
		reqFacts: requestFacts{
			recvTurnFacts: recvTurnFacts{
				traceID:          "trace-fault-affinity",
				aLegID:           aLegID,
				baseline:         aipapi.Call{ID: "req-1"},
				billingCallID:    usageevidence.BillingCallID("call-1"),
				billingCallState: &billingCallState{},
			},
			bus: ex.Bus,
		},
		routeFacts: routeFacts{
			sel:         sel,
			affinityKey: affinity.Key{Scope: affinity.ScopeSession, ID: "test-session-id"},
			affinitySet: true,
		},
		progress: &recoveryController{
			budget:   budget,
			failures: budget.getFailures(),
			excluded: make(map[string]struct{}),
		},
		mode: openModeInitial,
	}

	_, err := ex.openNext(ctx, req)
	if err == nil {
		t.Fatal("expected error on affinity store failure, got nil")
	}
	if !strings.Contains(err.Error(), "injected affinity lookup error") {
		t.Errorf("expected affinity store error message, got: %v", err)
	}
}

func TestExecutorFault_SetWeightedFirstConsumedError(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "res-1",
			ReservedAmount: authorityInputAmount(7),
			Reservations: []usagelimit.AdmissionReservation{{
				RuleID:         "rule-1",
				ReservationID:  "res-1",
				ReservedAmount: authorityInputAmount(7),
			}},
		},
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)
	ex.Store = &errorStore{
		Store:                       ex.Store,
		setWeightedFirstConsumedErr: errors.New("injected SetWeightedFirstConsumed error"),
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: routing.AttemptCandidate{
			Primary:     routing.Primary{Backend: "backend-1", Model: "model-1"},
			Key:         "backend-1:model-1",
			MarkedFirst: true,
		},
	}

	_, err := ex.evaluateAndOpenCandidate(ctx, req, plan)
	if err == nil {
		t.Fatal("expected error on SetWeightedFirstConsumed failure, got nil")
	}
	if !strings.Contains(err.Error(), "injected SetWeightedFirstConsumed error") {
		t.Errorf("expected SetWeightedFirstConsumed error message, got: %v", err)
	}
}

func TestExecutorFault_UsageAuthorityAdmitError(t *testing.T) {
	ctx := context.Background()
	admitErr := errors.New("injected admit error")
	auth := &recordingAuthorityService{
		admitErr: admitErr,
	}
	ex, _, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}

	_, err := ex.evaluateAndOpenCandidate(ctx, req, plan)
	if err == nil {
		t.Fatal("expected error on Admit failure, got nil")
	}
	if !errors.Is(err, admitErr) {
		t.Errorf("expected admit error to wrap admitErr, got: %v", err)
	}
}

func TestExecutorFault_UsageAuthorityReleaseError(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "res-1",
			ReservedAmount: authorityInputAmount(7),
			Reservations: []usagelimit.AdmissionReservation{{
				RuleID:         "rule-1",
				ReservationID:  "res-1",
				ReservedAmount: authorityInputAmount(7),
			}},
		},
		releaseErr: errors.New("injected release error"),
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}

	out, err := ex.evaluateAndOpenCandidate(ctx, req, plan)
	if err != nil {
		t.Fatalf("expected successful open: %v", err)
	}

	out.ready.session.authority.Release(ctx, usagelimit.ReleaseKindAdmissionFailure)

	if got := auth.releaseCalls.Load(); got != 1 {
		t.Errorf("expected 1 release call, got %d", got)
	}
}

func TestExecutorFault_UsageAuthoritySettleError(t *testing.T) {
	ctx := context.Background()
	auth := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Allowed:        true,
			Reserved:       true,
			ReservationID:  "res-1",
			ReservedAmount: authorityInputAmount(7),
			Reservations: []usagelimit.AdmissionReservation{{
				RuleID:         "rule-1",
				ReservationID:  "res-1",
				ReservedAmount: authorityInputAmount(7),
			}},
		},
		settleErr: errors.New("injected settle error"),
	}
	ex, backend, aLegID := newAuthorityRuntimeTestExecutor(t, auth)

	backend.openFn = func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		return aipapi.NewFixedEventStream([]aipapi.Event{
			{Kind: aipapi.EventResponseStarted},
			{Kind: aipapi.EventResponseFinished},
		}), nil
	}

	budget := &attemptBudget{max: 10}
	req := authorityOpenRequest(t, aLegID, budget)
	plan := candidatePlan{
		cand: authorityCandidate(),
	}

	out, err := ex.evaluateAndOpenCandidate(ctx, req, plan)
	if err != nil {
		t.Fatalf("expected successful open: %v", err)
	}

	out.ready.session.authority.Settle(ctx, usagelimit.SettlementKindFinal, aipapi.Event{}, false)

	if got := auth.settleCalls.Load(); got != 1 {
		t.Errorf("expected 1 settle call, got %d", got)
	}
}
