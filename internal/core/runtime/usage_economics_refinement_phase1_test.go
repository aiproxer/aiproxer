package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
)

// TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED is a behavioral
// boundary regression for the current V1 bridge. A finalizer event and a
// stream event are independent evidence sources; copying the stream cost into
// the finalizer record mixes those sources and loses the source-qualified
// distinction required by the refinement. The current single-evidence V1
// record cannot preserve both values yet, so this test remains intentionally
// RED until the parent V2 observation contract supplies that representation.
func TestRefinementFinalEvidenceDoesNotMergeDifferentSources_RED(t *testing.T) {
	t.Parallel()

	finalizer := aipapi.Event{
		Kind: aipapi.EventUsageDelta,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceProviderReported,
			Authority: aipapi.UsageAuthorityAuthoritative,
		},
	}
	stream := aipapi.Event{
		Kind:          aipapi.EventUsageDelta,
		CostNanoUnits: 42,
		Currency:      "USD",
		CostPresent:   true,
		Accounting: aipapi.UsageAccountingMetadata{
			Source:    aipapi.UsageSourceLocalEstimator,
			Authority: aipapi.UsageAuthorityEstimated,
		},
	}

	record := billingLegRecord(billingLegDraft{
		callID:     usageevidence.BillingCallID("bc_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		aLegID:     "a-phase1",
		bLegID:     "b-phase1",
		seq:        1,
		primary:    routing.Primary{Backend: "backend", Model: "model"},
		startedAt:  time.Unix(100, 0).UTC(),
		finishedAt: time.Unix(101, 0).UTC(),
		command:    sdkterminal.CommandNormalFinish,
		finalize:   finalizer,
		stream:     stream,
	})

	if record.Evidence.Cost.Present {
		t.Fatalf("runtime terminal bridge merged stream cost into finalizer evidence: %+v; V2 must retain independent source observations", record.Evidence)
	}
}

// TestRefinementContinuationAfterDoneUsesFreshCallState is a production-backed
// characterization for refinement requirement 3.5. It executes and collects
// two terminal invocations, then verifies that a resumed invocation reuses the
// A-leg while receiving a new BillingCallID and independent B-leg lineage.
func TestRefinementContinuationAfterDoneUsesFreshCallState(t *testing.T) {
	t.Parallel()

	store, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ex := TestExecutor()
	ex.Store = store
	ex.Bus = hooks.New(hooks.Config{})
	ex.Rand = routing.NewSeededRng(5)
	ex.BillingIdentity = testBillingIdentity()
	ex.BillingCreditGate = creditGateFunc(func(context.Context, string) error { return nil })
	ex.BillingExposureAdmission = exposureAdmissionFunc(func(_ context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
		return billingport.CallExposure{
			AccountID:       "acct",
			CallID:          in.CallID,
			PricingRef:      usageevidence.VersionRef{ID: "pricing:test", Version: "1"},
			ChargePolicyRef: usageevidence.VersionRef{ID: "policy:test", Version: "1"},
			Status:          billingport.ExposureOpen,
		}, nil
	})
	legs := make(chan usageevidence.CallLegUsageRecord, 2)
	calls := make(chan usageevidence.CallUsageRecord, 2)
	ex.TerminalUsageSink = testTerminalSink{
		appendLeg: func(_ context.Context, record usageevidence.CallLegUsageRecord) error {
			legs <- record
			return nil
		},
		appendCall: func(_ context.Context, record usageevidence.CallUsageRecord) error {
			calls <- record
			return nil
		},
	}
	ex.Backends = map[string]execbackend.Backend{
		"backend": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(context.Context, aipapi.Call, routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.NewFixedEventStream([]aipapi.Event{
					{Kind: aipapi.EventResponseStarted},
					{Kind: aipapi.EventMessageStarted},
					{Kind: aipapi.EventResponseFinished},
				}), nil
			},
		},
	}
	newCall := func(text string) *aipapi.Call {
		return &aipapi.Call{
			Session: aipapi.SessionRef{AuthoritativeSessionID: "sess-phase1", ContinuityKey: "sess-phase1"},
			Route:   aipapi.RouteIntent{Selector: "backend:model"},
			Messages: []aipapi.Message{{
				Role:  aipapi.RoleUser,
				Parts: []aipapi.Part{aipapi.TextPart(text)},
			}},
		}
	}
	receiveLeg := func(label string) usageevidence.CallLegUsageRecord {
		t.Helper()
		select {
		case record := <-legs:
			return record
		case <-time.After(time.Second):
			t.Fatalf("%s terminal B-leg was not appended", label)
			return usageevidence.CallLegUsageRecord{}
		}
	}
	receiveCall := func(label string) usageevidence.CallUsageRecord {
		t.Helper()
		select {
		case record := <-calls:
			return record
		case <-time.After(time.Second):
			t.Fatalf("%s call closure was not appended after terminal DONE", label)
			return usageevidence.CallUsageRecord{}
		}
	}

	firstCall := newCall("first")
	firstStream, err := ex.Execute(context.Background(), firstCall)
	if err != nil {
		t.Fatal(err)
	}
	firstID, ok := billingCallIDFromStream(firstStream)
	if !ok {
		t.Fatal("first invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), firstStream); err != nil {
		t.Fatalf("collect first invocation: %v", err)
	}
	firstLeg := receiveLeg("first")
	firstClosure := receiveCall("first")
	firstALeg := firstCall.Session.ALegID
	if firstALeg == "" {
		t.Fatal("first invocation did not establish an A-leg")
	}
	if firstCall.Session.ResumeToken == "" || firstCall.Session.AuthoritativeSessionID == "" {
		t.Fatal("first invocation did not expose continuation identity")
	}

	secondCall := newCall("second")
	secondCall.Session = aipapi.SessionRef{
		AuthoritativeSessionID: firstCall.Session.AuthoritativeSessionID,
		ContinuityKey:          firstCall.Session.ContinuityKey,
		ResumeToken:            firstCall.Session.ResumeToken,
		ALegID:                 firstALeg,
	}
	secondStream, err := ex.Execute(context.Background(), secondCall)
	if err != nil {
		t.Fatal(err)
	}
	secondID, ok := billingCallIDFromStream(secondStream)
	if !ok {
		t.Fatal("resumed invocation missing BillingCallID")
	}
	if _, err := aipapi.Collect(context.Background(), secondStream); err != nil {
		t.Fatalf("collect resumed invocation: %v", err)
	}
	secondLeg := receiveLeg("resumed")
	secondClosure := receiveCall("resumed")

	if secondCall.Session.ALegID != firstALeg {
		t.Fatalf("resumed A-leg = %q, want same A-leg %q", secondCall.Session.ALegID, firstALeg)
	}
	if firstID == secondID {
		t.Fatalf("resumed invocation reused BillingCallID %q", firstID)
	}
	if firstLeg.CallID != firstID || secondLeg.CallID != secondID {
		t.Fatalf("terminal B-leg call IDs = %q/%q, want %q/%q", firstLeg.CallID, secondLeg.CallID, firstID, secondID)
	}
	if firstClosure.CallID != firstID || secondClosure.CallID != secondID {
		t.Fatalf("terminal call-closure IDs = %q/%q, want %q/%q", firstClosure.CallID, secondClosure.CallID, firstID, secondID)
	}
	if firstLeg.ALegID != firstALeg || secondLeg.ALegID != firstALeg {
		t.Fatalf("terminal B-leg A-leg IDs = %q/%q, want %q", firstLeg.ALegID, secondLeg.ALegID, firstALeg)
	}
	if firstClosure.ALegID != firstALeg || secondClosure.ALegID != firstALeg {
		t.Fatalf("terminal call-closure A-leg IDs = %q/%q, want %q", firstClosure.ALegID, secondClosure.ALegID, firstALeg)
	}
	if firstLeg.BLegID == "" || secondLeg.BLegID == "" || firstLeg.BLegID == secondLeg.BLegID {
		t.Fatalf("resumed invocations must allocate independent B-legs: %q/%q", firstLeg.BLegID, secondLeg.BLegID)
	}
	if firstLeg.AttemptSeq <= 0 || secondLeg.AttemptSeq <= 0 {
		t.Fatalf("terminal B-leg attempt sequences = %d/%d, want positive persisted sequences", firstLeg.AttemptSeq, secondLeg.AttemptSeq)
	}
	if len(firstClosure.ExpectedBLegIDs) != 1 || firstClosure.ExpectedBLegIDs[0] != firstLeg.BLegID {
		t.Fatalf("first DONE closure B-legs = %v, want [%s]", firstClosure.ExpectedBLegIDs, firstLeg.BLegID)
	}
	if len(secondClosure.ExpectedBLegIDs) != 1 || secondClosure.ExpectedBLegIDs[0] != secondLeg.BLegID {
		t.Fatalf("resumed DONE closure B-legs = %v, want [%s]", secondClosure.ExpectedBLegIDs, secondLeg.BLegID)
	}
}
