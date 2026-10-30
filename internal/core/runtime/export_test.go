package runtime

// Test-only wiring: export_test.go is compiled only for `go test` on internal/core/runtime (same
// test binary as package runtime and co-located runtime_test). Normal imports of runtime omit this
// file (production, runtimebundle, stdhttp, internal/core/runtime/failclosed, etc.), so nil
// SecureSession fails closed there; see failclosed tests for an explicit regression.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/aiproxer/aiproxer/internal/core/affinity"
	"github.com/aiproxer/aiproxer/internal/core/b2bua"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/aipapidenial"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/b2bualineage"
	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/domain"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/compaction"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/execview"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/scope"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguard"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	aipworkspace "github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

// SecretGuardStageInputForTest is the exported mirror of secretGuardStageInput.
type SecretGuardStageInputForTest struct {
	TraceID   string
	Principal execview.PrincipalView
	Scope     scope.PrincipalScopeView
	Session   session.SessionView
	Workspace aipworkspace.WorkspaceView
	SessionID domain.SessionID
	TurnID    domain.TurnID
}

func init() {
	secureSessionTestPrepare = prepareExecutorSecureSessionForTests
}

func (e *Executor) ResolveAffinityKeyForTest(mode routing.AffinityMode, views execctx.Views, viewsOK bool) (affinity.Key, bool, error) {
	return e.resolveAffinityKey(&routing.Selector{Affinity: mode}, views, viewsOK)
}

// RouteAuthoritySnapshotBarrier is the test-only gate after A-leg resolution
// and before route-plan construction. Production requests never install one.
type RouteAuthoritySnapshotBarrier = routeAuthoritySnapshotBarrier

func NewRouteAuthoritySnapshotBarrier() *RouteAuthoritySnapshotBarrier {
	return newRouteAuthoritySnapshotBarrier()
}

func WithRouteAuthoritySnapshotBarrier(ctx context.Context, b *RouteAuthoritySnapshotBarrier) context.Context {
	return withRouteAuthoritySnapshotBarrier(ctx, b)
}

func (b *RouteAuthoritySnapshotBarrier) WaitUntilArrived(ctx context.Context) error {
	return b.waitUntilArrived(ctx)
}

func (b *RouteAuthoritySnapshotBarrier) Release() {
	b.releaseWaiters()
}

func (b *RouteAuthoritySnapshotBarrier) ALegID() string {
	return b.resolvedALegID()
}

// BuildRoutePlanPrimaryBackendForTest compiles the selector through buildRoutePlan
// and returns the primary backend id so tests can prove it shares CompileSelector.
func (e *Executor) BuildRoutePlanPrimaryBackendForTest(ctx context.Context, selector string) (string, error) {
	call := &aipapi.Call{
		Route: aipapi.RouteIntent{Selector: selector},
		Session: aipapi.SessionRef{
			ALegID: "a-1",
		},
	}
	preSession := session.SessionView{
		ALegID: "a-1",
	}
	ibt, err := newIdentityBoundTurn(
		"trace-1",
		call,
		execview.PrincipalView{},
		scope.PrincipalScopeView{},
		false,
		aipworkspace.WorkspaceView{},
		b2bua.ALegRecord{ALegID: "a-1"},
		routeAuthoritySnapshot{},
		execctx.SecureSessionTurn{},
		false,
		preSession,
	)
	if err != nil {
		return "", err
	}
	plan, err := e.buildRoutePlan(ctx, &preparedRequest{
		identity: ibt,
		call:     ibt.call,
	})
	if err != nil {
		return "", err
	}
	if plan == nil || plan.sel == nil || len(plan.sel.Alternatives) == 0 || plan.sel.Alternatives[0].Primary == nil {
		return "", nil
	}
	return plan.sel.Alternatives[0].Primary.Backend, nil
}

func prepareExecutorSecureSessionForTests(e *Executor) {
	if e == nil || e.SecureSession != nil {
		return
	}
	if e.Store == nil {
		panic("runtime test wiring requires a non-nil B2BUA store on the executor")
	}
	memSS := memory.New(memory.Options{SimulateDurable: true})
	fk := make([]byte, 32)
	if _, err := rand.Read(fk); err != nil {
		for i := range fk {
			fk[i] = byte(i + 1)
		}
	}
	mgr, err := app.NewManager(memSS, app.NewRandGenerator(fk), b2bualineage.New(e.Store), app.ManagerConfig{
		FingerprintKey: fk,
		StoreDurable:   true,
	})
	if err != nil {
		panic(fmt.Sprintf("runtime: test secure-session wiring: %v", err))
	}
	e.SecureSession = mgr
	if e.SessionDenialMapper == nil {
		e.SessionDenialMapper = aipapidenial.MapToSessionDenial
	}
	e.SyntheticLocalPrincipal = true
}

type ParallelPreWinFailStream struct{}

func (ParallelPreWinFailStream) Recv(context.Context) (aipapi.Event, error) {
	return aipapi.Event{}, errors.New("parallel leg failed before winner")
}

func (ParallelPreWinFailStream) Close() error { return nil }

func (ParallelPreWinFailStream) Cancel(context.Context, aipapi.CancelCause) aipapi.CancelResult {
	return aipapi.CancelResult{}
}

// RunSecretGuardStageForTest exposes runSecretGuardStage for invariant tests (empty SessionID, etc.).
func (e *Executor) RunSecretGuardStageForTest(ctx context.Context, call *aipapi.Call, in SecretGuardStageInputForTest) error {
	return e.runSecretGuardStage(ctx, call, secretGuardStageInput(in))
}

// ApplySecretGuardBlockForTest exposes the block finalization pipeline for precedence tests.
func (e *Executor) ApplySecretGuardBlockForTest(ctx context.Context, call *aipapi.Call, in SecretGuardStageInputForTest, block *extensions.SecretGuardBlockInfo) error {
	meta := secretguard.Meta{
		TraceID:   in.TraceID,
		Principal: in.Principal,
		Scope:     in.Scope,
		Session:   in.Session,
		Workspace: in.Workspace,
	}
	return e.applySecretGuardBlock(ctx, e.secretGuardAudit(in.TurnID), meta, call, block, secretGuardStageInput(in))
}

// ToolFinalActiveCountForTest returns residual assembler map/drain sizes for the
// retryRecvStream under stream (0,0,0,0 when cleared or inactive). Used to prove
// markFinished clears attempt-local finalizer state on normal response_finished.
func ToolFinalActiveCountForTest(stream aipapi.EventStream) (active, passThrough, completed, drain int) {
	rs, ok := stream.(*retryRecvStream)
	if !ok || rs == nil {
		return 0, 0, 0, 0
	}
	attempt := rs.attempt.snapshot()
	if attempt == nil || attempt.toolFinal == nil {
		return 0, 0, 0, 0
	}
	a := attempt.toolFinal
	return len(a.active), len(a.passThrough), len(a.completed), len(a.drain)
}

// SafeCompactionRequestOpenedForTest exposes safeCompactionRequestOpened for characterization tests.
// fail-open: detector panics isolated with nil log/metrics for test harness.
func SafeCompactionRequestOpenedForTest(d CompactionDetector, meta compaction.PreservationMeta, call aipapi.Call) []compaction.Event {
	return safeCompactionRequestOpened(context.Background(), nil, nil, d, meta, call)
}

// SafeCompactionResponseReleasedForTest exposes safeCompactionResponseReleased for characterization tests.
// fail-open: detector panics isolated with nil log/metrics for test harness.
func SafeCompactionResponseReleasedForTest(d CompactionDetector, meta compaction.PreservationMeta, ev aipapi.Event) []compaction.Event {
	return safeCompactionResponseReleased(context.Background(), nil, nil, d, meta, ev)
}

// SafeCompactionPreviewRequestForTest exposes safeCompactionPreviewRequest for characterization tests.
// fail-open: detector panics isolated with nil log/metrics for test harness.
func SafeCompactionPreviewRequestForTest(d CompactionDetector, meta compaction.PreservationMeta, call aipapi.Call) compaction.RequestPreview {
	return safeCompactionPreviewRequest(context.Background(), nil, nil, d, meta, call)
}

// SafeCompactionPreviewResponseForTest exposes safeCompactionPreviewResponse for characterization tests.
// fail-open: detector panics isolated with nil log/metrics for test harness.
func SafeCompactionPreviewResponseForTest(d CompactionDetector, meta compaction.PreservationMeta, ev aipapi.Event) compaction.ResponsePreview {
	return safeCompactionPreviewResponse(context.Background(), nil, nil, d, meta, ev)
}
