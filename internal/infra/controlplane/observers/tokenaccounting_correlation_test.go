package observers_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/tokenaccounting/ledger"
	"github.com/aiproxer/aiproxer/internal/infra/controlplane/observers"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	cp "github.com/aiproxer/aiproxer/pkg/aipsdk/controlplane"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/usage"
)

// TestPhase6Correlate_ControlPlaneUsageAlignsWithTokenAccountingDimensions
// proves the control-plane usage event records the same safe correlation
// dimensions the existing token-accounting ledger uses (request/trace, session,
// A-leg, B-leg, attempt seq, frontend, backend, model) and is explicitly marked
// observed — distinguishable from an accounting-authoritative ledger record
// (task 6.3; requirements 8.2, 9.2, 10.6, 10.7).
func TestPhase6Correlate_ControlPlaneUsageAlignsWithTokenAccountingDimensions(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	adapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.recorder,
	})

	ev := usage.Event{
		TraceID:       "trace-acct",
		ALegID:        "aleg-acct",
		BLegID:        "bleg-acct",
		SessionID:     "sess-acct",
		AttemptSeq:    2,
		BackendID:     "openai",
		FrontendID:    "openai-responses",
		Model:         "gpt-4o",
		Scope:         knownScope(),
		InputTokens:   110,
		OutputTokens:  40,
		TotalTokens:   150,
		CostNanoUnits: 1000,
		Currency:      "USD",
		CostSource:    "accounting",
		RecordedAt:    fixedTime,
	}
	if err := adapter.OnUsage(context.Background(), ev); err != nil {
		t.Fatalf("usage observer must be fail-open: %v", err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].Usage() == nil {
		t.Fatalf("usage event not recorded: %#v", evs)
	}
	cor := evs[0].Correlation
	if cor.TraceID != ev.TraceID || cor.SessionID != ev.SessionID ||
		cor.ALegID != ev.ALegID || cor.BLegID != ev.BLegID || cor.AttemptSeq != ev.AttemptSeq ||
		cor.FrontendID != ev.FrontendID || cor.BackendID != ev.BackendID || cor.Model != ev.Model {
		t.Fatalf("correlation dimensions lost/changed: %#v", cor)
	}

	// Control-plane evidence is observed, not accounting-authoritative: it must
	// be distinguishable from the authoritative token-accounting ledger record.
	if evs[0].Usage().Plane != cp.UsagePlaneObserved {
		t.Fatalf("control-plane usage plane must be observed, got %q", evs[0].Usage().Plane)
	}
	if evs[0].Usage().Availability != cp.UsageAvailabilityObserved {
		t.Fatalf("control-plane usage availability must be observed, got %q", evs[0].Usage().Availability)
	}
	if evs[0].Usage().Availability == cp.UsageAvailabilityAccountingAuth {
		t.Fatalf("control-plane observed usage must not masquerade as accounting-authoritative")
	}

	// The authoritative token-accounting ledger record for the same attempt is
	// the distinguishable accounting-plane fact.
	authLedger := ledger.NewMemoryLedger(ledger.Options{Now: func() time.Time { return fixedTime }})
	authRec := ledger.Record{
		RequestID:    ev.TraceID,
		AttemptID:    ev.BLegID + ":2",
		Backend:      ev.BackendID,
		Model:        ev.Model,
		Plane:        aipapi.UsagePlaneProviderBillable,
		InputTokens:  ev.InputTokens,
		OutputTokens: ev.OutputTokens,
		TotalTokens:  ev.TotalTokens,
		Metadata: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
		CreatedAt: fixedTime,
	}
	if err := authLedger.Record(context.Background(), authRec); err != nil {
		t.Fatalf("authoritative ledger record: %v", err)
	}
	got, err := authLedger.ListByAttempt(context.Background(), ev.TraceID, ev.BLegID+":2")
	if err != nil || len(got) != 1 {
		t.Fatalf("ledger lookup: %v len=%d", err, len(got))
	}
	if got[0].Plane != aipapi.UsagePlaneProviderBillable || got[0].Metadata.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("authoritative ledger record must remain accounting-authoritative: %#v", got[0])
	}
	// Accounting-authoritative ledger record and control-plane observed event
	// carry the same safe correlation but distinct plane/authority.
	if got[0].Backend != evs[0].Correlation.BackendID || got[0].Model != evs[0].Correlation.Model {
		t.Fatalf("ledger and control-plane correlation disagree on backend/model")
	}
}

// ledgerObserver is a test-only usage.Observer that projects a usage.Event into
// the accounting-authoritative token-accounting ledger, mirroring the existing
// accounting-observer wiring (so the chain remains realistic).
type ledgerObserver struct {
	mu      sync.Mutex
	led     *ledger.MemoryLedger
	calls   int
	lastRec ledger.Record
	err     error
}

func (l *ledgerObserver) OnUsage(ctx context.Context, ev usage.Event) error {
	rec := ledger.Record{
		RequestID:    ev.TraceID,
		AttemptID:    ev.BLegID + ":" + strconv.Itoa(ev.AttemptSeq),
		Backend:      ev.BackendID,
		Model:        ev.Model,
		Plane:        aipapi.UsagePlaneProviderBillable,
		InputTokens:  ev.InputTokens,
		OutputTokens: ev.OutputTokens,
		TotalTokens:  ev.TotalTokens,
		Metadata: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
		CreatedAt: ev.RecordedAt,
	}
	if err := l.led.Record(ctx, rec); err != nil {
		l.mu.Lock()
		l.err = err
		l.mu.Unlock()
		return err
	}
	l.mu.Lock()
	l.calls++
	l.lastRec = rec
	l.mu.Unlock()
	return nil
}

// TestPhase6Correlate_TokenAccountingStillWorksWithControlPlaneDisabled proves
// the existing token-accounting observer chain continues to produce
// accounting-authoritative ledger records when the control-plane capability is
// disabled — the control-plane adapter is a no-op and never blocks the chain
// (requirements 8.2, 8.4, 8.5, 10.6).
func TestPhase6Correlate_TokenAccountingStillWorksWithControlPlaneDisabled(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	cpAdapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   h.disabledRecorder(),
	})
	authLedger := ledger.NewMemoryLedger(ledger.Options{Now: func() time.Time { return fixedTime }})
	acct := &ledgerObserver{led: authLedger}

	chain := usage.ChainObservers(cpAdapter, acct)
	ev := sampleUsageEvent()
	if err := chain.OnUsage(context.Background(), ev); err != nil {
		t.Fatalf("chain must not surface error when control-plane disabled: %v", err)
	}
	if acct.calls != 1 {
		t.Fatalf("accounting observer must be called once, got %d", acct.calls)
	}
	if len(h.events()) != 0 {
		t.Fatalf("disabled control-plane must record nothing, got %d", len(h.events()))
	}
	got, err := authLedger.ListByRequest(context.Background(), ev.TraceID)
	if err != nil || len(got) != 1 || got[0].Metadata.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("authoritative ledger record must persist: err=%v len=%d", err, len(got))
	}
}

// TestPhase6Correlate_TokenAccountingStillWorksWithControlPlaneDegraded proves
// a degraded control-plane recorder (every Append fails) never blocks the
// existing token-accounting chain: the accounting-authoritative ledger record
// is still produced and the chain returns no error (requirements 5.2, 8.4,
// 8.5, 10.7).
func TestPhase6Correlate_TokenAccountingStillWorksWithControlPlaneDegraded(t *testing.T) {
	t.Parallel()
	h := newHarness(t, cp.RecordingBestEffort, nil)
	failingRec := newFailingRecorder(t, h.status, cp.RecordingBestEffort, nil)
	cpAdapter := observers.NewUsageObserverAdapter(observers.UsageObserverAdapterConfig{
		Normalizer: h.normal,
		Recorder:   failingRec,
	})
	authLedger := ledger.NewMemoryLedger(ledger.Options{Now: func() time.Time { return fixedTime }})
	acct := &ledgerObserver{led: authLedger}

	chain := usage.ChainObservers(cpAdapter, acct)
	ev := sampleUsageEvent()
	if err := chain.OnUsage(context.Background(), ev); err != nil {
		t.Fatalf("chain must not surface control-plane degradation: %v", err)
	}
	if acct.calls != 1 {
		t.Fatalf("accounting observer must still be called when control-plane degraded, got %d", acct.calls)
	}
	if got := h.status.Snapshot().State; got != cp.CapabilityDegraded {
		t.Fatalf("control-plane status must degrade, got %q", got)
	}
	got, err := authLedger.ListByRequest(context.Background(), ev.TraceID)
	if err != nil || len(got) != 1 {
		t.Fatalf("authoritative ledger record must persist despite control-plane degradation: err=%v len=%d", err, len(got))
	}
	if acct.err != nil {
		t.Fatalf("accounting observer must not surface control-plane error: %v", acct.err)
	}
	if got[0].Metadata.Authority != aipapi.UsageAuthorityAuthoritative {
		t.Fatalf("accounting authority must remain authoritative: %#v", got[0].Metadata)
	}
}
