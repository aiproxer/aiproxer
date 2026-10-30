package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/modelcatalog"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	accountingapp "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	accountingpreflight "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/preflight"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// TestWireComposition_NoAccounting_ReachesWireModeAndCompletesLifecycle proves
// that with token accounting and preflight disabled, a standard production composition
// (SecureSession + SessionRecorder + MeteringRecorder + B2BUA Store) reaches wire mode
// and completes the full wire lifecycle across FE ingress, BE attempt, failover attempt,
// and turn recording with zero Call retention (Requirements 15.1–15.4, 21.6; Task 10.3).
func TestWireComposition_NoAccounting_ReachesWireModeAndCompletesLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715624000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})

	factRecorder := &memoryFactRecorder{}
	turnRecorder := &capturingTurnRecorder{}

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionRecorder = turnRecorder
	ex.MeteringRecorder = factRecorder
	ex.Now = func() time.Time { return now }

	// Ensure token accounting / preflight is disabled (no accounting ports occupied)
	ex.Preflight = nil
	ex.AdminCountService = nil
	ex.StreamUsage = nil

	// Step 1: Wire Eligibility Compilation Proof (Req 15.4, 21.6)
	// Build generation-pinned eligibility with disabled accounting ports.
	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			t.Fatalf("missing plane ID for index %d", i)
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}

	summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
		GenerationID: "gen-no-accounting-v1",
		Planes:       planes,
		Hooks:        largebody.HookEligibilityInput{},
		Ports: largebody.NarrowPortEligibilityInput{
			BackendsEmpty:             false,
			PreflightEnabled:          false,
			PreflightHasExactCounter:  false,
			AdminCountServiceOccupied: false,
			StreamUsageOccupied:       false,
			TokenCountingRequired:     false,
		},
		TwoPhaseExecutorAvailable: true,
	}, 1024)
	if err != nil {
		t.Fatalf("CompileWireEligibilitySummary failed: %v", err)
	}

	if summary.HasStaticBlocker() {
		t.Fatalf("expected no static blocker with disabled accounting, got blockers: plane=%#x hook=%#x port=%#x",
			summary.PlaneBlockers(), uint8(summary.HookBlockers()), uint32(summary.PortBlockers()))
	}

	// Static disposition must reach wire assessment mode
	disposition, reason := largebody.StaticDisposition(summary, largebody.StaticDispositionInput{
		FeatureEnabled: true,
		GenerationID:   "gen-no-accounting-v1",
		ThresholdBytes: 64 * 1024,
		HasKnownLength: true,
		ContentLength:  128 * 1024,
	})
	if disposition != largebody.StaticWireNeedsRequestAssessment {
		t.Fatalf("expected NeedsRequestAssessment, got %v (reason: %v)", disposition, reason)
	}
	if reason != largebody.StaticWireReasonNone {
		t.Fatalf("expected ReasonNone, got %v", reason)
	}

	// Step 2: Prepare Secure Session & Turn Execution (9.x seam)
	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("user-comp-001"),
		TenantID:    scope.Known("tenant-comp-001"),
	}
	baseCtx := execview.WithFrontendID(context.Background(), "openai_responses")
	baseCtx = execview.WithPrincipal(baseCtx, execview.PrincipalView{ID: "user-comp-001"})
	baseCtx = scope.WithScope(baseCtx, sc)

	traceID := "trace-comp-001"
	requestID := "req-comp-001"
	maxOutput := 4096

	prep, err := ex.PrepareSecureSession(baseCtx, SecureSessionPrepInput{
		TraceID: traceID,
		Session: largebody.SessionInput{
			ClientSessionID:     "client-sess-comp-001",
			NewSessionRequested: true,
		},
		ContinuityKey: "ck-comp-001",
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}

	br, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}
	aLeg, _, err := prep.ResolveALeg(prep.Context(), br.Record.ALegID)
	if err != nil {
		t.Fatalf("ResolveALeg failed: %v", err)
	}

	// Record client turn using bounded ClientTurnShape without prompt text (Req 14.3, 14.5)
	shape := largebody.ClientTurnShape{
		Items: []largebody.ClientTurnItemShape{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    "system",
				Ordinal: 0,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: aipapi.ContentPartText, ContentBytes: 50},
				},
			},
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    "user",
				Ordinal: 1,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: aipapi.ContentPartText, ContentBytes: 200},
					{Kind: aipapi.ContentPartImageRef, ContentBytes: 150},
				},
			},
		},
	}
	if err := prep.RecordClientTurnWithShape(prep.Context(), br, shape, 4096); err != nil {
		t.Fatalf("RecordClientTurnWithShape failed: %v", err)
	}
	if len(turnRecorder.recorded) != 1 {
		t.Fatalf("recorded turns len=%d want 1", len(turnRecorder.recorded))
	}
	recTurn := turnRecorder.recorded[0]
	if recTurn.TraceID != traceID || len(recTurn.Lines) != 2 {
		t.Fatalf("recorded turn lines mismatch: %+v", recTurn)
	}

	// Step 3: Wire Frontend Ingress Checkpoint (10.1 seam)
	outCtx, holder, err := prep.CaptureFrontendIngressCheckpoint(
		prep.Context(),
		requestID,
		br,
		aLeg,
		&maxOutput,
	)
	if err != nil {
		t.Fatalf("CaptureFrontendIngressCheckpoint failed: %v", err)
	}
	if holder == nil || holder.FrontendIngress == nil {
		t.Fatal("expected non-nil RequestHolder and FrontendIngress")
	}
	if !holder.FrontendIngress.IsWire() {
		t.Fatal("holder.FrontendIngress must be wire mode")
	}
	if !reflect.DeepEqual(holder.FrontendIngress.Call, aipapi.Call{}) {
		t.Fatalf("wire FrontendIngress Call must be empty, got %+v", holder.FrontendIngress.Call)
	}

	// Verify quantities contain Request=1, MaxOutput=4096, and NO input tokens
	feQuantities := holder.FrontendIngress.Public.Quantities
	reqVal, ok := checkpoint.QuantityComponentValue(feQuantities, metering.ComponentRequest)
	if !ok || reqVal != 1 {
		t.Fatalf("ComponentRequest=%d (ok=%v) want 1", reqVal, ok)
	}
	outVal, ok := checkpoint.QuantityComponentValue(feQuantities, metering.ComponentOutputToken)
	if !ok || outVal != int64(maxOutput) {
		t.Fatalf("ComponentOutputToken=%d (ok=%v) want %d", outVal, ok, maxOutput)
	}
	if _, ok := checkpoint.QuantityComponentValue(feQuantities, metering.ComponentInputToken); ok {
		t.Fatal("wire FE ingress must not contain input tokens before tokenization/counting")
	}

	// Persist FE ingress fact using exported helper
	feFactID, err := ex.persistFrontendIngressFact(outCtx, holder)
	if err != nil {
		t.Fatalf("persistFrontendIngressFact failed: %v", err)
	}
	if feFactID != "fe-ingress:"+requestID {
		t.Fatalf("feFactID=%q want %q", feFactID, "fe-ingress:"+requestID)
	}
	// Verify idempotency (no duplicate facts appended on second call)
	feFactID2, err := ex.persistFrontendIngressFact(outCtx, holder)
	if err != nil || feFactID2 != feFactID {
		t.Fatalf("second persistFrontendIngressFact failed or diverged: id=%q err=%v", feFactID2, err)
	}
	if len(factRecorder.facts) != 1 {
		t.Fatalf("factRecorder len=%d want 1", len(factRecorder.facts))
	}

	// Enrichment on wire snapshot must be a clean no-op
	if err := ex.enrichFrontendIngressQuantities(outCtx); err != nil {
		t.Fatalf("enrichFrontendIngressQuantities must not error on wire snapshot: %v", err)
	}

	// Step 4: Wire Backend Attempt Checkpoint (10.2 seam)
	attempt1ID := "att-comp-001"
	bLeg1ID := "bleg-comp-001"
	sourceDigest := sha256.Sum256([]byte("wire-payload-bytes"))
	rewriteDigest := checkpoint.ComputeRewriteDigest(128, 10, `"gpt-4o"`)
	attempt1Digest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "gpt-4o")

	beSnap1, err := ex.captureWireBackendIngress(outCtx, holder, WireBackendIngressArgs{
		AttemptID:       attempt1ID,
		BLegID:          bLeg1ID,
		BackendID:       "openai_primary",
		Model:           "gpt-4o",
		MaxOutputTokens: &maxOutput,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attempt1Digest,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("captureWireBackendIngress attempt 1 failed: %v", err)
	}
	if !beSnap1.IsWire() {
		t.Fatal("beSnap1 must be wire mode")
	}
	if !reflect.DeepEqual(beSnap1.Call, aipapi.Call{}) {
		t.Fatalf("wire BackendIngress Call must be empty, got %+v", beSnap1.Call)
	}
	if beSnap1.Public.Correlation.RequestID != requestID || beSnap1.Public.Correlation.ALegID != aLeg.ALegID {
		t.Fatalf("beSnap1 correlation mismatch: %+v", beSnap1.Public.Correlation)
	}

	// Persist BE ingress fact using exported helper
	beFact1ID, err := ex.persistBackendIngressFact(outCtx, holder, attempt1ID)
	if err != nil {
		t.Fatalf("persistBackendIngressFact attempt 1 failed: %v", err)
	}
	if beFact1ID != "be-ingress:"+attempt1ID {
		t.Fatalf("beFact1ID=%q want %q", beFact1ID, "be-ingress:"+attempt1ID)
	}
	if len(factRecorder.facts) != 2 {
		t.Fatalf("factRecorder len=%d want 2 (FE + BE1)", len(factRecorder.facts))
	}

	// Verify BE enrichment safely ignores wire snapshots
	ex.enrichBackendIngressQuantities(holder, attempt1ID, accountingapp.CountResult{InputTokens: 999})
	ex.enrichBackendIngressQuantitiesWithDecision(holder, attempt1ID, accountingpreflight.Decision{
		Count: accountingapp.CountResult{InputTokens: 888},
	})
	beSnapCheck := holder.BackendIngressFor(attempt1ID)
	if _, ok := checkpoint.QuantityComponentValue(beSnapCheck.Public.Quantities, metering.ComponentInputToken); ok {
		t.Fatal("enrichBackendIngressQuantities must not inject unmeasured input tokens into wire snapshot")
	}

	// Verify widening check on attempt 1
	evidence1 := checkpoint.WireAttemptEvidence{
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attempt1Digest,
		Model:           "gpt-4o",
		MaxOutputTokens: &maxOutput,
	}
	if err := ex.assertWireAttemptNotWidened(holder, attempt1ID, evidence1); err != nil {
		t.Fatalf("assertWireAttemptNotWidened failed: %v", err)
	}

	// Step 5: Failover Second Attempt (10.2 failover correlation)
	attempt2ID := "att-comp-002"
	bLeg2ID := "bleg-comp-002"
	attempt2Digest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "gpt-4o-failover")

	beSnap2, err := ex.captureWireBackendIngress(outCtx, holder, WireBackendIngressArgs{
		AttemptID:       attempt2ID,
		BLegID:          bLeg2ID,
		BackendID:       "openai_secondary",
		Model:           "gpt-4o-failover",
		MaxOutputTokens: &maxOutput,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attempt2Digest,
		Now:             now.Add(200 * time.Millisecond),
	})
	if err != nil {
		t.Fatalf("captureWireBackendIngress attempt 2 failed: %v", err)
	}
	if beSnap2.Public.Correlation.ALegID != aLeg.ALegID || beSnap2.Public.Correlation.RequestID != requestID {
		t.Fatalf("beSnap2 correlation mismatch: %+v", beSnap2.Public.Correlation)
	}

	beFact2ID, err := ex.persistBackendIngressFact(outCtx, holder, attempt2ID)
	if err != nil {
		t.Fatalf("persistBackendIngressFact attempt 2 failed: %v", err)
	}
	if beFact2ID != "be-ingress:"+attempt2ID {
		t.Fatalf("beFact2ID=%q want %q", beFact2ID, "be-ingress:"+attempt2ID)
	}
	if len(factRecorder.facts) != 3 {
		t.Fatalf("factRecorder len=%d want 3 (FE + BE1 + BE2)", len(factRecorder.facts))
	}

	// Step 6: Response Carrier
	carrier := prep.ResponseCarrier(br)
	if carrier.AuthoritativeSessionID != string(br.Record.SessionID) {
		t.Fatalf("carrier AuthoritativeSessionID=%q want %q", carrier.AuthoritativeSessionID, br.Record.SessionID)
	}
	if carrier.ALegID != aLeg.ALegID {
		t.Fatalf("carrier ALegID=%q want %q", carrier.ALegID, aLeg.ALegID)
	}
	if carrier.ResumeToken.Reveal() != string(br.Response.ResumeToken) {
		t.Fatalf("carrier ResumeToken mismatch")
	}

	// Step 7: Zero Call retention check across all snapshots
	for _, snap := range []*checkpoint.Snapshot{holder.FrontendIngress, holder.BackendIngressFor(attempt1ID), holder.BackendIngressFor(attempt2ID)} {
		if snap == nil {
			t.Fatal("unexpected nil snapshot in holder")
		}
		if !snap.IsWire() {
			t.Fatal("all snapshots in holder must be wire mode")
		}
		if !reflect.DeepEqual(snap.Call, aipapi.Call{}) {
			t.Fatalf("snapshot Call must be empty, got %+v", snap.Call)
		}
	}
}

// TestWireComposition_AccountingEnabledWithoutWireCounter_BlocksWireMode proves
// that when accounting preflight or token counting is enabled without an exact wire
// counter, the composition correctly flags static blockers and declines to canonical
// mode, satisfying Requirements 15.5, 15.8, and 21.6.
func TestWireComposition_AccountingEnabledWithoutWireCounter_BlocksWireMode(t *testing.T) {
	t.Parallel()

	planes := make([]largebody.PlaneEligibilityInput, largebody.WireEligibilityPlaneCount)
	for i := range largebody.WireEligibilityPlaneCount {
		id, ok := largebody.WireEligibilityPlaneID(i)
		if !ok {
			t.Fatalf("missing plane ID for index %d", i)
		}
		planes[i] = largebody.PlaneEligibilityInput{
			ID:       id,
			Access:   largebody.PlaneAccessResponseOnly,
			Occupied: false,
		}
	}

	testCases := []struct {
		name          string
		ports         largebody.NarrowPortEligibilityInput
		expectBlocker largebody.WirePortBlocker
	}{
		{
			name: "preflight enabled count-only without exact counter blocks",
			ports: largebody.NarrowPortEligibilityInput{
				PreflightEnabled:         true,
				PreflightHasExactCounter: false,
			},
			expectBlocker: largebody.WirePortPreflightCountOnly,
		},
		{
			name: "admin count service occupied blocks",
			ports: largebody.NarrowPortEligibilityInput{
				AdminCountServiceOccupied: true,
			},
			expectBlocker: largebody.WirePortAdminCountService,
		},
		{
			name: "stream usage occupied blocks",
			ports: largebody.NarrowPortEligibilityInput{
				StreamUsageOccupied: true,
			},
			expectBlocker: largebody.WirePortStreamUsage,
		},
		{
			name: "token counting required without exact counter blocks",
			ports: largebody.NarrowPortEligibilityInput{
				TokenCountingRequired:        true,
				TokenCountingHasExactCounter: false,
			},
			expectBlocker: largebody.WirePortTokenCounting,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			summary, err := largebody.CompileWireEligibilitySummary(largebody.WireEligibilityInput{
				GenerationID:              "gen-accounting-blocked",
				Planes:                    planes,
				Hooks:                     largebody.HookEligibilityInput{},
				Ports:                     tc.ports,
				TwoPhaseExecutorAvailable: true,
			}, 1024)
			if err != nil {
				t.Fatalf("CompileWireEligibilitySummary failed: %v", err)
			}

			if !summary.HasStaticBlocker() {
				t.Fatal("expected summary to have static blocker")
			}
			if summary.PortBlockers()&tc.expectBlocker == 0 {
				t.Fatalf("expected port blocker %#x, got %#x", tc.expectBlocker, summary.PortBlockers())
			}

			disposition, reason := largebody.StaticDisposition(summary, largebody.StaticDispositionInput{
				FeatureEnabled: true,
				GenerationID:   "gen-accounting-blocked",
				ThresholdBytes: 64 * 1024,
				HasKnownLength: true,
				ContentLength:  128 * 1024,
			})
			if disposition != largebody.StaticWireDefinitelyCanonical {
				t.Fatalf("expected DefinitelyCanonical, got %v", disposition)
			}
			if reason != largebody.StaticWireReasonStaticBlocker {
				t.Fatalf("expected ReasonStaticBlocker, got %v", reason)
			}
		})
	}
}

// TestWireComposition_ContextFallbackAndWideningEdges verifies fallback of holder
// from context and strict rejection of unmeasured attempt widening (Req 10, 15.3).
func TestWireComposition_ContextFallbackAndWideningEdges(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715625000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})

	factRecorder := &memoryFactRecorder{}
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.MeteringRecorder = factRecorder
	ex.Now = func() time.Time { return now }

	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("user-edge-001"),
		TenantID:    scope.Known("tenant-edge-001"),
	}
	baseCtx := execview.WithFrontendID(context.Background(), "openai_responses")
	baseCtx = execview.WithPrincipal(baseCtx, execview.PrincipalView{ID: "user-edge-001"})
	baseCtx = scope.WithScope(baseCtx, sc)

	prep, err := ex.PrepareSecureSession(baseCtx, SecureSessionPrepInput{
		TraceID: "trace-edge-001",
		Session: largebody.SessionInput{ClientSessionID: "sess-edge-001", NewSessionRequested: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	br, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		t.Fatal(err)
	}
	aLeg, _, err := prep.ResolveALeg(prep.Context(), br.Record.ALegID)
	if err != nil {
		t.Fatal(err)
	}

	maxOutput := 1024
	outCtx, holder, err := prep.CaptureFrontendIngressCheckpoint(prep.Context(), "req-edge-001", br, aLeg, &maxOutput)
	if err != nil {
		t.Fatal(err)
	}

	// Passing holder=nil resolves from context in persistFrontendIngressFact
	factID, err := ex.persistFrontendIngressFact(outCtx, nil)
	if err != nil {
		t.Fatalf("persistFrontendIngressFact with context fallback failed: %v", err)
	}
	if factID != "fe-ingress:req-edge-001" {
		t.Fatalf("factID=%q want fe-ingress:req-edge-001", factID)
	}

	// Passing holder=nil resolves from context in captureWireBackendIngress
	sourceDigest := sha256.Sum256([]byte("edge-payload"))
	rewriteDigest := checkpoint.ComputeRewriteDigest(100, 5, `"model-a"`)
	attemptDigest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "model-a")

	beSnap, err := ex.captureWireBackendIngress(outCtx, nil, WireBackendIngressArgs{
		AttemptID:       "att-edge-001",
		BackendID:       "backend-a",
		Model:           "model-a",
		MaxOutputTokens: &maxOutput,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("captureWireBackendIngress with context fallback failed: %v", err)
	}
	if beSnap.Public.Correlation.RequestID != "req-edge-001" {
		t.Fatalf("inherited RequestID=%q want req-edge-001", beSnap.Public.Correlation.RequestID)
	}

	// Passing holder=nil resolves from context in persistBackendIngressFact
	beFactID, err := ex.persistBackendIngressFact(outCtx, nil, "att-edge-001")
	if err != nil {
		t.Fatalf("persistBackendIngressFact with context fallback failed: %v", err)
	}
	if beFactID != "be-ingress:att-edge-001" {
		t.Fatalf("beFactID=%q want be-ingress:att-edge-001", beFactID)
	}

	// Widening check: altered source digest must fail with ErrUnmeasuredWidening
	tamperedSource := sha256.Sum256([]byte("tampered-payload"))
	err = ex.assertWireAttemptNotWidened(holder, "att-edge-001", checkpoint.WireAttemptEvidence{
		SourceDigest:    tamperedSource,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
		Model:           "model-a",
		MaxOutputTokens: &maxOutput,
	})
	if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("expected ErrUnmeasuredWidening for tampered digest, got %v", err)
	}
}

type stubWireSource struct {
	data []byte
}

func (s *stubWireSource) Size() int64 {
	return int64(len(s.data))
}

func (s *stubWireSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.data)), nil
}

func (s *stubWireSource) Close() error {
	return nil
}

type stubCountCallOnly struct {
	calls int
}

func (s *stubCountCallOnly) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	s.calls++
	return accountingapp.CountResult{InputTokens: 10, TotalTokens: 10}, nil
}

type stubDualCounter struct {
	countRes            largebody.WireCountResult
	countErr            error
	waitForCancellation bool
	wireCalls           int
	callCalls           int
}

func (s *stubDualCounter) CountCall(ctx context.Context, in accountingapp.CountCallInput) (accountingapp.CountResult, error) {
	s.callCalls++
	return accountingapp.CountResult{InputTokens: s.countRes.InputTokens, TotalTokens: s.countRes.TotalTokens}, nil
}

func (s *stubDualCounter) CountWire(ctx context.Context, in largebody.WireCountInput) (largebody.WireCountResult, error) {
	s.wireCalls++
	if s.waitForCancellation {
		<-ctx.Done()
		return largebody.WireCountResult{}, ctx.Err()
	}
	if s.countErr != nil {
		return largebody.WireCountResult{}, s.countErr
	}
	return s.countRes, nil
}

// TestWireComposition_PreflightCountCallOnly_DynamicAssessmentDeclinesUnderSamePermit
// proves that when token accounting preflight requires tokens and only CountCall exists,
// dynamic assessment declines under the same permit with DeclineReasonCountingUnsupported,
// and does NOT call CountCall on a materialized or fake Call (Req 6.1–6.3, 15.5, 21; Task 10.4).
func TestWireComposition_PreflightCountCallOnly_DynamicAssessmentDeclinesUnderSamePermit(t *testing.T) {
	t.Parallel()

	counter := &stubCountCallOnly{}
	checker := accountingpreflight.NewChecker(counter, accountingpreflight.Config{
		Enabled:        true,
		MaxInputTokens: 1000,
	})

	ex := TestExecutor()
	ex.Preflight = checker

	src := &stubWireSource{data: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)}

	decision, declineReason, preflightDec := ex.assessWirePreflight(context.Background(), WirePreflightAssessmentArgs{
		Backend:          "openai",
		Model:            "gpt-4o",
		CallID:           "call-count-only-001",
		ProfileID:        "openai_responses",
		Source:           src,
		Semantics:        largebody.ExactTokenizerSemantics{TokenizerID: "tiktoken-cl100k", Exact: true},
		MaxScanBytes:     1024 * 1024,
		MaxPermitHoldCPU: 50 * time.Millisecond,
	})

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline, got %v", decision)
	}
	if declineReason != largebody.DeclineReasonCountingUnsupported {
		t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", declineReason)
	}
	if preflightDec.Allowed {
		t.Fatal("preflight decision must not be allowed when counting unsupported")
	}
	if preflightDec.Reason != accountingpreflight.ReasonCountUnavailable {
		t.Fatalf("expected ReasonCountUnavailable, got %v", preflightDec.Reason)
	}
	if counter.calls != 0 {
		t.Fatalf("CountCall must not be called during wire assessment, called %d times", counter.calls)
	}
}

// TestWireComposition_PreflightInexactTokenizerSemantics_DynamicAssessmentDeclinesUnderSamePermit
// proves that when a counter implements WireCounter but the profile lacks exact tokenizer
// semantics for the effective model, dynamic assessment declines under the same permit
// with DeclineReasonCountingUnsupported (Req 6.1–6.3, 15.5; Task 10.4).
func TestWireComposition_PreflightInexactTokenizerSemantics_DynamicAssessmentDeclinesUnderSamePermit(t *testing.T) {
	t.Parallel()

	dualCounter := &stubDualCounter{
		countRes: largebody.WireCountResult{InputTokens: 15, TotalTokens: 15},
	}

	checker := accountingpreflight.NewChecker(dualCounter, accountingpreflight.Config{
		Enabled:        true,
		MaxInputTokens: 1000,
	})

	ex := TestExecutor()
	ex.Preflight = checker

	src := &stubWireSource{data: []byte(`{"model":"gpt-4o-unknown","messages":[{"role":"user","content":"hello"}]}`)}

	// Semantics is inexact (Exact: false)
	semantics := largebody.ExactTokenizerSemantics{
		TokenizerID: "",
		Exact:       false,
	}

	decision, declineReason, preflightDec := ex.assessWirePreflight(context.Background(), WirePreflightAssessmentArgs{
		Backend:          "openai",
		Model:            "gpt-4o-unknown",
		CallID:           "call-inexact-001",
		ProfileID:        "openai_responses",
		Source:           src,
		Semantics:        semantics,
		MaxScanBytes:     1024 * 1024,
		MaxPermitHoldCPU: 50 * time.Millisecond,
	})

	if decision != largebody.AssessmentDecisionDecline {
		t.Fatalf("expected AssessmentDecisionDecline, got %v", decision)
	}
	if declineReason != largebody.DeclineReasonCountingUnsupported {
		t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", declineReason)
	}
	if preflightDec.Allowed {
		t.Fatal("preflight decision must not be allowed when tokenizer semantics are inexact")
	}
	if preflightDec.Reason != accountingpreflight.ReasonCountUnavailable {
		t.Fatalf("expected ReasonCountUnavailable, got %v", preflightDec.Reason)
	}
}

// TestWireComposition_PreflightUnboundedReplayOrCPUTimeout_DynamicAssessmentDeclinesUnderSamePermit
// proves that when wire counting is unbounded or exceeds permit-hold CPU budget,
// dynamic assessment declines under the same permit with DeclineReasonCountingUnsupported,
// leaving the composition canonical (Req 6.8, 15.5, 21.4; Task 10.4).
func TestWireComposition_PreflightUnboundedReplayOrCPUTimeout_DynamicAssessmentDeclinesUnderSamePermit(t *testing.T) {
	t.Parallel()

	t.Run("unbounded replay scan declines", func(t *testing.T) {
		dualCounter := &stubDualCounter{
			countRes: largebody.WireCountResult{InputTokens: 10, TotalTokens: 10},
		}

		checker := accountingpreflight.NewChecker(dualCounter, accountingpreflight.Config{
			Enabled:        true,
			MaxInputTokens: 1000,
		})

		ex := TestExecutor()
		ex.Preflight = checker

		// 100-byte source, but MaxScanBytes is 50
		src := &stubWireSource{data: make([]byte, 100)}

		decision, declineReason, preflightDec := ex.assessWirePreflight(context.Background(), WirePreflightAssessmentArgs{
			Backend:          "openai",
			Model:            "gpt-4o",
			CallID:           "call-unbounded-001",
			ProfileID:        "openai_responses",
			Source:           src,
			Semantics:        largebody.ExactTokenizerSemantics{TokenizerID: "tiktoken-cl100k", Exact: true},
			MaxScanBytes:     50,
			MaxPermitHoldCPU: 50 * time.Millisecond,
		})

		if decision != largebody.AssessmentDecisionDecline {
			t.Fatalf("expected AssessmentDecisionDecline, got %v", decision)
		}
		if declineReason != largebody.DeclineReasonCountingUnsupported {
			t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", declineReason)
		}
		if preflightDec.Allowed {
			t.Fatal("preflight decision must not be allowed when replay scan is unbounded")
		}
		if !errors.Is(preflightDec.Err, largebody.ErrUnboundedReplayScan) {
			t.Fatalf("expected ErrUnboundedReplayScan, got %v", preflightDec.Err)
		}
	})

	t.Run("permit hold CPU timeout declines", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		dualCounter := &stubDualCounter{
			waitForCancellation: true,
		}

		checker := accountingpreflight.NewChecker(dualCounter, accountingpreflight.Config{
			Enabled:        true,
			MaxInputTokens: 1000,
		})

		ex := TestExecutor()
		ex.Preflight = checker

		src := &stubWireSource{data: []byte(`{"model":"gpt-4o"}`)}

		decision, declineReason, preflightDec := ex.assessWirePreflight(ctx, WirePreflightAssessmentArgs{
			Backend:          "openai",
			Model:            "gpt-4o",
			CallID:           "call-timeout-001",
			ProfileID:        "openai_responses",
			Source:           src,
			Semantics:        largebody.ExactTokenizerSemantics{TokenizerID: "tiktoken-cl100k", Exact: true},
			MaxScanBytes:     1024 * 1024,
			MaxPermitHoldCPU: 2 * time.Millisecond, // very small CPU allowance
		})
		if ctx.Err() != nil {
			t.Fatal("counting must stop at the permit deadline before the parent safety deadline")
		}

		if decision != largebody.AssessmentDecisionDecline {
			t.Fatalf("expected AssessmentDecisionDecline, got %v", decision)
		}
		if declineReason != largebody.DeclineReasonCountingUnsupported {
			t.Fatalf("expected DeclineReasonCountingUnsupported, got %v", declineReason)
		}
		if preflightDec.Allowed {
			t.Fatal("preflight decision must not be allowed when counting times out")
		}
		if !errors.Is(preflightDec.Err, largebody.ErrPermitHoldCPUExceeded) {
			t.Fatalf("expected ErrPermitHoldCPUExceeded, got %v", preflightDec.Err)
		}
	})
}

// TestWireComposition_ExactWireCounter_ProceedsAndCompletesLifecycleWithMeasuredTokens
// proves that when a counter implements WireCounter and exact tokenizer semantics exist,
// dynamic preflight assessment accepts under the held permit with measured CPU,
// and the full wire lifecycle completes with measured input tokens merged into FE and BE
// snapshots and persisted journal facts, with zero Call retention (Req 6.1–6.3, 15.4, 15.5, 21.6; Task 10.4).
func TestWireComposition_ExactWireCounter_ProceedsAndCompletesLifecycleWithMeasuredTokens(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715626000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})

	factRecorder := &memoryFactRecorder{}
	turnRecorder := &capturingTurnRecorder{}

	countedTokens := 42
	dualCounter := &stubDualCounter{
		countRes: largebody.WireCountResult{
			InputTokens: countedTokens,
			TotalTokens: countedTokens,
		},
	}

	checker := accountingpreflight.NewChecker(dualCounter, accountingpreflight.Config{
		Enabled:        true,
		MaxInputTokens: 1000,
	})

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.SecureSessionRecorder = turnRecorder
	ex.MeteringRecorder = factRecorder
	ex.Preflight = checker
	ex.Now = func() time.Time { return now }

	srcPayload := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"exact wire token test"}]}`)
	src := &stubWireSource{data: srcPayload}

	// 1. Dynamic Assessment under held permit
	decision, declineReason, preflightDec := ex.assessWirePreflight(context.Background(), WirePreflightAssessmentArgs{
		Backend:                  "openai_primary",
		Model:                    "gpt-4o",
		CallID:                   "call-exact-001",
		ProfileID:                "openai_responses",
		Source:                   src,
		RequestedMaxOutputTokens: func(v int) *int { return &v }(2048),
		Facts: modelcatalog.ModelFacts{
			Source:       modelcatalog.FactSourceCatalog,
			MatchKind:    modelcatalog.MatchExact,
			ContextLimit: modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 8192},
			OutputLimit:  modelcatalog.LimitFact{State: modelcatalog.LimitPresent, Tokens: 4096},
		},
		Semantics:        largebody.ExactTokenizerSemantics{TokenizerID: "tiktoken-cl100k", Exact: true},
		MaxScanBytes:     1024 * 1024,
		MaxPermitHoldCPU: 50 * time.Millisecond,
	})

	if decision != largebody.AssessmentDecisionAccept {
		t.Fatalf("expected AssessmentDecisionAccept, got %v", decision)
	}
	if declineReason != largebody.DeclineReasonNone {
		t.Fatalf("expected DeclineReasonNone, got %v", declineReason)
	}
	if !preflightDec.Allowed {
		t.Fatalf("expected preflight allowed, got %v (err: %v)", preflightDec.Allowed, preflightDec.Err)
	}
	if preflightDec.Count.InputTokens != countedTokens {
		t.Fatalf("InputTokens=%d want %d", preflightDec.Count.InputTokens, countedTokens)
	}

	// 2. Prepare Secure Session & Turn Execution (post-commit)
	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("user-wire-001"),
		TenantID:    scope.Known("tenant-wire-001"),
	}
	baseCtx := execview.WithFrontendID(context.Background(), "openai_responses")
	baseCtx = execview.WithPrincipal(baseCtx, execview.PrincipalView{ID: "user-wire-001"})
	baseCtx = scope.WithScope(baseCtx, sc)

	traceID := "trace-wire-001"
	requestID := "req-wire-001"
	maxOutput := 2048

	prep, err := ex.PrepareSecureSession(baseCtx, SecureSessionPrepInput{
		TraceID: traceID,
		Session: largebody.SessionInput{
			ClientSessionID:     "client-sess-wire-001",
			NewSessionRequested: true,
		},
		ContinuityKey: "ck-wire-001",
	})
	if err != nil {
		t.Fatalf("PrepareSecureSession failed: %v", err)
	}

	br, err := prep.ExecuteBeginTurn(prep.Context())
	if err != nil {
		t.Fatalf("ExecuteBeginTurn failed: %v", err)
	}
	aLeg, _, err := prep.ResolveALeg(prep.Context(), br.Record.ALegID)
	if err != nil {
		t.Fatalf("ResolveALeg failed: %v", err)
	}

	// Record client turn using bounded ClientTurnShape without prompt text
	shape := largebody.ClientTurnShape{
		Items: []largebody.ClientTurnItemShape{
			{
				Kind:    aipapi.ItemKindMessage,
				Role:    "user",
				Ordinal: 0,
				Parts: []largebody.ClientTurnPartShape{
					{Kind: aipapi.ContentPartText, ContentBytes: int64(len(srcPayload))},
				},
			},
		},
	}
	if err := prep.RecordClientTurnWithShape(prep.Context(), br, shape, 4096); err != nil {
		t.Fatalf("RecordClientTurnWithShape failed: %v", err)
	}

	// 3. Capture Wire FE Ingress Checkpoint
	outCtx, holder, err := prep.CaptureFrontendIngressCheckpoint(
		prep.Context(),
		requestID,
		br,
		aLeg,
		&maxOutput,
	)
	if err != nil {
		t.Fatalf("CaptureFrontendIngressCheckpoint failed: %v", err)
	}

	// Merge measured wire count into FE ingress snapshot
	wireCount := largebody.WireCountResult{
		InputTokens: preflightDec.Count.InputTokens,
		TotalTokens: preflightDec.Count.TotalTokens,
		Accounting:  preflightDec.Count.Accounting,
	}
	prep.EnrichWireFrontendIngressQuantities(holder, wireCount)

	// Verify FE snapshot now has ComponentInputToken=42
	inTokVal, ok := checkpoint.QuantityComponentValue(holder.FrontendIngress.Public.Quantities, metering.ComponentInputToken)
	if !ok || inTokVal != int64(countedTokens) {
		t.Fatalf("FE ComponentInputToken=%d (ok=%v) want %d", inTokVal, ok, countedTokens)
	}
	// Verify Call remains empty
	if !reflect.DeepEqual(holder.FrontendIngress.Call, aipapi.Call{}) {
		t.Fatalf("wire FrontendIngress Call must remain empty, got %+v", holder.FrontendIngress.Call)
	}

	// Persist FE ingress fact
	feFactID, err := ex.persistFrontendIngressFact(outCtx, holder)
	if err != nil {
		t.Fatalf("persistFrontendIngressFact failed: %v", err)
	}
	if feFactID != "fe-ingress:"+requestID {
		t.Fatalf("feFactID=%q want %q", feFactID, "fe-ingress:"+requestID)
	}
	if len(factRecorder.facts) != 1 {
		t.Fatalf("factRecorder len=%d want 1", len(factRecorder.facts))
	}
	feFact := factRecorder.facts[0]
	feFactInTok, ok := checkpoint.QuantityComponentValue(feFact.Quantities, metering.ComponentInputToken)
	if !ok || feFactInTok != int64(countedTokens) {
		t.Fatalf("FE fact ComponentInputToken=%d (ok=%v) want %d", feFactInTok, ok, countedTokens)
	}

	// 4. Capture Wire BE Ingress Checkpoint
	attemptID := "att-wire-001"
	bLegID := "bleg-wire-001"
	sourceDigest := sha256.Sum256(srcPayload)
	rewriteDigest := checkpoint.ComputeRewriteDigest(128, 10, `"gpt-4o"`)
	attemptDigest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "gpt-4o")

	beSnap, err := ex.captureWireBackendIngress(outCtx, holder, WireBackendIngressArgs{
		AttemptID:       attemptID,
		BLegID:          bLegID,
		BackendID:       "openai_primary",
		Model:           "gpt-4o",
		MaxOutputTokens: &maxOutput,
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
		Now:             now,
	})
	if err != nil {
		t.Fatalf("captureWireBackendIngress failed: %v", err)
	}

	// Merge measured wire count into BE ingress snapshot
	ex.enrichWireBackendIngressQuantities(holder, attemptID, wireCount)

	beSnapEnriched := holder.BackendIngressFor(attemptID)
	beInTokVal, ok := checkpoint.QuantityComponentValue(beSnapEnriched.Public.Quantities, metering.ComponentInputToken)
	if !ok || beInTokVal != int64(countedTokens) {
		t.Fatalf("BE ComponentInputToken=%d (ok=%v) want %d", beInTokVal, ok, countedTokens)
	}
	if !reflect.DeepEqual(beSnap.Call, aipapi.Call{}) {
		t.Fatalf("wire BackendIngress Call must remain empty, got %+v", beSnap.Call)
	}

	// Persist BE ingress fact
	beFactID, err := ex.persistBackendIngressFact(outCtx, holder, attemptID)
	if err != nil {
		t.Fatalf("persistBackendIngressFact failed: %v", err)
	}
	if beFactID != "be-ingress:"+attemptID {
		t.Fatalf("beFactID=%q want %q", beFactID, "be-ingress:"+attemptID)
	}
	if len(factRecorder.facts) != 2 {
		t.Fatalf("factRecorder len=%d want 2", len(factRecorder.facts))
	}
	beFact := factRecorder.facts[1]
	beFactInTok, ok := checkpoint.QuantityComponentValue(beFact.Quantities, metering.ComponentInputToken)
	if !ok || beFactInTok != int64(countedTokens) {
		t.Fatalf("BE fact ComponentInputToken=%d (ok=%v) want %d", beFactInTok, ok, countedTokens)
	}

	// Verify all snapshots are wire mode and have zero Call retention
	for _, snap := range []*checkpoint.Snapshot{holder.FrontendIngress, holder.BackendIngressFor(attemptID)} {
		if !snap.IsWire() {
			t.Fatal("all snapshots must be wire mode")
		}
		if !reflect.DeepEqual(snap.Call, aipapi.Call{}) {
			t.Fatalf("snapshot Call must remain empty, got %+v", snap.Call)
		}
	}
}
