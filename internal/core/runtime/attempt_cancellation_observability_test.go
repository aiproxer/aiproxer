package runtime

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func TestDeriveAttemptCancellation(t *testing.T) {
	t.Parallel()

	explicitCause := &aipapi.CancelCause{Kind: aipapi.CancelExplicit, Detail: "user explicit cancel"}
	clientGoneCause := &aipapi.CancelCause{Kind: aipapi.CancelClientGone, Detail: "client disconnected"}
	emptyKindCause := &aipapi.CancelCause{Kind: "", Detail: "ignored"}
	customErr := errors.New("underlying network failure")

	tests := []struct {
		name               string
		intent             attemptTerminalIntent
		evidence           attemptEvidence
		pendingCancelCause *aipapi.CancelCause
		contextsCanceled   bool
		wantIsCancel       bool
		wantCause          aipapi.CancelCause
	}{
		{
			name:         "Success without any cancel signals",
			intent:       IntentSuccess,
			evidence:     attemptEvidence{},
			wantIsCancel: false,
			wantCause:    aipapi.CancelCause{},
		},
		{
			name:         "IntentCancellation with no explicit cause defaults to CancelClientGone",
			intent:       IntentCancellation,
			evidence:     attemptEvidence{},
			wantIsCancel: true,
			wantCause:    aipapi.CancelCause{Kind: aipapi.CancelClientGone},
		},
		{
			name:         "IntentTimeout defaults to CancelContextDone with timeout detail",
			intent:       IntentTimeout,
			evidence:     attemptEvidence{},
			wantIsCancel: true,
			wantCause:    aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "timeout"},
		},
		{
			name:         "IntentParallelLoser defaults to CancelRaceLoser with parallel race loser detail",
			intent:       IntentParallelLoser,
			evidence:     attemptEvidence{},
			wantIsCancel: true,
			wantCause:    aipapi.CancelCause{Kind: aipapi.CancelRaceLoser, Detail: "parallel race loser"},
		},
		{
			name:   "evidence.CancelCause takes precedence over intent and pending",
			intent: IntentTimeout,
			evidence: attemptEvidence{
				CancelCause: explicitCause,
				Err:         customErr,
			},
			pendingCancelCause: clientGoneCause,
			contextsCanceled:   true,
			wantIsCancel:       true,
			wantCause:          *explicitCause,
		},
		{
			name:   "evidence.CancelCause with empty Kind is bypassed to pendingCancelCause",
			intent: IntentSuccess,
			evidence: attemptEvidence{
				CancelCause: emptyKindCause,
			},
			pendingCancelCause: clientGoneCause,
			wantIsCancel:       true,
			wantCause:          *clientGoneCause,
		},
		{
			name:   "pendingCancelCause with non-empty Kind takes precedence over intent and err",
			intent: IntentSuccess,
			evidence: attemptEvidence{
				Err: customErr,
			},
			pendingCancelCause: clientGoneCause,
			wantIsCancel:       true,
			wantCause:          *clientGoneCause,
		},
		{
			name:   "pendingCancelCause with empty Kind is bypassed",
			intent: IntentSuccess,
			evidence: attemptEvidence{
				RecordOutcome: aipapi.AttemptCancelled,
				RecordReason:  "custom reason",
			},
			pendingCancelCause: emptyKindCause,
			wantIsCancel:       true,
			wantCause:          aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "custom reason"},
		},
		{
			name:             "contextsCanceled triggers CancelContextDone when no other cause specified",
			intent:           IntentSuccess,
			evidence:         attemptEvidence{},
			contextsCanceled: true,
			wantIsCancel:     true,
			wantCause:        aipapi.CancelCause{Kind: aipapi.CancelContextDone},
		},
		{
			name:   "evidence.Err triggers CancelContextDone with err detail",
			intent: IntentSuccess,
			evidence: attemptEvidence{
				RecordOutcome: aipapi.AttemptCancelled,
				Err:           customErr,
			},
			contextsCanceled: false,
			wantIsCancel:     true,
			wantCause:        aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: customErr.Error()},
		},
		{
			name:   "evidence.RecordReason triggers CancelContextDone with reason detail",
			intent: IntentSuccess,
			evidence: attemptEvidence{
				RecordOutcome: aipapi.AttemptCancelled,
				RecordReason:  "rate limit exceeded",
			},
			wantIsCancel: true,
			wantCause:    aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "rate limit exceeded"},
		},
		{
			name:   "RecordOutcome AttemptCancelled with no detail defaults to CancelContextDone",
			intent: IntentSuccess,
			evidence: attemptEvidence{
				RecordOutcome: aipapi.AttemptCancelled,
			},
			wantIsCancel: true,
			wantCause:    aipapi.CancelCause{Kind: aipapi.CancelContextDone},
		},
		{
			name:   "IntentSurfacedFailure with Err does not set isCancel, but derives cause",
			intent: IntentSurfacedFailure,
			evidence: attemptEvidence{
				Err: customErr,
			},
			wantIsCancel: false,
			wantCause:    aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: customErr.Error()},
		},
		{
			name:   "IntentSwallowedFailure with RecordReason does not set isCancel, but derives cause",
			intent: IntentSwallowedFailure,
			evidence: attemptEvidence{
				RecordReason: "transport reset",
			},
			wantIsCancel: false,
			wantCause:    aipapi.CancelCause{Kind: aipapi.CancelContextDone, Detail: "transport reset"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotIsCancel, gotCause := deriveAttemptCancellation(
				tc.intent,
				tc.evidence,
				tc.pendingCancelCause,
				tc.contextsCanceled,
			)
			if gotIsCancel != tc.wantIsCancel {
				t.Fatalf("deriveAttemptCancellation() isCancel = %v, want %v", gotIsCancel, tc.wantIsCancel)
			}
			if gotCause != tc.wantCause {
				t.Fatalf("deriveAttemptCancellation() cause = %+v, want %+v", gotCause, tc.wantCause)
			}
		})
	}
}

func TestStoredCancelResult(t *testing.T) {
	t.Parallel()

	// 1. Nil session
	if cr, ok := storedCancelResult(nil); ok || cr.Mode != "" {
		t.Fatalf("storedCancelResult(nil) = (%+v, %v), want ({}, false)", cr, ok)
	}

	// 2. Session with empty cancel result Mode
	sessEmpty := &attemptSession{}
	if cr, ok := storedCancelResult(sessEmpty); ok || cr.Mode != "" {
		t.Fatalf("storedCancelResult(empty) = (%+v, %v), want ({}, false)", cr, ok)
	}

	// 3. Session with non-empty cancel result Mode
	wantCR := aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
	sessWithResult := &attemptSession{cancelResult: wantCR}
	if cr, ok := storedCancelResult(sessWithResult); !ok || cr != wantCR {
		t.Fatalf("storedCancelResult(active) = (%+v, %v), want (%+v, true)", cr, ok, wantCR)
	}
}

type testProbeStream struct {
	negotiated  bool
	outcomeSeen bool
	forcedAbort bool
}

func (s *testProbeStream) Recv(ctx context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, io.EOF
}

func (s *testProbeStream) Cancel(ctx context.Context, c aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{Mode: aipapi.CancelModeProvider}
}
func (s *testProbeStream) Close() error                          { return nil }
func (s *testProbeStream) CancellationOutcomeSeen() bool         { return s.outcomeSeen }
func (s *testProbeStream) CancellationForcedAbort() bool         { return s.forcedAbort }
func (s *testProbeStream) CancellationHandshakeNegotiated() bool { return s.negotiated }

func TestRecordCancellationTelemetry(t *testing.T) {
	t.Parallel()

	var recorded []CancellationObservation
	sess := &attemptSession{
		recordCancellationFn: func(obs CancellationObservation) {
			recorded = append(recorded, obs)
		},
	}

	// Case 1: requested = false
	obs := sess.recordCancellationTelemetry(
		aipapi.CancelCause{Kind: aipapi.CancelExplicit},
		aipapi.CancelResult{Mode: aipapi.CancelModeProvider},
		nil,
		false,
	)
	if len(recorded) != 0 {
		t.Fatalf("expected 0 recordings when requested=false, got %d", len(recorded))
	}
	if obs.Phase != CancellationPhaseTerminal || obs.Mode != CancellationModeProvider {
		t.Fatalf("unexpected obs returned: %+v", obs)
	}

	// Case 2: requested = true with probed stream and forced abort
	recorded = nil
	probe := &testProbeStream{
		negotiated:  true,
		outcomeSeen: true,
		forcedAbort: true,
	}
	obs = sess.recordCancellationTelemetry(
		aipapi.CancelCause{Kind: aipapi.CancelExplicit},
		aipapi.CancelResult{Mode: aipapi.CancelModeProvider},
		probe,
		true,
	)
	if len(recorded) != 4 {
		t.Fatalf("expected 4 recordings (requested, outcome, forced, terminal), got %d: %+v", len(recorded), recorded)
	}
	if recorded[0].Phase != CancellationPhaseRequested || recorded[0].Fallback != CancellationFallbackNone {
		t.Fatalf("unexpected record[0]: %+v", recorded[0])
	}
	if recorded[1].Phase != CancellationPhaseOutcome || recorded[1].Fallback != CancellationFallbackNegotiated {
		t.Fatalf("unexpected record[1]: %+v", recorded[1])
	}
	if recorded[2].Phase != CancellationPhaseForced || recorded[2].Fallback != CancellationFallbackNegotiated {
		t.Fatalf("unexpected record[2]: %+v", recorded[2])
	}
	if recorded[3].Phase != CancellationPhaseTerminal || recorded[3].Fallback != CancellationFallbackNegotiated {
		t.Fatalf("unexpected record[3]: %+v", recorded[3])
	}
	if obs != recorded[3] {
		t.Fatalf("returned obs %+v does not match terminal record %+v", obs, recorded[3])
	}

	// Case 3: legacy non-negotiated stream with deadline exceeded err
	recorded = nil
	probeLegacy := &testProbeStream{
		negotiated:  false,
		outcomeSeen: false,
		forcedAbort: false,
	}
	obs = sess.recordCancellationTelemetry(
		aipapi.CancelCause{Kind: aipapi.CancelContextDone},
		aipapi.CancelResult{Mode: aipapi.CancelModeTransport, Err: context.DeadlineExceeded},
		probeLegacy,
		true,
	)
	if len(recorded) != 3 { // requested, forced (due to DeadlineExceeded), terminal
		t.Fatalf("expected 3 recordings, got %d: %+v", len(recorded), recorded)
	}
	if recorded[1].Phase != CancellationPhaseForced || recorded[1].Fallback != CancellationFallbackLegacy {
		t.Fatalf("unexpected forced record: %+v", recorded[1])
	}
}

func TestLogAttemptCanceled(t *testing.T) {
	t.Parallel()

	// Safe with nil session / nil log
	var nilSess *attemptSession
	nilSess.logAttemptCanceled(context.Background(), attemptEvidence{}, CancellationObservation{})

	emptySess := &attemptSession{}
	emptySess.logAttemptCanceled(context.Background(), attemptEvidence{}, CancellationObservation{})
}
