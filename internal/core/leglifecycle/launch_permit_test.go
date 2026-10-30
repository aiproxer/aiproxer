package leglifecycle_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

type recordingBLeg struct {
	mu       sync.Mutex
	callsLog []string
}

func (r *recordingBLeg) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, nil
}

func (r *recordingBLeg) Cancel(_ context.Context, cause leglifecycle.CancelCause) leglifecycle.CancelResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callsLog = append(r.callsLog, "cancel:"+string(cause.Kind))
	return leglifecycle.CancelResult{Mode: leglifecycle.CancelModeProvider}
}

func (r *recordingBLeg) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.callsLog = append(r.callsLog, "close")
	return nil
}

func (r *recordingBLeg) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.callsLog...)
}

func TestLaunchPermit_CancelBeforeBegin(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-1")

	cause := leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit, Detail: "client abort"}
	if err := a.Cancel(context.Background(), cause); err != nil {
		t.Fatal(err)
	}

	openCtx, permit, err := a.BeginBLegLaunch(context.Background(), "b-1")
	if !errors.Is(err, leglifecycle.ErrALegCanceled) {
		t.Fatalf("expected ErrALegCanceled, got %v", err)
	}
	if permit != nil {
		t.Fatalf("expected nil permit on canceled A-leg, got %v", permit)
	}
	if openCtx != nil {
		t.Fatalf("expected nil openCtx on canceled A-leg, got %v", openCtx)
	}
}

func TestLaunchPermit_BeginBeforeCancel(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-2")

	openCtx, permit, err := a.BeginBLegLaunch(context.Background(), "b-1")
	if err != nil {
		t.Fatal(err)
	}
	if permit == nil || openCtx == nil {
		t.Fatal("expected non-nil permit and openCtx")
	}

	select {
	case <-openCtx.Done():
		t.Fatal("openCtx should not be done before cancel")
	default:
	}

	cause := leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}
	if err := a.Cancel(context.Background(), cause); err != nil {
		t.Fatal(err)
	}

	select {
	case <-openCtx.Done():
		// Promptly canceled
	case <-time.After(100 * time.Millisecond):
		t.Fatal("openCtx was not canceled when A-leg was canceled")
	}

	// Committing after cancellation won should report Canceled
	handle := &recordingBLeg{}
	res, err := permit.Commit(handle)
	if err != nil {
		t.Fatalf("commit error: %v", err)
	}
	if !res.Canceled {
		t.Fatal("expected Commit result to be Canceled")
	}
	if res.Cause.Kind != leglifecycle.CancelExplicit {
		t.Fatalf("expected cause %v, got %v", leglifecycle.CancelExplicit, res.Cause.Kind)
	}

	// Handle should NOT have been registered into blegs, so subsequent release or cancel does not double-cancel
	if got := handle.calls(); len(got) != 0 {
		t.Fatalf("handle should not have been called, got %v", got)
	}
}

func TestLaunchPermit_CommitBeforeCancel(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	a := coord.StartALeg("a-3")

	openCtx, permit, err := a.BeginBLegLaunch(context.Background(), "b-1")
	if err != nil {
		t.Fatal(err)
	}

	handle := &recordingBLeg{}
	res, err := permit.Commit(handle)
	if err != nil {
		t.Fatalf("commit error: %v", err)
	}
	if res.Canceled {
		t.Fatal("expected Commit not to be canceled")
	}
	select {
	case <-openCtx.Done():
		t.Fatal("successful commit canceled the backend stream context")
	default:
	}

	cause := leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}
	if err := a.Cancel(context.Background(), cause); err != nil {
		t.Fatal(err)
	}

	if got, want := handle.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("handle calls = %v, want %v", got, want)
	}
}

func TestLaunchPermit_Abort(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-4")

	openCtx, permit, err := a.BeginBLegLaunch(context.Background(), "b-1")
	if err != nil {
		t.Fatal(err)
	}

	permit.Abort()

	select {
	case <-openCtx.Done():
		// Abort cancels openCtx
	case <-time.After(100 * time.Millisecond):
		t.Fatal("openCtx was not canceled after permit.Abort()")
	}

	// Subsequent Commit after Abort is a no-op
	handle := &recordingBLeg{}
	_, _ = permit.Commit(handle)

	// Cancel A-leg should not see handle
	_ = a.Cancel(context.Background(), leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit})
	if got := handle.calls(); len(got) != 0 {
		t.Fatalf("handle should not have been called after aborted permit, got %v", got)
	}
}

func TestLaunchPermit_DoubleCommitAndDoubleAbort(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-5")

	_, permit1, err := a.BeginBLegLaunch(context.Background(), "b-1")
	if err != nil {
		t.Fatal(err)
	}
	handle := &recordingBLeg{}
	res1, err := permit1.Commit(handle)
	if err != nil || res1.Canceled {
		t.Fatalf("first commit: res=%v err=%v", res1, err)
	}
	res2, err := permit1.Commit(handle)
	if err != nil || res2.Canceled {
		t.Fatalf("second commit: res=%v err=%v", res2, err)
	}

	_, permit2, err := a.BeginBLegLaunch(context.Background(), "b-2")
	if err != nil {
		t.Fatal(err)
	}
	permit2.Abort()
	permit2.Abort() // double abort safe
}

func TestLaunchPermit_RepeatedBeginSameID(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-6")

	openCtx1, permit1, err := a.BeginBLegLaunch(context.Background(), "b-same")
	if err != nil {
		t.Fatal(err)
	}

	openCtx2, permit2, err := a.BeginBLegLaunch(context.Background(), "b-same")
	if err != nil {
		t.Fatal(err)
	}

	// First openCtx should be canceled when replaced
	select {
	case <-openCtx1.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("openCtx1 was not canceled on replacement")
	}

	select {
	case <-openCtx2.Done():
		t.Fatal("openCtx2 should still be live")
	default:
	}

	permit1.Abort() // should not affect permit2
	select {
	case <-openCtx2.Done():
		t.Fatal("openCtx2 should still be live after old permit abort")
	default:
	}

	permit2.Abort()
	select {
	case <-openCtx2.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("openCtx2 was not canceled after permit2 abort")
	}
}

func TestLaunchPermit_ReleaseBLegCancelsLaunch(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-7")

	openCtx, _, err := a.BeginBLegLaunch(context.Background(), "b-release-launch")
	if err != nil {
		t.Fatal(err)
	}

	a.ReleaseBLeg("b-release-launch")

	select {
	case <-openCtx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("openCtx was not canceled after ReleaseBLeg")
	}
}

func TestLaunchPermit_StalePermitDoesNotRemoveSuccessor(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-x")

	_, permit1, err := a.BeginBLegLaunch(context.Background(), "b-dup")
	if err != nil {
		t.Fatal(err)
	}

	openCtx2, permit2, err := a.BeginBLegLaunch(context.Background(), "b-dup")
	if err != nil {
		t.Fatal(err)
	}

	handle2 := &recordingBLeg{}

	permit1.Abort()

	select {
	case <-openCtx2.Done():
		t.Fatal("openCtx2 should still be live after stale permit1 abort")
	default:
	}

	cause := leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}
	if err := a.Cancel(context.Background(), cause); err != nil {
		t.Fatal(err)
	}

	select {
	case <-openCtx2.Done():
		// openCtx2 canceled promptly
	case <-time.After(100 * time.Millisecond):
		t.Fatal("openCtx2 was not canceled when A-leg was canceled")
	}

	res, err := permit2.Commit(handle2)
	if err != nil {
		t.Fatalf("commit error: %v", err)
	}
	if !res.Canceled {
		t.Fatal("expected Commit result to be Canceled")
	}
	if res.Cause.Kind != leglifecycle.CancelExplicit {
		t.Fatalf("expected cause %v, got %v", leglifecycle.CancelExplicit, res.Cause.Kind)
	}
	if got := handle2.calls(); len(got) != 0 {
		t.Fatalf("handle2 should not have been called, got %v", got)
	}
}

func TestLaunchPermit_StalePermitCommitDoesNotRemoveSuccessor(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	a := coord.StartALeg("a-commit-stale")

	_, permit1, err := a.BeginBLegLaunch(context.Background(), "b-dup")
	if err != nil {
		t.Fatal(err)
	}

	openCtx2, permit2, err := a.BeginBLegLaunch(context.Background(), "b-dup")
	if err != nil {
		t.Fatal(err)
	}

	handle1 := &recordingBLeg{}
	if _, err := permit1.Commit(handle1); err != nil {
		t.Fatal(err)
	}

	select {
	case <-openCtx2.Done():
		t.Fatal("openCtx2 should still be live after stale permit1 commit")
	default:
	}

	cause := leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}
	if err := a.Cancel(context.Background(), cause); err != nil {
		t.Fatal(err)
	}

	select {
	case <-openCtx2.Done():
		// openCtx2 canceled promptly
	case <-time.After(100 * time.Millisecond):
		t.Fatal("openCtx2 was not canceled when A-leg was canceled")
	}

	handle2 := &recordingBLeg{}
	res, err := permit2.Commit(handle2)
	if err != nil {
		t.Fatalf("commit error: %v", err)
	}
	if !res.Canceled {
		t.Fatal("expected Commit result to be Canceled")
	}
	if res.Cause.Kind != leglifecycle.CancelExplicit {
		t.Fatalf("expected cause %v, got %v", leglifecycle.CancelExplicit, res.Cause.Kind)
	}
	if got := handle2.calls(); len(got) != 0 {
		t.Fatalf("handle2 should not have been called, got %v", got)
	}
}

func TestLaunchPermit_CommitThenAbort_SettledOnce(t *testing.T) {
	t.Parallel()
	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{CancelTimeout: time.Second})
	a := coord.StartALeg("a-commit-abort")

	openCtx, permit, err := a.BeginBLegLaunch(context.Background(), "b-1")
	if err != nil {
		t.Fatal(err)
	}

	handle := &recordingBLeg{}
	res, err := permit.Commit(handle)
	if err != nil || res.Canceled {
		t.Fatalf("commit error: %v, res: %+v", err, res)
	}

	// Calling Abort after Commit must be a no-op (settled once)
	permit.Abort()

	select {
	case <-openCtx.Done():
		t.Fatal("openCtx was canceled by Abort after Commit")
	default:
	}

	// Cancel A-leg should still cancel the committed handle
	cause := leglifecycle.CancelCause{Kind: leglifecycle.CancelExplicit}
	if err := a.Cancel(context.Background(), cause); err != nil {
		t.Fatal(err)
	}

	if got, want := handle.calls(), []string{"cancel:explicit", "close"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("handle calls = %v, want %v", got, want)
	}
}

func TestLaunchPermit_NilReceiverSafety(t *testing.T) {
	t.Parallel()

	var nilPermit *leglifecycle.LaunchPermit
	res, err := nilPermit.Commit(&recordingBLeg{})
	if err != nil || res.Canceled {
		t.Fatalf("nil permit Commit: res=%+v err=%v", res, err)
	}
	nilPermit.Abort() // should not panic

	var nilALeg *leglifecycle.ALeg
	ctx, permit, err := nilALeg.BeginBLegLaunch(context.Background(), "b-nil")
	if err != nil || permit != nil || ctx == nil {
		t.Fatalf("nil ALeg BeginBLegLaunch: ctx=%v permit=%v err=%v", ctx, permit, err)
	}
}
