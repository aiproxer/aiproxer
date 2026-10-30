package runtime

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/extensions"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/policydecision"
)

var _ aipsdk.ExecutorView = (*Executor)(nil)

// noCopy signals go vet's copylocks analyzer to reject accidental copies.
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}

// secureSessionTestPrepare is a no-op in production and in packages that import runtime without compiling
// runtime's *_test.go sources (for example tests under internal/core/runtime/failclosed). Only the
// internal/core/runtime test binary links export_test.go, which assigns this hook in init.
var secureSessionTestPrepare = func(*Executor) {}

// Executor orchestrates hooks, capability negotiation, routing, B2BUA, and backend attempts.
// Fields are grouped by concern; promoted fields preserve the historical flat access API.
type Executor struct {
	_ noCopy
	CoreRuntime
	BillingRuntime
	RoutingRuntime
	SecurityRuntime
	AccountingRuntime
	ObservabilityRuntime
	ExtensionRuntime
	CompactionRuntime

	lifecycleMu     sync.Mutex
	rngOnce         sync.Once
	lockedRand      routing.Rng
	secureSessionMu sync.Mutex
	// quarantinePersistenceFault is intentional process-wide fail-closed state after a
	// secret-guard quarantine write (or SessionID invariant) failure. While latched,
	// AssertActive-before-open denies further backend dispatch on this executor until
	// process restart/reconcile. See docs/secrets-guard.md incident response.
	quarantinePersistenceFault atomic.Bool
}

func (e *Executor) capsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt aipapi.Call,
	c routing.AttemptCandidate,
) aipapi.BackendCaps {
	wire := routing.BackendFacingCandidate(c)
	if e != nil && e.CapsResolver != nil {
		return e.CapsResolver.DescribeCandidate(ctx, wire, attempt)
	}
	return execbackend.EffectiveCaps(ctx, be, attempt, wire)
}

func (e *Executor) transportCapsForAttempt(
	ctx context.Context,
	be execbackend.Backend,
	attempt aipapi.Call,
	c routing.AttemptCandidate,
) aipapi.BackendTransportCaps {
	return execbackend.EffectiveTransportCaps(ctx, be, attempt, routing.BackendFacingCandidate(c))
}

func (e *Executor) effectiveTransportFallbackPolicy() aipapi.TransportFallbackPolicy {
	if e == nil || e.TransportFallbackPolicy == "" {
		return aipapi.TransportFallbackCompatibility
	}
	return e.TransportFallbackPolicy
}

// policyEvidenceEmitter builds an EvidenceEmitter from the snapshot's policy observer
// and the executor logger. Returns nil when snap is nil, or when the snapshot's policy
// observer is the no-op default and privileged diagnostics are not enabled, so the
// no-observer deployment does not attach an active emitter, build decision contexts,
// or emit policy logs (requirements 7.6, 10.5). Emit on a nil emitter is a no-op, so
// callers can always invoke it without nil checks. The emitter is built per-call to
// reflect the frozen snapshot without caching across generations.
func (e *Executor) policyEvidenceEmitter(snap *extensions.RequestRuntimeSnapshot) *extensions.EvidenceEmitter {
	if e == nil || snap == nil {
		return nil
	}
	obs := snap.PolicyObserver()
	if policydecision.IsNoopObserver(obs) && !e.PolicyDiagnosticsEnabled {
		return nil
	}
	return extensions.NewEvidenceEmitter(obs, e.Log, e.PolicyDiagnosticsEnabled)
}

// Execute runs submit hooks, resolves the A-leg, plans routes, negotiates per attempt,
// and returns a stream. Recoverable pre-output failures may consume additional B-legs
// before the returned stream yields events.
//
// ctx must be non-nil (same contract as [aipapi.EventStream.Recv]); nil returns [aipapi.ErrNilContext].
const otelScopeExecutor = "github.com/aiproxer/aiproxer/internal/core/runtime"

// stampSystemPromptLaunchSeq gives the request the next process-owned launch
// stampSystemPromptLaunchSeq gives the request the next process-owned launch
// ordinal when the prompt-cache diagnostic identity feeds are armed. The
// sequence is owned by the process composition, not by this executor, so every
// generation (including an older generation still draining) draws from the
// same chronology across reloads. The value rides the request context to each
// attempt, which publishes it on that leg's usage sample; every leg of the
// request shares it. A nil sequence (direct construction outside the process
// wiring) stamps nothing, and the diagnostic stays conservative. Both the
// instruction and switch comparisons require this ordinal.
func (e *Executor) stampSystemPromptLaunchSeq(ctx context.Context) context.Context {
	if e == nil || !e.SystemPromptDigestEnabled || e.SystemPromptLaunchSeq == nil {
		return ctx
	}
	return withSystemPromptLaunchSeq(ctx, e.SystemPromptLaunchSeq.Next())
}

// attemptOpenOwner owns pre-output candidate admission and backend stream open.
type attemptOpenOwner struct{ *Executor }

// streamAssembler owns retry-recv stream construction after a successful open.
type streamAssembler struct{ *Executor }

func (e *Executor) Execute(ctx context.Context, call *aipapi.Call) (_ aipapi.EventStream, err error) {
	stream, _, err := e.execute(ctx, call)
	return stream, err
}

// ExecuteWithReceipt is the lexical optional seam for an internal auxiliary
// caller that needs the host operation identity of the admitted execution. It
// runs the same private canonical implementation as Execute; only the returned
// receipt is additional, and the public Execute contract is unchanged.
func (e *Executor) ExecuteWithReceipt(ctx context.Context, call *aipapi.Call) (_ aipapi.EventStream, receipt execctx.ExecutionReceipt, err error) {
	return e.execute(ctx, call)
}

func (e *Executor) execute(ctx context.Context, call *aipapi.Call) (_ aipapi.EventStream, receipt execctx.ExecutionReceipt, err error) {
	ctx = e.stampSystemPromptLaunchSeq(ctx)
	prep, prepCtx, cleanup, perr := e.prepareRequest(ctx, call)
	if perr != nil {
		return nil, receipt, perr
	}
	defer func() {
		prep.finalize(err)
		cleanup()
	}()
	// Task 3.3: local-turn success has no B-leg/provider/inference billing.
	if prep.isLocal && prep.localStream != nil {
		return prep.localStream, receipt, nil
	}
	if err := e.checkCheapCredit(prepCtx, prep); err != nil {
		return nil, receipt, err
	}
	plan, err := e.buildRoutePlan(prepCtx, prep)
	if err != nil {
		return nil, receipt, err
	}
	if err := e.authorizeBillingOnce(prepCtx, prep, plan); err != nil {
		return nil, receipt, err
	}
	exposure := e.canonicalExposureAbort(prepCtx, prep)
	// The operation is admitted and the exposure owner is installed: this is
	// the one pre-open point where the host identity is complete enough to
	// publish. Pre-admission failures above return an unavailable receipt.
	receipt = executionReceipt(prepCtx, prep)
	out, err := attemptOpenOwner{e}.openInitial(prepCtx, prep, plan)
	if err != nil {
		exposure.Abort(prepCtx)
		if out.ready == nil {
			e.notifyCompactionOpenFailed(prepCtx, prep)
		}
		return nil, receipt, err
	}
	prep.compactionOpenMeta = e.observeCompactionOpened(prepCtx, prep, out)
	stream, err := streamAssembler{e}.assemble(prepCtx, prep, plan, out)
	if err != nil {
		e.appendPostOpenFallback(prepCtx, prep.billingCallState, prep.identity.aLeg.ALegID, out)
		exposure.Abort(prepCtx)
		return nil, receipt, err
	}
	return stream, receipt, nil
}
