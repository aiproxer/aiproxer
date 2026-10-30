package runtimebundle

import (
	"context"
	"testing"
	"time"

	concurrencyapp "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/app"
	"github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/domain"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
)

type phase6ReadyRules struct{}

func (phase6ReadyRules) Snapshot(context.Context) (concurrencyapp.RuleSnapshot, error) {
	return concurrencyapp.RuleSnapshot{
		Readiness: domain.Readiness{State: domain.ReadinessStateReady},
		Rules: []domain.Rule{{
			ID: "max-active", Namespace: "default", Version: "v1", Mode: domain.RuleModeStrict,
			Limit: 5, LeaseTTL: time.Minute, RenewBefore: 15 * time.Second,
			Match: domain.DimensionsMatcher{Principal: domain.DimensionMatcher{Value: scope.Known("alice")}},
		}},
	}, nil
}

type phase6FixedClock struct{ t time.Time }

func (c phase6FixedClock) Now() time.Time { return c.t }

func TestPhase6Remediation_PendingReleaseDegradesConcurrencyReadiness(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC)
	leaseStore := leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "phase6-pending-ready"})
	conc := concurrencyapp.NewService(phase6ReadyRules{}, leaseStore, phase6FixedClock{t: now})
	mem, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "phase6-pending-tw",
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	intents := terminalworkapp.NewIntentService(mem, terminalworkapp.IntentServiceConfig{
		Clock: func() time.Time { return now },
	})
	if err := intents.AcceptLeaseSetRelease(context.Background(), terminalworkapp.LeaseSetReleaseInput{
		RequestID:  "req-pending",
		AttemptID:  "a1",
		LeaseSetID: "set-pending",
		Reason:     "settle_release_failure",
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "concurrency"},
	}); err != nil {
		t.Fatal(err)
	}
	tw := &terminalWorkRuntime{Queries: terminalworkapp.NewQueryService(mem)}
	st, err := leaseSetConcurrencyStatus(context.Background(), conc, tw)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != controlplane.ConcurrencyAuthorityDegraded {
		t.Fatalf("state=%s want degraded", st.State)
	}
	if st.LeaseSets.PendingRelease != 1 {
		t.Fatalf("PendingRelease=%d want 1", st.LeaseSets.PendingRelease)
	}
}
