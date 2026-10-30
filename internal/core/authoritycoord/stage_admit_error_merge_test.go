package authoritycoord_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
)

// Stage-admit errors after a successful concurrency prefix must return the enriched
// composite (lease, aggregated readiness, bound versions, stage deny metadata) — not
// the bare stage evaluator output.
func TestRequestCoordinator_StageAdmitErrorPreservesConcurrencyPrefix(t *testing.T) {
	t.Parallel()
	leaseBV := economics.PolicySnapshotRef{VersionRef: economics.VersionRef{Version: "lease-v1"}}
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:         authority.LeaseAllow,
				LeaseID:      "lease-prefix",
				Generation:   1,
				ExpiresAt:    time.Now().Add(time.Minute),
				Readiness:    authority.ReadinessReady,
				BoundVersion: leaseBV,
			}, nil
		},
	}
	denyQuota := &fakeRequestProvider{
		id: "quota",
		admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
			return authority.Decision{Kind: authority.DecisionDeny, ProviderID: "quota"}, nil
		},
	}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: conc,
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: denyQuota,
		}},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("want DeniedError, got err=%v d=%+v", err, d)
	}
	if d.Lease.LeaseID != "lease-prefix" {
		t.Fatalf("Lease=%+v want concurrency prefix preserved on stage-admit error", d.Lease)
	}
	if d.DeniedBy != "quota" {
		t.Fatalf("DeniedBy=%q want stage provider id", d.DeniedBy)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("Kind=%s want deny", d.Kind)
	}
	if len(d.BoundVersions) != 1 || d.BoundVersions[0].Version != "lease-v1" {
		t.Fatalf("BoundVersions=%+v want lease prefix bound version", d.BoundVersions)
	}
	if d.Readiness != authority.ReadinessReady {
		t.Fatalf("Readiness=%s want aggregated readiness from concurrency prefix", d.Readiness)
	}
	if conc.released.Load() != 1 {
		t.Fatalf("concurrency released=%d want 1 (stage error must reverse-compensate prefix holds)", conc.released.Load())
	}
}

func TestRequestCoordinator_StageAdmitUnavailablePreservesConcurrencyPrefix(t *testing.T) {
	t.Parallel()
	conc := &fakeConcurrencyProvider{
		admit: func(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
			return authority.LeaseDecision{
				Kind:       authority.LeaseAllow,
				LeaseID:    "lease-prefix",
				Generation: 1,
				ExpiresAt:  time.Now().Add(time.Minute),
			}, nil
		},
	}
	failQuota := &fakeRequestProvider{
		id: "quota",
		admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
			return authority.Decision{}, errors.New("quota unavailable")
		},
	}
	coord := &authoritycoord.RequestCoordinator{
		Concurrency: conc,
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: failQuota,
		}},
		CleanupTimeout: time.Second,
	}
	d, err := coord.Admit(context.Background(), validRequestAdmission())
	var unavail *authoritycoord.UnavailableError
	if !errors.As(err, &unavail) || unavail.ProviderID != "quota" {
		t.Fatalf("want UnavailableError from quota, got %v", err)
	}
	if d.Lease.LeaseID != "lease-prefix" {
		t.Fatalf("Lease=%+v want concurrency prefix preserved on stage-admit error", d.Lease)
	}
	if d.DeniedBy != "quota" {
		t.Fatalf("DeniedBy=%q want stage provider id", d.DeniedBy)
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("Kind=%s want deny", d.Kind)
	}
}

func TestRequestCoordinator_StageAdmitErrorPreservesProviderDecisionEvidence(t *testing.T) {
	t.Parallel()
	bound := economics.PolicySnapshotRef{VersionRef: economics.VersionRef{ID: "provider-quota", Version: "2026-01"}, PolicyID: "provider-quota"}
	evidence := authority.SafeEvidence{
		Category:       "provider_quota",
		Code:           "telemetry_unavailable",
		RuleID:         "provider-quota",
		ProviderID:     "quota",
		QuotaPolicyRef: &authority.QuotaPolicyRef{ID: "provider-quota", Version: "2026-01", Hash: "hash"},
		Attrs:          map[string]string{"evidence_status": "unavailable"},
	}
	providerErr := errors.New("quota reader canceled")
	quota := &fakeRequestProvider{
		id: "quota",
		admit: func(context.Context, authority.RequestAdmission) (authority.Decision, error) {
			return authority.Decision{
				Kind:          authority.DecisionDeny,
				ProviderID:    "quota",
				Stage:         authority.StageRequestAdmit,
				Readiness:     authority.ReadinessUnavailable,
				BoundVersions: []economics.PolicySnapshotRef{bound},
				Evidence:      evidence,
			}, providerErr
		},
	}
	coord := &authoritycoord.RequestCoordinator{
		Slots:          []authoritycoord.RequestSlot{{ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: quota}},
		CleanupTimeout: time.Second,
	}

	d, err := coord.Admit(context.Background(), validRequestAdmission())
	if err == nil {
		t.Fatal("expected unavailable error")
	}
	if d.Kind != authority.DecisionDeny {
		t.Fatalf("kind=%s want deny", d.Kind)
	}
	if d.Readiness != authority.ReadinessUnavailable {
		t.Fatalf("readiness=%s want unavailable", d.Readiness)
	}
	if len(d.ProviderDecisions) != 1 {
		t.Fatalf("provider decisions=%+v want one decision accompanying error", d.ProviderDecisions)
	}
	got := d.ProviderDecisions[0]
	if got.Kind != authority.DecisionDeny || got.Readiness != authority.ReadinessUnavailable {
		t.Fatalf("provider decision=%+v", got)
	}
	if len(got.BoundVersions) != 1 || got.BoundVersions[0] != bound {
		t.Fatalf("provider bound versions=%+v want %+v", got.BoundVersions, bound)
	}
	if got.Evidence.Category != evidence.Category || got.Evidence.Code != evidence.Code || got.Evidence.Attrs["evidence_status"] != "unavailable" {
		t.Fatalf("provider evidence=%+v", got.Evidence)
	}
	if d.Evidence.Category != evidence.Category || d.Evidence.Code != evidence.Code || d.Evidence.QuotaPolicyRef == nil {
		t.Fatalf("composite evidence=%+v want provider evidence", d.Evidence)
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("error=%v must preserve provider error", err)
	}
	var unavailable *authoritycoord.UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error=%T %v must expose unavailable decision", err, err)
	}
	if unavailable.Decision.Evidence.Category != evidence.Category || unavailable.Decision.Readiness != authority.ReadinessUnavailable {
		t.Fatalf("unavailable decision=%+v", unavailable.Decision)
	}
}
