package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/metering/checkpoint"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/workspace"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

func TestExecutor_CaptureWireBackendIngress_ParityAndLifecycle(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715623000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})

	rec := &memoryFactRecorder{}
	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.MeteringRecorder = rec
	ex.Now = func() time.Time { return now }

	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("user-wire-be"),
		TenantID:    scope.Known("tenant-wire-be"),
	}

	maxOutput := 2048
	requestID := "req-wire-be-seam-001"
	traceID := "trace-wire-be-seam-001"
	attemptID := "att-wire-be-001"
	bLegID := "bleg-wire-be-001"

	sourceDigest := sha256.Sum256([]byte("runtime-wire-attempt-payload"))
	rewriteDigest := checkpoint.ComputeRewriteDigest(80, 10, `"gpt-4o"`)
	attemptDigest := checkpoint.ComputeAttemptDigest(sourceDigest, rewriteDigest, "gpt-4o")

	// 1. Prepare secure session & capture wire frontend ingress
	baseCtx := execview.WithFrontendID(context.Background(), "openai_responses")
	baseCtx = execview.WithPrincipal(baseCtx, execview.PrincipalView{ID: "user-wire-be"})
	baseCtx = scope.WithScope(baseCtx, sc)

	prep, err := ex.PrepareSecureSession(baseCtx, SecureSessionPrepInput{
		TraceID: traceID,
		Session: largebody.SessionInput{
			ClientSessionID:     "client-sess-be-001",
			NewSessionRequested: true,
		},
		ContinuityKey: "ck-wire-be-01",
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

	feCtx, holder, err := prep.CaptureFrontendIngressCheckpoint(
		prep.Context(),
		requestID,
		br,
		aLeg,
		&maxOutput,
	)
	if err != nil {
		t.Fatalf("CaptureFrontendIngressCheckpoint failed: %v", err)
	}

	// 2. Capture wire backend ingress via Executor helper
	beSnap, err := ex.captureWireBackendIngress(feCtx, holder, WireBackendIngressArgs{
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

	if !beSnap.IsWire() {
		t.Fatal("expected beSnap.IsWire() to be true")
	}
	if !reflect.DeepEqual(beSnap.Call, aipapi.Call{}) {
		t.Fatalf("expected empty Call in wire snapshot, got %+v", beSnap.Call)
	}

	// Verify correlation inherited from holder.FrontendIngress
	pub := beSnap.Public
	if pub.Correlation.RequestID != requestID {
		t.Fatalf("Correlation.RequestID=%q want %q", pub.Correlation.RequestID, requestID)
	}
	if pub.Correlation.TraceID != traceID {
		t.Fatalf("Correlation.TraceID=%q want %q", pub.Correlation.TraceID, traceID)
	}
	if pub.Correlation.ALegID != aLeg.ALegID {
		t.Fatalf("Correlation.ALegID=%q want %q", pub.Correlation.ALegID, aLeg.ALegID)
	}
	if pub.Correlation.SessionID != string(br.Record.SessionID) {
		t.Fatalf("Correlation.SessionID=%q want %q", pub.Correlation.SessionID, string(br.Record.SessionID))
	}
	if pub.Correlation.AttemptID != attemptID {
		t.Fatalf("Correlation.AttemptID=%q want %q", pub.Correlation.AttemptID, attemptID)
	}
	if pub.Correlation.BLegID != bLegID {
		t.Fatalf("Correlation.BLegID=%q want %q", pub.Correlation.BLegID, bLegID)
	}
	if pub.BackendID != "openai_primary" {
		t.Fatalf("BackendID=%q want %q", pub.BackendID, "openai_primary")
	}
	if pub.Model != "gpt-4o" {
		t.Fatalf("Model=%q want %q", pub.Model, "gpt-4o")
	}
	if pub.Boundary != metering.BoundaryBackendIngress {
		t.Fatalf("Boundary=%q want BoundaryBackendIngress", pub.Boundary)
	}
	if pub.Lifecycle != metering.LifecycleBackendAttempt {
		t.Fatalf("Lifecycle=%q want LifecycleBackendAttempt", pub.Lifecycle)
	}
	if pub.Perspective != metering.PerspectiveOperator {
		t.Fatalf("Perspective=%q want PerspectiveOperator", pub.Perspective)
	}

	// Verify holder contains the attempt
	fromHolder := holder.BackendIngressFor(attemptID)
	if fromHolder == nil {
		t.Fatal("expected BackendIngressFor(attemptID) to be found in holder")
	}
	if !reflect.DeepEqual(*fromHolder, beSnap) {
		t.Fatalf("holder snapshot diverges from returned snapshot:\nholder: %+v\nret:    %+v", *fromHolder, beSnap)
	}

	// 3. Persist BE ingress fact via persistBackendIngressFact
	factID, err := ex.persistBackendIngressFact(feCtx, holder, attemptID)
	if err != nil {
		t.Fatalf("persistBackendIngressFact failed: %v", err)
	}
	if factID != "be-ingress:"+attemptID {
		t.Fatalf("FactID=%q want %q", factID, "be-ingress:"+attemptID)
	}
	if len(rec.facts) != 1 {
		t.Fatalf("recorded facts len=%d want 1", len(rec.facts))
	}
	fact := rec.facts[0]
	if fact.FactID != factID || fact.Sequence != checkpoint.IngressSequence {
		t.Fatalf("Fact ID/Seq mismatch: %q, %d", fact.FactID, fact.Sequence)
	}
	if fact.Correlation.AttemptID != attemptID || fact.Correlation.BLegID != bLegID {
		t.Fatalf("Fact correlation mismatch: %+v", fact.Correlation)
	}

	// 4. Test widening check via assertWireAttemptNotWidened
	currEvidence := checkpoint.WireAttemptEvidence{
		SourceDigest:    sourceDigest,
		RewriteDigest:   rewriteDigest,
		AttemptDigest:   attemptDigest,
		Model:           "gpt-4o",
		MaxOutputTokens: &maxOutput,
	}

	// Exact matches pass
	if err := ex.assertWireAttemptNotWidened(holder, attemptID, currEvidence); err != nil {
		t.Fatalf("exact evidence must not fail widening: %v", err)
	}

	// Narrowed tokens pass
	narrowedVal := 1024
	narrowedEvidence := currEvidence
	narrowedEvidence.MaxOutputTokens = &narrowedVal
	if err := ex.assertWireAttemptNotWidened(holder, attemptID, narrowedEvidence); err != nil {
		t.Fatalf("narrowed tokens must not fail widening: %v", err)
	}

	// Raised tokens fail
	raisedVal := 4096
	raisedEvidence := currEvidence
	raisedEvidence.MaxOutputTokens = &raisedVal
	err = ex.assertWireAttemptNotWidened(holder, attemptID, raisedEvidence)
	if !errors.Is(err, checkpoint.ErrUnmeasuredWidening) {
		t.Fatalf("raised tokens must return ErrUnmeasuredWidening, got %v", err)
	}
}
