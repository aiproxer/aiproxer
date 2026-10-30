package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type recordingAuthority struct {
	id           string
	settleCalls  atomic.Int32
	releaseCalls atomic.Int32
	lastHandles  atomic.Value
	settleErr    error
	releaseErr   error
}

func (p *recordingAuthority) AdmitRequest(context.Context, authority.RequestAdmission) (authority.Decision, error) {
	return authority.Decision{Kind: authority.DecisionAllow}, nil
}

func (p *recordingAuthority) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	p.settleCalls.Add(1)
	p.lastHandles.Store(append([]string(nil), in.Handles...))
	if p.settleErr != nil {
		return authority.Settlement{}, p.settleErr
	}
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *recordingAuthority) ReleaseRequest(_ context.Context, in authority.RequestRelease) error {
	p.releaseCalls.Add(1)
	p.lastHandles.Store(append([]string(nil), in.Handles...))
	return p.releaseErr
}

func TestPhase45_AuthorityEffectProviderDecodesPayloadAndSettles(t *testing.T) {
	t.Parallel()
	inner := &recordingAuthority{id: "quota"}
	effect, err := app.NewAuthorityRequestEffectProvider(app.AuthorityRequestEffectConfig{
		ProviderID: "quota",
		Provider:   inner,
		Version:    "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"handles": []string{"h-a", "h-b"}})
	rec := terminalwork.WorkRecord{
		WorkID:     "tw_eff",
		SourceKey:  terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_eff"},
		Kind:       sdk.WorkKindSettleRequestProvider,
		State:      sdk.WorkStateClaimed,
		ProviderID: "quota",
		Lifecycle:  terminalwork.LifecycleCorrelation{RequestID: "req-eff", AttemptID: "a-1"},
		Payload:    payload,
	}
	if err := effect.Invoke(context.Background(), rec, rec.SourceKey.String()); err != nil {
		t.Fatal(err)
	}
	if inner.settleCalls.Load() != 1 {
		t.Fatalf("SettleRequest calls=%d want 1", inner.settleCalls.Load())
	}
	got, _ := inner.lastHandles.Load().([]string)
	if len(got) != 2 || got[0] != "h-a" || got[1] != "h-b" {
		t.Fatalf("handles=%v want [h-a h-b]", got)
	}
}

func TestPhase45_AuthorityEffectProviderReleaseAndRestartCompletes(t *testing.T) {
	t.Parallel()
	clock := time.Date(2026, 7, 18, 7, 20, 0, 0, time.UTC)
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{
		StoreID: "effect-restart",
		Now:     func() time.Time { return clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	inner := &recordingAuthority{id: "quota", settleErr: errors.New("down")}
	effect, err := app.NewAuthorityRequestEffectProvider(app.AuthorityRequestEffectConfig{
		ProviderID: "quota",
		Provider:   inner,
		Version:    "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	intents := app.NewIntentService(store, app.IntentServiceConfig{Clock: func() time.Time { return clock }})
	if err := intents.AcceptSettleFailure(context.Background(), app.SettleFailureInput{
		RequestID:  "req-eff-rst",
		AttemptID:  "a-1",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "g1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	inner.settleErr = nil
	reg := app.NewRegistry()
	if err := reg.Register(effect); err != nil {
		t.Fatal(err)
	}
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:    "effect-worker",
		ClaimTTL:   time.Minute,
		ClaimLimit: 10,
		Clock:      fixedNow{t: clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if inner.settleCalls.Load() < 1 {
		t.Fatal("processor must invoke authority SettleRequest via effect adapter")
	}
	page, err := store.List(context.Background(), workstore.Query{
		RequestID: "req-eff-rst",
		State:     sdk.WorkStateCompleted,
		Limit:     10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Records) == 0 {
		t.Fatal("work must complete after authority effect success")
	}
}

func TestPhase45_AuthorityEffectProviderReleaseDecodesHandle(t *testing.T) {
	t.Parallel()
	inner := &recordingAuthority{id: "quota"}
	effect, err := app.NewAuthorityRequestEffectProvider(app.AuthorityRequestEffectConfig{
		ProviderID: "quota",
		Provider:   inner,
		Version:    "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{"handles": []string{"lease-h1"}})
	rec := terminalwork.WorkRecord{
		WorkID:     "tw_rel",
		SourceKey:  terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_rel"},
		Kind:       sdk.WorkKindReleaseRequestProvider,
		State:      sdk.WorkStateClaimed,
		ProviderID: "quota",
		Lifecycle:  terminalwork.LifecycleCorrelation{RequestID: "req-rel"},
		Payload:    payload,
	}
	if err := effect.Invoke(context.Background(), rec, rec.SourceKey.String()); err != nil {
		t.Fatal(err)
	}
	if inner.releaseCalls.Load() != 1 {
		t.Fatalf("ReleaseRequest calls=%d want 1", inner.releaseCalls.Load())
	}
}

type fixedNow struct{ t time.Time }

func (c fixedNow) Now() time.Time { return c.t }
