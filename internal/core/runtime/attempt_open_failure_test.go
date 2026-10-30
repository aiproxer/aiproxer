package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestOpenFailureClassify_OrdinaryAndWire(t *testing.T) {
	t.Parallel()
	live := context.Background()
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	deadlineCancel()
	boom := errors.New("upstream 500")

	type want struct {
		outcome aipapi.AttemptOutcome
		release usagelimit.ReleaseKind
		leg     usageevidence.LegOutcome
		loser   bool
	}
	surfaced := want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeNeverStarted, false}
	cases := []struct {
		name     string
		err      error
		openCtx  context.Context
		parallel bool
		parent   context.Context
		postOpen bool
		ordinary want
		wire     want
	}{
		{"plain failure", boom, live, false, nil, false, surfaced, surfaced},
		{
			// The adapter returned a cancellation the caller did not cause:
			// the leg never started, but the backend stays accountable.
			"cancel not caused by open context", context.Canceled, live, false, nil, false,
			want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, false},
			want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, false},
		},
		{
			"parallel race loser", context.Canceled, canceledCtx, true, live, false,
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, true},
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeCanceled, true},
		},
		{
			// The round ended this leg, so it is a cancellation on both
			// payload paths; only the race-loser mechanics stay per-payload.
			"parallel leg whose open context was canceled", boom, canceledCtx, true, live, false,
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, true},
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeFailed, false},
		},
		{
			"parallel cancel after parent canceled", context.Canceled, canceledCtx, true, canceledCtx, false,
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeNeverStarted, false},
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeNeverStarted, false},
		},
		{
			"parallel plain failure", boom, live, true, live, false,
			surfaced,
			want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeFailed, false},
		},
		{
			// The Open call returned without an error, so the provider may
			// have processed the request: the leg failed rather than never
			// started, whatever the classified error says.
			"open returned without an error", boom, live, false, nil, true,
			want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeFailed, false},
			want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeFailed, false},
		},
		{
			"open returned without an error and the adapter canceled", context.Canceled, live, false, nil, true,
			want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeFailed, false},
			want{aipapi.AttemptSurfacedFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeFailed, false},
		},
		{
			// A request deadline is a caller cancellation, not a backend
			// failure, and the dispatched leg keeps its liability.
			"request deadline", context.DeadlineExceeded, deadlineCtx, false, nil, true,
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeFailed, false},
			want{aipapi.AttemptCancelled, usagelimit.ReleaseKindLosing, usageevidence.LegOutcomeFailed, false},
		},
	}
	for _, tc := range cases {
		plan := candidatePlan{parallel: tc.parallel, parentCtx: tc.parent}
		for _, wire := range []bool{false, true} {
			w := tc.ordinary
			if wire {
				w = tc.wire
			}
			got := openFailure{err: tc.err, openErr: tc.openCtx.Err(), wire: wire, postOpen: tc.postOpen}.classify(plan)
			if got.recordOutcome != w.outcome || got.releaseKind != w.release || got.legOutcome != w.leg || got.raceLoser != w.loser {
				t.Errorf("%s (wire=%v): got outcome=%v release=%v leg=%v loser=%v; want %+v",
					tc.name, wire, got.recordOutcome, got.releaseKind, got.legOutcome, got.raceLoser, w)
			}
		}
	}
}
