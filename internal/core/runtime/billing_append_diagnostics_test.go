package runtime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

type failingTerminalSink struct{ err error }

func (f failingTerminalSink) AppendCall(context.Context, usageevidence.CallUsageRecord) error {
	return f.err
}

func (f failingTerminalSink) AppendLeg(context.Context, usageevidence.CallLegUsageRecord) error {
	return f.err
}

func TestAppendIndependentCallLegLogsBoundedFailure(t *testing.T) {
	var buf bytes.Buffer
	ex := TestExecutor()
	ex.Log = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ex.TerminalUsageSink = failingTerminalSink{err: errors.New("local spool unavailable")}
	callID, err := usageevidence.NewBillingCallID()
	if err != nil {
		t.Fatal(err)
	}
	ex.appendIndependentCallLeg(context.Background(), callID, usageevidence.CallLegUsageRecord{
		ALegID: "a-1", BLegID: "b-1", AttemptSeq: 1, BackendID: "be", ProviderID: "be", ModelID: "m",
		StartedAt: time.Unix(1, 0).UTC(), FinishedAt: time.Unix(2, 0).UTC(),
		Outcome: usageevidence.LegOutcomeFailed, Surfaced: usageevidence.SurfacedNo,
		Evidence: usageevidence.FinalBillingEvidence{Source: usageevidence.EvidenceSourceUnavailable, Authority: usageevidence.EvidenceAuthorityUnavailable},
	})
	got := buf.String()
	if !strings.Contains(got, "billing call-leg append failed") {
		t.Fatalf("want bounded append warning, got %s", got)
	}
	if strings.Contains(got, "billing_call_leg_append_critical") {
		t.Fatalf("local sink failure must not invoke removed outbox critical path: %s", got)
	}
}

func TestTask51BillingClosureAppendFailurePreservesDiagnosticLevels(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		level string
	}{
		{name: "replay conflict", err: usageevidence.ErrReplayConflict, level: "DEBUG"},
		{name: "ordinary failure", err: errors.New("spool unavailable"), level: "WARN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			term := newTurnTerminal()
			term.log = logger
			term.now = func() time.Time { return time.Unix(2, 0).UTC() }
			term.billingWorkload = func(context.Context, string) usageevidence.WorkloadIdentity { return usageevidence.WorkloadIdentity{} }
			term.appendBillingCall = func(context.Context, usageevidence.CallUsageRecord) error { return tc.err }
			logExecutor := &Executor{}
			logExecutor.Log = logger
			term.logBillingAppendFailure = logExecutor.logBillingUsageAppendFailure
			callID, err := usageevidence.NewBillingCallID()
			if err != nil {
				t.Fatal(err)
			}
			facts := recvTurnFacts{
				aLegID: "a-1", billingCallID: callID, billingCallState: newBillingCallState(callID),
				billingIdentityStamped: true, billingAccountID: "acct-1",
				baseline: aipapi.Call{Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-1"}},
			}
			// The injected append failure is the subject of this test; the
			// returned closure error is intentionally observed via the log only.
			_ = term.handoffBillingTurn(context.Background(), facts.terminalFacts(), sdkterminal.CommandNormalFinish)
			got := logs.String()
			if !strings.Contains(got, `"level":"`+tc.level+`"`) {
				t.Fatalf("append failure log level = %s, want %s: %s", got, tc.level, got)
			}
			if !strings.Contains(got, "billing call-closure append failed") {
				t.Fatalf("append failure message missing: %s", got)
			}
		})
	}
}
