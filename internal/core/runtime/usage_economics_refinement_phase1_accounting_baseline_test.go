package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

// accountingBaselineCapture records only callbacks that the runtime actually
// invokes. In particular, append counts are not derived from the number of
// Execute calls; they are incremented at the TerminalUsageSink boundary.
type accountingBaselineCapture struct {
	backendOpens    atomic.Int64
	creditChecks    atomic.Int64
	exposureAdmits  atomic.Int64
	observedLegs    atomic.Int64
	appendedLegs    atomic.Int64
	appendedCalls   atomic.Int64
	observedRecords chan usageevidence.CallLegUsageRecord
	legRecords      chan usageevidence.CallLegUsageRecord
	callRecords     chan usageevidence.CallUsageRecord
}

func newAccountingBaselineCapture(retainRecords bool) *accountingBaselineCapture {
	capture := &accountingBaselineCapture{}
	if retainRecords {
		capture.observedRecords = make(chan usageevidence.CallLegUsageRecord, 1)
		capture.legRecords = make(chan usageevidence.CallLegUsageRecord, 1)
		capture.callRecords = make(chan usageevidence.CallUsageRecord, 1)
	}
	return capture
}

func (c *accountingBaselineCapture) observeLeg(_ context.Context, record usageevidence.CallLegUsageRecord) {
	c.observedLegs.Add(1)
	if c.observedRecords != nil {
		c.observedRecords <- record
	}
}

func (c *accountingBaselineCapture) appendLeg(_ context.Context, record usageevidence.CallLegUsageRecord) error {
	c.appendedLegs.Add(1)
	if c.legRecords != nil {
		c.legRecords <- record
	}
	return nil
}

func (c *accountingBaselineCapture) appendCall(_ context.Context, record usageevidence.CallUsageRecord) error {
	c.appendedCalls.Add(1)
	if c.callRecords != nil {
		c.callRecords <- record
	}
	return nil
}

func accountingBaselineExecutor(tb testing.TB, enabled bool, capture *accountingBaselineCapture) *Executor {
	tb.Helper()
	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		tb.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(17)
	ex.Backends = map[string]execbackend.Backend{
		"accounting-backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				capture.backendOpens.Add(1)
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventTextDelta, Delta: "accounting baseline"},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}

	if !enabled {
		// TestExecutor installs a no-op sink by default. Removing it is the
		// explicit accounting-disabled configuration: no observer, credit gate,
		// exposure admission, or terminal sink is reachable from this executor.
		ex.BillingLegObserver = nil
		ex.TerminalUsageSink = nil
		return ex
	}

	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error {
		capture.creditChecks.Add(1)
		return nil
	})
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		capture.exposureAdmits.Add(1)
		return billingport.CallExposure{
			AccountID:       "acct",
			CallID:          in.CallID,
			PricingRef:      usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"},
			Status:          billingport.ExposureOpen,
		}, nil
	})
	ex.BillingLegObserver = BillingLegObserverFunc(capture.observeLeg)
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg:  capture.appendLeg,
		appendCall: capture.appendCall,
	}
	return ex
}

func accountingBaselineCall() *aipapi.Call {
	return &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "accounting-backend:model"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("accounting baseline")},
		}},
	}
}

func receiveAccountingBaselineLeg(t *testing.T, capture *accountingBaselineCapture) usageevidence.CallLegUsageRecord {
	t.Helper()
	select {
	case record := <-capture.legRecords:
		return record
	case <-time.After(time.Second):
		t.Fatal("enabled accounting run did not append a terminal B-leg")
		return usageevidence.CallLegUsageRecord{}
	}
}

func receiveAccountingBaselineObservedLeg(t *testing.T, capture *accountingBaselineCapture) usageevidence.CallLegUsageRecord {
	t.Helper()
	select {
	case record := <-capture.observedRecords:
		return record
	case <-time.After(time.Second):
		t.Fatal("enabled accounting run did not invoke the billing-leg observer")
		return usageevidence.CallLegUsageRecord{}
	}
}

func receiveAccountingBaselineCall(t *testing.T, capture *accountingBaselineCapture) usageevidence.CallUsageRecord {
	t.Helper()
	select {
	case record := <-capture.callRecords:
		return record
	case <-time.After(time.Second):
		t.Fatal("enabled accounting run did not append a terminal call closure")
		return usageevidence.CallUsageRecord{}
	}
}

// TestRefinementAccountingBaselineTerminalWrites characterizes the current V1
// terminal accounting boundary. Enabled billing reaches one observer callback,
// one B-leg append, and one call-closure append for one real execution. Disabled
// billing still executes the backend but performs no monetary callback/I/O.
func TestRefinementAccountingBaselineTerminalWrites(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		enabled bool
	}{
		{name: "Disabled", enabled: false},
		{name: "Enabled", enabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			capture := newAccountingBaselineCapture(tc.enabled)
			ex := accountingBaselineExecutor(t, tc.enabled, capture)
			stream, err := ex.Execute(context.Background(), accountingBaselineCall())
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			collected, err := aipapi.Collect(context.Background(), stream)
			if err != nil {
				t.Fatalf("collect: %v", err)
			}
			if got := collected.Text.String(); got != "accounting baseline" {
				t.Fatalf("collected text = %q, want %q", got, "accounting baseline")
			}
			if got := capture.backendOpens.Load(); got != 1 {
				t.Fatalf("backend opens = %d, want 1", got)
			}

			if !tc.enabled {
				if got := capture.creditChecks.Load(); got != 0 {
					t.Fatalf("disabled credit checks = %d, want 0", got)
				}
				if got := capture.exposureAdmits.Load(); got != 0 {
					t.Fatalf("disabled exposure admissions = %d, want 0", got)
				}
				if got := capture.observedLegs.Load(); got != 0 {
					t.Fatalf("disabled observed legs = %d, want 0", got)
				}
				if got := capture.appendedLegs.Load(); got != 0 {
					t.Fatalf("disabled terminal B-leg appends = %d, want 0", got)
				}
				if got := capture.appendedCalls.Load(); got != 0 {
					t.Fatalf("disabled terminal call appends = %d, want 0", got)
				}
				return
			}

			if got := capture.creditChecks.Load(); got != 1 {
				t.Fatalf("enabled credit checks = %d, want 1", got)
			}
			if got := capture.exposureAdmits.Load(); got != 1 {
				t.Fatalf("enabled exposure admissions = %d, want 1", got)
			}
			if got := capture.observedLegs.Load(); got != 1 {
				t.Fatalf("enabled observed legs = %d, want 1", got)
			}
			if got := capture.appendedLegs.Load(); got != 1 {
				t.Fatalf("enabled terminal B-leg appends = %d, want 1", got)
			}
			if got := capture.appendedCalls.Load(); got != 1 {
				t.Fatalf("enabled terminal call appends = %d, want 1", got)
			}

			observed := receiveAccountingBaselineObservedLeg(t, capture)
			leg := receiveAccountingBaselineLeg(t, capture)
			call := receiveAccountingBaselineCall(t, capture)
			if observed.BLegID == "" || observed.BLegID != leg.BLegID || observed.CallID != leg.CallID {
				t.Fatalf("observer/append B-leg identity differs: observed=%q/%q append=%q/%q", observed.CallID, observed.BLegID, leg.CallID, leg.BLegID)
			}
			if leg.CallID == "" || call.CallID == "" || leg.CallID != call.CallID {
				t.Fatalf("terminal identities differ: leg call %q, closure call %q", leg.CallID, call.CallID)
			}
			if leg.ALegID == "" || call.ALegID == "" || leg.ALegID != call.ALegID {
				t.Fatalf("terminal A-leg identities differ: leg %q, closure %q", leg.ALegID, call.ALegID)
			}
			if leg.BLegID == "" || leg.AttemptSeq <= 0 {
				t.Fatalf("terminal B-leg identity = %q, attempt sequence = %d", leg.BLegID, leg.AttemptSeq)
			}
			if len(call.ExpectedBLegIDs) != 1 || call.ExpectedBLegIDs[0] != leg.BLegID {
				t.Fatalf("closure expected B-legs = %v, want [%s]", call.ExpectedBLegIDs, leg.BLegID)
			}
			if leg.Evidence.Source != usageevidence.EvidenceSourceUnavailable || leg.Evidence.Authority != usageevidence.EvidenceAuthorityUnavailable {
				t.Fatalf("no-provider-usage evidence = source %q authority %q, want unavailable/unavailable", leg.Evidence.Source, leg.Evidence.Authority)
			}
		})
	}
}

// BenchmarkRefinementAccountingBaseline measures the current V1 Executor path
// with the monetary seams absent and present. The terminal metrics are derived
// from actual observer/sink callbacks and therefore also guard that each fixed
// iteration reached the expected terminal boundary.
func BenchmarkRefinementAccountingBaseline(b *testing.B) {
	for _, tc := range []struct {
		name    string
		enabled bool
	}{
		{name: "Disabled", enabled: false},
		{name: "Enabled", enabled: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			capture := newAccountingBaselineCapture(false)
			ex := accountingBaselineExecutor(b, tc.enabled, capture)
			ctx := b.Context()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				stream, err := ex.Execute(ctx, accountingBaselineCall())
				if err != nil {
					b.Fatalf("execute: %v", err)
				}
				if _, err := aipapi.Collect(ctx, stream); err != nil {
					b.Fatalf("collect: %v", err)
				}
			}
			b.StopTimer()

			iterations := int64(b.N)
			if got := capture.backendOpens.Load(); got != iterations {
				b.Fatalf("backend opens = %d, want %d", got, iterations)
			}
			wantTerminal := int64(0)
			if tc.enabled {
				wantTerminal = iterations
			}
			if got := capture.observedLegs.Load(); got != wantTerminal {
				b.Fatalf("observed terminal legs = %d, want %d", got, wantTerminal)
			}
			if got := capture.appendedLegs.Load(); got != wantTerminal {
				b.Fatalf("appended terminal B-legs = %d, want %d", got, wantTerminal)
			}
			if got := capture.appendedCalls.Load(); got != wantTerminal {
				b.Fatalf("appended terminal calls = %d, want %d", got, wantTerminal)
			}
			if iterations > 0 {
				b.ReportMetric(float64(capture.observedLegs.Load())/float64(iterations), "terminal-observed-legs/op")
				b.ReportMetric(float64(capture.appendedLegs.Load())/float64(iterations), "terminal-leg-appends/op")
				b.ReportMetric(float64(capture.appendedCalls.Load())/float64(iterations), "terminal-call-appends/op")
			}
		})
	}
}
