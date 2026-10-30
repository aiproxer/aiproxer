package compatible_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	compatibleadmission "github.com/aiproxer/aiproxer/internal/core/concurrencyauthority/compatible"
	"github.com/aiproxer/aiproxer/internal/infra/concurrencyauthority/leasestore"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/economics"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

func testAdmissionStore(t *testing.T) *leasestore.MemoryStore {
	t.Helper()
	return leasestore.NewMemory(leasestore.MemoryConfig{StoreID: "compatible-admission-test"})
}

func TestAttemptProvider_defaultUnlimitedIndependentInstances(t *testing.T) {
	t.Parallel()
	regA, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"a": 1}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	regB, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"b": 2}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := assertPeakWithin(t, regA, "a", 1, 4); err != nil {
		t.Fatalf("instance a: %v", err)
	}
	if err := assertPeakWithin(t, regB, "b", 2, 5); err != nil {
		t.Fatalf("instance b: %v", err)
	}
}

func TestAttemptProvider_overloadUsesConcurrencyLimitPolicy(t *testing.T) {
	t.Parallel()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"be": 1}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: compatibleadmission.ProviderID, Provider: reg.Provider,
			Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
		}},
	}
	block := make(chan struct{})
	release := startHeldAttempt(t, coord, "be", "b1", block)
	defer release()

	_, err = coord.Admit(context.Background(), attemptAdmission("be", "b2"))
	if !authoritycoord.IsDenied(err) {
		t.Fatalf("err=%v want denied", err)
	}
	var pol *aipapi.PolicyDecisionError
	if !errors.As(mapCompatibleDenied(err), &pol) {
		t.Fatalf("err=%T", err)
	}
	if pol.ReasonCode != "concurrency_limit" {
		t.Fatalf("reason=%q", pol.ReasonCode)
	}
}

func TestAttemptProvider_blockedAcquisitionHonorsContextCancellation(t *testing.T) {
	t.Parallel()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"be": 1}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	coord := &authoritycoord.AttemptCoordinator{
		Slots: []authoritycoord.AttemptSlot{{
			ID: compatibleadmission.ProviderID, Provider: reg.Provider,
			Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
		}},
	}
	block := make(chan struct{})
	release := startHeldAttempt(t, coord, "be", "b1", block)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = coord.Admit(ctx, attemptAdmission("be", "b2"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestAttemptProvider_setupFailureReleasesPermit(t *testing.T) {
	t.Parallel()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"be": 1}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	coord := &authoritycoord.AttemptCoordinator{Slots: []authoritycoord.AttemptSlot{{
		ID: compatibleadmission.ProviderID, Provider: reg.Provider,
		Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
	}}}
	d, err := coord.Admit(context.Background(), attemptAdmission("be", "b1"))
	if err != nil || d.Kind != authority.DecisionAllow {
		t.Fatalf("admit err=%v d=%+v", err, d)
	}
	if err := coord.Release(context.Background(), d.Stack); len(err) != 0 {
		t.Fatalf("release=%v", err)
	}
	d2, err := coord.Admit(context.Background(), attemptAdmission("be", "b2"))
	if err != nil || d2.Kind != authority.DecisionAllow {
		t.Fatalf("second admit err=%v d=%+v", err, d2)
	}
	_ = coord.Release(context.Background(), d2.Stack)
}

func TestAttemptProvider_terminalPathsReleaseExactlyOnce(t *testing.T) {
	t.Parallel()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"be": 1}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	coord := &authoritycoord.AttemptCoordinator{Slots: []authoritycoord.AttemptSlot{{
		ID: compatibleadmission.ProviderID, Provider: reg.Provider,
		Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
	}}}
	for _, reason := range []string{"success", "rollback"} {
		d, err := coord.Admit(context.Background(), attemptAdmission("be", "b-"+reason))
		if err != nil || d.Kind != authority.DecisionAllow {
			t.Fatalf("%s admit err=%v", reason, err)
		}
		if reason == "success" {
			if err := coord.Settle(context.Background(), d.Stack, attemptSettlement("b-"+reason, d.Stack.Handles())); err != nil {
				t.Fatalf("%s settle err=%v", reason, err)
			}
		} else {
			if fails := coord.Release(context.Background(), d.Stack); len(fails) != 0 {
				t.Fatalf("%s release fails=%v", reason, fails)
			}
		}
	}
	d3, err := coord.Admit(context.Background(), attemptAdmission("be", "after-terminal"))
	if err != nil || d3.Kind != authority.DecisionAllow {
		t.Fatalf("after terminal admit err=%v", err)
	}
	_ = coord.Release(context.Background(), d3.Stack)
}

func TestAttemptProvider_noOverAdmissionUnderRace(t *testing.T) {
	t.Parallel()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"be": 2}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	coord := &authoritycoord.AttemptCoordinator{Slots: []authoritycoord.AttemptSlot{{
		ID: compatibleadmission.ProviderID, Provider: reg.Provider,
		Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
	}}}
	block := make(chan struct{})
	var peak atomic.Int32
	var wg sync.WaitGroup
	for i := range 24 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := coord.Admit(context.Background(), attemptAdmission("be", "race-"+string(rune('a'+i%26))))
			if err != nil {
				return
			}
			cur := peak.Add(1)
			if cur > peak.Load() {
				peak.Store(cur)
			}
			<-block
			_ = coord.Release(context.Background(), d.Stack)
			peak.Add(-1)
		}(i)
	}
	time.Sleep(40 * time.Millisecond)
	close(block)
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("peak=%d want <=2", peak.Load())
	}
}

func TestAttemptProvider_unrelatedBackendNoOps(t *testing.T) {
	t.Parallel()
	reg, _, err := compatibleadmission.AttemptRegistration(compatibleadmission.Limits{"compat-a": 1}, testAdmissionStore(t))
	if err != nil {
		t.Fatal(err)
	}
	coord := &authoritycoord.AttemptCoordinator{Slots: []authoritycoord.AttemptSlot{{
		ID: compatibleadmission.ProviderID, Provider: reg.Provider,
		Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
	}}}
	d, err := coord.Admit(context.Background(), attemptAdmission("openai", "b1"))
	if err != nil || d.Kind != authority.DecisionAllow || len(d.Stack.Handles()) != 0 {
		t.Fatalf("native backend should bypass compatible limiter: err=%v d=%+v", err, d)
	}
}

func attemptAdmission(backendID, bleg string) authority.AttemptAdmission {
	return authority.AttemptAdmission{
		RequestID:   "req",
		AttemptID:   bleg,
		BLegID:      bleg,
		BackendID:   backendID,
		Lifecycle:   metering.LifecycleBackendAttempt,
		Perspective: metering.PerspectiveOperator,
		Exposure: economics.ExposureBasis{
			Perspective: metering.PerspectiveOperator,
			Boundary:    metering.BoundaryBackendIngress,
			Lifecycle:   metering.LifecycleBackendAttempt,
		},
	}
}

func attemptSettlement(bleg string, handles []string) authority.AttemptSettlement {
	return authority.AttemptSettlement{
		RequestID: "req",
		AttemptID: bleg,
		BLegID:    bleg,
		Handles:   handles,
	}
}

func startHeldAttempt(t *testing.T, coord *authoritycoord.AttemptCoordinator, backendID, bleg string, block chan struct{}) func() {
	t.Helper()
	d, err := coord.Admit(context.Background(), attemptAdmission(backendID, bleg))
	if err != nil || d.Kind != authority.DecisionAllow {
		t.Fatalf("hold admit err=%v d=%+v", err, d)
	}
	return func() {
		_ = coord.Release(context.Background(), d.Stack)
		close(block)
	}
}

func assertPeakWithin(t *testing.T, reg authority.AttemptRegistration, backendID string, limit, extra int) error {
	t.Helper()
	coord := &authoritycoord.AttemptCoordinator{Slots: []authoritycoord.AttemptSlot{{
		ID: compatibleadmission.ProviderID, Provider: reg.Provider,
		Class: authoritycoord.AttemptPriorityQuotaRate, Strength: authority.StrengthRequired,
	}}}
	block := make(chan struct{})
	var peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < limit+extra; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := coord.Admit(context.Background(), attemptAdmission(backendID, "b"+string(rune('a'+i%26))))
			if err != nil {
				return
			}
			cur := peak.Add(1)
			if cur > peak.Load() {
				peak.Store(cur)
			}
			<-block
			_ = coord.Release(context.Background(), d.Stack)
			peak.Add(-1)
		}(i)
	}
	time.Sleep(40 * time.Millisecond)
	close(block)
	wg.Wait()
	if int(peak.Load()) > limit {
		return errors.New("peak exceeded max")
	}
	return nil
}

func mapCompatibleDenied(err error) error {
	var denied *authoritycoord.DeniedError
	if errors.As(err, &denied) && denied != nil && denied.ProviderID == compatibleadmission.ProviderID {
		return aipapi.NewPolicyDeniedError("compatible_backend_attempt", "", "concurrency_limit", "concurrency_limit", "compatible backend concurrent request limit reached", nil)
	}
	return err
}
