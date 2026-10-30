package runtimebundle_test

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	terminalworkapp "github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimebundle"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestGenerationProvider_TwoGenerationLocalSameID(t *testing.T) {
	t.Parallel()
	oldProv := &countingEffect{id: "quota", version: "old"}
	newProv := &countingEffect{id: "quota", version: "new"}

	oldView := terminalworkapp.SnapshotTerminalProviders(regWith(t, oldProv))
	newView := terminalworkapp.SnapshotTerminalProviders(regWith(t, newProv))

	mgr := runtimehost.NewManagerWithInstanceID(4, nil, "gen-local")
	oldPlane := &stubPlane{view: oldView}
	newPlane := &stubPlane{view: newView}
	oldGen := mgr.PrepareRequestPlane("old", oldPlane)
	if err := mgr.Publish(oldGen); err != nil {
		t.Fatal(err)
	}
	oldID := oldGen.ID()
	// Pin oldGen so Manager's automatic post-publish retirement (task 7.3)
	// cannot quiesce/close/sweep it before GenerationByIdentity resolves it
	// below (GenerationByIdentity excludes GenClosed generations).
	oldLease, ok := mgr.Acquire()
	if !ok {
		t.Fatal("acquire old generation")
	}
	oldPin, ok := oldLease.TransferPin(runtimehost.PinProvider)
	if !ok {
		t.Fatal("pin old generation")
	}
	t.Cleanup(oldPin.Release)
	newGen := mgr.PrepareRequestPlane("new", newPlane)
	if err := mgr.Publish(newGen); err != nil {
		t.Fatal(err)
	}

	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "two-gen"})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalwork.WorkRecord{
		WorkID:         "tw_old",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_old"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStatePending,
		ProviderID:     "quota",
		Versions: terminalwork.BoundVersions{
			GenerationID:        "1",
			RuntimeInstanceID:   mgr.InstanceID(),
			RuntimeGenerationID: strconv.FormatInt(oldID, 10),
			ProviderID:          "quota",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.AppendIntent(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	_ = store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: time.Now().UTC()})

	resolver := runtimebundle.NewTestGenerationPresentResolver(mgr.GenerationByIdentity)
	procReg := terminalworkapp.NewRegistry()
	_ = procReg.Register(newProv) // process registry has only new
	proc, err := terminalworkapp.NewProcessor(store, procReg, terminalworkapp.Config{
		OwnerID:            "o",
		ClaimTTL:           time.Minute,
		ClaimLimit:         4,
		GenerationResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if oldProv.calls.Load() != 1 {
		t.Fatalf("old provider calls=%d", oldProv.calls.Load())
	}
	if newProv.calls.Load() != 0 {
		t.Fatalf("new provider must not be invoked; calls=%d", newProv.calls.Load())
	}
}

func TestGenerationProvider_MissingOldDoesNotInvokeNew(t *testing.T) {
	t.Parallel()
	newProv := &countingEffect{id: "quota", version: "new"}
	newView := terminalworkapp.SnapshotTerminalProviders(regWith(t, newProv))
	mgr := runtimehost.NewManagerWithInstanceID(4, nil, "missing-old")
	oldPlane := &stubPlane{view: terminalworkapp.SnapshotTerminalProviders(nil)} // no providers
	newPlane := &stubPlane{view: newView}
	oldGen := mgr.PrepareRequestPlane("old", oldPlane)
	if err := mgr.Publish(oldGen); err != nil {
		t.Fatal(err)
	}
	oldID := oldGen.ID()
	if err := mgr.Publish(mgr.PrepareRequestPlane("new", newPlane)); err != nil {
		t.Fatal(err)
	}

	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "missing-old"})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalwork.WorkRecord{
		WorkID:         "tw_miss",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_miss"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStatePending,
		ProviderID:     "quota",
		Versions: terminalwork.BoundVersions{
			RuntimeInstanceID:   mgr.InstanceID(),
			RuntimeGenerationID: strconv.FormatInt(oldID, 10),
			ProviderID:          "quota",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	_ = store.AppendIntent(context.Background(), rec)
	_ = store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{WorkID: rec.WorkID, Now: time.Now().UTC()})

	procReg := terminalworkapp.NewRegistry()
	_ = procReg.Register(newProv)
	proc, err := terminalworkapp.NewProcessor(store, procReg, terminalworkapp.Config{
		OwnerID:            "o",
		ClaimTTL:           time.Minute,
		ClaimLimit:         4,
		GenerationResolver: runtimebundle.NewTestGenerationPresentResolver(mgr.GenerationByIdentity),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.ProcessDue(context.Background())
	if newProv.calls.Load() != 0 {
		t.Fatalf("must not invoke new; calls=%d", newProv.calls.Load())
	}
}

type stubPlane struct {
	view terminalworkapp.TerminalProviderView
}

func (p *stubPlane) Quiesce(context.Context) error { return nil }
func (p *stubPlane) Close() error                  { return nil }
func (p *stubPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *stubPlane) TerminalProviders() terminalworkapp.TerminalProviderView {
	return p.view
}

type countingEffect struct {
	id, version string
	calls       atomic.Int32
}

func (p *countingEffect) ProviderID() string { return p.id }
func (p *countingEffect) Version() string    { return p.version }
func (p *countingEffect) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}
}

func (p *countingEffect) Invoke(context.Context, terminalwork.WorkRecord, string) error {
	p.calls.Add(1)
	return nil
}

func regWith(t *testing.T, p terminalworkapp.EffectProvider) *terminalworkapp.Registry {
	t.Helper()
	r := terminalworkapp.NewRegistry()
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	return r
}
