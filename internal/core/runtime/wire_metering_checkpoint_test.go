package runtime

import (
	"context"
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

type memoryFactRecorder struct {
	facts []metering.Fact
}

func (m *memoryFactRecorder) Append(_ context.Context, fact metering.Fact) error {
	m.facts = append(m.facts, fact)
	return nil
}

func TestPreparedSecureSession_CaptureFrontendIngressCheckpoint_Parity(t *testing.T) {
	t.Parallel()

	now := time.Unix(1715622000, 0).UTC()
	b2, err := b2bua.NewMemoryStore(b2bua.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	mgr := testSecureManager(t, memSS, b2)
	snap := extensions.NewRequestRuntimeSnapshot(hooks.New(hooks.Config{}), extensions.SnapshotOptions{
		Workspace: workspace.NewResolverChain([]aipworkspace.Resolver{voidWS{}}),
	})

	ex := setSecureSessionDenialMapper(TestExecutor())
	ex.Store = b2
	ex.Bus = hooks.New(hooks.Config{})
	ex.RuntimeSnapshot = snap
	ex.SecureSession = mgr
	ex.Now = func() time.Time { return now }

	sc := scope.PrincipalScopeView{
		PrincipalID: scope.Known("user-wire-fe"),
		TenantID:    scope.Known("tenant-wire"),
	}

	maxOutput := 1536
	requestID := "req-wire-seam-001"
	traceID := "trace-wire-seam-001"

	// 1. Prepare secure session via 9.x seam
	baseCtx := execview.WithFrontendID(context.Background(), "openai_responses")
	baseCtx = execview.WithPrincipal(baseCtx, execview.PrincipalView{ID: "user-wire-fe"})
	baseCtx = scope.WithScope(baseCtx, sc)

	prep, err := ex.PrepareSecureSession(baseCtx, SecureSessionPrepInput{
		TraceID: traceID,
		Session: largebody.SessionInput{
			ClientSessionID:     "client-sess-001",
			NewSessionRequested: true,
		},
		ContinuityKey: "ck-wire-01",
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

	// 2. Wire capture via PreparedSecureSession seam
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

	if holder == nil {
		t.Fatal("expected non-nil RequestHolder")
	}
	ctxHolder := meteringHolderFrom(outCtx)
	if ctxHolder != holder {
		t.Fatalf("context holder %p != returned holder %p", ctxHolder, holder)
	}

	fe := holder.FrontendIngress
	if fe == nil {
		t.Fatal("expected holder.FrontendIngress to be set")
	}

	// Verify no Call retention
	if !fe.IsWire() {
		t.Fatal("expected wire snapshot")
	}
	if !reflect.DeepEqual(fe.Call, aipapi.Call{}) {
		t.Fatalf("wire snapshot Call must be zero value, got %+v", fe.Call)
	}

	// Checkpoint public parity checks
	pub := fe.Public
	if pub.CheckpointID != "customer-request:"+requestID {
		t.Fatalf("CheckpointID=%q want %q", pub.CheckpointID, "customer-request:"+requestID)
	}
	if pub.StreamID != "customer-request:"+requestID {
		t.Fatalf("StreamID=%q want %q", pub.StreamID, "customer-request:"+requestID)
	}
	if pub.Boundary != metering.BoundaryFrontendIngress {
		t.Fatalf("Boundary=%q want BoundaryFrontendIngress", pub.Boundary)
	}
	if pub.Lifecycle != metering.LifecycleLogicalRequest {
		t.Fatalf("Lifecycle=%q want LifecycleLogicalRequest", pub.Lifecycle)
	}
	if pub.Perspective != metering.PerspectiveCustomer {
		t.Fatalf("Perspective=%q want PerspectiveCustomer", pub.Perspective)
	}
	if pub.FrontendID != "openai_responses" {
		t.Fatalf("FrontendID=%q want %q", pub.FrontendID, "openai_responses")
	}
	if !pub.CapturedAt.Equal(now) {
		t.Fatalf("CapturedAt=%v want %v", pub.CapturedAt, now)
	}

	// Correlation post-BeginTurn parity
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

	// Quantities: 1 request count, 1536 max-output tokens
	if len(pub.Quantities) != 2 {
		t.Fatalf("Quantities len=%d want 2", len(pub.Quantities))
	}
	reqVal, ok := checkpoint.QuantityComponentValue(pub.Quantities, metering.ComponentRequest)
	if !ok || reqVal != 1 {
		t.Fatalf("ComponentRequest=%d (ok=%v) want 1", reqVal, ok)
	}
	outVal, ok := checkpoint.QuantityComponentValue(pub.Quantities, metering.ComponentOutputToken)
	if !ok || outVal != int64(maxOutput) {
		t.Fatalf("ComponentOutputToken=%d (ok=%v) want %d", outVal, ok, maxOutput)
	}

	// 3. Test fact persistence via persistFrontendIngressFact
	rec := &memoryFactRecorder{}
	ex.MeteringRecorder = rec

	factID, err := ex.persistFrontendIngressFact(outCtx, holder)
	if err != nil {
		t.Fatalf("persistFrontendIngressFact failed: %v", err)
	}
	if factID != "fe-ingress:"+requestID {
		t.Fatalf("FactID=%q want %q", factID, "fe-ingress:"+requestID)
	}
	if len(rec.facts) != 1 {
		t.Fatalf("recorded facts len=%d want 1", len(rec.facts))
	}
	fact := rec.facts[0]
	if fact.FactID != factID || fact.Sequence != checkpoint.IngressSequence {
		t.Fatalf("Fact ID/Seq mismatch: %q, %d", fact.FactID, fact.Sequence)
	}
	if fact.Correlation.ALegID != aLeg.ALegID || fact.Correlation.SessionID != string(br.Record.SessionID) {
		t.Fatalf("Fact correlation mismatch: %+v", fact.Correlation)
	}

	// 4. Test enrichFrontendIngressQuantities safely skips wire snapshots
	if err := ex.enrichFrontendIngressQuantities(outCtx); err != nil {
		t.Fatalf("enrichFrontendIngressQuantities must not error on wire snapshot: %v", err)
	}
}
