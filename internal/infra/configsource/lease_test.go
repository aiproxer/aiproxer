package configsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type leaseCloseWaitBarrierContext struct {
	reached chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newLeaseCloseWaitBarrierContext() *leaseCloseWaitBarrierContext {
	return &leaseCloseWaitBarrierContext{reached: make(chan struct{}), done: make(chan struct{})}
}

func (c *leaseCloseWaitBarrierContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *leaseCloseWaitBarrierContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.reached) })
	return c.done
}
func (*leaseCloseWaitBarrierContext) Err() error    { return nil }
func (*leaseCloseWaitBarrierContext) Value(any) any { return nil }

func awaitLeaseCloseWaitBarrier(t *testing.T, reached <-chan struct{}) {
	t.Helper()
	select {
	case <-reached:
	case <-time.After(5 * time.Second): // deadlock guard only; the barrier provides synchronization.
		t.Fatal("Close did not enter its completion wait")
	}
}

func awaitLeaseCloseResult(t *testing.T, results <-chan error) error {
	t.Helper()
	guard, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	select {
	case err := <-results:
		return err
	case <-guard.Done():
		t.Fatal("Close waiter was not notified after final borrower release")
		return nil
	}
}

func TestSourceLeaseOwner_CloseWaitsForBorrowAndCachesFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{1}}
	closeFailure := errors.New("injected close failure")
	var closeCalls atomic.Int32
	owner := newSourceLeaseOwnerWithCloser(file, id, func(f *os.File) error {
		closeCalls.Add(1)
		return errors.Join(f.Close(), closeFailure)
	})
	version := &ActiveSourceVersion{HandleIdentity: id, leaseCore: owner.core, leaseRequired: true}
	borrow, err := version.Borrow()
	if err != nil {
		t.Fatal(err)
	}

	waitCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := owner.Close(waitCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close with canceled wait context=%v want context canceled", err)
	}
	if got := closeCalls.Load(); got != 0 {
		t.Fatalf("file closed with an outstanding borrow: calls=%d", got)
	}
	if _, err := version.Borrow(); err == nil || SafeReasonOf(err) != "lease_closed" {
		t.Fatalf("new borrow after RequestClose must fail with bounded lease_closed reason: %v", err)
	}

	borrow.Release()
	borrow.Release()
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("final idempotent Release close calls=%d want 1", got)
	}
	// The final release must physically release the descriptor: the path is
	// still present while Stat through the handle fails. The closed-handle
	// error value is platform-defined (Unix reports os.ErrClosed, Windows
	// reports an invalid-handle errno), so this asserts the portable property
	// instead. A leaked handle is exactly what blocks temporary directory
	// removal on Windows.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("source path missing at the closed-handle check: %v", err)
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("file handle remained open after final Release: Stat through the handle succeeded")
	}
	first := owner.Close(context.Background())
	second := owner.Close(context.Background())
	if !IsSourceCleanupError(first) || !IsSourceCleanupError(second) || !errors.Is(first, closeFailure) || !errors.Is(second, closeFailure) {
		t.Fatalf("close failure must be cached and marked: first=%v second=%v", first, second)
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("cached close calls=%d want 1", got)
	}
}

func TestSourceLeaseOwner_ConcurrentCloseWaitersShareFinalBorrowFailure(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "test", Scheme: "test"}
	wantCloseErr := errors.New("injected concurrent close failure")
	closeEntered, allowClose := make(chan struct{}), make(chan struct{})
	var closeEnteredOnce, allowCloseOnce sync.Once
	var closeCalls atomic.Int32
	owner := newSourceLeaseOwnerWithCloser(file, id, func(f *os.File) error {
		closeCalls.Add(1)
		closeEnteredOnce.Do(func() { close(closeEntered) })
		<-allowClose
		return errors.Join(f.Close(), wantCloseErr)
	})
	borrow, err := owner.core.borrow(id)
	if err != nil {
		t.Fatal(err)
	}
	defer borrow.Release()
	defer allowCloseOnce.Do(func() { close(allowClose) })

	waiters := []*leaseCloseWaitBarrierContext{
		newLeaseCloseWaitBarrierContext(),
		newLeaseCloseWaitBarrierContext(),
	}
	closeResults := make(chan error, len(waiters))
	for _, waiter := range waiters {
		go func(ctx context.Context) { closeResults <- owner.Close(ctx) }(waiter)
	}
	for _, waiter := range waiters {
		awaitLeaseCloseWaitBarrier(t, waiter.reached)
	}

	releaseDone := make(chan struct{})
	go func() {
		borrow.Release()
		close(releaseDone)
	}()
	awaitLeaseCloseWaitBarrier(t, closeEntered)
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("physical close calls while final close is blocked=%d want 1", got)
	}
	for range waiters {
		select {
		case err := <-closeResults:
			t.Fatalf("Close returned before the physical close completed: %v", err)
		default:
		}
	}

	allowCloseOnce.Do(func() { close(allowClose) })
	<-releaseDone
	var cached error
	for range waiters {
		got := awaitLeaseCloseResult(t, closeResults)
		if !IsSourceCleanupError(got) || !errors.Is(got, wantCloseErr) {
			t.Fatalf("concurrent Close lost the cached cleanup cause: %v", got)
		}
		if cached == nil {
			cached = got
		} else if got != cached {
			t.Fatalf("Close waiters received different cached errors: %p and %p", got, cached)
		}
	}
	if got := closeCalls.Load(); got != 1 {
		t.Fatalf("physical close calls=%d want once", got)
	}
}

func TestSourceOwnerSlot_MoveToClearsSourceAndReturnsDisplacedOwner(t *testing.T) {
	t.Parallel()
	openOwner := func(t *testing.T, id byte) *SourceLeaseOwner {
		t.Helper()
		file, err := os.Create(filepath.Join(t.TempDir(), "source.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return newSourceLeaseOwner(file, FileIdentity{Platform: "test", Scheme: "test", Opaque: [32]byte{id}})
	}
	prior := openOwner(t, 1)
	candidate := openOwner(t, 2)
	destination := NewSourceOwnerSlot(prior)
	source := NewSourceOwnerSlot(candidate)
	if displaced := source.MoveTo(destination); displaced != prior {
		t.Fatalf("displaced owner=%p want %p", displaced, prior)
	}
	if source.Take() != nil {
		t.Fatal("source slot retained close authority after MoveTo")
	}
	if owner := destination.Take(); owner != candidate {
		t.Fatalf("destination owner=%p want candidate %p", owner, candidate)
	}
	if err := prior.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceOwnerSlot_MoveToEmptyPreservesDestination(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := newSourceLeaseOwner(file, FileIdentity{Platform: "test", Scheme: "test"})
	destination := NewSourceOwnerSlot(want)
	if displaced := NewSourceOwnerSlot(nil).MoveTo(destination); displaced != nil {
		t.Fatalf("empty source displaced owner %p", displaced)
	}
	if got := destination.Take(); got != want {
		t.Fatalf("empty move changed destination owner: got %p want %p", got, want)
	}
	if err := want.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSourceBorrowValueCopiesShareReleaseToken(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "source.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease}
	var closes atomic.Int32
	owner := newSourceLeaseOwnerWithCloser(file, id, func(f *os.File) error {
		closes.Add(1)
		return f.Close()
	})
	borrow, err := owner.core.borrow(id)
	if err != nil {
		t.Fatal(err)
	}
	independent, err := owner.core.borrow(id)
	if err != nil {
		t.Fatal(err)
	}
	copyA, copyB := *borrow, *borrow
	if err := owner.RequestClose(); err != nil {
		t.Fatal(err)
	}
	copyA.Release()
	copyB.Release()
	if closes.Load() != 0 {
		t.Fatal("releasing borrow copies also released an independent borrow")
	}
	independent.Release()
	if closes.Load() != 1 {
		t.Fatalf("copied Release calls closed file %d times, want once", closes.Load())
	}
	if err := owner.WaitClosed(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyAtomicReplacement_LinuxRequiresLiveLeaseProvenance(t *testing.T) {
	t.Parallel()
	active := ActiveSourceVersion{
		HandleIdentity: FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{1}},
		PrivateDigest:  [32]byte{1},
	}
	candidate := SourceSnapshot{
		HandleIdentity: FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{2}},
		PrivateDigest:  [32]byte{2}, Bytes: []byte("new"),
	}
	result, category, err := ClassifyAtomicReplacement(active, candidate)
	if result != AtomicReject || category != CategoryNonAtomicUpdate || SafeReasonOf(err) != "lease_unavailable" {
		t.Fatalf("unowned Linux identity must reject: result=%q category=%q reason=%q err=%v", result, category, SafeReasonOf(err), err)
	}
}

func TestSourceLeaseOwner_CloseWaitCancellationLeavesReleaseArmed(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "test", Scheme: "test"}
	var closes atomic.Int32
	owner := newSourceLeaseOwnerWithCloser(file, id, func(f *os.File) error {
		closes.Add(1)
		return f.Close()
	})
	borrow, err := owner.core.borrow(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := owner.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close error=%v want canceled context", err)
	}
	borrow.Release()
	if closes.Load() != 1 {
		t.Fatalf("close calls after final release=%d want 1", closes.Load())
	}
}

func TestSourceOwnerSlotRejectsInvalidCapabilityPairs(t *testing.T) {
	t.Parallel()
	file, err := os.Create(filepath.Join(t.TempDir(), "source"))
	if err != nil {
		t.Fatal(err)
	}
	id := FileIdentity{Platform: "linux", Scheme: identitySchemeLinuxExt4Lease, Opaque: [32]byte{1}}
	owner := newSourceLeaseOwner(file, id)
	defer func() { _ = owner.Close(context.Background()) }()
	valid := ActiveSourceVersion{HandleIdentity: id, leaseCore: owner.core, leaseRequired: true}
	for _, tc := range []struct {
		name string
		edit func(*ActiveSourceVersion)
		slot *SourceOwnerSlot
	}{
		{name: "missing", slot: NewSourceOwnerSlot(nil)},
		{name: "consumed", slot: func() *SourceOwnerSlot { s := NewSourceOwnerSlot(owner); s.Take(); return s }()},
		{name: "forged ownerless", slot: NewSourceOwnerSlot(nil), edit: func(v *ActiveSourceVersion) { v.leaseCore = nil; v.leaseRequired = false }},
		{name: "wrong platform", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.HandleIdentity.Platform = "windows" }},
		{name: "wrong scheme", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.HandleIdentity.Scheme = identitySchemeDeviceInode }},
		{name: "identity mismatch", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.HandleIdentity.Opaque[0] = 2 }},
		{name: "mount mismatch", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.mountID = 7 }},
		{name: "device mismatch", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.devMinor = 7 }},
		{name: "unavailable with owner", slot: NewSourceOwnerSlot(owner), edit: func(v *ActiveSourceVersion) { v.leaseUnavailable = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := valid
			if tc.edit != nil {
				tc.edit(&v)
			}
			var sourceOwner *SourceLeaseOwner
			if tc.slot != nil {
				sourceOwner = tc.slot.owner.Load()
			}
			destination := NewSourceOwnerSlot(owner)
			if tc.slot.ValidFor(&v) {
				t.Fatal("invalid capability accepted")
			}
			if _, ok := tc.slot.MoveMatchingTo(destination, &v); ok {
				t.Fatal("invalid capability moved")
			}
			if destination.owner.Load() != owner {
				t.Fatal("rejection displaced active owner")
			}
			if sourceOwner != nil && (tc.slot.owner.Load() != sourceOwner || !tc.slot.ValidFor(&valid)) {
				t.Fatal("failed ownership transfer discarded the still-valid source owner")
			}
		})
	}
	slot := NewSourceOwnerSlot(owner)
	if !slot.ValidFor(&valid) {
		t.Fatal("valid supported owner rejected")
	}
	if err := owner.RequestClose(); err != nil {
		t.Fatal(err)
	}
	if slot.ValidFor(&valid) {
		t.Fatal("closed owner accepted")
	}
	if _, ok := slot.MoveMatchingTo(NewSourceOwnerSlot(nil), &valid); ok {
		t.Fatal("closed owner moved")
	}
}

func TestSourceOwnerSlotAllowsOnlyExplicitOwnerlessCapabilities(t *testing.T) {
	t.Parallel()
	for _, v := range []ActiveSourceVersion{
		{HandleIdentity: FileIdentity{Platform: "windows", Scheme: identitySchemeFileID}},
		{HandleIdentity: FileIdentity{Platform: "darwin", Scheme: identitySchemeDeviceInode}},
		{HandleIdentity: FileIdentity{Platform: "linux", Scheme: identitySchemeDeviceInode}, leaseRequired: true, leaseUnavailable: true},
	} {
		if !NewSourceOwnerSlot(nil).ValidFor(&v) {
			t.Fatalf("valid ownerless capability rejected: %+v", v)
		}
	}
	for _, id := range []FileIdentity{
		{Platform: "windows", Scheme: identitySchemeDeviceInode},
		{Platform: "darwin", Scheme: identitySchemeFileID},
		{Platform: "linux", Scheme: identitySchemeDeviceInode},
		{Platform: "test", Scheme: "test"},
	} {
		v := ActiveSourceVersion{HandleIdentity: id}
		if NewSourceOwnerSlot(nil).ValidFor(&v) {
			t.Fatalf("forged ownerless capability accepted: %+v", id)
		}
	}
}
