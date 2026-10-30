package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aiproxer/aiproxer/internal/core/authoritycoord"
	"github.com/aiproxer/aiproxer/internal/core/billingport"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/authority"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
)

type wireParityRecordingProvider struct {
	id           string
	mu           sync.Mutex
	admitCalls   int
	settleCalls  int
	releaseCalls int
	lastFacts    []metering.Fact
	lastHandles  []string
}

func (p *wireParityRecordingProvider) AdmitRequest(_ context.Context, in authority.RequestAdmission) (authority.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.admitCalls++
	return authority.Decision{
		Kind: authority.DecisionAllow,
		Reservations: []authority.Reservation{{
			Handle: p.id + "-reservation",
			Kind:   authority.ReservationQuota,
			Quantity: &metering.Quantity{
				Component: metering.ComponentInputToken,
				Unit:      metering.UnitToken,
				Value:     10,
				Present:   true,
			},
		}},
	}, nil
}

func (p *wireParityRecordingProvider) SettleRequest(_ context.Context, in authority.RequestSettlement) (authority.Settlement, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.settleCalls++
	p.lastFacts = append([]metering.Fact(nil), in.Facts...)
	p.lastHandles = append([]string(nil), in.Handles...)
	return authority.OwnedFinalSettlement(in.Handles), nil
}

func (p *wireParityRecordingProvider) ReleaseRequest(_ context.Context, in authority.RequestRelease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.releaseCalls++
	return nil
}

func (p *wireParityRecordingProvider) Counts() (admit, settle, release int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.admitCalls, p.settleCalls, p.releaseCalls
}

func (p *wireParityRecordingProvider) Facts() []metering.Fact {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]metering.Fact(nil), p.lastFacts...)
}

func (p *wireParityRecordingProvider) Handles() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.lastHandles...)
}

type stubExposureAdmission struct {
	exposure billingport.CallExposure
	err      error
}

func (s stubExposureAdmission) Admit(ctx context.Context, in BillingExposureAdmissionInput) (billingport.CallExposure, error) {
	if s.err != nil {
		return billingport.CallExposure{}, s.err
	}
	if s.exposure.AccountID != "" {
		return s.exposure, nil
	}
	return billingport.CallExposure{AccountID: in.AccountID}, nil
}

// TestWireRequestAuthority_SettlementParity_Differential is the reviewer-ordered differential test
// verifying parity between canonical Execute and accepted-wire ExecuteLargeBody:
// 1. Both paths admit request-level authority once.
// 2. Both paths run through committed output and EventResponseFinished.
// 3. On successful completion, both paths settle request authority exactly once with frontend-egress facts.
// 4. Neither path releases the reservation instead of settling (pre-fix wire bug).
// 5. Both paths carry complete bounded economic identity and append terminal call closure.
func TestWireRequestAuthority_SettlementParity_Differential(t *testing.T) {
	testScript := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hello differential authority parity"},
		{Kind: aipapi.EventUsageDelta, InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		{Kind: aipapi.EventResponseFinished},
	}

	pricingRef := usageevidence.VersionRef{ID: "price-parity-v1", Version: "1"}
	chargePolicyRef := usageevidence.VersionRef{ID: "policy-parity-v1", Version: "1"}

	// 1. Canonical path execution
	exCanonical, _, _ := setupTestExecutor(t)
	provCanonical := &wireParityRecordingProvider{id: "coord-canonical"}
	recCanonical := &recordingMeter{}
	exCanonical.MeteringRecorder = recCanonical
	exCanonical.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: provCanonical, Strength: authority.StrengthRequired,
		}},
	}
	exCanonical.BillingExposureAdmission = stubExposureAdmission{
		exposure: billingport.CallExposure{
			AccountID:       "acct-parity",
			PricingRef:      pricingRef,
			ChargePolicyRef: chargePolicyRef,
		},
	}
	var canonicalCalls []usageevidence.CallUsageRecord
	var cCallsMu sync.Mutex
	exCanonical.TerminalUsageSink = testTerminalSink{
		appendCall: func(ctx context.Context, record usageevidence.CallUsageRecord) error {
			cCallsMu.Lock()
			canonicalCalls = append(canonicalCalls, record)
			cCallsMu.Unlock()
			return nil
		},
	}
	exCanonical.Backends = map[string]execbackend.Backend{
		"default": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			Open: func(ctx context.Context, call aipapi.Call, cand routing.AttemptCandidate) (aipapi.ManagedEventStream, error) {
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(testScript)}, nil
			},
		},
	}

	principal := execview.PrincipalView{ID: "usr-parity-test"}
	canonicalCtx := execview.WithPrincipal(context.Background(), principal)
	canonicalCall := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: "default:gpt-4o"},
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("hello parity test")},
		}},
	}

	cStream, err := exCanonical.Execute(canonicalCtx, canonicalCall)
	require.NoError(t, err)
	defer func() { _ = cStream.Close() }()

	var canonicalEvents []aipapi.Event
	for {
		ev, rerr := cStream.Recv(canonicalCtx)
		if rerr != nil {
			break
		}
		canonicalEvents = append(canonicalEvents, ev)
	}
	require.NoError(t, cStream.Close())
	require.NotEmpty(t, canonicalEvents, "canonical path must produce events")

	cAdmit, cSettle, cRelease := provCanonical.Counts()
	require.Equal(t, 1, cAdmit, "canonical must admit exactly once")
	require.Equal(t, 1, cSettle, "canonical must settle exactly once")
	require.Equal(t, 0, cRelease, "canonical must not release on success")
	cFacts := provCanonical.Facts()
	require.Len(t, cFacts, 1, "canonical must pass frontend-egress fact into settlement")
	require.Equal(t, metering.BoundaryFrontendEgress, cFacts[0].Boundary)
	require.Equal(t, metering.PerspectiveCustomer, cFacts[0].Perspective)
	cHandles := provCanonical.Handles()
	require.NotEmpty(t, cHandles, "canonical must settle with reservation handles")

	// 2. Wire path execution
	exWire, _, _ := setupTestExecutor(t)
	provWire := &wireParityRecordingProvider{id: "coord-wire"}
	recWire := &recordingMeter{}
	exWire.MeteringRecorder = recWire
	exWire.RequestCoordinator = &authoritycoord.RequestCoordinator{
		Slots: []authoritycoord.RequestSlot{{
			ID: "quota", Class: authoritycoord.PriorityQuotaBudgetRate, Provider: provWire, Strength: authority.StrengthRequired,
		}},
	}
	exWire.BillingExposureAdmission = stubExposureAdmission{
		exposure: billingport.CallExposure{
			AccountID:       "acct-parity",
			PricingRef:      pricingRef,
			ChargePolicyRef: chargePolicyRef,
		},
	}
	var wireCalls []usageevidence.CallUsageRecord
	var wCallsMu sync.Mutex
	exWire.TerminalUsageSink = testTerminalSink{
		appendCall: func(ctx context.Context, record usageevidence.CallUsageRecord) error {
			wCallsMu.Lock()
			wireCalls = append(wireCalls, record)
			wCallsMu.Unlock()
			return nil
		},
	}
	exWire.Backends = map[string]execbackend.Backend{
		"default": {
			Caps: aipapi.NewBackendCaps(aipapi.CapabilityStreaming),
			OpenWire: func(ctx context.Context, req largebody.WireOpenRequest) (aipapi.ManagedEventStream, error) {
				return aipapi.CloseOnlyManagedStream{Stream: aipapi.NewFixedEventStream(testScript)}, nil
			},
		},
	}

	rawJSON := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello parity test"}]}`
	src := newTestSource(rawJSON)
	acc := makeTestAcceptedAssessment(t, "gen-1", "openai-chat", src, true)
	acc.WireRequest.CandidateModel = "default:gpt-4o"

	wireCtx := execview.WithPrincipal(context.Background(), principal)
	wireCtx = largebody.WithWireIdentity(wireCtx, "req-wire-parity", "trace-wire-parity")

	res, err := exWire.ExecuteLargeBody(wireCtx, acc, src)
	require.NoError(t, err)
	defer func() { _ = res.Stream.Close() }()

	var wireEvents []aipapi.Event
	for {
		ev, rerr := res.Stream.Recv(wireCtx)
		if rerr != nil {
			break
		}
		wireEvents = append(wireEvents, ev)
	}
	require.NoError(t, res.Stream.Close())
	require.NotEmpty(t, wireEvents, "wire path must produce events")

	// Differential assertions:
	wAdmit, wSettle, wRelease := provWire.Counts()
	require.Equal(t, cAdmit, wAdmit, "wire and canonical must admit identical count")
	require.Equal(t, cSettle, wSettle, "wire must settle exactly once (parity with canonical; pre-fix settlement was skipped)")
	require.Equal(t, cRelease, wRelease, "wire must not release reservation on successful completion (pre-fix released instead of settled)")

	wFacts := provWire.Facts()
	require.Len(t, wFacts, len(cFacts), "wire must pass identical number of settlement facts as canonical")
	assert.Equal(t, cFacts[0].Boundary, wFacts[0].Boundary, "frontend-egress boundary parity")
	assert.Equal(t, cFacts[0].Perspective, wFacts[0].Perspective, "frontend-egress perspective parity")
	assert.Equal(t, cFacts[0].Lifecycle, wFacts[0].Lifecycle, "frontend-egress lifecycle parity")
	assert.Equal(t, cFacts[0].Quantities, wFacts[0].Quantities, "settlement fact quantities parity")
	// Handles are coordinator-scoped (coord-canonical-* vs coord-wire-*), so parity is
	// same cardinality and non-empty — i.e. wire settled WITH its reservation handle.
	wHandles := provWire.Handles()
	require.Len(t, wHandles, len(cHandles), "wire must settle with identical handle count as canonical")
	require.NotEmpty(t, wHandles, "wire must settle with reservation handles")

	// Economic identity & terminal call closure assertions:
	cCallsMu.Lock()
	require.Len(t, canonicalCalls, 1, "canonical path must produce exactly one call-level billing closure")
	cCall := canonicalCalls[0]
	cCallsMu.Unlock()

	wCallsMu.Lock()
	require.Len(t, wireCalls, 1, "wire path must produce exactly one call-level billing closure (pre-fix missing identityStamped caused drop)")
	wCall := wireCalls[0]
	wCallsMu.Unlock()

	assert.Equal(t, cCall.AccountID, wCall.AccountID, "call closure account parity")
	assert.Equal(t, cCall.CustomerPricingRef, wCall.CustomerPricingRef, "call closure pricing ref parity")
	assert.Equal(t, cCall.ChargePolicyRef, wCall.ChargePolicyRef, "call closure charge policy parity")
	assert.NotEmpty(t, wCall.CallID, "wire call ID must not be empty")
}
