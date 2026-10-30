package compaction

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/execctx"
	featurecontinuity "github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity"
	"github.com/aiproxer/aiproxer/internal/plugins/features/compactioncontinuity/state"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipstate "github.com/aiproxer/aiproxer/pkg/aipsdk/state"
)

func TestNewParentPort_NilCoordinator(t *testing.T) {
	t.Parallel()
	port, err := NewParentPort(nil)
	if err == nil {
		t.Fatal("expected error on nil coordinator, got nil")
	}
	if !errors.Is(err, ErrInvalidCompactionContinuityParentPort) {
		t.Fatalf("expected ErrInvalidCompactionContinuityParentPort, got: %v", err)
	}
	if port != nil {
		t.Fatalf("expected nil port, got: %+v", port)
	}
}

func TestParentPortUsesTrustedParentForDetachedChild(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	parentCtx := execctx.WithViews(context.Background(), execctx.Views{
		Scope:     scope.PrincipalScopeView{PrincipalID: scope.Known("principal-parent")},
		Session:   session.SessionView{AuthoritativeSessionID: "session-parent", ALegID: "a-parent"},
		Principal: execview.PrincipalView{ID: "principal-parent"},
	})
	ctx := execctx.WithDetachedSession(parentCtx, execctx.DetachedSession{
		ParentSessionID: "session-parent",
		ParentALegID:    "a-parent",
		ParentTraceID:   "trace-parent",
	})
	parent, err := port.CaptureMeta(ctx, compaction.PreservationMeta{
		SessionID: "session-child",
		ALegID:    "a-child",
		TraceID:   "trace-child",
		BLegID:    "b-child",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantKey, err := state.NewBranchKey("session-parent", "a-parent", "principal-parent")
	if err != nil {
		t.Fatal(err)
	}
	if parent.Binding != wantKey.Binding() {
		t.Fatalf("parent binding = %q, want %q", parent.Binding, wantKey.Binding())
	}
	if parent.ALegID != "a-parent" {
		t.Fatalf("parent ALegID = %q, want a-parent", parent.ALegID)
	}
	if parent.TraceID != "trace-parent" {
		t.Fatalf("parent TraceID = %q, want trace-parent", parent.TraceID)
	}
	if parent.BLegID != "b-child" {
		t.Fatalf("parent BLegID = %q, want b-child", parent.BLegID)
	}

	// Also verify Capture with dummy Call delegates identically
	parentFromCall, err := port.Capture(ctx, aipapi.Call{}, compaction.PreservationMeta{
		SessionID: "session-child-2",
		ALegID:    "a-child-2",
		TraceID:   "trace-child-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if parentFromCall.Binding != wantKey.Binding() {
		t.Fatalf("parentFromCall binding = %q, want %q", parentFromCall.Binding, wantKey.Binding())
	}
}

func TestParentPortRejectsUntrustedMetadata(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	_, err := port.CaptureMeta(context.Background(), compaction.PreservationMeta{
		SessionID: "forged",
		ALegID:    "forged-a",
	})
	if !errors.Is(err, state.ErrInvalidBranchKey) {
		t.Fatalf("expected ErrInvalidBranchKey for untrusted context, got: %v", err)
	}
}

func TestParentPortRejectsEmptySessionAndPrincipal(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Session: session.SessionView{ALegID: "a-leg-only"},
	})
	_, err := port.CaptureMeta(ctx, compaction.PreservationMeta{
		ALegID: "a-leg-only",
	})
	if !errors.Is(err, state.ErrInvalidBranchKey) {
		t.Fatalf("expected ErrInvalidBranchKey for empty session and principal, got: %v", err)
	}
}

func TestParentPortRejectsAdversarialSessionMismatch(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		Session: session.SessionView{AuthoritativeSessionID: "session-auth", ALegID: "a-leg-1"},
	})
	_, err := port.CaptureMeta(ctx, compaction.PreservationMeta{
		SessionID: "session-adversarial",
		ALegID:    "a-leg-1",
	})
	if !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("expected ErrBranchMismatch for session conflict, got: %v", err)
	}
}

func TestParentPortRejectsAdversarialALegMismatch(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("p1")},
		Session: session.SessionView{AuthoritativeSessionID: "session-auth", ALegID: "a-leg-1"},
	})
	_, err := port.CaptureMeta(ctx, compaction.PreservationMeta{
		SessionID: "session-auth",
		ALegID:    "a-leg-adversarial",
	})
	if !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("expected ErrBranchMismatch for A-leg conflict, got: %v", err)
	}
}

func TestParentPortCASAndLifecycle(t *testing.T) {
	t.Parallel()
	port := newTestPort(t)
	ctx := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("principal-boundary")},
		Session: session.SessionView{AuthoritativeSessionID: "session-boundary", ALegID: "a-boundary"},
	})

	parent, err := port.CaptureMeta(ctx, compaction.PreservationMeta{
		SessionID: "session-boundary",
		ALegID:    "a-boundary",
	})
	if err != nil {
		t.Fatal(err)
	}

	snap, err := port.Snapshot(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Revision != 0 {
		t.Fatalf("initial snapshot revision = %d, want 0", snap.Revision)
	}

	st, err := port.CommitSource(ctx, parent, 0, []byte(`{"source":1}`), "wm-source-0")
	if err != nil || st.Revision != 0 {
		t.Fatalf("CommitSource revision = %d, err = %v", st.Revision, err)
	}
	if string(st.SourceJSON) != `{"source":1}` {
		t.Fatalf("SourceJSON = %s, want %s", string(st.SourceJSON), `{"source":1}`)
	}
	if st.SourceHighWatermark != "wm-source-0" {
		t.Fatalf("SourceHighWatermark = %s, want wm-source-0", st.SourceHighWatermark)
	}

	st, err = port.CommitCapsule(ctx, parent, 0, []byte(`{"capsule":1}`), [32]byte{1}, "wm-capsule-1")
	if err != nil || st.Revision != 1 {
		t.Fatalf("CommitCapsule revision = %d, err = %v", st.Revision, err)
	}

	// Pending Job
	st, err = port.RecordPendingJob(ctx, parent, auxiliary.JobID("job-42"), 1)
	if err != nil || st.PendingJobID != "job-42" {
		t.Fatalf("RecordPendingJob failed: st=%+v err=%v", st, err)
	}
	if _, err := port.ValidatePendingJob(ctx, parent, auxiliary.JobID("job-42")); err != nil {
		t.Fatalf("ValidatePendingJob failed: %v", err)
	}

	// CommitCapsuleForJob with wrong resultBinding
	if _, err := port.CommitCapsuleForJob(ctx, parent, auxiliary.JobID("job-42"), "wrong-binding", 1, []byte(`{"capsule":2}`), [32]byte{2}, "wm-2"); !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("expected ErrBranchMismatch for mismatched resultBinding, got: %v", err)
	}

	// CommitCapsuleForJob with correct binding
	st, err = port.CommitCapsuleForJob(ctx, parent, auxiliary.JobID("job-42"), parent.Binding, 1, []byte(`{"capsule":2}`), [32]byte{2}, "wm-2")
	if err != nil || st.Revision != 2 {
		t.Fatalf("CommitCapsuleForJob failed: st=%+v err=%v", st, err)
	}

	// Preview Intent
	st, err = port.RecordPreviewIntent(ctx, parent, featurecontinuity.PreviewIntent{Key: "intent", TargetSourceRevision: 1})
	if err != nil || st.PendingPreviewIntent == nil {
		t.Fatalf("RecordPreviewIntent failed: st=%+v err=%v", st, err)
	}
	if _, err := port.BindPreviewIntent(ctx, parent, "intent", ""); !errors.Is(err, state.ErrInvalidTransaction) {
		t.Fatalf("expected ErrInvalidTransaction for empty tx, got: %v", err)
	}
	st, err = port.BindPreviewIntent(ctx, parent, "intent", "tx-1")
	if err != nil || st.LastCompactionTransaction != "tx-1" {
		t.Fatalf("BindPreviewIntent failed: st=%+v err=%v", st, err)
	}

	// Injection lifecycle
	for _, boundary := range []string{"boundary-a", "boundary-b"} {
		target := featurecontinuity.InjectionTarget{BoundaryKey: boundary, CapsuleRevision: 2}
		st, err = port.SetPendingInjection(ctx, parent, target)
		if err != nil || st.PendingInjection == nil {
			t.Fatalf("SetPendingInjection(%s) failed: st=%+v err=%v", boundary, st, err)
		}
		if _, err = port.ValidateInjection(ctx, parent, target); err != nil {
			t.Fatalf("ValidateInjection(%s) failed: %v", boundary, err)
		}
		st, err = port.CommitReleasedInjection(ctx, parent, featurecontinuity.InjectionWatermark{
			BranchBinding:   parent.Binding,
			BoundaryKey:     boundary,
			CapsuleRevision: 2,
		})
		if err != nil || st.LastReleasedInjection == nil || st.LastReleasedInjection.BoundaryKey != boundary {
			t.Fatalf("CommitReleasedInjection(%s) failed: st=%+v err=%v", boundary, st, err)
		}
	}

	// Wrong/unknown branch binding
	wrong := parent
	wrong.Binding = "sha256:unknown"
	if _, err := port.Snapshot(ctx, wrong); !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("Snapshot with wrong binding: got %v, want ErrBranchMismatch", err)
	}
	if _, err := port.SetPendingInjection(ctx, wrong, featurecontinuity.InjectionTarget{BoundaryKey: "wrong", CapsuleRevision: 2}); !errors.Is(err, state.ErrBranchMismatch) {
		t.Fatalf("SetPendingInjection with wrong binding: got %v, want ErrBranchMismatch", err)
	}
}

func TestParentPortPropagatesCancellationToBlockingStatePersistence(t *testing.T) {
	t.Parallel()

	store := newBlockingPutStore()
	coordinator, err := state.NewBranchCoordinator(context.Background(), state.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewParentPort(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	base := execctx.WithViews(context.Background(), execctx.Views{
		Scope:   scope.PrincipalScopeView{PrincipalID: scope.Known("principal-cancel")},
		Session: session.SessionView{AuthoritativeSessionID: "session-cancel", ALegID: "a-cancel"},
	})
	ctx, cancel := context.WithCancel(base)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		_, callErr := port.CaptureMeta(ctx, compaction.PreservationMeta{SessionID: "session-cancel", ALegID: "a-cancel"})
		result <- callErr
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("CaptureMeta did not reach state persistence")
	}
	cancel()

	select {
	case callErr := <-result:
		if !errors.Is(callErr, context.Canceled) {
			t.Fatalf("CaptureMeta error = %v, want context.Canceled", callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("CaptureMeta did not unblock after request cancellation")
	}
	if _, found, err := coordinator.Snapshot(context.Background(), mustBranchKey(t, "session-cancel", "a-cancel")); err != nil || found {
		t.Fatalf("canceled capture published state: found=%v err=%v", found, err)
	}
	store.mu.Lock()
	writes := store.writes
	store.mu.Unlock()
	if writes != 0 {
		t.Fatalf("canceled capture recorded %d store writes, want 0", writes)
	}
}

func mustBranchKey(t *testing.T, sessionID, aLegID string) state.BranchKey {
	t.Helper()
	key, err := state.NewBranchKey(sessionID, aLegID, "principal-cancel")
	if err != nil {
		t.Fatal(err)
	}
	return key
}

type blockingPutStore struct {
	started   chan struct{}
	startOnce sync.Once
	mu        sync.Mutex
	value     any
	writes    int
}

func newBlockingPutStore() *blockingPutStore {
	return &blockingPutStore{started: make(chan struct{})}
}

func (s *blockingPutStore) Get(_ context.Context, _ aipstate.Scope, _, _ string, out any) (bool, error) {
	s.mu.Lock()
	value := s.value
	s.mu.Unlock()
	if value == nil {
		return false, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, out)
}

func (s *blockingPutStore) Put(ctx context.Context, _ aipstate.Scope, _, _ string, value any, _ time.Duration) error {
	s.startOnce.Do(func() { close(s.started) })
	<-ctx.Done()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.value = value
	s.writes++
	s.mu.Unlock()
	return nil
}

func (s *blockingPutStore) Delete(context.Context, aipstate.Scope, string, string) error {
	return nil
}

func (s *blockingPutStore) InspectTTL(context.Context, aipstate.Scope, string, string) (time.Duration, bool, error) {
	return 0, false, nil
}

func newTestPort(t *testing.T) *ParentPort {
	t.Helper()
	coordinator, err := state.NewBranchCoordinator(context.Background(), state.Config{})
	if err != nil {
		t.Fatal(err)
	}
	port, err := NewParentPort(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
