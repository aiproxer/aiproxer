package compaction

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/auxreq"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

// certificationGenerationRunners is deliberately local to this test file:
// it models two immutable generation-bound executor views without introducing
// a production test hook or provider dependency.
func certificationGenerationRunners(t *testing.T) (*certificationRunner, *certificationRunner) {
	t.Helper()
	return &certificationRunner{name: "generation-1", started: make(chan struct{}), release: make(chan struct{}), bindings: make(chan string, 1), selectors: make(chan string, 1)},
		&certificationRunner{name: "generation-2", bindings: make(chan string, 1), selectors: make(chan string, 1)}
}

type certificationRunner struct {
	name      string
	started   chan struct{}
	release   chan struct{}
	bindings  chan string
	selectors chan string
	calls     atomic.Int32
	once      sync.Once
}

func (r *certificationRunner) Execute(ctx context.Context, call *aipapi.Call) (aipapi.EventStream, error) {
	r.calls.Add(1)
	if r.started != nil {
		r.once.Do(func() { close(r.started) })
	}
	if r.bindings != nil {
		if binding, ok := execctx.DetachedSessionFromContext(ctx); ok {
			select {
			case r.bindings <- binding.ParentBranchBinding:
			default:
			}
		}
	}
	if r.selectors != nil && call != nil {
		select {
		case r.selectors <- call.Route.Selector:
		default:
		}
	}
	if r.release != nil {
		select {
		case <-r.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: r.name},
		{Kind: aipapi.EventResponseFinished},
	}), nil
}

func certificationRequest(binding, parentALeg, childALeg, selector string) auxiliary.Request {
	return auxiliary.Request{
		Role:                "compaction_continuity_extractor",
		Visibility:          "private",
		SessionMode:         auxiliary.SessionModeDetached,
		ParentALegID:        parentALeg,
		ParentBranchBinding: binding,
		Call: &aipapi.Call{
			Route:   aipapi.RouteIntent{Selector: selector},
			Session: aipapi.SessionRef{AuthoritativeSessionID: "parent-session", ALegID: childALeg},
		},
	}
}

func TestReloadConcurrencyCertification_InFlightJobRetainsCapturedGeneration(t *testing.T) {
	t.Parallel()
	first, second := certificationGenerationRunners(t)
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return first }, auxreq.SchedulerConfig{Workers: 1, QueueCapacity: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })

	coordinator, err := state.NewBranchCoordinator(context.Background(), state.Config{})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := state.CaptureParentBranchKey("parent-session", "parent-a", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Capture(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CommitCapsule(context.Background(), parent, 0, []byte(`{"revision":1}`), [32]byte{1}, "source-1"); err != nil {
		t.Fatal(err)
	}

	oldClient := scheduler.BindRunner(first)
	oldID, err := oldClient.SubmitCollect(context.Background(), certificationRequest(parent.Binding(), parent.ALegID, "child-a-1", "old/extractor"), auxiliary.SubmitOptions{CoalesceKey: "old-generation-job", Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RecordPendingJob(context.Background(), parent, oldID, 1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.started:
	case <-time.After(time.Second):
		t.Fatal("old-generation worker did not start")
	}

	// Publishing a new generation only changes the bound view used for new
	// submissions. The already-running job keeps its old runner and budget.
	newClient := scheduler.BindRunner(second)
	newID, err := newClient.SubmitCollect(context.Background(), certificationRequest(parent.Binding(), parent.ALegID, "child-a-2", "new/extractor"), auxiliary.SubmitOptions{CoalesceKey: "new-generation-job", Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	close(first.release)

	awaitCtx, cancelAwait := context.WithTimeout(t.Context(), time.Second)
	defer cancelAwait()
	oldResult, err := oldClient.Await(awaitCtx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	newResult, err := newClient.Await(awaitCtx, newID)
	if err != nil {
		t.Fatal(err)
	}
	if got := oldResult.Text.String(); got != first.name {
		t.Fatalf("old job result=%q want %q", got, first.name)
	}
	if got := newResult.Text.String(); got != second.name {
		t.Fatalf("new job result=%q want %q", got, second.name)
	}
	select {
	case got := <-first.bindings:
		if got != parent.Binding() {
			t.Fatalf("detached child binding=%q want captured parent %q", got, parent.Binding())
		}
	case <-time.After(time.Second):
		t.Fatal("old detached worker did not expose captured parent binding")
	}
	select {
	case got := <-first.selectors:
		if got != "old/extractor" {
			t.Fatalf("old job route=%q want old/extractor", got)
		}
	case <-time.After(time.Second):
		t.Fatal("old route was not captured")
	}
	select {
	case got := <-second.selectors:
		if got != "new/extractor" {
			t.Fatalf("new job route=%q want new/extractor", got)
		}
	case <-time.After(time.Second):
		t.Fatal("new route was not captured")
	}

	child, err := state.NewBranchKey(parent.AuthoritativeSessionID, "child-a-1", parent.PrincipalPartition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ValidatePendingJob(context.Background(), child, oldID); !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("child A-leg lookup error=%v want ErrBranchMismatch", err)
	}
	if _, err := coordinator.CommitCapsuleForJob(context.Background(), parent, oldID, parent.Binding(), 1, []byte(`{"generation":"old"}`), [32]byte{2}, "source-2"); err != nil {
		t.Fatalf("parent late-result merge: %v", err)
	}
	st, found, err := coordinator.Snapshot(context.Background(), parent)
	if err != nil || !found || st.PendingJobID != "" || !bytes.Equal(st.CapsuleJSON, []byte(`{"generation":"old"}`)) {
		t.Fatalf("parent state after late merge=%#v found=%v err=%v", st, found, err)
	}
}

func TestReloadConcurrencyCertification_ExpiryClearsBranchAndResultTogether(t *testing.T) {
	t.Parallel()
	clock := &certificationClock{now: time.Unix(100, 0)}
	coordinator, err := state.NewBranchCoordinator(context.Background(), state.Config{TTL: time.Second, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := state.CaptureParentBranchKey("expiry-session", "parent-a", "principal-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Capture(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CommitCapsule(context.Background(), parent, 0, []byte(`{"intent":"pending"}`), [32]byte{1}, "source"); err != nil {
		t.Fatal(err)
	}
	job := auxiliary.JobID("expiry-job")
	if _, err := coordinator.RecordPendingJob(context.Background(), parent, job, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RecordPreviewIntent(context.Background(), parent, state.PreviewIntent{Key: "preview-expiry", TargetSourceRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.SetPendingInjection(context.Background(), parent, state.InjectionTarget{BoundaryKey: "boundary-expiry", CapsuleRevision: 1}); err != nil {
		t.Fatal(err)
	}

	runner := &certificationRunner{name: "expiry", bindings: make(chan string, 1), selectors: make(chan string, 1)}
	scheduler, err := auxreq.NewBackgroundScheduler(context.Background(), func() auxreq.ExecutorRunner { return runner }, auxreq.SchedulerConfig{ResultTTL: time.Second, Now: clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scheduler.Close() })
	id, err := scheduler.SubmitCollect(context.Background(), certificationRequest(parent.Binding(), parent.ALegID, "child-a", "expiry/extractor"), auxiliary.SubmitOptions{CoalesceKey: "expiry-key"})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("scheduler returned empty job id")
	}
	awaitCtx, cancelAwait := context.WithTimeout(t.Context(), time.Second)
	defer cancelAwait()
	if _, err := scheduler.Await(awaitCtx, id); err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	if _, found, err := coordinator.Snapshot(context.Background(), parent); err != nil || found {
		t.Fatalf("expired branch state found=%v err=%v", found, err)
	}
	if _, err := coordinator.ValidateInjection(context.Background(), parent, state.InjectionTarget{BoundaryKey: "boundary-expiry", CapsuleRevision: 1}); !errors.Is(err, state.ErrInjectionMismatch) {
		t.Fatalf("expired injection validation error=%v want ErrInjectionMismatch", err)
	}
	if _, err := scheduler.Await(awaitCtx, id); !errors.Is(err, auxreq.ErrJobNotFound) {
		t.Fatalf("expired auxiliary result error=%v want ErrJobNotFound", err)
	}
	child, err := state.NewBranchKey(parent.AuthoritativeSessionID, "child-a", parent.PrincipalPartition)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := coordinator.Snapshot(context.Background(), child); err != nil || found {
		t.Fatalf("expired parent state migrated to child branch: found=%v err=%v", found, err)
	}
}

type certificationClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *certificationClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *certificationClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
