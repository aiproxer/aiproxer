package app_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type countingPin struct {
	releases atomic.Int32
}

func (p *countingPin) Kind() genpin.Kind { return genpin.KindProvider }
func (p *countingPin) Release()          { p.releases.Add(1) }

type staticRetainer struct {
	inst string
	id   string
	pin  genpin.Pin
	fail bool
}

func (r *staticRetainer) RuntimeInstanceID() string   { return r.inst }
func (r *staticRetainer) RuntimeGenerationID() string { return r.id }
func (r *staticRetainer) Retain(kind genpin.Kind) (genpin.Pin, bool) {
	if r.fail || kind != genpin.KindProvider || r.pin == nil {
		return nil, false
	}
	return r.pin, true
}

type mapGenerationResolver struct {
	// key = instanceID + "/" + generationID
	byGen map[string]map[string]app.EffectProvider
}

func (m *mapGenerationResolver) Resolve(runtimeInstanceID, runtimeGenerationID, providerID string, kind sdk.WorkKind) (app.EffectProvider, error) {
	key := runtimeInstanceID + "/" + runtimeGenerationID
	gens := m.byGen[key]
	if gens == nil {
		return nil, fmt.Errorf("%w: generation %s", app.ErrMissingProvider, key)
	}
	p, ok := gens[providerID]
	if !ok {
		return nil, fmt.Errorf("%w: %s@%s", app.ErrMissingProvider, providerID, key)
	}
	return p, nil
}

type stubProv struct {
	id      string
	version string
	calls   atomic.Int32
}

func (p *stubProv) ProviderID() string { return p.id }
func (p *stubProv) Version() string    { return p.version }
func (p *stubProv) SupportedKinds() []sdk.WorkKind {
	return []sdk.WorkKind{sdk.WorkKindSettleRequestProvider}
}

func (p *stubProv) Invoke(context.Context, terminalwork.WorkRecord, string) error {
	p.calls.Add(1)
	return nil
}

func TestGenerationPin_Terminal_DistinctIdentitiesAndExactOnceRelease(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "genpin-term"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "42", pin: pin})

	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	in := app.SettleFailureInput{
		RequestID:  "req-1",
		AttemptID:  "att-1",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions: terminalwork.BoundVersions{
			GenerationID: "7", // executable-policy
			ProviderID:   "quota",
			RatingID:     "r1",
		},
	}
	if err := svc.AcceptSettleFailure(ctx, in); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 1 {
		t.Fatalf("pins=%d want 1", pins.Len())
	}
	// Idempotent replay must not double-retain.
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "42", pin: pin2})
	if err := svc.AcceptSettleFailure(ctx2, in); err != nil {
		t.Fatal(err)
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("replay pin releases=%d want 1", pin2.releases.Load())
	}
	if pins.Len() != 1 {
		t.Fatalf("pins after replay=%d", pins.Len())
	}

	page, err := store.List(context.Background(), workstore.Query{RequestID: "req-1", Limit: 10})
	if err != nil || len(page.Records) != 1 {
		t.Fatalf("list: %v n=%d", err, len(page.Records))
	}
	rec := page.Records[0]
	if rec.Versions.ExecutableGenerationID() != "7" {
		t.Fatalf("executable=%q", rec.Versions.ExecutableGenerationID())
	}
	if rec.Versions.RuntimeGenerationID != "42" {
		t.Fatalf("runtime=%q want 42", rec.Versions.RuntimeGenerationID)
	}
	if rec.Versions.RuntimeInstanceID != "inst-a" {
		t.Fatalf("instance=%q want inst-a", rec.Versions.RuntimeInstanceID)
	}
	if rec.Versions.GenerationID == rec.Versions.RuntimeGenerationID {
		t.Fatal("executable and runtime generation ids must stay distinct")
	}

	reg := app.NewRegistry()
	prov := &stubProv{id: "quota", version: "1"}
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}
	resolver := &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
		"inst-a/42": {"quota": prov},
	}}
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:            "o",
		ClaimTTL:           time.Minute,
		ClaimLimit:         4,
		GenerationPins:     pins,
		GenerationResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prov.calls.Load() != 1 {
		t.Fatalf("invoke calls=%d", prov.calls.Load())
	}
	if pins.Len() != 0 {
		t.Fatalf("pins after complete=%d", pins.Len())
	}
	if pin.releases.Load() != 1 {
		t.Fatalf("original pin releases=%d want 1", pin.releases.Load())
	}
	pin.Release() // exact-once no-op at tracker already released
}

func TestGenerationPin_Terminal_ProviderLookupRejectsNewerSameID(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "genpin-lookup"})
	if err != nil {
		t.Fatal(err)
	}
	oldProv := &stubProv{id: "quota", version: "old"}
	newProv := &stubProv{id: "quota", version: "new"}
	reg := app.NewRegistry()
	if err := reg.Register(newProv); err != nil {
		t.Fatal(err)
	}
	// Explicit runtime generation 1 only knows oldProv; process registry has newProv.
	resolver := &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
		"inst-a/1": {}, // generation present but provider absent → fail closed
	}}
	rec := terminalwork.WorkRecord{
		WorkID:         "tw_explicit",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_explicit"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStatePending,
		ProviderID:     "quota",
		Versions: terminalwork.BoundVersions{
			GenerationID:        "99",
			RuntimeInstanceID:   "inst-a",
			RuntimeGenerationID: "1",
			ProviderID:          "quota",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.AppendIntent(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{
		WorkID: rec.WorkID, Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:            "o",
		ClaimTTL:           time.Minute,
		ClaimLimit:         4,
		GenerationResolver: resolver,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.ProcessDue(context.Background())
	if newProv.calls.Load() != 0 || oldProv.calls.Load() != 0 {
		t.Fatalf("must not fall through to process/new provider; new=%d old=%d", newProv.calls.Load(), oldProv.calls.Load())
	}
	ids := proc.UnresolvedProviderIDs()
	if len(ids) == 0 {
		t.Fatal("expected unresolved provider")
	}
}

func TestGenerationPin_Terminal_LegacyRowUsesProcessRegistry(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "genpin-legacy"})
	if err != nil {
		t.Fatal(err)
	}
	prov := &stubProv{id: "quota", version: "1"}
	reg := app.NewRegistry()
	if err := reg.Register(prov); err != nil {
		t.Fatal(err)
	}
	rec := terminalwork.WorkRecord{
		WorkID:         "tw_legacy",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_legacy"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStatePending,
		ProviderID:     "quota",
		Versions: terminalwork.BoundVersions{
			GenerationID: "5", // executable only; no RuntimeGenerationID
			ProviderID:   "quota",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.AppendIntent(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
	if err := store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{
		WorkID: rec.WorkID, Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:    "o",
		ClaimTTL:   time.Minute,
		ClaimLimit: 4,
		// No GenerationResolver: legacy rows use process registry.
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if prov.calls.Load() != 1 {
		t.Fatalf("legacy invoke=%d", prov.calls.Load())
	}
}

func TestGenerationPin_Terminal_HandlerReturnCannotClosePinnedGeneration(t *testing.T) {
	t.Parallel()
	m := runtimehost.NewManager(4, nil)
	closer := &countingCloser{}
	g := m.PrepareOwned("term-pin", closer)
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
	lease, ok := m.Acquire()
	if !ok {
		t.Fatal("acquire")
	}
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "genpin-http"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})

	b := leaseAsBinding(t, lease)
	ctx := genpin.WithRetainer(context.Background(), b)
	if err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-http",
		AttemptID:  "a1",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	lease.Release() // HTTP handler returns
	if g.Refs() < 1 {
		t.Fatalf("refs=%d; terminal pin must retain generation", g.Refs())
	}
	mustPublishNext(t, m)
	select {
	case <-g.Drained():
		t.Fatal("accepted terminal work must block drain")
	default:
	}
	if closer.closes.Load() != 0 {
		t.Fatal("generation force-closed under terminal pin")
	}

	reg := app.NewRegistry()
	prov := &stubProv{id: "quota", version: "1"}
	_ = reg.Register(prov)
	runtimeID := strconv.FormatInt(g.ID(), 10)
	instID := m.InstanceID()
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:        "o",
		ClaimTTL:       time.Minute,
		ClaimLimit:     4,
		GenerationPins: pins,
		GenerationResolver: &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
			instID + "/" + runtimeID: {"quota": prov},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	<-g.Drained()
	if err := g.BeginClose(); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if closer.closes.Load() != 1 {
		t.Fatalf("closes=%d", closer.closes.Load())
	}
}

func TestGenerationPin_Terminal_PartialAppendReleasesPin(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "9", pin: pin})
	svc := app.NewIntentService(&rejectingStore{}, app.IntentServiceConfig{Pins: pins})
	err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-fail",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	})
	if err == nil {
		t.Fatal("expected append failure")
	}
	if pin.releases.Load() != 1 {
		t.Fatalf("releases=%d want 1", pin.releases.Load())
	}
	if pins.Len() != 0 {
		t.Fatalf("tracker leak len=%d", pins.Len())
	}
}

type rejectingStore struct{}

func (rejectingStore) AppendIntent(context.Context, terminalwork.WorkRecord) error {
	return errors.New("append boom")
}

func (rejectingStore) PromotePending(context.Context, terminalwork.PromotePendingCommand) error {
	return nil
}

func (rejectingStore) LookupIntent(context.Context, string) (terminalwork.WorkRecord, bool, error) {
	return terminalwork.WorkRecord{}, false, nil
}

type countingCloser struct{ closes atomic.Int32 }

func (c *countingCloser) Close() error {
	c.closes.Add(1)
	return nil
}

// leaseRetainer adapts a live lease to genpin.Retainer for tests without the dispatcher.
type leaseRetainer struct {
	lease *runtimehost.Lease
}

func (r leaseRetainer) RuntimeInstanceID() string {
	return r.lease.Meta().InstanceID
}

func (r leaseRetainer) RuntimeGenerationID() string {
	id := r.lease.Meta().ID
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func (r leaseRetainer) Retain(kind genpin.Kind) (genpin.Pin, bool) {
	var pk runtimehost.PinKind
	switch kind {
	case genpin.KindSSE:
		pk = runtimehost.PinSSE
	case genpin.KindAsync:
		pk = runtimehost.PinAsync
	case genpin.KindProvider:
		pk = runtimehost.PinProvider
	default:
		return nil, false
	}
	p, ok := r.lease.RetainPin(pk)
	if !ok {
		return nil, false
	}
	return genpinLeasePin{p: p}, true
}

type genpinLeasePin struct{ p *runtimehost.Pin }

func (w genpinLeasePin) Kind() genpin.Kind {
	switch w.p.Kind() {
	case runtimehost.PinSSE:
		return genpin.KindSSE
	case runtimehost.PinAsync:
		return genpin.KindAsync
	case runtimehost.PinProvider:
		return genpin.KindProvider
	default:
		return 0
	}
}
func (w genpinLeasePin) Release() { w.p.Release() }

func leaseAsBinding(t *testing.T, lease *runtimehost.Lease) genpin.Retainer {
	t.Helper()
	return leaseRetainer{lease: lease}
}

func mustPublishNext(t *testing.T, m *runtimehost.Manager) {
	t.Helper()
	g := m.Prepare("next")
	if err := m.Publish(g); err != nil {
		t.Fatal(err)
	}
}
