package adapter

import (
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

func TestCancelState_Request_FirstVsRepeatAndDeadlineMinMerge(t *testing.T) {
	t.Parallel()

	now := time.Now()
	d1 := now.Add(2 * time.Second)
	d2 := now.Add(1 * time.Second) // earlier
	d3 := now.Add(3 * time.Second) // later

	type step struct {
		cause        aipapi.CancelCause
		deadline     time.Time
		wantFirst    bool
		wantCause    aipapi.CancelCause
		wantDeadline time.Time
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "single first request sets state",
			steps: []step{
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "explicit stop"},
					deadline:     d1,
					wantFirst:    true,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "explicit stop"},
					wantDeadline: d1,
				},
			},
		},
		{
			name: "repeat request with earlier deadline merges to earlier",
			steps: []step{
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					deadline:     d1,
					wantFirst:    true,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					wantDeadline: d1,
				},
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "second"},
					deadline:     d2, // earlier than d1
					wantFirst:    false,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "second"},
					wantDeadline: d2,
				},
			},
		},
		{
			name: "repeat request with later deadline preserves earlier deadline",
			steps: []step{
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					deadline:     d1,
					wantFirst:    true,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					wantDeadline: d1,
				},
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelClientGone, Detail: "second"},
					deadline:     d3, // later than d1
					wantFirst:    false,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelClientGone, Detail: "second"},
					wantDeadline: d1, // unchanged
				},
			},
		},
		{
			name: "repeat request with zero deadline preserves earlier deadline",
			steps: []step{
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					deadline:     d1,
					wantFirst:    true,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					wantDeadline: d1,
				},
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelRaceLoser, Detail: "second"},
					deadline:     time.Time{},
					wantFirst:    false,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelRaceLoser, Detail: "second"},
					wantDeadline: d1, // unchanged
				},
			},
		},
		{
			name: "first request with zero deadline updated by second request with non-zero deadline",
			steps: []step{
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					deadline:     time.Time{},
					wantFirst:    true,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "first"},
					wantDeadline: time.Time{},
				},
				{
					cause:        aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "second"},
					deadline:     d1,
					wantFirst:    false,
					wantCause:    aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "second"},
					wantDeadline: d1,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var cs cancelState
			for i, s := range tc.steps {
				gotFirst := cs.request(s.cause, s.deadline)
				if gotFirst != s.wantFirst {
					t.Fatalf("step %d: request() first = %v, want %v", i, gotFirst, s.wantFirst)
				}
				snap := cs.snapshot()
				if !snap.Requested {
					t.Fatalf("step %d: snap.Requested = false, want true", i)
				}
				if snap.Cause != s.wantCause {
					t.Fatalf("step %d: snap.Cause = %+v, want %+v", i, snap.Cause, s.wantCause)
				}
				if !snap.EffectiveDeadline.Equal(s.wantDeadline) {
					t.Fatalf("step %d: snap.EffectiveDeadline = %v, want %v", i, snap.EffectiveDeadline, s.wantDeadline)
				}
			}
		})
	}
}

func TestCancelState_ObserveOutcome_MappingAndNilSafety(t *testing.T) {
	t.Parallel()

	t.Run("nil outcome is a safe no-op", func(t *testing.T) {
		t.Parallel()
		var cs cancelState
		cs.observeOutcome(nil)

		snap := cs.snapshot()
		if snap.OutcomeSeen {
			t.Fatal("expected OutcomeSeen to be false for nil outcome")
		}
		if cs.isOutcomeSeen() {
			t.Fatal("expected isOutcomeSeen() accessor to be false")
		}
	})

	t.Run("valid outcome populates all fields", func(t *testing.T) {
		t.Parallel()
		var cs cancelState
		outcome := &backendplugin.CancelOutcome{
			Acknowledged: true,
			Mode:         backendplugin.CancelModeProvider,
			Reason:       backendplugin.CancelReasonHost,
			Detail:       "provider cancel success",
		}
		cs.observeOutcome(outcome)

		if !cs.isOutcomeSeen() {
			t.Fatal("expected isOutcomeSeen() to be true")
		}

		snap := cs.snapshot()
		if !snap.OutcomeSeen {
			t.Fatal("expected snap.OutcomeSeen to be true")
		}
		if !snap.OutcomeAcknowledged {
			t.Fatal("expected snap.OutcomeAcknowledged to be true")
		}
		if snap.OutcomeMode != backendplugin.CancelModeProvider {
			t.Fatalf("snap.OutcomeMode = %v, want %v", snap.OutcomeMode, backendplugin.CancelModeProvider)
		}
		if snap.OutcomeReason != backendplugin.CancelReasonHost {
			t.Fatalf("snap.OutcomeReason = %v, want %v", snap.OutcomeReason, backendplugin.CancelReasonHost)
		}
		if snap.OutcomeDetail != "provider cancel success" {
			t.Fatalf("snap.OutcomeDetail = %q, want 'provider cancel success'", snap.OutcomeDetail)
		}
	})

	t.Run("negative ack outcome with detail", func(t *testing.T) {
		t.Parallel()
		var cs cancelState
		outcome := &backendplugin.CancelOutcome{
			Acknowledged: false,
			Mode:         backendplugin.CancelModeTransport,
			Reason:       backendplugin.CancelReasonClient,
			Detail:       "upstream failed",
		}
		cs.observeOutcome(outcome)

		snap := cs.snapshot()
		if !snap.OutcomeSeen {
			t.Fatal("expected snap.OutcomeSeen to be true")
		}
		if snap.OutcomeAcknowledged {
			t.Fatal("expected snap.OutcomeAcknowledged to be false")
		}
		if snap.OutcomeMode != backendplugin.CancelModeTransport {
			t.Fatalf("snap.OutcomeMode = %v, want %v", snap.OutcomeMode, backendplugin.CancelModeTransport)
		}
		if snap.OutcomeReason != backendplugin.CancelReasonClient {
			t.Fatalf("snap.OutcomeReason = %v, want %v", snap.OutcomeReason, backendplugin.CancelReasonClient)
		}
		if snap.OutcomeDetail != "upstream failed" {
			t.Fatalf("snap.OutcomeDetail = %q, want 'upstream failed'", snap.OutcomeDetail)
		}
	})
}

func TestCancelState_MarkForced_IdempotenceAndInterrupted(t *testing.T) {
	t.Parallel()

	var cs cancelState
	if cs.forcedAbort() {
		t.Fatal("initial forcedAbort() must be false")
	}
	if cs.interrupted() {
		t.Fatal("initial interrupted() must be false")
	}

	cs.markForced()
	if !cs.forcedAbort() {
		t.Fatal("forcedAbort() must be true after markForced()")
	}
	if !cs.interrupted() {
		t.Fatal("interrupted() must be true after markForced()")
	}
	if !cs.snapshot().ForcedAbort {
		t.Fatal("snapshot().ForcedAbort must be true")
	}

	// Idempotence: calling again leaves it true
	cs.markForced()
	if !cs.forcedAbort() {
		t.Fatal("forcedAbort() must stay true after second markForced()")
	}
	if !cs.interrupted() {
		t.Fatal("interrupted() must stay true after second markForced()")
	}
}

func TestCancelState_SnapshotCompletenessAndIsolation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	var cs cancelState
	cs.request(aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "initial"}, now)
	cs.observeOutcome(&backendplugin.CancelOutcome{
		Acknowledged: true,
		Mode:         backendplugin.CancelModeProvider,
		Reason:       backendplugin.CancelReasonHost,
		Detail:       "outcome detail",
	})
	cs.markForced()

	snap1 := cs.snapshot()
	if !snap1.Requested || snap1.Cause.Kind != aipapi.CancelExplicit || snap1.Cause.Detail != "initial" {
		t.Fatalf("snap1 unexpected requested/cause: %+v", snap1)
	}
	if !snap1.EffectiveDeadline.Equal(now) {
		t.Fatalf("snap1 unexpected deadline: %v", snap1.EffectiveDeadline)
	}
	if !snap1.OutcomeSeen || !snap1.OutcomeAcknowledged || snap1.OutcomeMode != backendplugin.CancelModeProvider {
		t.Fatalf("snap1 unexpected outcome fields: %+v", snap1)
	}
	if snap1.OutcomeReason != backendplugin.CancelReasonHost || snap1.OutcomeDetail != "outcome detail" {
		t.Fatalf("snap1 unexpected outcome reason/detail: %+v", snap1)
	}
	if !snap1.ForcedAbort {
		t.Fatal("snap1 ForcedAbort must be true")
	}
	if snap1.TerminalSeen {
		t.Fatal("snap1 TerminalSeen must be false in cancelState snapshot")
	}

	// Mutate state afterwards, ensure snap1 is unchanged (value isolation)
	cs.request(aipapi.CancelCause{Kind: aipapi.CancelClientGone, Detail: "updated"}, now.Add(-time.Second))
	if snap1.Cause.Kind != aipapi.CancelExplicit {
		t.Fatalf("snap1 was mutated: %+v", snap1)
	}
}
