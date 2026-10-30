package runtimebundle

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/configreload"
	"github.com/aiproxer/aiproxer/internal/infra/configsource"
	"github.com/aiproxer/aiproxer/internal/infra/runtimehost"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// Task 7.4: Host is the sole process shutdown coordinator. These tests pin the
// canonical ordering (reject reload → wait candidate → retire generations →
// close process → tracing last), retry semantics at each failure boundary, and
// concurrent Close admission that honours every caller's own context.
// Synchronization uses channels and synchronous state barriers, not sleeps.

// --- deterministic recording helpers ---

type hostCloseLog struct {
	mu     sync.Mutex
	events []string
}

func (l *hostCloseLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *hostCloseLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func (l *hostCloseLog) count(event string) int {
	n := 0
	for _, e := range l.snapshot() {
		if e == event {
			n++
		}
	}
	return n
}

func assertCloseOrder(t *testing.T, log *hostCloseLog, want ...string) {
	t.Helper()
	events := log.snapshot()
	prev := -1
	for _, event := range want {
		at := slices.Index(events, event)
		if at <= prev {
			t.Fatalf("expected order %v, got %v", want, events)
		}
		prev = at
	}
}

// awaitState spins on a synchronous predicate, yielding the processor between
// observations. It bounds itself only to fail a hung test, never to sleep.
func awaitState(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

// --- host collaborators under test ---

type closeTestPlane struct {
	log    *hostCloseLog
	name   string
	closed atomic.Bool
}

func (p *closeTestPlane) Handler() http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
}

func (p *closeTestPlane) Quiesce(context.Context) error { return nil }

func (p *closeTestPlane) Close() error {
	if p.closed.Swap(true) {
		return runtimehost.ErrAlreadyClosed
	}
	p.log.add("generation")
	return nil
}

func (p *closeTestPlane) BackendFactoryKindCounts() map[string]int {
	return map[string]int{"local-stub": 1}
}

type closeTestSource struct{}

func (closeTestSource) AbsolutePath() string { return "/fixed/startup/config.yaml" }

func (closeTestSource) ReadStable(context.Context, *configsource.ActiveSourceVersion) (configsource.SourceSnapshot, configsource.AtomicResult, error) {
	snap := configsource.SourceSnapshot{Bytes: []byte("candidate: 1"), HandleIdentity: configsource.FileIdentity{Platform: "windows", Scheme: "win-fileid"}}
	snap.PrivateDigest[0] = 2
	return snap, configsource.AtomicEligible, nil
}

// closeStageGate blocks a candidate compile until the test releases it.
type closeStageGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newCloseStageGate() *closeStageGate {
	return &closeStageGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *closeStageGate) awaitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(20 * time.Second):
		t.Fatal("candidate compile never started")
	}
}

func (g *closeStageGate) releaseOnce() { g.once.Do(func() { close(g.release) }) }

type closeHoldingCompiler struct {
	gate *closeStageGate
	log  *hostCloseLog
}

// Compile parks the candidate until the test releases it. It deliberately
// ignores ctx cancellation so the host's reject-reload phase is observable
// before the candidate finishes rolling back.
func (c *closeHoldingCompiler) Compile(context.Context, *config.Config, map[string]int) (runtimehost.PublishedRequestPlane, error) {
	select {
	case <-c.gate.entered:
	default:
		close(c.gate.entered)
	}
	<-c.gate.release
	c.log.add("candidate-rollback")
	return nil, errors.New("candidate abandoned during shutdown")
}

type closeHostOptions struct {
	compile    runtimehost.CandidateCompiler
	processErr error
	tracing    func(context.Context) error
}

func closeTestEffective(fingerprint string, digest byte) *config.EffectiveConfig {
	eff := &config.EffectiveConfig{
		Config:   &config.Config{},
		Identity: config.EffectiveIdentity{PublicFingerprint: fingerprint},
		LoadedAt: time.Now().UTC(),
	}
	eff.Identity.PrivateDigest[0] = digest
	return eff
}

// newCloseTestHost builds a complete Host wired to recording collaborators.
func newCloseTestHost(t *testing.T, log *hostCloseLog, opts closeHostOptions) *Host {
	t.Helper()
	mgr := runtimehost.NewManager(8, nil)
	g0 := mgr.PrepareRequestPlane("startup", &closeTestPlane{log: log, name: "g1"})
	g0.SetMetaHints(runtimehost.MetaHints{PublicFingerprint: "fp-startup", TriggerKind: "startup"})
	if err := mgr.Publish(g0); err != nil {
		t.Fatalf("publish gen1: %v", err)
	}

	compile := opts.compile
	if compile == nil {
		compile = &closeHoldingCompiler{gate: newCloseStageGate(), log: log}
	}
	coord, err := runtimehost.NewCoordinator(runtimehost.CoordinatorDeps{
		Source: closeTestSource{},
		Loader: runtimehost.FuncEffectiveLoader(func(context.Context, []byte) (*config.EffectiveConfig, error) {
			return closeTestEffective("fp-candidate", 2), nil
		}),
		Compile:         compile,
		Manager:         mgr,
		Timeout:         20 * time.Second,
		ActiveEffective: closeTestEffective("fp-startup", 1),
	})
	if err != nil {
		t.Fatalf("NewCoordinator: %v", err)
	}

	ps := &ProcessServices{}
	ps.closers = []func() error{func() error {
		log.add("process")
		return opts.processErr
	}}

	host := &Host{
		coordinator:     coord,
		manager:         mgr,
		process:         ps,
		shutdownTracing: opts.tracing,
	}
	t.Cleanup(func() {
		coord.BeginShutdown()
		_ = mgr.ShutdownDetached(context.Background())
		_ = ps.Close()
	})
	return host
}

func recordingTracing(log *hostCloseLog, fail func() error) func(context.Context) error {
	return func(context.Context) error {
		log.add("tracing")
		if fail == nil {
			return nil
		}
		return fail()
	}
}

// --- ordering ---

// TestHostClose_ShutdownOrderIsCanonical proves one Host.Close rejects reload
// triggers first, waits for the in-flight candidate to roll back, retires
// generations, closes process services, and shuts tracing down last.
func TestHostClose_ShutdownOrderIsCanonical(t *testing.T) {
	t.Parallel()
	log := &hostCloseLog{}
	gate := newCloseStageGate()
	t.Cleanup(gate.releaseOnce)
	host := newCloseTestHost(t, log, closeHostOptions{
		compile: &closeHoldingCompiler{gate: gate, log: log},
		tracing: recordingTracing(log, nil),
	})

	reloadDone := make(chan sdkreload.Result, 1)
	go func() {
		reloadDone <- host.coordinator.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	}()
	gate.awaitEntered(t)

	closeDone := make(chan error, 1)
	go func() { closeDone <- host.Close(context.Background()) }()

	// Reload rejection is the first shutdown phase: it is observable before the
	// candidate rolls back and long before any generation/process teardown.
	awaitState(t, "reload trigger rejection", host.manager.ShuttingDown)
	raced := host.coordinator.Reload(context.Background(), sdkreload.Trigger{Kind: sdkreload.TriggerAPI})
	if raced.Category != sdkreload.ResultCanceled || raced.ReasonCategory != configreload.StageShutdown {
		t.Fatalf("reload raced with shutdown must be rejected, got %+v", raced)
	}
	if log.count("generation") != 0 || log.count("process") != 0 || log.count("tracing") != 0 {
		t.Fatalf("teardown must not start before the candidate rolls back; got %v", log.snapshot())
	}

	gate.releaseOnce()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Close did not complete")
	}
	<-reloadDone

	assertCloseOrder(t, log, "candidate-rollback", "generation", "process", "tracing")
}

// TestHostClose_SuccessIsIdempotent proves a terminal Close is never repeated.
func TestHostClose_SuccessIsIdempotent(t *testing.T) {
	t.Parallel()
	log := &hostCloseLog{}
	host := newCloseTestHost(t, log, closeHostOptions{tracing: recordingTracing(log, nil)})

	for i := range 3 {
		if err := host.Close(context.Background()); err != nil {
			t.Fatalf("Close #%d: %v", i, err)
		}
	}
	if got := log.count("process"); got != 1 {
		t.Fatalf("process closes=%d want 1", got)
	}
	if got := log.count("tracing"); got != 1 {
		t.Fatalf("tracing shutdowns=%d want 1", got)
	}
}

// --- retry at each failure boundary ---

// TestHostClose_PinnedGenerationDeadlineLeavesProcessOpenAndRetrySucceeds
// proves a pinned generation is never force-closed to reclaim shutdown, that
// process/tracing stay untouched, and that releasing the pin makes a later
// Close attempt succeed.
func TestHostClose_PinnedGenerationDeadlineLeavesProcessOpenAndRetrySucceeds(t *testing.T) {
	t.Parallel()
	log := &hostCloseLog{}
	host := newCloseTestHost(t, log, closeHostOptions{tracing: recordingTracing(log, nil)})

	lease, ok := host.manager.Acquire()
	if !ok {
		t.Fatal("acquire active lease")
	}
	pin, ok := lease.TransferPin(runtimehost.PinAsync)
	if !ok {
		t.Fatal("transfer pin")
	}

	pinnedCtx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if err := host.Close(pinnedCtx); err == nil {
		t.Fatal("expected pinned-drain deadline failure")
	}
	if got := log.count("generation"); got != 0 {
		t.Fatalf("pinned generation closes=%d want 0", got)
	}
	if got := log.count("process"); got != 0 {
		t.Fatalf("process closes under pin=%d want 0", got)
	}
	if got := log.count("tracing"); got != 0 {
		t.Fatalf("tracing shutdowns under pin=%d want 0", got)
	}
	if host.process.Closed() {
		t.Fatal("process must stay open beneath pinned work")
	}

	pin.Release()
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("retry Close after pin release: %v", err)
	}
	assertCloseOrder(t, log, "generation", "process", "tracing")
	if got := log.count("tracing"); got != 1 {
		t.Fatalf("tracing shutdowns=%d want 1", got)
	}
}

// TestHostClose_ProcessCloseFailurePreventsTracingAndRetryStaysTruthful
// proves tracing never runs after a failed process close and that a retry
// reports the unresolved failure instead of claiming success.
func TestHostClose_ProcessCloseFailurePreventsTracingAndRetryStaysTruthful(t *testing.T) {
	t.Parallel()
	log := &hostCloseLog{}
	host := newCloseTestHost(t, log, closeHostOptions{
		processErr: errors.New("process close boom"),
		tracing:    recordingTracing(log, nil),
	})

	err := host.Close(context.Background())
	if err == nil || !strings.Contains(err.Error(), "process close boom") {
		t.Fatalf("Close err=%v want process close failure", err)
	}
	if got := log.count("tracing"); got != 0 {
		t.Fatalf("tracing must not run after process close failure; got %d", got)
	}

	if retry := host.Close(context.Background()); retry == nil {
		t.Fatal("retry must not report success while process close is unresolved")
	}
	if got := log.count("tracing"); got != 0 {
		t.Fatalf("tracing must stay unrun on retry; got %d", got)
	}
}

// TestHostClose_TracingFailureRetrySucceedsWithoutRepeatingProcessClose proves
// tracing is retryable and already-successful phases are not repeated.
func TestHostClose_TracingFailureRetrySucceedsWithoutRepeatingProcessClose(t *testing.T) {
	t.Parallel()
	log := &hostCloseLog{}
	var attempts atomic.Int32
	host := newCloseTestHost(t, log, closeHostOptions{
		tracing: recordingTracing(log, func() error {
			if attempts.Add(1) == 1 {
				return errors.New("tracing flush boom")
			}
			return nil
		}),
	})

	if err := host.Close(context.Background()); err == nil {
		t.Fatal("expected tracing failure")
	}
	if got := log.count("process"); got != 1 {
		t.Fatalf("process closes=%d want 1", got)
	}

	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("retry after tracing failure: %v", err)
	}
	if got := log.count("process"); got != 1 {
		t.Fatalf("process closes after retry=%d want 1 (successful phase must not repeat)", got)
	}
	if got := log.count("tracing"); got != 2 {
		t.Fatalf("tracing attempts=%d want 2", got)
	}
	if err := host.Close(context.Background()); err != nil {
		t.Fatalf("terminal Close: %v", err)
	}
	if got := log.count("tracing"); got != 2 {
		t.Fatalf("tracing must not run again after success; got %d", got)
	}
}

// --- concurrency ---

// TestHostClose_ConcurrentCloseSharesTerminalResultWithoutOverlap proves only
// one attempt mutates shutdown phases at a time and that later callers observe
// the shared successful terminal result.
func TestHostClose_ConcurrentCloseSharesTerminalResultWithoutOverlap(t *testing.T) {
	t.Parallel()
	log := &hostCloseLog{}
	var inTracing, maxOverlap atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	host := newCloseTestHost(t, log, closeHostOptions{
		tracing: func(context.Context) error {
			log.add("tracing")
			n := inTracing.Add(1)
			for {
				cur := maxOverlap.Load()
				if n <= cur || maxOverlap.CompareAndSwap(cur, n) {
					break
				}
			}
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			inTracing.Add(-1)
			return nil
		},
	})

	const callers = 6
	errs := make(chan error, callers)
	for range callers {
		go func() { errs <- host.Close(context.Background()) }()
	}
	<-entered
	close(release)

	for range callers {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
	if got := maxOverlap.Load(); got != 1 {
		t.Fatalf("overlapping shutdown attempts=%d want 1", got)
	}
	if got := log.count("process"); got != 1 {
		t.Fatalf("process closes=%d want 1", got)
	}
	if got := log.count("tracing"); got != 1 {
		t.Fatalf("tracing shutdowns=%d want 1", got)
	}
}

// TestHostClose_ConcurrentCloseHonorsOwnContextWhileWaiting proves a second
// caller with a shorter context is not trapped behind the in-flight attempt.
func TestHostClose_ConcurrentCloseHonorsOwnContextWhileWaiting(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := &hostCloseLog{}
		tracingEntered := make(chan struct{})
		releaseTracing := make(chan struct{})
		host := newCloseTestHost(t, log, closeHostOptions{
			tracing: func(context.Context) error {
				close(tracingEntered)
				<-releaseTracing
				log.add("tracing")
				return nil
			},
		})

		first := make(chan error, 1)
		go func() { first <- host.Close(context.Background()) }()
		<-tracingEntered

		impatient, cancel := context.WithCancel(context.Background())
		second := make(chan error, 1)
		go func() { second <- host.Close(impatient) }()
		synctest.Wait() // waiter durably blocked on the in-flight attempt
		cancel()
		synctest.Wait()

		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting caller err=%v want context.Canceled", err)
		}

		close(releaseTracing)
		synctest.Wait()
		if err := <-first; err != nil {
			t.Fatalf("first Close: %v", err)
		}
		if err := host.Close(context.Background()); err != nil {
			t.Fatalf("terminal Close after shared success: %v", err)
		}
		if got := log.count("tracing"); got != 1 {
			t.Fatalf("tracing shutdowns=%d want 1", got)
		}
	})
}

// TestHostClose_SyntheticAttemptSharedResult proves Close waiters share a
// synthetic in-flight attempt's published error without any production test hook.
func TestHostClose_SyntheticAttemptSharedResult(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		host := &Host{}
		attempt := &hostCloseAttempt{done: make(chan struct{})}
		want := errors.New("shared synthetic boom")
		host.closeMu.Lock()
		host.closeAttempt = attempt
		host.closeMu.Unlock()

		const waiters = 8
		results := make(chan error, waiters)
		for range waiters {
			go func() { results <- host.Close(context.Background()) }()
		}
		synctest.Wait() // all waiters durably blocked on attempt.done

		// Publish in the production order while holding closeMu.
		host.closeMu.Lock()
		attempt.err = want
		close(attempt.done)
		synctest.Wait() // waiters observe done and return without reacquiring closeMu
		for range waiters {
			if got := <-results; got == nil || got.Error() != want.Error() {
				t.Fatalf("waiter err=%v want %v", got, want)
			}
		}
		if host.closeAttempt != attempt {
			t.Fatal("retry slot must stay occupied until after waiter notification")
		}
		host.closeAttempt = nil
		host.closeMu.Unlock()
	})
}

// TestHostClose_PublicationNotifiesWaitersBeforeRetrySlot proves a later
// caller cannot enter attempt N+1 before waiters on N are notified: done closes
// while closeAttempt is still set, and only then is the slot cleared.
func TestHostClose_PublicationNotifiesWaitersBeforeRetrySlot(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		host := &Host{}
		attempt := &hostCloseAttempt{done: make(chan struct{})}
		want := errors.New("publish-order boom")
		host.closeMu.Lock()
		host.closeAttempt = attempt
		host.closeMu.Unlock()

		waiter := make(chan error, 1)
		go func() { waiter <- host.Close(context.Background()) }()
		synctest.Wait()

		host.closeMu.Lock()
		attempt.err = want
		close(attempt.done) // notify while retry slot still occupied
		synctest.Wait()
		if got := <-waiter; got == nil || got.Error() != want.Error() {
			t.Fatalf("waiter err=%v want %v", got, want)
		}
		if host.closeAttempt != attempt {
			t.Fatal("clearing closeAttempt before notification would admit N+1 early")
		}
		// Until clear+unlock, a new Close cannot become owner of N+1.
		host.closeAttempt = nil
		host.closeMu.Unlock()

		// After full publication, a later caller may start a fresh attempt.
		if err := host.Close(context.Background()); err != nil {
			t.Fatalf("later retry after published failure: %v", err)
		}
	})
}

// TestHostClose_ConcurrentCloseSharesFailedAttemptResult proves a waiting
// caller receives the in-flight attempt's exact failure and does not silently
// start a retry. A later third caller may explicitly retry incomplete phases.
func TestHostClose_ConcurrentCloseSharesFailedAttemptResult(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		log := &hostCloseLog{}
		var attempts atomic.Int32
		releaseFirst := make(chan struct{})
		host := newCloseTestHost(t, log, closeHostOptions{
			tracing: func(context.Context) error {
				log.add("tracing")
				if attempts.Add(1) == 1 {
					<-releaseFirst
					return errors.New("tracing flush boom")
				}
				return nil
			},
		})

		first := make(chan error, 1)
		go func() { first <- host.Close(context.Background()) }()
		synctest.Wait() // owner durably blocked in tracing

		second := make(chan error, 1)
		go func() { second <- host.Close(context.Background()) }()
		synctest.Wait() // waiter durably blocked on the shared attempt
		close(releaseFirst)
		synctest.Wait()

		err1 := <-first
		err2 := <-second
		if err1 == nil || err2 == nil {
			t.Fatalf("shared attempt must fail for owner and waiter; owner=%v waiter=%v", err1, err2)
		}
		if err1.Error() != err2.Error() {
			t.Fatalf("waiter must share attempt error; owner=%v waiter=%v", err1, err2)
		}
		if got := log.count("tracing"); got != 1 {
			t.Fatalf("tracing ran %d times during shared failed attempt; want 1", got)
		}
		if got := log.count("process"); got != 1 {
			t.Fatalf("process closes=%d want 1", got)
		}

		if err := host.Close(context.Background()); err != nil {
			t.Fatalf("explicit later retry: %v", err)
		}
		if got := log.count("process"); got != 1 {
			t.Fatalf("process must not repeat on retry; closes=%d", got)
		}
		if got := log.count("tracing"); got != 2 {
			t.Fatalf("tracing attempts=%d want 2 (one shared failure + one explicit retry)", got)
		}
	})
}

// TestHostClose_ConcurrentCloseMultiWaiterNoRetryStampede proves many waiters
// observe one shared failure without each starting a fresh attempt.
func TestHostClose_ConcurrentCloseMultiWaiterNoRetryStampede(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		host := &Host{}
		attempt := &hostCloseAttempt{done: make(chan struct{})}
		want := errors.New("tracing flush boom")
		host.closeMu.Lock()
		host.closeAttempt = attempt
		host.closeMu.Unlock()

		const waiters = 8
		errs := make(chan error, waiters)
		for range waiters {
			go func() { errs <- host.Close(context.Background()) }()
		}
		synctest.Wait()

		host.closeMu.Lock()
		attempt.err = want
		close(attempt.done)
		synctest.Wait()
		host.closeAttempt = nil
		host.closeMu.Unlock()

		for range waiters {
			got := <-errs
			if got == nil || got.Error() != want.Error() {
				t.Fatalf("waiter err=%v want shared %v", got, want)
			}
		}
	})
}

// TestHostClose_StatusInspectionFromCleanupCallbackDoesNotDeadlock proves the
// host holds no lock across shutdown phases.
func TestHostClose_StatusInspectionFromCleanupCallbackDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	log := &hostCloseLog{}
	var host *Host
	host = newCloseTestHost(t, log, closeHostOptions{
		tracing: func(ctx context.Context) error {
			log.add("tracing")
			_ = host.coordinator.Status()
			return host.WaitForIdle(ctx)
		},
	})

	done := make(chan error, 1)
	go func() { done <- host.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Close deadlocked while a cleanup callback inspected host status")
	}
}
