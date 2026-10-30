package app_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type recordingLeaseConc struct {
	releases []authority.LeaseRelease
}

func (r *recordingLeaseConc) AdmitLease(context.Context, authority.LeaseAdmission) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow}, nil
}

func (r *recordingLeaseConc) RenewLease(context.Context, authority.LeaseRenew) (authority.LeaseDecision, error) {
	return authority.LeaseDecision{Kind: authority.LeaseAllow}, nil
}

func (r *recordingLeaseConc) ReleaseLease(_ context.Context, in authority.LeaseRelease) error {
	r.releases = append(r.releases, in)
	return nil
}

func (r *recordingLeaseConc) QueryLeases(context.Context, authority.LeaseQuery) (authority.LeasePage, error) {
	return authority.LeasePage{}, nil
}

func TestPhase64_LeaseSetEffectReleasesOnlyAfterStoreConfirm(t *testing.T) {
	t.Parallel()
	conc := &recordingLeaseConc{}
	effect, err := app.NewLeaseSetEffectProvider(app.LeaseSetEffectConfig{
		ProviderID: "concurrency", Provider: conc, Version: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]string{"set_id": "set-xyz", "reason": "rollback"})
	rec := terminalwork.WorkRecord{
		WorkID: "w1", Kind: sdk.WorkKindReleaseLeaseSet, LeaseSetID: "set-xyz",
		Lifecycle: terminalwork.LifecycleCorrelation{RequestID: "req-1"},
		Payload:   payload,
	}
	if err := effect.Invoke(context.Background(), rec, "idem-1"); err != nil {
		t.Fatal(err)
	}
	if len(conc.releases) != 1 || conc.releases[0].SetID != "set-xyz" {
		t.Fatalf("releases=%+v", conc.releases)
	}
}

func TestPhase64_AcceptLeaseSetReleaseIntent(t *testing.T) {
	t.Parallel()
	store := &recordingIntentStore{}
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Clock: func() time.Time { return time.Date(2026, 7, 18, 16, 0, 0, 0, time.UTC) },
	})
	if err := svc.AcceptLeaseSetRelease(context.Background(), app.LeaseSetReleaseInput{
		RequestID: "req-1", AttemptID: "a1", LeaseSetID: "set-1", Reason: "renew_fail_closed",
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.appended) != 1 || store.appended[0].Kind != sdk.WorkKindReleaseLeaseSet {
		t.Fatalf("appended=%+v", store.appended)
	}
	if store.appended[0].LeaseSetID != "set-1" {
		t.Fatalf("lease_set_id=%q", store.appended[0].LeaseSetID)
	}
}

type recordingIntentStore struct {
	appended []terminalwork.WorkRecord
}

func (s *recordingIntentStore) AppendIntent(_ context.Context, rec terminalwork.WorkRecord) error {
	s.appended = append(s.appended, rec)
	return nil
}

func (s *recordingIntentStore) PromotePending(context.Context, terminalwork.PromotePendingCommand) error {
	return nil
}
