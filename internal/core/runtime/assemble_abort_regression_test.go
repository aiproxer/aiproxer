package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	usagelimit "github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
)

// trackingStream records Cancel/Close calls.
type trackingStream struct {
	cancelCalls atomic.Int64
	closeCalls  atomic.Int64
	cancelErr   error
	closeErr    error
}

func (s *trackingStream) Recv(context.Context) (aipapi.Event, error) { return aipapi.Event{}, nil }

func (s *trackingStream) Cancel(_ context.Context, _ aipapi.CancelCause) aipapi.CancelResult {
	s.cancelCalls.Add(1)
	return aipapi.CancelResult{Err: s.cancelErr}
}

func (s *trackingStream) Close() error {
	s.closeCalls.Add(1)
	return s.closeErr
}

func (s *trackingStream) Counts() (int64, int64) {
	return s.cancelCalls.Load(), s.closeCalls.Load()
}

func TestAttemptSession_AbortBeforeReturn_ClosesStreamAndReleasesAuthority(t *testing.T) {
	t.Parallel()

	coord := leglifecycle.NewCoordinator(leglifecycle.CoordinatorConfig{})
	aScope := coord.StartALeg("a-1")
	stream := &trackingStream{}
	bleg := b2bua.BLegRecord{BLegID: "b-1", Seq: 1}
	if err := aScope.RegisterBLeg(context.Background(), leglifecycle.BLegHandle{ID: bleg.BLegID, Attempt: stream}); err != nil {
		t.Fatalf("register b-leg: %v", err)
	}

	svc := &recordingAuthorityService{
		admitResult: usagelimit.AdmissionResult{
			Reserved:       true,
			ReservationID:  "res-1",
			SelectedRuleID: "rule-1",
		},
	}
	state := attemptAuthorityState{
		admissionInput: usagelimit.AdmissionInput{
			Correlation: controlplane.Correlation{TraceID: "trace-1", ALegID: "a-1", BLegID: "b-1", RequestID: "req-1"},
		},
		admissionResult: usagelimit.AdmissionResult{
			Reserved:       true,
			ReservationID:  "res-1",
			SelectedRuleID: "rule-1",
		},
	}
	ex := TestExecutor()
	ex.UsageAuthority = svc
	lc := ex.newAttemptAuthorityLifecycle(state, routing.AttemptCandidate{Key: "backend-1:model-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}})
	lc.backendAttempted.Store(true)

	sess := newAttemptSession(attemptSessionInput{
		inner:          stream,
		bleg:           bleg,
		cand:           routing.AttemptCandidate{Key: "backend-1:model-1", Primary: routing.Primary{Backend: "backend-1", Model: "model-1"}},
		authority:      lc,
		aScope:         aScope,
		finalStreamObs: &extensions.FinalStreamObservationSession{},
	})

	cause := errors.New("observer open fail")
	if err := sess.AbortBeforeReturn(context.Background(), cause); !errors.Is(err, cause) {
		t.Fatalf("abort must return cause, got %v", err)
	}

	cancel, closeCalls := stream.Counts()
	if cancel != 1 || closeCalls != 1 {
		t.Fatalf("want exactly 1 Cancel and 1 Close, got cancel=%d close=%d", cancel, closeCalls)
	}
	if !lc.Settled() {
		t.Fatalf("authority must be settled after abort")
	}
	if svc.settleCalls.Load()+svc.releaseCalls.Load() == 0 {
		t.Fatalf("authority settle/release must be called")
	}
	if err := aScope.Cancel(context.Background(), leglifecycle.CancelCause{Kind: leglifecycle.CancelContextDone}); err != nil {
		t.Fatalf("second cancel failed: %v", err)
	}
	cancel2, close2 := stream.Counts()
	if cancel2 != 1 || close2 != 1 {
		t.Fatalf("B-leg must have been released; second Cancel sweep must not re-cancel, got cancel=%d close=%d", cancel2, close2)
	}

	_ = sess.AbortBeforeReturn(context.Background(), cause)
	cancel3, close3 := stream.Counts()
	if cancel3 != 1 || close3 != 1 {
		t.Fatalf("second abort must be idempotent, got cancel=%d close=%d", cancel3, close3)
	}
	if sess.loadInner() != nil {
		t.Fatalf("inner must be nil after abort")
	}
}
