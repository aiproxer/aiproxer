package stdhttp

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Task 5.1 C4: the retained generation host worker keeps the http.Server, its
// serve worker, and the single ordered shutdown, so a failed HTTP drain leaves
// the host open and a later ordered Shutdown resumes drain/await/host-close
// instead of tearing the host down directly under accepted handlers. The
// compatibility wrapper RunWithGenerationHost is a consumer of this seam and
// its behavior stays pinned by the existing serve tests.

// workerServeStub replaces the listener and drain with test-controlled
// barriers while recording the ordered events in the shared serve order log.
type workerServeStub struct {
	log      *serveOrderLog
	mu       sync.Mutex
	drainErr error
	gate     chan struct{}

	release   chan struct{}
	released  sync.Once
	started   chan struct{}
	startOnce sync.Once
	entered   chan struct{}
	enterOnce sync.Once
	drains    atomic.Int32
}

func stubWorkerServe(t *testing.T, log *serveOrderLog) *workerServeStub {
	t.Helper()
	stub := &workerServeStub{
		log:     log,
		release: make(chan struct{}),
		started: make(chan struct{}),
		entered: make(chan struct{}),
	}
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		stub.releaseServe()
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})

	listenAndServe = func(*http.Server) error {
		stub.startOnce.Do(func() { close(stub.started) })
		<-stub.release
		return http.ErrServerClosed
	}
	httpServerShutdown = func(context.Context, *http.Server) error {
		stub.drains.Add(1)
		stub.log.add("http-drain")
		stub.enterOnce.Do(func() { close(stub.entered) })
		stub.mu.Lock()
		err, gate := stub.drainErr, stub.gate
		stub.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if err != nil {
			return err
		}
		stub.releaseServe()
		return nil
	}
	return stub
}

func (s *workerServeStub) awaitStarted() { <-s.started }

func (s *workerServeStub) awaitDrainEntered() { <-s.entered }

func (s *workerServeStub) setDrainError(err error) {
	s.mu.Lock()
	s.drainErr = err
	s.mu.Unlock()
}

// holdDrain blocks every drain attempt until the returned release function is
// called, so a test can observe a shutdown attempt that is still in flight.
func (s *workerServeStub) holdDrain() (release func()) {
	gate := make(chan struct{})
	s.mu.Lock()
	s.gate = gate
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(gate) }) }
}

func (s *workerServeStub) releaseServe() { s.released.Do(func() { close(s.release) }) }

func startWorkerForTest(t *testing.T, host *serveHostStub) *GenerationHostWorker {
	t.Helper()
	worker, err := StartGenerationHostWorker(context.Background(), GenerationHostInput{
		Config:          serveTestConfig(),
		Log:             serveTestLogger(),
		Host:            host,
		ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartGenerationHostWorker: %v", err)
	}
	if worker == nil {
		t.Fatal("StartGenerationHostWorker returned no worker without an error")
	}
	return worker
}

func startWorkerWithManagementForTest(t *testing.T, host *serveHostStub, mgmt *serveManagementStub) *GenerationHostWorker {
	t.Helper()
	worker, err := StartGenerationHostWorker(context.Background(), GenerationHostInput{
		Config:          serveTestConfig(),
		Log:             serveTestLogger(),
		Host:            host,
		Management:      mgmt,
		ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("StartGenerationHostWorker: %v", err)
	}
	if worker == nil {
		t.Fatal("StartGenerationHostWorker returned no worker without an error")
	}
	return worker
}

// TestGenerationHostWorker_ManagementFailureRetriesOnlyManagement proves a
// failed management shutdown never re-enters the successfully closed host: the
// failure stays observable, the retry closes only the management listener, and
// Host.Close total stays exactly 1.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestGenerationHostWorker_ManagementFailureRetriesOnlyManagement(t *testing.T) {
	log := &serveOrderLog{}
	stub := stubWorkerServe(t, log)
	host := newServeHostStub(log)
	mgmtFailure := errors.New("management shutdown boom")
	mgmt := &serveManagementStub{log: log, err: mgmtFailure}
	worker := startWorkerWithManagementForTest(t, host, mgmt)
	stub.awaitStarted()

	err := worker.Shutdown(context.Background())
	if !errors.Is(err, mgmtFailure) {
		t.Fatalf("first Shutdown err=%v want the management failure", err)
	}
	if got := mgmt.call.Load(); got != 1 {
		t.Fatalf("management Shutdown calls=%d want 1", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1 on the first attempt", got)
	}
	if !worker.HostClosed() {
		t.Fatal("HostClosed must report the successful host close")
	}

	mgmt.err = nil
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("management retry: %v", err)
	}
	if got := mgmt.call.Load(); got != 2 {
		t.Fatalf("management Shutdown calls=%d want 2 after the retry", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1: a closed host must never be re-entered", got)
	}
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent Shutdown: %v", err)
	}
	if got := mgmt.call.Load(); got != 2 || host.closes.Load() != 1 {
		t.Fatalf("management=%d host=%d want 2/1 after idempotent Shutdown", mgmt.call.Load(), host.closes.Load())
	}
	assertServeOrder(t, log,
		"begin-shutdown", "http-drain", "management-shutdown", "host-close",
		"begin-shutdown", "http-drain", "management-shutdown")
}

// TestGenerationHostWorker_ComponentFailuresRetryOnlyFailedSteps proves both
// failed components are retried on the next attempt and the successful retry is
// idempotent afterwards.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestGenerationHostWorker_ComponentFailuresRetryOnlyFailedSteps(t *testing.T) {
	log := &serveOrderLog{}
	stub := stubWorkerServe(t, log)
	host := newServeHostStub(log)
	hostBoom := errors.New("host close boom")
	host.closeErr = hostBoom
	mgmtFailure := errors.New("management shutdown boom")
	mgmt := &serveManagementStub{log: log, err: mgmtFailure}
	worker := startWorkerWithManagementForTest(t, host, mgmt)
	stub.awaitStarted()

	err := worker.Shutdown(context.Background())
	if !errors.Is(err, mgmtFailure) || !errors.Is(err, hostBoom) {
		t.Fatalf("first Shutdown err=%v want both component failures", err)
	}
	if worker.HostClosed() {
		t.Fatal("HostClosed must stay false after a failed host close")
	}
	if mgmt.call.Load() != 1 || host.closes.Load() != 1 {
		t.Fatalf("first attempt mgmt=%d host=%d want 1/1", mgmt.call.Load(), host.closes.Load())
	}

	mgmt.err = nil
	host.closeErr = nil
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
	if mgmt.call.Load() != 2 || host.closes.Load() != 2 {
		t.Fatalf("retry mgmt=%d host=%d want both retried once", mgmt.call.Load(), host.closes.Load())
	}
	if !worker.HostClosed() {
		t.Fatal("HostClosed must report the successful host close")
	}
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent Shutdown: %v", err)
	}
	if mgmt.call.Load() != 2 || host.closes.Load() != 2 {
		t.Fatalf("idempotent Shutdown mgmt=%d host=%d want 2/2", mgmt.call.Load(), host.closes.Load())
	}
	assertServeOrder(t, log,
		"begin-shutdown", "http-drain", "management-shutdown", "host-close",
		"begin-shutdown", "http-drain", "management-shutdown", "host-close")
}

// TestGenerationHostWorker_ShutdownJoinsActiveAttemptBeforeCachedCompletion
// pins the linearization contract directly: while an attempt is in flight a
// concurrent Shutdown joins that attempt and observes its final error, even
// when every component has already closed; the cached completion result is
// only a fast path once no attempt is active.
func TestGenerationHostWorker_ShutdownJoinsActiveAttemptBeforeCachedCompletion(t *testing.T) {
	t.Parallel()
	listenerFailure := errors.New("abnormal listener failure")
	worker := &GenerationHostWorker{}
	worker.hostClosed = true // all components closed...
	worker.shutdownErr = nil // ...with a stale cached success
	attempt := &generationHostShutdownAttempt{done: make(chan struct{})}
	worker.attempt = attempt // ...while this attempt is still active

	result := make(chan error, 1)
	go func() { result <- worker.Shutdown(context.Background()) }()

	// The active attempt must be joined, never bypassed by the cached result:
	// a stale return surfaces here while the attempt is still pending.
	select {
	case err := <-result:
		t.Fatalf("Shutdown returned %v instead of joining the active attempt", err)
	case <-time.After(100 * time.Millisecond):
	}

	worker.mu.Lock()
	attempt.err = listenerFailure
	worker.shutdownErr = listenerFailure
	worker.attempt = nil
	close(attempt.done)
	worker.mu.Unlock()

	select {
	case err := <-result:
		if !errors.Is(err, listenerFailure) {
			t.Fatalf("joined Shutdown err=%v want the attempt's final listener failure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("joined Shutdown never returned")
	}
}

// TestGenerationHostWorker_ConcurrentShutdownPreservesListenerError repeats the
// component-closure/publication race with the existing test-owned seams: an
// abnormal listener exit must reach every concurrent Shutdown caller, and the
// host closes exactly once per bounded iteration.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestGenerationHostWorker_ConcurrentShutdownPreservesListenerError(t *testing.T) {
	origListen := listenAndServe
	origShutdown := httpServerShutdown
	t.Cleanup(func() {
		listenAndServe = origListen
		httpServerShutdown = origShutdown
	})
	listenerFailure := errors.New("abnormal listener failure")
	listenAndServe = func(*http.Server) error { return listenerFailure }
	httpServerShutdown = func(context.Context, *http.Server) error { return nil }

	const callers = 8
	for iteration := range 256 {
		host := newServeHostStub(&serveOrderLog{})
		worker, err := StartGenerationHostWorker(context.Background(), GenerationHostInput{
			Config:          serveTestConfig(),
			Log:             serveTestLogger(),
			Host:            host,
			ShutdownTimeout: time.Second,
		})
		if err != nil {
			t.Fatalf("iteration %d: StartGenerationHostWorker: %v", iteration, err)
		}
		<-worker.Exited()

		start := make(chan struct{})
		results := make(chan error, callers)
		var wg sync.WaitGroup
		wg.Add(callers)
		for range callers {
			go func() {
				defer wg.Done()
				<-start
				results <- worker.Shutdown(context.Background())
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		for err := range results {
			if !errors.Is(err, listenerFailure) {
				t.Fatalf("iteration %d: concurrent Shutdown lost the listener error: %v (final=%v)", iteration, err, worker.Shutdown(context.Background()))
			}
		}
		if got := host.closes.Load(); got != 1 {
			t.Fatalf("iteration %d: Host.Close calls=%d want exactly 1", iteration, got)
		}
	}
}

// TestGenerationHostWorker_ConcurrentShutdownObservesRetryResult is the
// successful-retry publication control: after a failed management attempt, all
// concurrent retry callers observe the successful retry result (never the
// stale previous failure), the management listener is retried once, and the
// host is not re-entered.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestGenerationHostWorker_ConcurrentShutdownObservesRetryResult(t *testing.T) {
	log := &serveOrderLog{}
	stub := stubWorkerServe(t, log)
	host := newServeHostStub(log)
	mgmtFailure := errors.New("management shutdown boom")
	mgmt := &serveManagementStub{log: log, err: mgmtFailure}
	worker := startWorkerWithManagementForTest(t, host, mgmt)
	stub.awaitStarted()

	if err := worker.Shutdown(context.Background()); !errors.Is(err, mgmtFailure) {
		t.Fatalf("first Shutdown err=%v want the management failure", err)
	}
	mgmt.err = nil

	const callers = 8
	start := make(chan struct{})
	results := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			results <- worker.Shutdown(context.Background())
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent retry Shutdown err=%v want the successful retry result", err)
		}
	}
	if got := mgmt.call.Load(); got != 2 {
		t.Fatalf("management Shutdown calls=%d want 2 (one retry)", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1: a closed host must never be re-entered", got)
	}
	assertServeOrder(t, log,
		"begin-shutdown", "http-drain", "management-shutdown", "host-close",
		"begin-shutdown", "http-drain", "management-shutdown")
}

// TestStartGenerationHostWorker_InputFailureRetriesOnlyFailedCleanupComponent
// proves the post-validation cleanup path tracks component success the same
// way: a failed management close with a successful host close never re-enters
// the host on retry.
func TestStartGenerationHostWorker_InputFailureRetriesOnlyFailedCleanupComponent(t *testing.T) {
	t.Parallel()
	log := &serveOrderLog{}
	host := newServeHostStub(log)
	mgmtFailure := errors.New("management shutdown boom")
	mgmt := &serveManagementStub{log: log, err: mgmtFailure}

	worker, err := StartGenerationHostWorker(context.Background(), GenerationHostInput{
		Log:             serveTestLogger(),
		Host:            host,
		Management:      mgmt,
		ShutdownTimeout: time.Second,
	})
	if err == nil || !errors.Is(err, mgmtFailure) {
		t.Fatalf("err=%v want the management cleanup failure", err)
	}
	if worker == nil {
		t.Fatal("input failure must return the retained cleanup worker, not nil")
	}
	if !worker.HostClosed() {
		t.Fatal("HostClosed must report the successful host cleanup")
	}
	if got := mgmt.call.Load(); got != 1 {
		t.Fatalf("management Shutdown calls=%d want 1", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1", got)
	}

	mgmt.err = nil
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("management cleanup retry: %v", err)
	}
	if got := mgmt.call.Load(); got != 2 {
		t.Fatalf("management Shutdown calls=%d want 2 after the retry", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1: cleanup must never re-enter the closed host", got)
	}
	assertServeOrder(t, log,
		"begin-shutdown", "management-shutdown", "host-close",
		"begin-shutdown", "management-shutdown")
}

// TestGenerationHostWorker_ShutdownResumesAfterFailedDrain proves a timed-out
// drain keeps the host open and a later ordered Shutdown resumes drain, await,
// and host close exactly once.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestGenerationHostWorker_ShutdownResumesAfterFailedDrain(t *testing.T) {
	log := &serveOrderLog{}
	stub := stubWorkerServe(t, log)
	host := newServeHostStub(log)
	worker := startWorkerForTest(t, host)
	stub.awaitStarted()

	stub.setDrainError(context.DeadlineExceeded)
	err := worker.Shutdown(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown err=%v want HTTP drain failure", err)
	}
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 after failed HTTP drain", got)
	}
	if worker.HostClosed() {
		t.Fatal("HostClosed must stay false after a failed drain")
	}

	stub.setDrainError(nil)
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("resumed Shutdown: %v", err)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1 after resumed drain", got)
	}
	if !worker.HostClosed() {
		t.Fatal("HostClosed must report the completed ordered shutdown")
	}
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent Shutdown: %v", err)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1 after idempotent Shutdown", got)
	}
	if got := stub.drains.Load(); got != 2 {
		t.Fatalf("http drain calls=%d want 2 (retry, no third attempt)", got)
	}
	assertServeOrder(t, log, "begin-shutdown", "http-drain", "begin-shutdown", "http-drain", "host-close")
}

// TestGenerationHostWorker_ConcurrentShutdownJoinsOneAttempt proves concurrent
// callers share a single in-flight ordered shutdown attempt.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestGenerationHostWorker_ConcurrentShutdownJoinsOneAttempt(t *testing.T) {
	log := &serveOrderLog{}
	stub := stubWorkerServe(t, log)
	host := newServeHostStub(log)
	worker := startWorkerForTest(t, host)
	stub.awaitStarted()

	release := stub.holdDrain()
	results := make(chan error, 2)
	go func() { results <- worker.Shutdown(context.Background()) }()
	go func() { results <- worker.Shutdown(context.Background()) }()
	stub.awaitDrainEntered()
	if got := host.closes.Load(); got != 0 {
		t.Fatalf("Host.Close calls=%d want 0 while the drain is in flight", got)
	}

	release()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent Shutdown: %v", err)
		}
	}
	if got := stub.drains.Load(); got != 1 {
		t.Fatalf("http drain calls=%d want exactly 1 shared attempt", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1", got)
	}
	if !worker.HostClosed() {
		t.Fatal("HostClosed must report the completed ordered shutdown")
	}
}

// TestGenerationHostWorker_ShutdownCallerExpiryKeepsAttemptRunning proves an
// expired caller returns while the owned attempt finishes safely and a later
// Shutdown joins it instead of starting a second one.
//
//nolint:paralleltest // mutates package-level listenAndServe / httpServerShutdown
func TestGenerationHostWorker_ShutdownCallerExpiryKeepsAttemptRunning(t *testing.T) {
	log := &serveOrderLog{}
	stub := stubWorkerServe(t, log)
	host := newServeHostStub(log)
	worker := startWorkerForTest(t, host)
	stub.awaitStarted()

	release := stub.holdDrain()
	expired, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := worker.Shutdown(expired); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired caller err=%v want deadline", err)
	}
	if worker.HostClosed() {
		t.Fatal("attempt must still be running after caller expiry")
	}

	release()
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("joined Shutdown: %v", err)
	}
	if got := stub.drains.Load(); got != 1 {
		t.Fatalf("http drain calls=%d want exactly 1 joined attempt", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want exactly 1", got)
	}
}

// TestStartGenerationHostWorker_InputFailureKeepsRetryableCleanupWorker proves
// a post-host input failure still returns the retained ownership seam: the
// first cleanup closes the host once (joined in the error), a failed cleanup
// stays retryable through Shutdown, and a successful one is idempotent.
func TestStartGenerationHostWorker_InputFailureKeepsRetryableCleanupWorker(t *testing.T) {
	t.Parallel()
	log := &serveOrderLog{}
	host := newServeHostStub(log)
	closeBoom := errors.New("host close boom")
	host.closeErr = closeBoom

	worker, err := StartGenerationHostWorker(context.Background(), GenerationHostInput{
		Log:             serveTestLogger(),
		Host:            host,
		ShutdownTimeout: time.Second,
	})
	if err == nil {
		t.Fatal("nil config must fail validation")
	}
	if worker == nil {
		t.Fatal("input failure must return the retained cleanup worker, not nil")
	}
	if !errors.Is(err, closeBoom) {
		t.Fatalf("err=%v must join the cleanup failure", err)
	}
	if got := host.begins.Load(); got != 1 {
		t.Fatalf("BeginShutdown calls=%d want 1", got)
	}
	if got := host.closes.Load(); got != 1 {
		t.Fatalf("Host.Close calls=%d want 1 cleanup attempt", got)
	}
	if worker.HostClosed() {
		t.Fatal("HostClosed must stay false after a failed cleanup close")
	}

	host.closeErr = nil
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry cleanup Shutdown: %v", err)
	}
	if got := host.closes.Load(); got != 2 {
		t.Fatalf("Host.Close calls=%d want 2 after retry", got)
	}
	if !worker.HostClosed() {
		t.Fatal("HostClosed must report the completed cleanup")
	}
	if err := worker.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent cleanup Shutdown: %v", err)
	}
	if got := host.closes.Load(); got != 2 {
		t.Fatalf("Host.Close calls=%d want 2 (idempotent)", got)
	}
}
