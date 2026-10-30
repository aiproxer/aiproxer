package app_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/snapshotgen"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork"
	"github.com/aiproxer/aiproxer/internal/core/terminalwork/app"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	"github.com/aiproxer/aiproxer/internal/infra/terminalwork/workstore"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/genpin"
	sdk "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

func TestRuntimeGeneration_RestartInstanceCollision(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "restart-collision"})
	if err != nil {
		t.Fatal(err)
	}
	rec := terminalwork.WorkRecord{
		WorkID:         "tw_restart",
		SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_restart"},
		PayloadVersion: 1,
		Kind:           sdk.WorkKindSettleRequestProvider,
		State:          sdk.WorkStatePending,
		ProviderID:     "quota",
		Versions: terminalwork.BoundVersions{
			GenerationID:        "1",
			RuntimeInstanceID:   "instance-A",
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

	mgrB := runtimehost.NewManagerWithInstanceID(4, nil, "instance-B")
	g := mgrB.Prepare("b")
	if err := mgrB.Publish(g); err != nil {
		t.Fatal(err)
	}
	if g.ID() != 1 {
		t.Fatalf("manager B gen id=%d want 1", g.ID())
	}

	oldProv := &stubProv{id: "quota", version: "old"}
	newProv := &stubProv{id: "quota", version: "new"}
	reg := app.NewRegistry()
	_ = reg.Register(newProv)

	resolver := &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
		"instance-B/1": {"quota": newProv},
		"instance-A/1": {"quota": oldProv},
	}}
	// Simulate production resolver identity check: B manager cannot see A rows.
	failClosed := generationBoundResolverFunc(func(inst, gen, providerID string, kind sdk.WorkKind) (app.EffectProvider, error) {
		if inst != "instance-B" {
			return nil, fmt.Errorf("%w: instance mismatch", app.ErrMissingProvider)
		}
		return resolver.Resolve(inst, gen, providerID, kind)
	})
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:            "o",
		ClaimTTL:           time.Minute,
		ClaimLimit:         4,
		GenerationResolver: failClosed,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.ProcessDue(context.Background())
	if newProv.calls.Load() != 0 || oldProv.calls.Load() != 0 {
		t.Fatalf("restart collision must not resolve; new=%d old=%d", newProv.calls.Load(), oldProv.calls.Load())
	}
}

func TestRuntimeGeneration_PartialIdentityFailsClosed(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "partial-id"})
	if err != nil {
		t.Fatal(err)
	}
	prov := &stubProv{id: "quota", version: "1"}
	reg := app.NewRegistry()
	_ = reg.Register(prov)
	for _, versions := range []terminalwork.BoundVersions{
		{GenerationID: "1", RuntimeInstanceID: "only-inst", ProviderID: "quota"},
		{GenerationID: "1", RuntimeGenerationID: "9", ProviderID: "quota"},
	} {
		rec := terminalwork.WorkRecord{
			WorkID:         "tw_partial_" + versions.RuntimeInstanceID + versions.RuntimeGenerationID,
			SourceKey:      terminalwork.SourceKey{IdentityVersion: 1, Key: "sk_" + recKey(versions)},
			PayloadVersion: 1,
			Kind:           sdk.WorkKindSettleRequestProvider,
			State:          sdk.WorkStatePending,
			ProviderID:     "quota",
			Versions:       versions,
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		}
		if err := store.AppendIntent(context.Background(), rec); err != nil {
			t.Fatal(err)
		}
		_ = store.PromotePending(context.Background(), terminalwork.PromotePendingCommand{
			WorkID: rec.WorkID, Now: time.Now().UTC(),
		})
	}
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:    "o",
		ClaimTTL:   time.Minute,
		ClaimLimit: 4,
		GenerationResolver: &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
			"only-inst/9": {"quota": prov},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.ProcessDue(context.Background())
	if prov.calls.Load() != 0 {
		t.Fatalf("partial identity must not invoke provider; calls=%d", prov.calls.Load())
	}
	if len(proc.UnresolvedProviderIDs()) == 0 {
		t.Fatal("expected unresolved")
	}
}

func recKey(v terminalwork.BoundVersions) string {
	return v.RuntimeInstanceID + v.RuntimeGenerationID
}

type generationBoundResolverFunc func(runtimeInstanceID, runtimeGenerationID, providerID string, kind sdk.WorkKind) (app.EffectProvider, error)

func (f generationBoundResolverFunc) Resolve(runtimeInstanceID, runtimeGenerationID, providerID string, kind sdk.WorkKind) (app.EffectProvider, error) {
	return f(runtimeInstanceID, runtimeGenerationID, providerID, kind)
}

func TestGenerationPin_Append_CommitThenErrorAdopts(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "7", pin: pin})
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "commit-then-err"})
	if err != nil {
		t.Fatal(err)
	}
	store := &commitThenErrorStore{inner: backing}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	if err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-cte",
		AttemptID:  "a",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	}); err != nil {
		t.Fatalf("reconcile should succeed: %v", err)
	}
	if pins.Len() != 1 {
		t.Fatalf("pins=%d want 1", pins.Len())
	}
	if pin.releases.Load() != 0 {
		t.Fatalf("pin released early: %d", pin.releases.Load())
	}
}

func TestGenerationPin_Append_DefiniteAbsenceReleases(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "8", pin: pin})
	// Without reconciler handoff, ambiguous not-found still releases the candidate
	// and reports reconciler-not-configured (subtask B wires the reconciler).
	svc := app.NewIntentService(&rejectingStore{}, app.IntentServiceConfig{Pins: pins})
	err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-abs",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	})
	if err == nil || !errors.Is(err, app.ErrAmbiguousAppendReconcilerNotConfigured) {
		t.Fatalf("got %v want reconciler-not-configured", err)
	}
	if pin.releases.Load() != 1 || pins.Len() != 0 {
		t.Fatalf("releases=%d pins=%d", pin.releases.Load(), pins.Len())
	}
}

func TestGenerationPin_Append_LookupErrorHandoffNotTracker(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "8", pin: pin})
	handoff := &capturingHandoff{}
	svc := app.NewIntentService(&lookupErrorStore{}, app.IntentServiceConfig{
		Pins:             pins,
		AmbiguousHandoff: handoff,
	})
	err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-lookup",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	})
	if err == nil || !errors.Is(err, app.ErrDurablePending) {
		t.Fatalf("got %v want ErrDurablePending", err)
	}
	if pins.Len() != 0 || pins.EntryCount() != 0 {
		t.Fatalf("must not park in tracker; pins=%d entries=%d", pins.Len(), pins.EntryCount())
	}
	if handoff.took.Load() != 1 || pin.releases.Load() != 0 {
		t.Fatalf("handoff took=%d pin releases=%d", handoff.took.Load(), pin.releases.Load())
	}
	if handoff.pin != pin {
		t.Fatal("handoff must receive candidate pin")
	}
}

func TestGenerationPin_Append_LookupErrorNoHandoffReleases(t *testing.T) {
	t.Parallel()
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "8b", pin: pin})
	svc := app.NewIntentService(&lookupErrorStore{}, app.IntentServiceConfig{Pins: pins})
	err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-lookup-nohand",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	})
	if err == nil || !errors.Is(err, app.ErrAmbiguousAppendReconcilerNotConfigured) {
		t.Fatalf("got %v want reconciler-not-configured", err)
	}
	if pins.Len() != 0 || pin.releases.Load() != 1 {
		t.Fatalf("must release candidate without tracker park; pins=%d releases=%d", pins.Len(), pin.releases.Load())
	}
}

func TestGenerationPin_Append_ConflictReleasesLoser(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "conflict"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin1 := &countingPin{}
	ctx1 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "1", pin: pin1})
	svc := app.NewIntentService(backing, app.IntentServiceConfig{Pins: pins})
	in := app.SettleFailureInput{
		RequestID:  "req-cf",
		AttemptID:  "a",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	}
	if err := svc.AcceptSettleFailure(ctx1, in); err != nil {
		t.Fatal(err)
	}
	// Force conflict: same WorkID via AppendIntent of different payload under same identity is hard
	// because WorkID is hash-based. Use conflictStore that returns collision after first success.
	conflict := &conflictOnSecondStore{inner: backing}
	pin2 := &countingPin{}
	ctx2 := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "1", pin: pin2})
	svc2 := app.NewIntentService(conflict, app.IntentServiceConfig{Pins: pins})
	err = svc2.AcceptSettleFailure(ctx2, app.SettleFailureInput{
		RequestID:  "req-cf",
		AttemptID:  "a",
		ProviderID: "quota",
		Handles:    []string{"h1"},
		Versions: terminalwork.BoundVersions{
			GenerationID: "1",
			ProviderID:   "quota",
			RatingID:     "different", // SameIntentReplay false vs stored
		},
	})
	if err == nil || !errors.Is(err, app.ErrIntentReplayConflict) {
		t.Fatalf("got %v want conflict", err)
	}
	if pin2.releases.Load() != 1 {
		t.Fatalf("loser releases=%d", pin2.releases.Load())
	}
	if pins.Len() != 1 {
		t.Fatalf("winner pin must remain; len=%d", pins.Len())
	}
}

func TestGenerationPin_Append_PromoteFailureRetainsOne(t *testing.T) {
	t.Parallel()
	backing, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "promote-fail"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "3", pin: pin})
	store := &promoteFailStore{MemoryStore: backing}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	err = svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-pf",
		AttemptID:  "a",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	})
	if err == nil {
		t.Fatal("expected promote failure")
	}
	if pins.Len() != 1 || pin.releases.Load() != 0 {
		t.Fatalf("promote failure must retain pin; pins=%d releases=%d", pins.Len(), pin.releases.Load())
	}
}

func TestGenerationPin_DuplicateConcurrent_OneRuntimeAndExecutable(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "dup-race"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 42, Version: "v1"}
	binder := executableBinder{gen: execGen}
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins, ExecutablePending: binder})

	const n = 32
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			pin := &countingPin{}
			ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "5", pin: pin})
			_ = svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
				RequestID:  "req-race",
				AttemptID:  "a",
				ProviderID: "quota",
				Handles:    []string{"h"},
				Versions:   terminalwork.BoundVersions{GenerationID: "42", ProviderID: "quota"},
			})
		}()
	}
	wg.Wait()
	if pins.Len() != 1 {
		t.Fatalf("runtime pins=%d want 1", pins.Len())
	}
	if execGen.PendingWorkCount() != 1 {
		t.Fatalf("executable pending=%d want 1", execGen.PendingWorkCount())
	}
}

func TestGenerationPin_CompleteFailureRetains_SuccessClearsBoth(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "complete-fail"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	execGen := &snapshotgen.ExecutableGeneration{ID: 7, Version: "v1"}
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "9", pin: pin})
	svc := app.NewIntentService(store, app.IntentServiceConfig{
		Pins:              pins,
		ExecutablePending: executableBinder{gen: execGen},
	})
	if err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-cfail",
		AttemptID:  "a",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "7", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	failStore := &completeFailOnceStore{MemoryStore: store, fail: true}
	reg := app.NewRegistry()
	prov := &stubProv{id: "quota", version: "1"}
	_ = reg.Register(prov)
	proc, err := app.NewProcessor(failStore, reg, app.Config{
		OwnerID:        "o",
		ClaimTTL:       time.Minute,
		ClaimLimit:     4,
		GenerationPins: pins,
		GenerationResolver: &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
			"inst-a/9": {"quota": prov},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = proc.ProcessDue(context.Background())
	if pins.Len() != 1 || execGen.PendingWorkCount() != 1 {
		t.Fatalf("complete failure must retain; pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
	failStore.fail = false
	proc2, err := app.NewProcessor(failStore, reg, app.Config{
		OwnerID:        "o2",
		ClaimTTL:       time.Minute,
		ClaimLimit:     4,
		GenerationPins: pins,
		GenerationResolver: &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
			"inst-a/9": {"quota": prov},
		}},
		Clock: clockFixed{t: time.Now().UTC().Add(2 * time.Minute)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := proc2.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 0 || execGen.PendingWorkCount() != 0 {
		t.Fatalf("success must clear both; pins=%d pending=%d", pins.Len(), execGen.PendingWorkCount())
	}
	if pin.releases.Load() != 1 {
		t.Fatalf("releases=%d", pin.releases.Load())
	}
}

func TestProcessor_OnTerminalDone_RaceAndPanicIsolation(t *testing.T) {
	t.Parallel()
	store, err := workstore.NewMemoryStore(workstore.MemoryConfig{StoreID: "cb-race"})
	if err != nil {
		t.Fatal(err)
	}
	pins := app.NewGenerationPinTracker()
	pin := &countingPin{}
	ctx := genpin.WithRetainer(context.Background(), &staticRetainer{inst: "inst-a", id: "2", pin: pin})
	svc := app.NewIntentService(store, app.IntentServiceConfig{Pins: pins})
	if err := svc.AcceptSettleFailure(ctx, app.SettleFailureInput{
		RequestID:  "req-cb",
		AttemptID:  "a",
		ProviderID: "quota",
		Handles:    []string{"h"},
		Versions:   terminalwork.BoundVersions{GenerationID: "1", ProviderID: "quota"},
	}); err != nil {
		t.Fatal(err)
	}
	reg := app.NewRegistry()
	prov := &stubProv{id: "quota", version: "1"}
	_ = reg.Register(prov)
	var goodCalls atomic.Int32
	proc, err := app.NewProcessor(store, reg, app.Config{
		OwnerID:        "o",
		ClaimTTL:       time.Minute,
		ClaimLimit:     4,
		GenerationPins: pins,
		GenerationResolver: &mapGenerationResolver{byGen: map[string]map[string]app.EffectProvider{
			"inst-a/2": {"quota": prov},
		}},
		OnTerminalDone: func(terminalwork.WorkRecord) {
			panic("boom")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			proc.AddOnTerminalDone(func(terminalwork.WorkRecord) {
				goodCalls.Add(1)
			})
		})
	}
	wg.Wait()
	if err := proc.ProcessDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pins.Len() != 0 || pin.releases.Load() != 1 {
		t.Fatalf("panic must not skip pin release; pins=%d releases=%d", pins.Len(), pin.releases.Load())
	}
	if goodCalls.Load() == 0 {
		t.Fatal("other subscribers must still run")
	}
}

type capturingHandoff struct {
	took atomic.Int32
	pin  genpin.Pin
	mu   sync.Mutex
}

func (h *capturingHandoff) Take(_ context.Context, amb app.AmbiguousAppend) error {
	h.mu.Lock()
	h.pin = amb.Pin
	h.mu.Unlock()
	h.took.Add(1)
	return nil
}

type commitThenErrorStore struct {
	inner *workstore.MemoryStore
}

func (s *commitThenErrorStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	if err := s.inner.AppendIntent(ctx, rec); err != nil {
		return err
	}
	return errors.New("network after commit")
}

func (s *commitThenErrorStore) PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
	return s.inner.PromotePending(ctx, cmd)
}

func (s *commitThenErrorStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	return s.inner.LookupIntent(ctx, workID)
}

type lookupErrorStore struct{}

func (lookupErrorStore) AppendIntent(context.Context, terminalwork.WorkRecord) error {
	return errors.New("append boom")
}

func (lookupErrorStore) PromotePending(context.Context, terminalwork.PromotePendingCommand) error {
	return nil
}

func (lookupErrorStore) LookupIntent(context.Context, string) (terminalwork.WorkRecord, bool, error) {
	return terminalwork.WorkRecord{}, false, errors.New("lookup unavailable")
}

type conflictOnSecondStore struct {
	inner *workstore.MemoryStore
}

func (s *conflictOnSecondStore) AppendIntent(ctx context.Context, rec terminalwork.WorkRecord) error {
	existing, found, err := s.inner.LookupIntent(ctx, rec.WorkID)
	if err != nil {
		return err
	}
	if found && !terminalwork.SameIntentReplay(existing, rec) {
		return errors.New("identity collision")
	}
	return s.inner.AppendIntent(ctx, rec)
}

func (s *conflictOnSecondStore) PromotePending(ctx context.Context, cmd terminalwork.PromotePendingCommand) error {
	return s.inner.PromotePending(ctx, cmd)
}

func (s *conflictOnSecondStore) LookupIntent(ctx context.Context, workID string) (terminalwork.WorkRecord, bool, error) {
	return s.inner.LookupIntent(ctx, workID)
}

type promoteFailStore struct {
	*workstore.MemoryStore
}

func (s *promoteFailStore) PromotePending(context.Context, terminalwork.PromotePendingCommand) error {
	return errors.New("promote boom")
}

type completeFailOnceStore struct {
	*workstore.MemoryStore
	fail bool
}

func (s *completeFailOnceStore) Complete(ctx context.Context, cmd terminalwork.CompleteCommand) error {
	if s.fail {
		return errors.New("complete boom")
	}
	return s.MemoryStore.Complete(ctx, cmd)
}

func (s *completeFailOnceStore) ClaimDue(ctx context.Context, cmd terminalwork.ClaimDueCommand) ([]terminalwork.WorkRecord, error) {
	return s.MemoryStore.ClaimDue(ctx, cmd)
}

func (s *completeFailOnceStore) RenewClaim(ctx context.Context, cmd terminalwork.RenewClaimCommand) error {
	return s.MemoryStore.RenewClaim(ctx, cmd)
}

func (s *completeFailOnceStore) ScheduleRetry(ctx context.Context, cmd terminalwork.ScheduleRetryCommand) error {
	return s.MemoryStore.ScheduleRetry(ctx, cmd)
}

func (s *completeFailOnceStore) Quarantine(ctx context.Context, cmd terminalwork.QuarantineCommand) error {
	return s.MemoryStore.Quarantine(ctx, cmd)
}

type executableBinder struct {
	gen *snapshotgen.ExecutableGeneration
}

func (b executableBinder) Bind(workID string, versions terminalwork.BoundVersions) (func(), bool) {
	if b.gen == nil {
		return nil, false
	}
	if !b.gen.AddPendingWork(workID, versions.ProviderID) {
		return nil, false
	}
	return func() { b.gen.ClearPendingWork(workID) }, true
}

type clockFixed struct{ t time.Time }

func (c clockFixed) Now() time.Time { return c.t }
