package runtimebundle

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"maps"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/config"
	"github.com/aiproxer/aiproxer/internal/core/controlplane"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/runtime"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// cpRuntimeNonInterference builds a best-effort, query-enabled control-plane
// runtime backed by the memory store. It is the closest injectable executor-
// level seam: the standard distribution wraps the B2BUA store with
// controlPlane.wrapB2BUA at Build time, so the executor's recordAttempt path
// projects attempt lineage into the recorder without changing routing or
// streaming semantics (requirements 1.3, 3.2, 3.3, 5.1, 5.3, 10.7).
func cpRuntimeNonInterference(t *testing.T) *controlPlaneRuntime {
	t.Helper()
	cfg := &config.Config{}
	cfg.ControlPlane.Enabled = true
	cfg.ControlPlane.Store = "memory"
	cfg.ControlPlane.RecordingPolicy = "best_effort"
	cfg.ControlPlane.Query.Enabled = true
	cfg.ControlPlane.Query.DefaultPageSize = 50
	cfg.ControlPlane.Query.MaxPageSize = 200
	rt, err := buildControlPlaneRuntime(controlPlaneBuildInput{
		StartupContext: context.Background(),
		Cfg:            cfg,
		Clock:          func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("buildControlPlaneRuntime: %v", err)
	}
	if rt == nil || rt.queries == nil {
		t.Fatalf("expected wired control-plane runtime with queries")
	}
	t.Cleanup(func() {
		if rt.closer != nil {
			_ = rt.closer()
		}
	})
	return rt
}

// cpExecutor builds a runtime.Executor whose B2BUA store is wrapped by the
// control-plane decorator, mirroring the standard Build wiring at
// build.go:174. It returns the executor, the underlying memory store (for
// A-leg lookup), and an opens tracker so tests can prove no retry/failover
// happened after first output.
func cpExecutor(t *testing.T, rt *controlPlaneRuntime, backends map[string]execbackend.Backend) (*runtime.Executor, *b2bua.MemoryStore, *openTracker) {
	t.Helper()
	delegate, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatalf("b2bua NewMemoryStore: %v", err)
	}
	store := rt.wrapB2BUA(delegate)
	tr := &openTracker{}
	for id, be := range backends {
		be.Open = tr.wrap(id, be.Open)
		backends[id] = be
	}
	ex := runtime.TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Backends = backends
	ex.Rand = routing.NewSeededRng(1)
	ex.Now = func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }
	wireSecureSessionForTest(t, ex)
	return ex, delegate, tr
}

// wireSecureSessionForTest mirrors internal/core/runtime/export_test.go's
// prepareExecutorSecureSessionForTests so a runtime.Executor constructed
// outside the runtime test binary can run real prepare/stream paths. The
// secure-session manager is backed by an in-memory store and the B2BUA
// decorator lineage; it is the minimal wiring required for Execute to proceed
// without changing routing, streaming, or control-plane semantics.
func wireSecureSessionForTest(t *testing.T, ex *runtime.Executor) {
	t.Helper()
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := make([]byte, 32)
	if _, err := rand.Read(fk); err != nil {
		for i := range fk {
			fk[i] = byte(i + 1)
		}
	}
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(ex.Store), app.ManagerConfig{
		FingerprintKey: fk,
		StoreDurable:   true,
	})
	if err != nil {
		t.Fatalf("secure-session test wiring: %v", err)
	}
	ex.SecureSession = mgr
	if ex.SessionDenialMapper == nil {
		ex.SessionDenialMapper = aipapidenial.MapToSessionDenial
	}
	ex.SyntheticLocalPrincipal = true
}

type openTracker struct {
	mu     sync.Mutex
	opens  []string
	counts map[string]int
}

func (t *openTracker) wrap(id string, open func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error)) func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
	if t.counts == nil {
		t.counts = map[string]int{}
	}
	return func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
		t.mu.Lock()
		t.opens = append(t.opens, id)
		t.counts[id]++
		t.mu.Unlock()
		if open == nil {
			return aipapi.NewFixedEventStream(completionEvents("ok")), nil
		}
		return open(ctx, call, cand)
	}
}

func (t *openTracker) snapshot() ([]string, map[string]int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := append([]string(nil), t.opens...)
	counts := map[string]int{}
	maps.Copy(counts, t.counts)
	return out, counts
}

func completionEvents(text string) []aipapi.Event {
	return []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: text},
		{Kind: aipapi.EventResponseFinished},
	}
}

func parallelCallText(selector, text string) *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart(text)},
		}},
	}
}

func collectText(t *testing.T, ex *runtime.Executor, call *aipapi.Call) string {
	t.Helper()
	stream, err := ex.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	col, err := aipapi.Collect(context.Background(), stream)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	return col.Text.String()
}

func queryAttempts(t *testing.T, rt *controlPlaneRuntime, aLegID string) []cp.AttemptRow {
	t.Helper()
	page, err := rt.queries.Attempts(context.Background(), cp.AttemptQuery{
		Common: cp.CommonFilters{ALegID: aLegID},
		Limit:  50,
	})
	if err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	return page.Items
}

// TestControlPlaneRuntimeNonInterference_FailoverRecordsSurfacedAndSwallowed
// is an executor-level runtime test: a pre-output recoverable failure on the
// primary backend is swallowed and the failover arm surfaces success. It proves
// BOTH that the client-visible output and no-retry-after-first-output invariant
// are unchanged with the control-plane decorator wired, AND that the
// control-plane ledger distinguishes the surfaced success from the swallowed
// failure (requirements 1.3, 3.2, 3.3, 5.1, 5.3, 5.6, 10.7).
func TestControlPlaneRuntimeNonInterference_FailoverRecordsSurfacedAndSwallowed(t *testing.T) {
	t.Parallel()
	rt := cpRuntimeNonInterference(t)
	backends := map[string]execbackend.Backend{
		"primary": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return nil, aipapi.RecoverablePreOutputError(errors.New("primary backend down"))
			},
		},
		"failover": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(completionEvents("failover-response")), nil
			},
		},
	}
	ex, _, tr := cpExecutor(t, rt, backends)

	call := parallelCallText("primary:model|failover:model", "hello")
	got := collectText(t, ex, call)
	if got != "failover-response" {
		t.Fatalf("client output: got %q want failover-response (no mutation)", got)
	}
	opens, counts := tr.snapshot()
	if len(opens) != 2 || counts["primary"] != 1 || counts["failover"] != 1 {
		t.Fatalf("expected one primary (swallowed) + one failover (surfaced) open, no retry: opens=%v counts=%v", opens, counts)
	}

	rows := queryAttempts(t, rt, call.Session.ALegID)
	if len(rows) != 2 {
		t.Fatalf("expected 2 attempt rows (swallowed + surfaced), got %d: %+v", len(rows), rows)
	}
	surfaced, swallowed := 0, 0
	for _, row := range rows {
		if row.Correlation.ALegID != call.Session.ALegID {
			t.Fatalf("attempt row leaked to another a-leg: %q", row.Correlation.ALegID)
		}
		switch row.Surfaced {
		case cp.AttemptSurfacedSurfaced:
			if row.Outcome != cp.AttemptOutcomeSucceeded {
				t.Fatalf("surfaced attempt must be succeeded, got %q (backend %s)", row.Outcome, row.BackendID)
			}
			if row.BackendID != "failover" {
				t.Fatalf("surfaced attempt must be the failover backend, got %q", row.BackendID)
			}
			surfaced++
		case cp.AttemptSurfacedSwallowed:
			if row.Outcome != cp.AttemptOutcomeFailed {
				t.Fatalf("swallowed attempt must be failed, got %q (backend %s)", row.Outcome, row.BackendID)
			}
			if row.BackendID != "primary" {
				t.Fatalf("swallowed attempt must be the primary backend, got %q", row.BackendID)
			}
			swallowed++
		default:
			t.Fatalf("attempt surfaced must be explicit surfaced or swallowed, got %q (backend %s)", row.Surfaced, row.BackendID)
		}
	}
	if surfaced != 1 || swallowed != 1 {
		t.Fatalf("expected 1 surfaced + 1 swallowed, got surfaced=%d swallowed=%d", surfaced, swallowed)
	}
}

// TestControlPlaneRuntimeNonInterference_ParallelRaceRecordsWinnerAndLoser
// exercises a real parallel race at the executor level with the control-plane
// B2BUA decorator wired. It proves the winner's client-visible output is
// unchanged (non-interference) and the ledger records the surfaced winner
// distinctly from the swallowed/cancelled loser (requirements 1.3, 3.2, 3.3,
// 5.1, 5.3, 5.6, 10.7).
func TestControlPlaneRuntimeNonInterference_ParallelRaceRecordsWinnerAndLoser(t *testing.T) {
	t.Parallel()
	rt := cpRuntimeNonInterference(t)
	// Rendezvous both Opens so the fast arm cannot short-circuit the race
	// before the slow arm opens. tryOpenParallelGroup guards Open with a
	// winnerIdx check; under -race the fast arm can win before the slow
	// goroutine is scheduled, skipping the slow Open entirely (a legal
	// optimization codified by TestParallelRace_HandicapShortCircuitOnEarlyWinner).
	// This test needs both arms to open so the ledger has a surfaced winner
	// and a swallowed/cancelled loser, so the Opens synchronize here.
	slowStarted := make(chan struct{})
	fastStarted := make(chan struct{})
	backends := map[string]execbackend.Backend{
		"slow": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				close(slowStarted)
				<-fastStarted
				return &cpDelayedStream{delay: 200 * time.Millisecond, events: completionEvents("slow-response")}, nil
			},
		},
		"fast": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				close(fastStarted)
				<-slowStarted
				return aipapi.NewFixedEventStream(completionEvents("fast-response")), nil
			},
		},
	}
	ex, _, tr := cpExecutor(t, rt, backends)

	call := parallelCallText("slow:model!fast:model", "race")
	got := collectText(t, ex, call)
	if got != "fast-response" {
		t.Fatalf("race winner text: got %q want fast-response (no mutation)", got)
	}
	opens, counts := tr.snapshot()
	if counts["fast"] != 1 || counts["slow"] != 1 {
		t.Fatalf("expected each race arm to open exactly once: opens=%v counts=%v", opens, counts)
	}

	rows := queryAttempts(t, rt, call.Session.ALegID)
	if len(rows) < 2 {
		t.Fatalf("expected at least 2 attempt rows (winner + loser), got %d: %+v", len(rows), rows)
	}
	winner, losers := 0, 0
	for _, row := range rows {
		if row.Correlation.ALegID != call.Session.ALegID {
			t.Fatalf("attempt row leaked to another a-leg: %q", row.Correlation.ALegID)
		}
		if row.Surfaced == cp.AttemptSurfacedSurfaced && row.Outcome == cp.AttemptOutcomeSucceeded {
			if row.BackendID != "fast" {
				t.Fatalf("surfaced winner must be fast backend, got %q", row.BackendID)
			}
			winner++
			continue
		}
		if row.Surfaced == cp.AttemptSurfacedSwallowed {
			if row.BackendID != "slow" {
				t.Fatalf("swallowed loser must be slow backend, got %q", row.BackendID)
			}
			losers++
		}
	}
	if winner != 1 {
		t.Fatalf("expected exactly 1 surfaced winner row, got %d (rows=%+v)", winner, rows)
	}
	if losers != 1 {
		t.Fatalf("expected exactly 1 swallowed loser row, got %d (rows=%+v)", losers, rows)
	}
}

// TestControlPlaneRuntimeNonInterference_PostOutputRecordingFailureNoRetryNoMutation
// proves at the executor level that a post-output control-plane recording
// failure (the B2BUA decorator's RecordBestEffort path) cannot trigger retry or
// failover and cannot mutate already-surfaced client output. The control-plane
// recorder is backed by a store whose Append always fails; the runtime stream
// must complete with the original output, only one backend may open, and the
// capability status must degrade (requirements 5.1, 5.2, 5.3, 5.6, 5.7, 10.7).
func TestControlPlaneRuntimeNonInterference_PostOutputRecordingFailureNoRetryNoMutation(t *testing.T) {
	t.Parallel()
	rt := cpRuntimeNonInterference(t)
	failing := &cpFailingAppendStore{Store: rt.store, appendErr: errors.New("post-output store down")}
	rt.recorder = controlplane.NewRecorderService(failing, rt.status, controlplane.RecorderConfig{
		Policy: cp.RecordingBestEffort,
		Clock:  clockFunc{now: func() time.Time { return time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC) }},
	})

	backends := map[string]execbackend.Backend{
		"ok": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream(completionEvents("ok-response")), nil
			},
		},
		"must-not-open": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				// The Open callback may run on a backend goroutine, where
				// t.Fatal would race the test driver. Return a sentinel
				// error instead; if this backend is wrongly opened the
				// open-count assertion below fails deterministically.
				return nil, errCpMustNotOpen
			},
		},
	}
	ex, _, tr := cpExecutor(t, rt, backends)

	call := parallelCallText("ok:model|must-not-open:model", "post-output")
	got := collectText(t, ex, call)
	if got != "ok-response" {
		t.Fatalf("post-output recording failure must not mutate stream output: got %q want ok-response", got)
	}
	opens, counts := tr.snapshot()
	if counts["ok"] != 1 || counts["must-not-open"] != 0 {
		t.Fatalf("expected exactly one ok open and no failover: opens=%v counts=%v", opens, counts)
	}
	if failing.appends.Load() == 0 {
		t.Fatal("expected the control-plane recorder to be invoked at least once")
	}
	status := rt.status.Snapshot()
	if status.State != cp.CapabilityDegraded {
		t.Fatalf("post-output recording failure must degrade status only, got %q", status.State)
	}
}

// cpDelayedStream delays the first recv to simulate a slow race arm.

// errCpMustNotOpen is a sentinel returned by a backend Open callback that must
// never be invoked. Returning it (instead of calling t.Fatal) keeps the test
// safe when Open runs on a backend goroutine; the open-count assertion is the
// deterministic failure signal when the contract is violated.
var errCpMustNotOpen = errors.New("must-not-open backend was invoked")

type cpDelayedStream struct {
	delay  time.Duration
	events []aipapi.Event
	idx    int
}

func (d *cpDelayedStream) Recv(ctx context.Context) (aipapi.Event, error) {
	if d.idx == 0 {
		select {
		case <-time.After(d.delay):
		case <-ctx.Done():
			return aipapi.Event{}, ctx.Err()
		}
	}
	if d.idx >= len(d.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := d.events[d.idx]
	d.idx++
	return ev, nil
}

func (d *cpDelayedStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}
func (d *cpDelayedStream) Close() error { return nil }

// cpFailingAppendStore wraps a control-plane Store and forces Append to fail,
// simulating a post-output recording failure while keeping query/retention
// methods delegating to the real store.
type cpFailingAppendStore struct {
	controlplane.Store
	appendErr error
	appends   atomic.Int64
}

func (s *cpFailingAppendStore) Append(context.Context, cp.Event) (cp.RecordResult, error) {
	s.appends.Add(1)
	return cp.RecordResult{}, s.appendErr
}
