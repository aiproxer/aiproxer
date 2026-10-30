package runtime

// PR #1067 teardown-barrier regressions (RED): concurrent teardown must not
// release the replay root before the winning inner close completes (D1),
// a CloseOnly cancel notification must not consume the physical-close
// entitlement (D2), and a close panic must not overwrite the receive-panic
// identity (D3). Each test FAILS on the pre-fix code.

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// pr1067BlockingCloseStream blocks inside its first Close until the test
// releases it, so the test can observe concurrent teardown behavior while
// the winning inner close is still in flight.
type pr1067BlockingCloseStream struct {
	closeEntered chan struct{}
	releaseClose chan struct{}
	closeCalls   atomic.Int32
	recvCalls    atomic.Int32
	recvErr      error
}

func (s *pr1067BlockingCloseStream) Recv(context.Context) (aipapi.Event, error) {
	s.recvCalls.Add(1)
	if s.recvErr != nil {
		return aipapi.Event{}, s.recvErr
	}
	return aipapi.Event{}, io.EOF
}

func (s *pr1067BlockingCloseStream) Close() error {
	if s.closeCalls.Add(1) == 1 {
		close(s.closeEntered)
		<-s.releaseClose
	}
	return nil
}

// pr1067TeardownBarrierAsserts records the production cleanup effects in the
// order the real cleanup performs them (replay-root release, then turn-audit
// finalization) so the test can assert all three D1 properties directly:
// source still open, audit not finalized, cleanup not run.
type pr1067TeardownBarrierAsserts struct {
	rootReleases atomic.Int32
	auditFinals  atomic.Int32
	cleanups     atomic.Int32
}

func (a *pr1067TeardownBarrierAsserts) cleanupFn() func(error) {
	return func(error) {
		a.rootReleases.Add(1)
		a.auditFinals.Add(1)
		a.cleanups.Add(1)
	}
}

func (a *pr1067TeardownBarrierAsserts) requireSilent(t *testing.T, where string) {
	t.Helper()
	if got := a.rootReleases.Load(); got != 0 {
		t.Fatalf("%s: replay root released %d times while inner close blocked, want 0", where, got)
	}
	if got := a.auditFinals.Load(); got != 0 {
		t.Fatalf("%s: turn audit finalized %d times while inner close blocked, want 0", where, got)
	}
	if got := a.cleanups.Load(); got != 0 {
		t.Fatalf("%s: shared cleanup ran %d times while inner close blocked, want 0", where, got)
	}
}

func (a *pr1067TeardownBarrierAsserts) requireOnce(t *testing.T, where string) {
	t.Helper()
	if got := a.rootReleases.Load(); got != 1 {
		t.Fatalf("%s: replay root releases=%d, want exactly 1", where, got)
	}
	if got := a.auditFinals.Load(); got != 1 {
		t.Fatalf("%s: turn audit finals=%d, want exactly 1", where, got)
	}
	if got := a.cleanups.Load(); got != 1 {
		t.Fatalf("%s: shared cleanups=%d, want exactly 1", where, got)
	}
}

// pr1067ParkTimeout bounds the "competitor must stay parked" window. It is a
// deadlock backstop, not synchronization: progress is channel-driven, and
// expiry fails the test. A broken gate lets the competitor through in
// microseconds, so expiry always means the gate held for the whole window.
const pr1067ParkTimeout = 3 * time.Second

// D1, pair 1: two concurrent Close callers. The loser must block behind the
// winning inner close instead of skipping it and releasing the root early.
func TestPR1067_D1_ConcurrentCloseHoldsRootUntilInnerCloseCompletes(t *testing.T) {
	inner := &pr1067BlockingCloseStream{
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
	}
	var barrier pr1067TeardownBarrierAsserts
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup:     barrier.cleanupFn(),
	}

	aDone := make(chan struct{})
	go func() {
		_ = wles.Close()
		close(aDone)
	}()
	<-inner.closeEntered // A provably blocked inside inner.Close.

	bStarted := make(chan struct{})
	bDone := make(chan struct{})
	go func() {
		close(bStarted)
		_ = wles.Close()
		close(bDone)
	}()
	<-bStarted

	select {
	case <-bDone:
		close(inner.releaseClose)
		<-aDone
		t.Fatal("concurrent Close completed while inner Close blocked: " +
			"shared cleanup ran before the inner close completed")
	case <-time.After(pr1067ParkTimeout):
	}

	barrier.requireSilent(t, "parked competitor")
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("parked competitor: inner Close calls=%d, want exactly 1 (no double-close)", got)
	}

	close(inner.releaseClose)
	<-aDone
	<-bDone

	barrier.requireOnce(t, "after release")
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("after release: inner Close calls=%d, want exactly 1", got)
	}
}

// D1, pair 2: a Recv-termination teardown holds the gate while a concurrent
// Close arrives. The Close must wait for the winning close instead of
// releasing the root around it.
func TestPR1067_D1_RecvTerminationHoldsGateAgainstConcurrentClose(t *testing.T) {
	recvBoom := errors.New("pr1067 d1 recv failure")
	inner := &pr1067BlockingCloseStream{
		closeEntered: make(chan struct{}),
		releaseClose: make(chan struct{}),
		recvErr:      recvBoom,
	}
	var barrier pr1067TeardownBarrierAsserts
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup:     barrier.cleanupFn(),
	}

	// A holds the gate through the Recv-error termination path (Recv itself
	// never sets the closed flag, so this deterministically reaches teardown,
	// unlike a Recv racing a prior Close which takes the EOF shortcut).
	aDone := make(chan struct{})
	aErr := make(chan error, 1)
	go func() {
		_, err := wles.Recv(context.Background())
		aErr <- err
		close(aDone)
	}()
	<-inner.closeEntered // A provably blocked inside inner.Close.

	bStarted := make(chan struct{})
	bDone := make(chan struct{})
	go func() {
		close(bStarted)
		_ = wles.Close()
		close(bDone)
	}()
	<-bStarted

	select {
	case <-bDone:
		close(inner.releaseClose)
		<-aDone
		t.Fatal("concurrent Close completed while Recv-termination close blocked: " +
			"shared cleanup ran before the inner close completed")
	case <-time.After(pr1067ParkTimeout):
	}

	barrier.requireSilent(t, "parked competitor")
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("parked competitor: inner Close calls=%d, want exactly 1 (no double-close)", got)
	}

	close(inner.releaseClose)
	<-aDone
	<-bDone

	if err := <-aErr; !errors.Is(err, recvBoom) {
		t.Fatalf("terminating Recv err=%v, want %v", err, recvBoom)
	}
	barrier.requireOnce(t, "after release")
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("after release: inner Close calls=%d, want exactly 1", got)
	}
}

// pr1067CloseOnlyStream models aipapi.CloseOnlyManagedStream: its Cancel
// reports CancelModeCloseOnly WITHOUT closing the stream.
type pr1067CloseOnlyStream struct {
	closeCalls  atomic.Int32
	cancelCalls atomic.Int32
}

func (s *pr1067CloseOnlyStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *pr1067CloseOnlyStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *pr1067CloseOnlyStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// D2: a CloseOnly cancellation notification must not consume the
// physical-close entitlement. Outer Cancel then outer Close must leave the
// inner stream closed exactly once.
func TestPR1067_D2_CloseOnlyCancelThenCloseClosesExactlyOnce(t *testing.T) {
	inner := &pr1067CloseOnlyStream{}
	var cleanups atomic.Int32
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup:     func(error) { cleanups.Add(1) },
	}

	res := wles.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeCloseOnly {
		t.Fatalf("CancelResult.Mode=%q, want %q", res.Mode, aipapi.CancelModeCloseOnly)
	}
	if got := inner.cancelCalls.Load(); got != 1 {
		t.Fatalf("inner Cancel notifications=%d, want exactly 1", got)
	}
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("inner Close calls after outer Cancel=%d, want exactly 1 "+
			"(CloseOnly notify performs no close; the wrapper still owes it)", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups after outer Cancel=%d, want exactly 1", got)
	}

	if err := wles.Close(); err != nil {
		t.Fatalf("later Close err=%v, want nil", err)
	}
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("inner Close calls after later Close=%d, want exactly 1 (no double-close)", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups after later Close=%d, want exactly 1", got)
	}
}

// pr1067SelfClosingProviderStream stops itself during Cancel (provider mode),
// so the wrapper must trust the notification and never re-close.
type pr1067SelfClosingProviderStream struct {
	closeCalls  atomic.Int32
	cancelCalls atomic.Int32
}

func (s *pr1067SelfClosingProviderStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *pr1067SelfClosingProviderStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *pr1067SelfClosingProviderStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	_ = s.Close()
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

// D2 guard: a managed stream that closes itself during a provider-mode
// Cancel must not be closed again by the wrapper, neither in Cancel nor in
// a later Close.
func TestPR1067_D2_ProviderCancelDoesNotDoubleClose(t *testing.T) {
	inner := &pr1067SelfClosingProviderStream{}
	var cleanups atomic.Int32
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup:     func(error) { cleanups.Add(1) },
	}

	res := wles.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	if res.Mode != aipapi.CancelModeProvider {
		t.Fatalf("CancelResult.Mode=%q, want %q", res.Mode, aipapi.CancelModeProvider)
	}
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("inner Close calls after outer Cancel=%d, want exactly 1 (self-close only)", got)
	}

	if err := wles.Close(); err != nil {
		t.Fatalf("later Close err=%v, want nil", err)
	}
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("inner Close calls after later Close=%d, want exactly 1 (no double-close)", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups=%d, want exactly 1", got)
	}
}

// pr1067SlowCancelStream is a managed stream whose first Cancel blocks until
// released, so a concurrent caller must wait behind the once-only
// notification gate.
type pr1067SlowCancelStream struct {
	cancelEntered chan struct{}
	releaseCancel chan struct{}
	cancelCalls   atomic.Int32
	closeCalls    atomic.Int32
}

func (s *pr1067SlowCancelStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *pr1067SlowCancelStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *pr1067SlowCancelStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	if s.cancelCalls.Add(1) == 1 {
		close(s.cancelEntered)
		<-s.releaseCancel
	}
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}

// D2: cancellation notification is once-only and separate from physical
// closure. Two concurrent Cancel callers must produce exactly one inner
// notification; the winner returns the provider result, the loser reports
// CloseOnly without touching the inner stream, and the provider-mode
// notification consumes the physical-close entitlement.
func TestPR1067_D2_ConcurrentCancelNotifiesInnerOnceAndLoserReportsCloseOnly(t *testing.T) {
	inner := &pr1067SlowCancelStream{
		cancelEntered: make(chan struct{}),
		releaseCancel: make(chan struct{}),
	}
	var cleanups atomic.Int32
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup:     func(error) { cleanups.Add(1) },
	}

	entered := make(chan struct{}, 2)
	results := make(chan aipapi.CancelResult, 2)
	for range 2 {
		go func() {
			entered <- struct{}{}
			results <- wles.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
		}()
	}
	<-entered
	<-entered
	<-inner.cancelEntered // the winning notification is provably in flight
	close(inner.releaseCancel)

	modes := map[aipapi.CancelMode]int{}
	for range 2 {
		res := <-results
		modes[res.Mode]++
	}
	if got := inner.cancelCalls.Load(); got != 1 {
		t.Fatalf("inner Cancel notifications=%d, want exactly 1", got)
	}
	if got := modes[aipapi.CancelModeProvider]; got != 1 {
		t.Fatalf("provider-mode results=%d, want exactly 1 (modes=%v)", got, modes)
	}
	if got := modes[aipapi.CancelModeCloseOnly]; got != 1 {
		t.Fatalf("loser CloseOnly results=%d, want exactly 1 (modes=%v)", got, modes)
	}
	if got := inner.closeCalls.Load(); got != 0 {
		t.Fatalf("inner Close calls=%d, want 0 (the provider notification is the lifetime stop)", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups=%d, want exactly 1", got)
	}
}

// pr1067PanicCancelStream panics in Cancel without closing the stream.
type pr1067PanicCancelStream struct {
	cancelCalls atomic.Int32
	closeCalls  atomic.Int32
}

func (s *pr1067PanicCancelStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *pr1067PanicCancelStream) Close() error {
	s.closeCalls.Add(1)
	return nil
}

func (s *pr1067PanicCancelStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	panic("inner cancel exploded")
}

// D2: a panicking cancellation notification resolves the physical-close gate
// (the deferred consume in Cancel), so the wrapper treats the notification as
// the lifetime stop and a later Close must not issue a second close; the
// once-cleanup still runs exactly once before the panic propagates.
func TestPR1067_D2_CancelPanicConsumesCloseEntitlement(t *testing.T) {
	inner := &pr1067PanicCancelStream{}
	var cleanups atomic.Int32
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup:     func(error) { cleanups.Add(1) },
	}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = wles.Cancel(context.Background(), aipapi.CancelCause{Kind: aipapi.CancelExplicit})
	}()
	if recovered != "inner cancel exploded" {
		t.Fatalf("recovered=%v, want the inner cancel panic", recovered)
	}
	if got := inner.cancelCalls.Load(); got != 1 {
		t.Fatalf("inner Cancel notifications=%d, want exactly 1", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups=%d, want exactly 1", got)
	}
	if got := inner.closeCalls.Load(); got != 0 {
		t.Fatalf("inner Close calls after panicking Cancel=%d, want 0 (notification is the lifetime stop)", got)
	}

	if err := wles.Close(); err != nil {
		t.Fatalf("later Close err=%v, want nil", err)
	}
	if got := inner.closeCalls.Load(); got != 0 {
		t.Fatalf("inner Close calls after later Close=%d, want 0 (gate already consumed)", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups after later Close=%d, want exactly 1", got)
	}
}

// pr1067PanicPanicStream panics distinctively on both Recv and Close.
type pr1067PanicPanicStream struct {
	recvPanic  error
	closePanic error
	closeCalls atomic.Int32
}

func (s *pr1067PanicPanicStream) Recv(context.Context) (aipapi.Event, error) {
	panic(s.recvPanic)
}

func (s *pr1067PanicPanicStream) Close() error {
	s.closeCalls.Add(1)
	panic(s.closePanic)
}

// D3: when both Recv and the teardown Close panic, the ORIGINAL receive
// panic must propagate (identity via errors.Is on error sentinels), the
// secondary close panic must still be reported through the cleanup cause,
// and the audit must finalize exactly once.
func TestPR1067_D3_RecvPanicIdentitySurvivesClosePanic(t *testing.T) {
	recvSentinel := errors.New("pr1067 d3 recv panic")
	closeSentinel := errors.New("pr1067 d3 close panic")
	inner := &pr1067PanicPanicStream{recvPanic: recvSentinel, closePanic: closeSentinel}
	var cleanups atomic.Int32
	var cleanupCause atomic.Value
	wles := &wireLifecycleEventStream{
		EventStream: inner,
		cleanup: func(err error) {
			cleanups.Add(1)
			if err != nil {
				cleanupCause.Store(err)
			}
		},
	}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = wles.Recv(context.Background())
	}()
	recErr, ok := recovered.(error)
	if !ok {
		t.Fatalf("propagated panic has type %T (%v), want the original error sentinel", recovered, recovered)
	}
	if !errors.Is(recErr, recvSentinel) {
		t.Fatalf("propagated panic=%v, want original receive sentinel %v", recErr, recvSentinel)
	}
	if errors.Is(recErr, closeSentinel) {
		t.Fatalf("close panic overwrote the receive panic identity: %v", recErr)
	}
	if got := inner.closeCalls.Load(); got != 1 {
		t.Fatalf("inner Close calls=%d, want exactly 1", got)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups=%d, want exactly 1 (audit finalizes once)", got)
	}
	cause, ok := cleanupCause.Load().(error)
	if !ok || cause == nil {
		t.Fatal("cleanup must observe a failure cause")
	}
	if !errors.Is(cause, recvSentinel) {
		t.Fatalf("cleanup cause=%v, must retain the primary receive failure", cause)
	}
	if !errors.Is(cause, closeSentinel) {
		t.Fatalf("cleanup cause=%v, must separately report the secondary close panic", cause)
	}

	if err := wles.Close(); err != nil {
		t.Fatalf("later Close err=%v, want nil", err)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("shared cleanups after later Close=%d, want exactly 1", got)
	}
}
