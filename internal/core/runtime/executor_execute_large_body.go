package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/internal/core/conversationprojection"
	"github.com/aiproxer/aiproxer/internal/core/execbackend"
	"github.com/aiproxer/aiproxer/internal/core/execctx"
	"github.com/aiproxer/aiproxer/internal/core/hooks"
	"github.com/aiproxer/aiproxer/internal/core/largebody"
	"github.com/aiproxer/aiproxer/internal/core/leglifecycle"
	coremetering "github.com/aiproxer/aiproxer/internal/core/metering"
	"github.com/aiproxer/aiproxer/internal/core/routing"
	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/usageevidence"
	"github.com/aiproxer/aiproxer/internal/core/usagelimit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/metering"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	sdkterminal "github.com/aiproxer/aiproxer/pkg/aipsdk/terminal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var (
	_ largebody.LargeBodyExecutor                  = (*Executor)(nil)
	_ largebody.LargeBodyWireExecutor              = (*Executor)(nil)
	_ largebody.LargeBodyStaticDispositionProvider = (*Executor)(nil)
)

// wireLifecycleEventStream wraps an EventStream to release request authority
// and execute terminal lifecycle and economic cleanup once when the stream
// is closed, consumed to EOF, or canceled.
type wireLifecycleEventStream struct {
	aipapi.EventStream
	cleanup func(err error)
	mu      sync.Mutex
	lastErr error
	closed  bool
	// closeOnce gates the wrapped stream's physical Close. The first
	// teardown caller performs it while every concurrent Close/Cancel/Recv
	// teardown caller blocks inside Do until that close completes. A
	// panicking close still marks the gate done, so waiters are released
	// rather than wedged. Every teardown path sequences the once-only shared
	// cleanup after this gate returns, so the replay root cannot be released
	// while an inner close is still in flight (Requirement 1.3).
	closeOnce sync.Once
	// cancelOnce gates the managed cancellation notification, which is
	// once-only and distinct from physical closure: notifying the inner
	// stream never consumes the closeOnce entitlement by itself.
	cancelOnce sync.Once
	// cancelRes records the winning managed notification result so the
	// winning Cancel caller can return it after the gate resolves.
	// Concurrent Cancel losers return CancelModeCloseOnly without touching
	// the inner stream again, matching the pre-existing loser contract.
	cancelRes aipapi.CancelResult
	once      sync.Once
}

var _ aipapi.ManagedEventStream = (*wireLifecycleEventStream)(nil)

func (s *wireLifecycleEventStream) lastRecvErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

func (s *wireLifecycleEventStream) noteRecvErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
}

func (s *wireLifecycleEventStream) innerStream() aipapi.EventStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.EventStream
}

// closeInnerReceiveLifetime runs the wrapped stream's Close exactly once.
// The winning caller performs the close and gets its error; concurrent
// teardown callers block until the winning close completes (a panicking
// close still releases them through the Once done-mark) and then return nil
// without touching the inner stream again, so the inner stream is never
// double-closed. Callers always sequence
// the shared once-cleanup after this returns, which keeps the replay-root
// release ordered behind the completed inner close on every path.
func (s *wireLifecycleEventStream) closeInnerReceiveLifetime() error {
	var closeErr error
	s.closeOnce.Do(func() {
		if inner := s.innerStream(); inner != nil {
			closeErr = inner.Close()
		}
	})
	return closeErr
}

// teardownSharedReceiveLifetime is the single ordered teardown for the
// non-panic receive-termination paths (EOF, non-EOF receive error, and the
// nil-stream case; the receive-panic path uses teardownAfterRecvPanic to
// preserve panic identity): the once-gated inner close completes first and
// only then runs the once-only shared cleanup that releases the replay root
// and finalizes the turn audit (Requirement 1.3). Concurrent teardown callers
// serialize on the close gate, so no caller can run the shared cleanup while
// another caller's inner close is still in flight. The cleanup is deferred
// before the inner close, so an inner Close panic still runs the audit before
// propagating. The teardown cause is advisory; the audit verdict comes from
// the bound stream's logical outcome evidence, which is stable under this
// ordering because the accepted-completion latch is set-only and the request
// terminal claim is already won (a later inner Close is a no-op on a finished
// terminal).
func (s *wireLifecycleEventStream) teardownSharedReceiveLifetime(cause error) {
	defer s.once.Do(func() {
		if s.cleanup != nil {
			s.cleanup(cause)
		}
	})
	_ = s.closeInnerReceiveLifetime()
}

// recvInner invokes the wrapped stream. When the inner Recv panics, the
// once-cleanup still runs exactly once with the panic recorded as the
// receive-return failure, and the original panic propagates unchanged, so a
// panicking receive finalizes the turn audit instead of stranding it.
func (s *wireLifecycleEventStream) recvInner(ctx context.Context) (ev aipapi.Event, err error) {
	defer func() {
		if r := recover(); r != nil {
			var perr error
			if perrErr, ok := r.(error); ok {
				perr = perrErr
			} else {
				perr = fmt.Errorf("runtime: wire stream recv panic: %v", r)
			}
			s.noteRecvErr(perr)
			s.teardownAfterRecvPanic(perr)
			panic(r)
		}
	}()
	return s.EventStream.Recv(ctx)
}

// teardownAfterRecvPanic runs the shared teardown for a receive panic while
// preserving the original panic as the primary failure. A secondary
// inner-Close panic is contained and joined into the advisory cleanup cause
// (so it is still reported to the audit path) instead of replacing the
// original panic; recvInner re-panics the original value unchanged.
func (s *wireLifecycleEventStream) teardownAfterRecvPanic(perr error) {
	cause := perr
	defer func() {
		s.once.Do(func() {
			if s.cleanup != nil {
				s.cleanup(cause)
			}
		})
	}()
	s.closeInnerReceiveLifetimeContained(&cause)
}

// closeInnerReceiveLifetimeContained runs the once-gated inner close and
// converts a close panic into a joined secondary cause. It exists so a
// receive-panic unwind cannot have its panic identity overwritten by a close
// panic raised mid-teardown.
func (s *wireLifecycleEventStream) closeInnerReceiveLifetimeContained(cause *error) {
	defer func() {
		if r := recover(); r != nil {
			var closeErr error
			if closePanic, ok := r.(error); ok {
				closeErr = closePanic
			} else {
				closeErr = fmt.Errorf("runtime: wire stream inner close panic: %v", r)
			}
			*cause = errors.Join(*cause, closeErr)
		}
	}()
	_ = s.closeInnerReceiveLifetime()
}

// wireStreamLogicalSuccess reports whether the wire turn audit may record
// success. Success requires the shared receive lifetime's accepted logical
// outcome (request success code with released effects plus the latched
// receive-release completion) and no receive-return failure. Only errors
// actually returned from Recv veto success: the Close-synthesized
// cancellation cause is not a receive failure, so an accepted success
// followed by Close-without-EOF or by a later teardown cancellation stays
// success. An unknown inner stream fails closed.
func wireStreamLogicalSuccess(outer *wireLifecycleEventStream) bool {
	if outer == nil {
		return false
	}
	if err := outer.lastRecvErr(); err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	rs, ok := outer.EventStream.(*retryRecvStream)
	if !ok || rs == nil || rs.terminal == nil {
		return false
	}
	return rs.terminal.wireLogicalOutcome().logicallySuccessful()
}

func (s *wireLifecycleEventStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	// A close with no prior Recv error is an early close before EOF, which
	// canonical semantics classify as canceled (CommandClose), never Winner.
	cause := s.lastRecvErr()
	if cause == nil {
		cause = context.Canceled
	}
	// The once-gated inner close completes before the once-cleanup below can
	// run, including against concurrent Close/Cancel/Recv teardown: they
	// serialize on the same gate, so the replay root is never released while
	// this close is still in flight. The once-cleanup runs even when the
	// inner Close panics, before the panic propagates to the caller, so
	// source disposal and the turn audit cannot be skipped on unwinding.
	// Teardown order is unchanged: the inner receive lifetime stops first,
	// the shared cleanup runs after.
	defer s.once.Do(func() {
		if s.cleanup != nil {
			s.cleanup(cause)
		}
	})

	return s.closeInnerReceiveLifetime()
}

func (s *wireLifecycleEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return aipapi.Event{}, io.EOF
	}
	s.mu.Unlock()

	if s.EventStream == nil {
		s.teardownSharedReceiveLifetime(io.EOF)
		return aipapi.Event{}, io.EOF
	}
	ev, err := s.recvInner(ctx)
	if err != nil {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return aipapi.Event{}, io.EOF
		}
		s.noteRecvErr(err)
		s.teardownSharedReceiveLifetime(err)
	}
	return ev, err
}

func (s *wireLifecycleEventStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.mu.Lock()
	s.closed = true
	inner := s.EventStream
	s.mu.Unlock()

	defer s.once.Do(func() {
		if s.cleanup != nil {
			s.cleanup(context.Canceled)
		}
	})

	if ms, ok := inner.(aipapi.ManagedEventStream); ok {
		// Cancellation notification is once-only and separate from physical
		// closure. A CloseOnly notification is treated as "close still owed",
		// so the wrapper performs the gated close itself below; any other
		// mode's notification is the lifetime stop, and the close gate is
		// consumed (not re-closed) so a notifier that already closed is
		// never closed again by this wrapper while a later Close still
		// observes a resolved gate. This does NOT rest on CloseOnly
		// guaranteeing a no-op: in-tree CloseOnly reporters already close
		// during Cancel (openresponsescompat/provider_stream.go and
		// openaiusage/stream.go both return CloseOnly with Err: s.Close()),
		// so for them the wrapper's gated close is a benign repeat only
		// because those Close implementations are idempotent. A
		// non-idempotent closer reporting CloseOnly would be closed twice.
		// The deferred consume resolves the gate even when the
		// notification panics, so concurrent teardown waiters are released
		// rather than wedged behind this notification.
		winner := false
		s.cancelOnce.Do(func() {
			winner = true
			defer s.closeOnce.Do(func() {})
			res := ms.Cancel(ctx, cause)
			s.cancelRes = res
			if res.Mode == aipapi.CancelModeCloseOnly {
				_ = s.closeInnerReceiveLifetime()
			}
		})
		if winner {
			return s.cancelRes
		}
		return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
	}
	_ = s.closeInnerReceiveLifetime()
	return aipapi.CancelResult{Mode: aipapi.CancelModeCloseOnly}
}

// AssessLargeBody evaluates frontend proof for candidate fast-path execution (Phase 4).
// If LargeBodyAssessor is not configured or interleaved thinking is enabled,
// it returns a declined assessment (fail closed to canonical; Item 4).
func (e *Executor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	if e != nil && e.localBoundaryCaptureRequired() {
		// Wire execution has no canonical Call or transform callback with which
		// to prove the final provider-bound representation. Decline before any
		// upstream commitment so the canonical path can capture it.
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonMeteringUnsupported)
		return dec, nil
	}
	if e == nil || e.LargeBodyAssessor == nil || e.interleavedEnabled() {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}
	return e.LargeBodyAssessor.AssessLargeBody(ctx, proof)
}

// LargeBodyStaticDisposition returns an O(1) static wire disposition for a profile (Phase 4).
// If LargeBodyAssessor implements LargeBodyStaticDispositionProvider, it delegates to it.
// Otherwise, it returns DefinitelyCanonical with StaticBlocker.
func (e *Executor) LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason) {
	if e == nil || e.LargeBodyAssessor == nil || e.interleavedEnabled() {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	if sdp, ok := e.LargeBodyAssessor.(largebody.LargeBodyStaticDispositionProvider); ok {
		return sdp.LargeBodyStaticDisposition(profileID)
	}
	return largebody.StaticWireNeedsRequestAssessment, largebody.StaticWireReasonNone
}

// LaneDomainPolicy configures late-route domain constraints per frontend lane.
type LaneDomainPolicy struct {
	// UniversalOnly requires backend universal model certification (AnyAcceptedModel: true)
	// and evaluates universal late-route domain proof (e.g. Lane 3 OpenResponses).
	UniversalOnly bool
	// CandidateModels lists finite candidate models certified for the lane when not UniversalOnly.
	CandidateModels []string
}

// StandardLaneDomainPolicies returns the default domain policies for standard frontend lanes.
func StandardLaneDomainPolicies() map[string]LaneDomainPolicy {
	return map[string]LaneDomainPolicy{
		"openai_responses_v1": {UniversalOnly: false},
		"openai_chat_v1":      {UniversalOnly: false},
		"openresponses_v1":    {UniversalOnly: true},
	}
}

// ProductionLargeBodyAssessor unifies authority assessment, backend wire proof,
// and route override/late selector gates into a side-effect-free proof assessor
// for runtime.Executor (Phase 4, Requirements 5, 6, 7, 8, 9, 13, 14, 15, 19).
type ProductionLargeBodyAssessor struct {
	GenerationID              string
	CandidateDomainGeneration string
	AuthorityGate             *largebody.AuthorityAssessmentGate
	WireProofGate             *largebody.BackendWireProofGate
	LaneDomainPolicies        map[string]LaneDomainPolicy
}

var (
	_ largebody.LargeBodyAssessor                  = (*ProductionLargeBodyAssessor)(nil)
	_ largebody.LargeBodyStaticDispositionProvider = (*ProductionLargeBodyAssessor)(nil)
)

// NewProductionLargeBodyAssessor constructs a ProductionLargeBodyAssessor.
func NewProductionLargeBodyAssessor(
	generationID string,
	candidateDomainGen string,
	authGate *largebody.AuthorityAssessmentGate,
	wireProofGate *largebody.BackendWireProofGate,
	lanePolicies map[string]LaneDomainPolicy,
) *ProductionLargeBodyAssessor {
	return &ProductionLargeBodyAssessor{
		GenerationID:              generationID,
		CandidateDomainGeneration: candidateDomainGen,
		AuthorityGate:             authGate,
		WireProofGate:             wireProofGate,
		LaneDomainPolicies:        lanePolicies,
	}
}

// AssessLargeBody evaluates frontend proof across authority and wire proof gates.
// Invariants:
//   - Streaming-only delivery gate (proof.Delivery == DeliveryModeStreaming; 15.4 carry).
//   - Universal-vs-finite domain policy per lane.
//   - Same-permit decline discipline: always returns (declined, nil) on decline, never an error,
//     so caller continues canonical processing under held decode permit.
func (a *ProductionLargeBodyAssessor) AssessLargeBody(ctx context.Context, proof largebody.Proof) (largebody.Assessment, error) {
	if ctx != nil && ctx.Err() != nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonCanceled)
		return dec, nil
	}
	if strings.TrimSpace(proof.ProfileID) == "" {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonProofUncertain)
		return dec, nil
	}
	if proof.Delivery != aipapi.DeliveryModeStreaming {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}
	if a == nil || a.AuthorityGate == nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}

	authDecision, authReason := a.AuthorityGate.Evaluate()
	if authDecision == largebody.AssessmentDecisionDecline {
		dec, _ := largebody.NewDeclinedAssessment(authReason)
		return dec, nil
	}

	if a.AuthorityGate.Census.Ports.ConversationViewReaderOccupied && !proof.Session.ProvesFreshALeg() {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonSessionUnsupported)
		return dec, nil
	}

	budget := largebody.SemanticFactBudget(ctx)
	if proof.AggregateFactBytes() > budget {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}
	if a.AuthorityGate.Census.Ports.CompactionDetectorOccupied && a.AuthorityGate.Census.Ports.CompactionDetectorWireSupported {
		if !proof.CompactionComplete {
			dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
			return dec, nil
		}
	}

	if a.WireProofGate == nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonAuthorityBlocker)
		return dec, nil
	}

	wpGate := a.WireProofGate
	if a.LaneDomainPolicies != nil {
		if pol, ok := a.LaneDomainPolicies[proof.ProfileID]; ok && pol.UniversalOnly {
			if wpGate.OverrideGate != nil && len(wpGate.OverrideGate.CandidateModels) > 0 {
				cloned := *wpGate
				ovClone := *wpGate.OverrideGate
				ovClone.CandidateModels = nil
				cloned.OverrideGate = &ovClone
				wpGate = &cloned
			}
		}
	}

	wpDecision, wpReason, wireReq, wireDomain, cands := wpGate.Evaluate(ctx, proof)
	if wpDecision == largebody.AssessmentDecisionDecline {
		dec, _ := largebody.NewDeclinedAssessment(wpReason)
		return dec, nil
	}

	if pol, ok := a.LaneDomainPolicies[proof.ProfileID]; ok && pol.UniversalOnly {
		domainFacts := largebody.WireDomainFacts{
			ProfileID:            proof.ProfileID,
			Operation:            proof.Operation,
			Delivery:             proof.Delivery,
			BodyMode:             proof.Mode,
			Rewrite:              proof.Rewrite,
			UniversalModel:       true,
			RequiredCapabilities: append([]aipapi.Capability(nil), proof.RequiredCapabilities...),
		}
		for _, cand := range cands {
			backendID := strings.TrimSpace(cand.Primary.Backend)
			var wb largebody.WireBackend
			var ok bool
			if wpGate.BackendResolver != nil {
				wb, ok = wpGate.BackendResolver.ResolveWireBackend(backendID)
			}
			if !ok || wb == nil {
				dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonBackendIncompatible)
				return dec, nil
			}
			support := wb.ResolveWireDomain(ctx, domainFacts)
			if !support.Compatible || !support.AnyAcceptedModel {
				dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonBackendIncompatible)
				return dec, nil
			}
		}
		wireDomain = domainFacts
	}

	cGen := a.CandidateDomainGeneration
	if cGen == "" {
		cGen = a.GenerationID
	}
	stamp, err := largebody.BindAssessmentStamp(a.GenerationID, proof, cGen)
	if err != nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonProofUncertain)
		return dec, nil
	}

	accepted, err := largebody.NewAcceptedAssessment(stamp, wireReq, wireDomain)
	if err != nil {
		dec, _ := largebody.NewDeclinedAssessment(largebody.DeclineReasonProofUncertain)
		return dec, nil
	}
	accepted.CompactionFacts = proof.CompactionFacts.Clone()
	accepted.CompactionComplete = proof.CompactionComplete
	return accepted, nil
}

// LargeBodyStaticDisposition returns an O(1) static wire disposition for a profile.
func (a *ProductionLargeBodyAssessor) LargeBodyStaticDisposition(profileID string) (largebody.StaticWireDisposition, largebody.StaticWireReason) {
	if a == nil || a.AuthorityGate == nil {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	if a.AuthorityGate.Summary.HasStaticBlocker() {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	if a.WireProofGate == nil || a.WireProofGate.BackendResolver == nil {
		return largebody.StaticWireDefinitelyCanonical, largebody.StaticWireReasonStaticBlocker
	}
	return largebody.StaticWireNeedsRequestAssessment, largebody.StaticWireReasonNone
}

// wireAdmittedViews freezes the complete admitted request views for wire routing
// (Requirement 3.1): resolved principal, scope, bound session, and workspace.
// The bound view is valid independently of principal presence — preparation's
// principal-presence flag stays separate from view availability — so a created
// authoritative session always reaches routing while genuinely absent identities
// keep existing missing-identity policy downstream (Requirements 3.2, 3.3).
// Body metadata is never consulted here (Requirement 3.3).
func wireAdmittedViews(bound session.SessionView, secured *PreparedSecureSession) execctx.Views {
	admitted := execctx.Views{Session: bound}
	if secured != nil {
		admitted.Principal = secured.Principal()
		admitted.Scope = secured.Scope()
		admitted.Workspace = secured.Workspace()
	}
	return admitted
}

// ExecuteLargeBody implements largebody.LargeBodyWireExecutor (Requirements 6, 7, 14, 15, 18, 19; Task 13.1).
// It crosses the one-way wire commit barrier, runs the wire secure-session preparation and exactly one
// BeginTurn/A-leg lifecycle, reads the post-BeginTurn live route override constrained to the assessed domain,
// applies request authority and economic admission, and returns an ExecutionResult containing the canonical
// EventStream, bounded ResponseFacts, and the sensitive SessionResponseCarrier.
// Under no circumstance does this method fall back to canonical Execute.
func (e *Executor) ExecuteLargeBody(
	ctx context.Context,
	accepted largebody.Assessment,
	src largebody.Source,
) (largebody.ExecutionResult, error) {
	turnFinished := false
	defer func() {
		if !turnFinished && src != nil {
			_ = src.Close()
		}
	}()

	if ctx != nil && ctx.Err() != nil {
		return largebody.ExecutionResult{}, ctx.Err()
	}

	// 1. Validate assessment stamp and source ownership (Requirements 6.6, 6.7, 8.5)
	if src == nil {
		return largebody.ExecutionResult{}, fmt.Errorf("executor: largebody source must not be nil (invariant failure)")
	}

	var srcDigest largebody.SourceDigest
	if digester, ok := src.(interface{ Digest() largebody.SourceDigest }); ok {
		srcDigest = digester.Digest()
	} else if digester, ok := src.(interface{ SourceDigest() largebody.SourceDigest }); ok {
		srcDigest = digester.SourceDigest()
	} else {
		srcDigest = accepted.Stamp.SourceDigest()
	}

	genID := e.LargeBodyGenerationID
	if genID == "" {
		genID = accepted.Stamp.GenerationID()
	}
	cGen := e.LargeBodyCandidateDomainGeneration
	if cGen == "" {
		cGen = accepted.Stamp.CandidateDomainGeneration()
	}

	live := largebody.LiveExecutionFacts{
		GenerationID:              genID,
		ProfileID:                 accepted.Stamp.ProfileID(),
		Source:                    srcDigest,
		BodyBytes:                 src.Size(),
		Mode:                      accepted.Stamp.BodyMode(),
		Rewrite:                   accepted.Stamp.Rewrite(),
		CandidateDomainGeneration: cGen,
	}

	if err := largebody.ValidateExecuteLargeBody(accepted, src, live); err != nil {
		return largebody.ExecutionResult{}, err
	}
	if _, ok := e.CompactionDetector.(CompactionWireDetector); ok && !accepted.CompactionComplete {
		return largebody.ExecutionResult{}, fmt.Errorf("executor: large body accepted for wire execution but compaction facts are incomplete")
	}

	// 2. Resolve wire turn facts
	turnFacts := e.resolveWireTurnFacts(ctx, accepted, src, srcDigest)

	traceID := strings.TrimSpace(turnFacts.Identity.TraceID)
	if traceID == "" {
		traceID = strings.TrimSpace(turnFacts.Identity.RequestID)
	}
	if traceID == "" {
		traceID = "wire-trace"
	}
	requestID := strings.TrimSpace(turnFacts.Identity.RequestID)
	if requestID == "" {
		requestID = traceID
	}

	// 3. Wire secure-session preparation (Requirements 6.2, 14.1)
	prep, err := e.PrepareSecureSession(ctx, SecureSessionPrepInput{
		TraceID: traceID,
		Session: turnFacts.Session.Input,
	})
	if err != nil {
		return largebody.ExecutionResult{}, err
	}
	outCtx := prep.Context()

	// 4. One-way commit barrier: ExecuteBeginTurn (Requirements 6.5, 6.6, 14.1)
	br, err := prep.ExecuteBeginTurn(outCtx)
	if err != nil {
		return largebody.ExecutionResult{}, err
	}
	outCtx = execctx.WithSecureSessionTurn(outCtx, prep.SecureTurn(br))

	defer func() {
		if !turnFinished && e.SecureSession != nil {
			auditCtx, auditCancel := cleanupContext(outCtx, defaultAuthorityCleanupTimeout)
			defer auditCancel()
			_ = e.SecureSession.FinishTurn(auditCtx, br.Record.SessionID, br.TurnID, app.TurnOutcome{
				Kind: app.TurnOutcomeSurfacedFailure,
			})
		}
	}()

	// 5. Resolve A-leg & snapshot route override (Requirements 7.4, 14.1)
	aLeg, routeAuth, err := prep.ResolveALeg(outCtx, br.Record.ALegID)
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	// Verify conversation projection invariant on fresh A-leg: snapshot must be clean
	if reader := e.conversationViewReader(); reader != nil {
		snap, serr := reader.Snapshot(outCtx, aLeg.ALegID)
		if serr != nil {
			if obs := e.conversationViewObserver(); obs != nil {
				safeObserver{obs: obs}.OnProjectionFailure(conversationprojection.StageEarly)
			}
			return largebody.ExecutionResult{}, fmt.Errorf("executor: conversation view snapshot: %w", serr)
		}
		if len(snap.NeverBackend) > 0 || len(snap.Steering) > 0 {
			return largebody.ExecutionResult{}, fmt.Errorf("executor: conversation projection invariant failure: unexpected non-empty projection in fresh session: %d never_backend, %d steering", len(snap.NeverBackend), len(snap.Steering))
		}
	}

	// Bind session view and record client turn shape if present
	boundSession := prep.BindSession(br, aLeg)
	if len(turnFacts.Session.TurnShape.Items) > 0 {
		if rerr := prep.RecordClientTurnWithShape(outCtx, br, turnFacts.Session.TurnShape, largebody.DefaultMaxSemanticFactBytes); rerr != nil {
			if e.SecureSessionRecordingMandatory {
				return largebody.ExecutionResult{}, rerr
			}
		}
	}

	// Stage session_classification sits after the post-commit secure-session,
	// A-leg and workspace binding and before the route/request consumers
	// (design "Wire execution ordering"). The classifier receives the bound
	// SessionView, the workspace PrepareSecureSession already resolved for this
	// generation, and the bounded proof ClassificationEvidence - never a
	// reconstructed canonical request, which the wire lane does not have
	// (requirements 4.1, 5.2, 5.5). The stage returns nothing on purpose: an
	// absent plane is a no-op and an unknown/error outcome continues this one
	// committed wire execution without any post-commit canonical fallback
	// (requirements 4.4, 5.4, 5.7).
	e.runWireSessionClassificationStage(outCtx, wireSessionClassificationInput{
		TraceID:   traceID,
		Session:   boundSession,
		Workspace: prep.Workspace(),
		Evidence:  turnFacts.Session.ClassificationEvidence,
	})

	// 6. Read live route override only now, constrained to assessed domain (Requirement 7)
	effectiveModel := turnFacts.Route.CandidateModel
	if effectiveModel == "" {
		effectiveModel = turnFacts.Route.ClientModel
	}
	if effectiveModel == "" {
		effectiveModel = accepted.WireRequest.CandidateModel
	}
	if effectiveModel == "" {
		effectiveModel = accepted.WireRequest.ClientModel
	}

	if routeAuth.active() {
		overrideSel := strings.TrimSpace(routeAuth.State.Selector)
		if overrideSel != "" {
			if accepted.WireDomain.UniversalModel {
				effectiveModel = overrideSel
			} else {
				matched := slices.Contains(accepted.WireDomain.CandidateModels, overrideSel)
				if !matched && e.SelectorAliases != nil {
					resolved := e.SelectorAliases.Resolve(overrideSel)
					if slices.Contains(accepted.WireDomain.CandidateModels, resolved) {
						matched = true
						overrideSel = resolved
					}
				}
				if !matched {
					return largebody.ExecutionResult{}, fmt.Errorf("executor: route override selector %q is outside assessed wire domain", overrideSel)
				}
				effectiveModel = overrideSel
			}
		}
	}

	// 7. Frontend ingress checkpoint capture (Requirements 15.1–15.3, 19)
	var maxOutputTokens *int
	if turnFacts.MaxOutput.MaxOutputTokens > 0 {
		v := int(turnFacts.MaxOutput.MaxOutputTokens)
		maxOutputTokens = &v
	}
	outCtx, _, err = prep.CaptureFrontendIngressCheckpoint(outCtx, requestID, br, aLeg, maxOutputTokens)
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	// 8. Request authority admission (Requirements 4.5, 8.1, 10.4)
	outCtx, err = e.admitRequestAuthorityOnce(outCtx, requestID, aLeg.ALegID, traceID, prep.Scope())
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	// Ensure request authority is released if subsequent economic admission steps fail
	committed := false
	defer func() {
		if !committed {
			_ = e.releaseRequestAuthority(outCtx)
		}
	}()

	// 9. Economic admission (Requirements 15.6, 19)
	if err := e.checkWireCheapCredit(outCtx, WireBillingCreditArgs{
		Scope: prep.Scope(),
	}); err != nil {
		return largebody.ExecutionResult{}, err
	}

	var billingCallID usageevidence.BillingCallID
	if parsed, perr := usageevidence.ParseBillingCallID(turnFacts.Economic.BillingCallID); perr == nil {
		billingCallID = parsed
	} else {
		newID, nerr := usageevidence.NewBillingCallID()
		if nerr != nil {
			return largebody.ExecutionResult{}, fmt.Errorf("executor: new billing call id: %w", nerr)
		}
		billingCallID = newID
	}
	turnFacts.Economic.BillingCallID = billingCallID.String()
	turnFacts.Route.RouteSelector = effectiveModel
	// The route origin is final at the last authority that could replace the
	// route. An administrator's override selects the route the request runs, so
	// that route is not the proxy's own default route and must not keep the
	// generation's frozen model binding: the wire lane resolves it with no
	// binding, exactly as it resolves any other explicit route.
	// The route origin is final at the last authority that could replace the
	// route. An administrator's override selects the route the request runs, so
	// that route is not the proxy's own default route and must not keep the
	// generation's frozen model binding: the wire lane resolves it with no
	// binding, exactly as it resolves any other explicit route.
	if routeAuth.active() {
		outCtx = routing.WithRouteOrigin(outCtx, routing.RouteOriginOverride)
	}

	sel, err := e.prepareRouteSelector(
		effectiveModel,
		wireRouteModelResolver(outCtx),
	)
	if err != nil {
		return largebody.ExecutionResult{}, fmt.Errorf("executor: route selector: %w", err)
	}

	// Resolve the trusted store identity before exposure admission so a
	// faulty resolver fails closed with zero exposure admitted (no abort
	// owed) and never a fabricated store identity.
	wireStoreID, err := e.resolveBillingStoreID(outCtx)
	if err != nil {
		return largebody.ExecutionResult{}, fmt.Errorf("%w: trusted billing store identity: %w", ErrBillingAdmissionDenied, err)
	}

	callExposure, err := e.AuthorizeWireBilling(outCtx, WireBillingExposureArgs{
		BillingCallID:   billingCallID,
		TraceID:         traceID,
		ALegID:          aLeg.ALegID,
		SessionID:       string(br.Record.SessionID),
		Scope:           prep.Scope(),
		Route:           sel,
		RequestSize:     routing.RequestSizeEstimate{Available: true, Tokens: turnFacts.Source.BodyBytes, Basis: "wire_body_bytes"},
		MaxOutputTokens: maxOutputTokens,
	})
	if err != nil {
		return largebody.ExecutionResult{}, err
	}

	billingState := newBillingCallState(billingCallID)
	exposure := e.wireExposureAbort(callExposure, billingCallID, aLeg.ALegID, string(br.Record.SessionID), billingState)

	// 10. Attempt execution under canonical attempt and stream assembly (Requirements 8, 9, 10, 12, 15)
	aScope := e.lifecycleCoordinator().StartALeg(aLeg.ALegID)

	wp := &wireAttemptPayload{
		src:                   src,
		accepted:              accepted,
		turnFacts:             turnFacts,
		requestID:             requestID,
		sessionID:             string(br.Record.SessionID),
		maxOutputTokens:       maxOutputTokens,
		weightedFirstConsumed: aLeg.WeightedFirstConsumed,
		selector:              sel,
	}

	// Freeze the complete admitted views after BindSession: resolved principal,
	// scope, bound session, and workspace. View availability never depends
	// solely on principal presence, so the created authoritative session
	// reaches routing while genuinely absent identities keep existing
	// missing-identity policy (Requirements 3.1, 3.2, 3.3).
	boundViews := wireAdmittedViews(boundSession, prep)

	st, stOK := execctx.SecureSessionTurnFromContext(outCtx)

	preparedReq := new(preparedRequest)
	preparedReq.billingCallID = billingCallID
	preparedReq.billingCallState = billingState
	// Freeze the trusted economic lineage once for this wire request alongside
	// the existing billing identity. An empty resolver result stays a frozen
	// absence; replacements and parallel legs inherit these frozen facts while
	// each attempt binds its own B-leg identity at open. The store identity
	// was resolved before exposure admission (wireStoreID above) so a faulty
	// resolver fails closed pre-admission; stamping here would resolve twice
	// and reintroduce the post-admission panic window.
	preparedReq.billingStoreID = wireStoreID
	preparedReq.billingStoreIDStamped = true

	var rFactIn recvTurnFactsInput
	rFactIn.traceID = traceID
	rFactIn.aLegID = aLeg.ALegID
	rFactIn.recvViews = boundViews
	rFactIn.recvViewsOK = true
	rFactIn.secureTurn = st
	rFactIn.secureTurnOK = stOK
	rFactIn.metering = meteringHolderFrom(outCtx)
	rFactIn.requestAuth = requestAuthorityFrom(outCtx)
	rFactIn.billingAccountID = callExposure.AccountID
	rFactIn.billingCustomerPricing = callExposure.PricingRef
	rFactIn.billingChargePolicy = callExposure.ChargePolicyRef
	rFactIn.billingIdentityStamped = strings.TrimSpace(callExposure.AccountID) != ""
	rFactIn.billingCallID = billingCallID
	rFactIn.billingCallState = billingState
	rFactIn.wirePayload = wp
	recvFacts := newRecvTurnFacts(outCtx, withBillingStoreID(rFactIn, preparedReq.billingStoreID, preparedReq.billingStoreIDStamped))

	bus := e.Bus
	if bus == nil {
		bus = hooks.New(hooks.Config{})
	}

	preparedReq.recvTurnFacts = recvFacts
	preparedReq.bus = bus
	preparedReq.aScope = aScope
	preparedReq.billingExposure = callExposure

	plan, err := e.buildRoutePlan(outCtx, preparedReq)
	if err != nil {
		if aScope != nil {
			aScope.End()
		}
		exposure.Abort(outCtx)
		return largebody.ExecutionResult{}, err
	}

	out, err := attemptOpenOwner{e}.openInitial(outCtx, preparedReq, plan)
	if err != nil {
		if aScope != nil {
			aScope.End()
		}
		exposure.Abort(outCtx)
		return largebody.ExecutionResult{}, err
	}

	if e.CompactionDetector != nil && accepted.CompactionComplete {
		preparedReq.compactionOpenMeta = e.observeCompactionOpenedWire(outCtx, preparedReq, out, accepted.CompactionFacts)
	}

	stream, err := streamAssembler{e}.assemble(outCtx, preparedReq, plan, out)
	if err != nil {
		e.appendPostOpenFallback(outCtx, billingState, aLeg.ALegID, out)
		if aScope != nil {
			aScope.End()
		}
		exposure.Abort(outCtx)
		return largebody.ExecutionResult{}, err
	}

	// 11. Build authoritative response and session facts (Requirements 14.6, 18.1, 18.2)
	respFacts := turnFacts.ToResponseFacts(effectiveModel)
	respFacts.RequestID = requestID
	respFacts.TraceID = traceID
	respFacts.SessionID = string(br.Record.SessionID)
	respFacts.ALegID = aLeg.ALegID
	respFacts.EffectiveModel = effectiveModel
	respFacts.Source = turnFacts.Source.SourceDigest
	respFacts.BodyBytes = turnFacts.Source.BodyBytes
	if respFacts.Operation == "" {
		respFacts.Operation = accepted.WireRequest.Operation
	}
	if respFacts.Delivery == "" {
		respFacts.Delivery = accepted.WireRequest.Delivery
	}

	sessionCarrier := prep.ResponseCarrier(br)

	// Wrap canonical stream with request authority release and terminal lifecycle cleanup upon completion/close
	//
	// The wrapper binds the assembled wire stream before this result is
	// published to the caller. At teardown the cleanup reads the scalar
	// logical outcome projection owned by that shared receive lifetime
	// (existing request-terminal code/state plus the latched receive-release
	// completion); the projection retains no attempt and inspects no raw
	// events.
	cleanupStream := &wireLifecycleEventStream{
		EventStream: stream,
	}
	// The teardown cause is advisory only (Close synthesizes cancellation
	// when no Recv failed); the audit verdict comes from the bound stream's
	// logical outcome evidence read here.
	cleanupStream.cleanup = func(_ error) {
		// Ordered teardown: every path that reaches this cleanup
		// (wireLifecycleEventStream.teardownSharedReceiveLifetime for
		// receive termination, Close, Cancel) stops the inner receive
		// publication lifetime before this runs; release the replay root
		// before authority release and the turn audit.
		if src != nil {
			_ = src.Close()
		}
		_ = e.releaseRequestAuthority(outCtx)

		// The turn audit reflects the accepted logical outcome, never a
		// guess from the Close/EOF cause: an accepted success stays
		// success without an extra Recv, while cancellation and every
		// failure keep the established surfaced_failure taxonomy. No
		// new audit kind appears.
		turnOutcomeKind := app.TurnOutcomeSurfacedFailure
		if wireStreamLogicalSuccess(cleanupStream) {
			turnOutcomeKind = app.TurnOutcomeSuccess
		}
		if aScope != nil {
			aScope.End()
		}
		if e.SecureSession != nil {
			auditCtx, auditCancel := cleanupContext(outCtx, defaultAuthorityCleanupTimeout)
			defer auditCancel()
			_ = e.SecureSession.FinishTurn(auditCtx, br.Record.SessionID, br.TurnID, app.TurnOutcome{
				Kind: turnOutcomeKind,
			})
		}
	}

	committed = true
	turnFinished = true
	return largebody.ExecutionResult{
		Stream:  cleanupStream,
		Facts:   respFacts,
		Session: sessionCarrier,
	}, nil
}

// resolveWireTurnFacts constructs bounded WireTurnFacts from accepted facts, source facts,
// and request context (e.g. SessionInput and ClientTurnShape).
// Production invariant: frontendpipe.replayCandidate enriches ctx via
// largebody.ContextWithWireProof with assessed proof Session/Turn and canonical
// Request/Trace IDs, overwriting any stale values. Missing handoff falls back to the
// assessed stamp identity digest (call_+token), never the generation ID, so silent
// gen-scoped IDs cannot hide a missing handoff. Direct test executors calling
// ExecuteLargeBody with a stamp-bound assessment continue to work via the same
// stamp-derived fallback; Session/Turn fallbacks remain for those callers.
func (e *Executor) resolveWireTurnFacts(
	ctx context.Context,
	accepted largebody.Assessment,
	src largebody.Source,
	srcDigest largebody.SourceDigest,
) largebody.WireTurnFacts {
	stampDigest := accepted.Stamp.IdentityDigest()
	reqID := ""
	traceID := ""
	if wid, ok := largebody.WireIdentityFromContext(ctx); ok {
		reqID = strings.TrimSpace(wid.RequestID)
		traceID = strings.TrimSpace(wid.TraceID)
		if traceID == "" {
			traceID = reqID
		}
	}
	if reqID == "" {
		if !stampDigest.IsZero() {
			reqID = stampDigest.CallID("")
		} else {
			reqID = accepted.Stamp.GenerationID()
		}
	}
	if traceID == "" {
		traceID = reqID
	}
	candModel := accepted.WireRequest.CandidateModel
	if candModel == "" {
		candModel = accepted.WireRequest.ClientModel
	}

	bodyBytes := accepted.Stamp.BodyBytes()
	if src != nil {
		bodyBytes = src.Size()
	}

	sessInput := largebody.SessionInput{NewSessionRequested: true}
	if si, ok := largebody.WireSessionInputFromContext(ctx); ok {
		sessInput = si
	}

	turnShape := largebody.ClientTurnShape{}
	if ts, ok := largebody.WireClientTurnShapeFromContext(ctx); ok {
		turnShape = ts
	}

	// The bounded classification evidence a certified frontend proof compiled for
	// this turn rides the same ctx handoff as Session/Turn (see
	// largebody.ContextWithWireProof). Absent evidence is legal and simply keeps
	// the session unknown (Requirements 5.2, 5.4).
	classificationEvidence, _ := largebody.WireClassificationEvidenceFromContext(ctx)

	return largebody.WireTurnFacts{
		Route: largebody.WireRouteFacts{
			ProfileID:      accepted.Stamp.ProfileID(),
			ClientModel:    accepted.WireRequest.ClientModel,
			CandidateModel: candModel,
			MaxAttempts:    largebody.DefaultWireMaxAttempts,
		},
		Protocol: largebody.WireProtocolFacts{
			Operation: accepted.WireRequest.Operation,
			Delivery:  accepted.WireRequest.Delivery,
		},
		MaxOutput: largebody.WireMaxOutputFacts{
			MaxOutputTokens: accepted.WireRequest.MaxOutputTokens,
		},
		Identity: largebody.WireIdentityFacts{
			RequestID:       reqID,
			TraceID:         traceID,
			CanonicalDigest: stampDigest,
			CheckpointID:    "customer-request:" + reqID,
		},
		Session: largebody.WireSessionFacts{
			Input:                  sessInput,
			TurnShape:              turnShape,
			ClassificationEvidence: classificationEvidence,
		},
		Source: largebody.WireSourceFacts{
			SourceDigest: srcDigest,
			BodyBytes:    bodyBytes,
			BodyMode:     accepted.Stamp.BodyMode(),
		},
		Rewrite: largebody.WireRewriteFacts{
			Semantics: accepted.Stamp.Rewrite(),
		},
		Economic: largebody.WireEconomicFacts{
			BillingCallID:     reqID,
			RequestCount:      1,
			MaxOutputQuantity: accepted.WireRequest.MaxOutputTokens,
		},
	}
}

// openFreshWireBody opens an independent offset-zero reader over src, applying
// candidate model splicing only if approved by the assessment rewrite (Requirements 9, 10.1, 10.2).
func openFreshWireBody(src largebody.Source, accepted largebody.Assessment, candidateModel string) (io.ReadCloser, int64, error) {
	if src == nil {
		return nil, 0, fmt.Errorf("largebody: nil source")
	}
	rewrite := accepted.Stamp.Rewrite()
	if rewrite.NeedsModelRewrite() {
		span := rewrite.Span()
		splice, err := largebody.SpliceModelToken(src, span, candidateModel)
		if err != nil {
			return nil, 0, fmt.Errorf("largebody: splice model token: %w", err)
		}
		return splice, splice.RewrittenLength(), nil
	}
	rc, err := src.Open()
	if err != nil {
		return nil, 0, fmt.Errorf("largebody: open source: %w", err)
	}
	return rc, src.Size(), nil
}

// wireAttemptPayload carries wire-mode payload facts through canonical candidate evaluation and attempt transactions.
type wireAttemptPayload struct {
	src                   largebody.Source
	accepted              largebody.Assessment
	turnFacts             largebody.WireTurnFacts
	requestID             string
	sessionID             string
	maxOutputTokens       *int
	weightedFirstConsumed bool
	selector              *routing.Selector
}

// wireBodyClosingStream ensures the fresh wire body reader is closed whenever the stream terminates.
type wireBodyClosingStream struct {
	aipapi.ManagedEventStream
	closer io.Closer
	cancel context.CancelFunc
	once   sync.Once
}

// wireBoundaryEventStream keeps the wire fast lane observable when no
// customer-side transform is occupied. Wire eligibility guarantees the
// provider event is delivered unchanged, so the two local output boundaries
// are still recorded as independent snapshots with independent identities.
type wireBoundaryEventStream struct {
	aipapi.ManagedEventStream
	boundary *coremetering.BoundaryAccumulator
}

func (s *wireBoundaryEventStream) Recv(ctx context.Context) (aipapi.Event, error) {
	ev, err := s.ManagedEventStream.Recv(ctx)
	if err == nil && s.boundary != nil {
		s.boundary.ObserveProviderEvent(ev)
		s.boundary.ObserveCustomerEvent(ev)
	}
	return ev, err
}

// Wire evidence delegation (Requirement 4.3): both local wire adapters embed
// aipapi.ManagedEventStream, whose promotion hides the provider-owned optional
// evidence capabilities from the runtime consumers that drain the attempt
// inner stream. Each adapter therefore forwards the five evidence methods to
// its wrapped stream without taking queue, checkpoint, or drain ownership:
// no storage, no terminalization, no Close/Cancel change. The helpers mirror
// the backend first-event adapter delegation pattern locally; core never
// imports a provider adapter for this.
func delegateWireBindEconomicEvidence(inner aipapi.ManagedEventStream, identity coremetering.ObservationIdentity) {
	if inner == nil {
		return
	}
	binder, ok := inner.(coremetering.ProviderEvidenceBinder)
	if !ok || binder == nil {
		return
	}
	binder.BindEconomicEvidence(identity)
}

// delegateWireDrainUsageEvidence forwards the independent legacy usage drain.
func delegateWireDrainUsageEvidence(inner aipapi.ManagedEventStream) []aipapi.Event {
	if inner == nil {
		return nil
	}
	legacySource, ok := inner.(aipapi.UsageEvidenceSource)
	if !ok || legacySource == nil {
		return nil
	}
	return legacySource.DrainUsageEvidence()
}

// delegateWireDrainEconomicObservations forwards the native observation drain.
func delegateWireDrainEconomicObservations(inner aipapi.ManagedEventStream) []metering.Observation {
	if inner == nil {
		return nil
	}
	nativeSource, ok := inner.(metering.ObservationSource)
	if !ok || nativeSource == nil {
		return nil
	}
	return nativeSource.DrainEconomicObservations()
}

// delegateWireDrainEconomicEvidenceRecords forwards rich records unchanged
// when the wrapped stream carries them; otherwise it lifts native
// observations into the neutral carrier, exactly matching the canonical
// native-only consumer path (rememberEconomicObservationOnce wraps the same
// carrier with empty coverage). Consumers prefer the rich branch, so a bare
// delegation would shadow native-only streams; the lift keeps each
// provider-owned batch draining once, and no coverage or reason is invented.
// Rich and native are never drained for the same batch here.
func delegateWireDrainEconomicEvidenceRecords(inner aipapi.ManagedEventStream) []execbackend.EconomicEvidence {
	if inner == nil {
		return nil
	}
	richSource, ok := inner.(execbackend.EconomicEvidenceSource)
	if ok && richSource != nil {
		return richSource.DrainEconomicEvidenceRecords()
	}
	nativeSource, ok := inner.(metering.ObservationSource)
	if !ok || nativeSource == nil {
		return nil
	}
	observations := nativeSource.DrainEconomicObservations()
	if len(observations) == 0 {
		return nil
	}
	records := make([]execbackend.EconomicEvidence, 0, len(observations))
	for _, observation := range observations {
		records = append(records, execbackend.EconomicEvidence{Observation: observation})
	}
	return records
}

// delegateWireAccountingEvidenceEnabled preserves the wrapped stream's
// explicit accounting state when it reports one; otherwise it recognizes
// genuine rich, native, or legacy capability. A capability-free stream stays
// false even though the adapters themselves expose drain methods.
func delegateWireAccountingEvidenceEnabled(inner aipapi.ManagedEventStream) bool {
	if inner == nil {
		return false
	}
	if state, ok := inner.(interface{ AccountingEvidenceEnabled() bool }); ok && state != nil {
		return state.AccountingEvidenceEnabled()
	}
	if legacySource, ok := inner.(aipapi.UsageEvidenceSource); ok && legacySource != nil {
		return true
	}
	if richSource, ok := inner.(execbackend.EconomicEvidenceSource); ok && richSource != nil {
		return true
	}
	nativeSource, ok := inner.(metering.ObservationSource)
	return ok && nativeSource != nil
}

func (s *wireBoundaryEventStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s == nil {
		return
	}
	delegateWireBindEconomicEvidence(s.ManagedEventStream, identity)
}

func (s *wireBoundaryEventStream) DrainUsageEvidence() []aipapi.Event {
	if s == nil {
		return nil
	}
	return delegateWireDrainUsageEvidence(s.ManagedEventStream)
}

func (s *wireBoundaryEventStream) DrainEconomicObservations() []metering.Observation {
	if s == nil {
		return nil
	}
	return delegateWireDrainEconomicObservations(s.ManagedEventStream)
}

func (s *wireBoundaryEventStream) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	if s == nil {
		return nil
	}
	return delegateWireDrainEconomicEvidenceRecords(s.ManagedEventStream)
}

func (s *wireBoundaryEventStream) AccountingEvidenceEnabled() bool {
	if s == nil {
		return false
	}
	return delegateWireAccountingEvidenceEnabled(s.ManagedEventStream)
}

func (s *wireBodyClosingStream) BindEconomicEvidence(identity coremetering.ObservationIdentity) {
	if s == nil {
		return
	}
	delegateWireBindEconomicEvidence(s.ManagedEventStream, identity)
}

func (s *wireBodyClosingStream) DrainUsageEvidence() []aipapi.Event {
	if s == nil {
		return nil
	}
	return delegateWireDrainUsageEvidence(s.ManagedEventStream)
}

func (s *wireBodyClosingStream) DrainEconomicObservations() []metering.Observation {
	if s == nil {
		return nil
	}
	return delegateWireDrainEconomicObservations(s.ManagedEventStream)
}

func (s *wireBodyClosingStream) DrainEconomicEvidenceRecords() []execbackend.EconomicEvidence {
	if s == nil {
		return nil
	}
	return delegateWireDrainEconomicEvidenceRecords(s.ManagedEventStream)
}

func (s *wireBodyClosingStream) AccountingEvidenceEnabled() bool {
	if s == nil {
		return false
	}
	return delegateWireAccountingEvidenceEnabled(s.ManagedEventStream)
}

var (
	_ coremetering.ProviderEvidenceBinder = (*wireBoundaryEventStream)(nil)
	_ aipapi.UsageEvidenceSource          = (*wireBoundaryEventStream)(nil)
	_ metering.ObservationSource          = (*wireBoundaryEventStream)(nil)
	_ execbackend.EconomicEvidenceSource  = (*wireBoundaryEventStream)(nil)
	_ coremetering.ProviderEvidenceBinder = (*wireBodyClosingStream)(nil)
	_ aipapi.UsageEvidenceSource          = (*wireBodyClosingStream)(nil)
	_ metering.ObservationSource          = (*wireBodyClosingStream)(nil)
	_ execbackend.EconomicEvidenceSource  = (*wireBodyClosingStream)(nil)
)

func (s *wireBodyClosingStream) Close() error {
	var closeErr error
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.closer != nil {
			closeErr = s.closer.Close()
		}
	})
	streamErr := s.ManagedEventStream.Close()
	if closeErr != nil {
		return closeErr
	}
	return streamErr
}

func (s *wireBodyClosingStream) Cancel(ctx context.Context, cause aipapi.CancelCause) aipapi.CancelResult {
	s.once.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.closer != nil {
			_ = s.closer.Close()
		}
	})
	return s.ManagedEventStream.Cancel(ctx, cause)
}

func (e *Executor) openWireAttemptTx(
	ctx context.Context,
	tx *attemptTx,
	be execbackend.Backend,
	beOK bool,
	c routing.AttemptCandidate,
	cPlan candidatePlan,
) error {
	if tx.budget != nil {
		if !tx.budget.tryAcquire() {
			return fmt.Errorf("executor: %w", aipapi.ErrMaxRouteAttempts)
		}
		tx.budgetAcquired = true
	}

	if !beOK {
		err := fmt.Errorf("executor: backend %q not configured", c.Primary.Backend)
		tx.recordFailure(ctx, aipapi.AttemptSwallowedFailure, err.Error(), err)
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindSwallowed, usageevidence.LegOutcomeNeverStarted, err, "")
		return nil
	}

	parentCtx := ctx
	if cPlan.parallel && cPlan.parentCtx != nil {
		parentCtx = cPlan.parentCtx
	}
	legCtx, legCancel := context.WithCancel(parentCtx)
	openedOK := false
	defer func() {
		if !openedOK {
			legCancel()
		}
	}()

	var cancelOpen context.CancelFunc = func() {}
	ttftDeadline := ttftContextDeadline{}
	openCtx := legCtx
	if tx.failures != nil && tx.failures.progress != nil && tx.failures.progress.ttft != nil {
		openCtx, cancelOpen, ttftDeadline = tx.failures.progress.ttft.scopedContext(legCtx, e.now(), c.Key, c.Primary.TTFTTimeout)
	}
	defer cancelOpen()

	var stopWatcher func() bool
	if ttftDeadline.scope != ttftTimeoutNone {
		stopWatcher = context.AfterFunc(openCtx, legCancel)
	}

	var stopArmWatcher func() bool
	if cPlan.parallel {
		stopArmWatcher = context.AfterFunc(ctx, legCancel)
	}

	baseOpenCtx := legCtx
	if tx.reqFacts.aScope != nil {
		permitCtx, permit, perr := tx.reqFacts.aScope.BeginBLegLaunch(legCtx, tx.bleg.BLegID)
		if perr != nil {
			if stopWatcher != nil {
				stopWatcher()
			}
			if stopArmWatcher != nil {
				stopArmWatcher()
			}
			legCancel()
			if errors.Is(perr, leglifecycle.ErrALegCanceled) {
				tx.recordFailure(ctx, aipapi.AttemptCancelled, "a-leg canceled before launch", perr)
				tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeNeverStarted, perr, "a-leg canceled before launch")
				return nil
			}
			return perr
		}
		tx.launchPermit = permit
		baseOpenCtx = permitCtx
	}

	wp := tx.reqFacts.wirePayload
	openStart := e.now()
	tx.openStartedAt = openStart

	holder := tx.reqFacts.metering
	if holder == nil {
		holder = meteringHolderFrom(ctx)
	}
	if holder != nil {
		_, ingErr := e.captureWireBackendIngress(baseOpenCtx, holder, WireBackendIngressArgs{
			RequestID:       wp.requestID,
			TraceID:         tx.reqFacts.traceID,
			AttemptID:       tx.bleg.BLegID,
			BLegID:          tx.bleg.BLegID,
			ALegID:          tx.reqFacts.aLegID,
			SessionID:       wp.sessionID,
			Scope:           tx.reqFacts.recvViews.Scope,
			BackendID:       c.Primary.Backend,
			Model:           c.Primary.Model,
			MaxOutputTokens: wp.maxOutputTokens,
			Now:             openStart,
			SourceDigest:    wp.turnFacts.Source.SourceDigest.Sum(),
		})
		if ingErr != nil {
			tx.abortLaunchPermit()
			tx.recordFailure(ctx, aipapi.AttemptSurfacedFailure, ingErr.Error(), ingErr)
			tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindAdmissionFailure, usageevidence.LegOutcomeFailed, ingErr, "")
			return fmt.Errorf("executor: backend ingress checkpoint: %w", ingErr)
		}
	}

	bodyReader, contentLength, openErr := openFreshWireBody(wp.src, wp.accepted, c.Primary.WireModel())
	if openErr != nil {
		tx.abortLaunchPermit()
		tx.recordFailure(ctx, aipapi.AttemptSwallowedFailure, openErr.Error(), openErr)
		tx.rollbackSimple(ctx, sdkterminal.CommandBackendOpenFailure, usagelimit.ReleaseKindSwallowed, usageevidence.LegOutcomeNeverStarted, openErr, "")
		return nil
	}
	if tx.boundary != nil {
		// Wire mode has no canonical Call. Retain only a bounded payload-byte
		// estimate until an optional wire adapter reports final text/media
		// properties through the context observer.
		tx.boundary.PreparePayloadBytes(contentLength)
	}

	op := wp.turnFacts.Protocol.Operation
	if op == "" {
		op = wp.accepted.WireRequest.Operation
	}
	del := wp.turnFacts.Protocol.Delivery
	if del == "" {
		del = wp.accepted.WireRequest.Delivery
	}

	// Prove exact-bytes replay for adapter-local credential rotation before
	// any output or effect. An invalid factory keeps the single-attempt lane:
	// no route-plan, failover, commitment, or B-leg change here.
	replay, _ := largebody.ProveWireReplay(wp.src, wp.accepted, c.Primary.WireModel())

	wireReq := largebody.WireOpenRequest{
		Candidate:     c,
		Body:          bodyReader,
		ContentLength: contentLength,
		WireRequest: largebody.WireRequestFacts{
			ProfileID:            wp.turnFacts.Route.ProfileID,
			Operation:            op,
			Delivery:             del,
			BodyMode:             wp.turnFacts.Source.BodyMode,
			Rewrite:              wp.turnFacts.Rewrite.Semantics,
			ClientModel:          wp.turnFacts.Route.ClientModel,
			CandidateModel:       c.Primary.WireModel(),
			MaxOutputTokens:      wp.turnFacts.MaxOutput.MaxOutputTokens,
			RequiredCapabilities: wp.accepted.WireRequest.RequiredCapabilities,
		},
		TraceID: tx.reqFacts.traceID,
		ALegID:  tx.reqFacts.aLegID,
		BLegID:  tx.bleg.BLegID,
		Replay:  replay,
	}

	openSpanCtx, openSpan := otel.Tracer(otelScopeExecutor).Start(
		baseOpenCtx, "aip.executor.backend_wire_open",
		trace.WithAttributes(
			attribute.String("aip.backend", c.Primary.Backend),
			attribute.Int("aip.b_leg_seq", int(tx.bleg.Seq)),
		),
	)
	defer openSpan.End()

	tx.openInvoked = true
	tx.backendAttempted = true
	if tx.boundary != nil {
		tx.boundary.MarkAttempted()
		openSpanCtx = coremetering.WithPreparedInputObserver(openSpanCtx, tx.boundary.ObservePreparedInput)
	}

	openSpanCtx = execbackend.WithAttemptMeta(openSpanCtx, execbackend.AttemptMeta{
		ID:  tx.bleg.BLegID,
		Seq: tx.bleg.Seq,
	})
	stream, err := safety.CallValue(safety.BoundaryBackend, "backend_open", func() (aipapi.ManagedEventStream, error) {
		return execbackend.EffectiveWireOpen(openSpanCtx, be, wireReq)
	})
	// openReturned is the raw acquisition fact: the Open call completed
	// without an error. It is kept apart from the classified error because a
	// call that returned may have been dispatched upstream, so the leg failed
	// rather than never started - whatever the error says.
	openReturned := err == nil
	var nilStreamErr error
	// A backend Open that succeeds without a usable stream opened nothing:
	// there is no output to publish, no provider evidence to bind and no
	// attempt to attribute to that backend. It is a failed open, not a
	// successful one, so the body opened for it is released with it and the
	// route fails over to the next candidate.
	if err == nil && nilManagedStream(stream) {
		// The unusable stream must not become cleanup ownership: the terminal
		// path would call its lifecycle and cancellation-telemetry methods on
		// a nil receiver.
		stream = nil
		nilStreamErr = nilStreamOpenFailure(c.Key)
		err = classifyNilStreamOpen(openSpanCtx, nilStreamErr)
	}
	if tx.boundary != nil {
		// The unusable-stream check above turned a stream-less open into a
		// failure, so a nil error means a usable stream was acquired.
		tx.boundary.MarkAccepted(err == nil)
	}
	// Capture the cancellation provenance before the local cleanup below: once
	// legCancel runs, the leg context can no longer tell who ended the
	// attempt. A done open context means the request, the A-leg or the core's
	// own budget ended it, not the adapter.
	openCtxErr := openSpanCtx.Err()
	openDur := time.Since(openStart).Seconds()
	if e.Metrics != nil {
		e.Metrics.OnBackendOpenDuration(c.Primary.Backend, openDur)
	}

	if err != nil {
		if stopWatcher != nil {
			stopWatcher()
		}
		if stopArmWatcher != nil {
			stopArmWatcher()
		}
		legCancel()
		_ = bodyReader.Close()
		tx.abortLaunchPermit()
		if pe, ok := errors.AsType[*safety.PanicError](err); ok {
			err = mapBackendPanic(pe, false, c.Key)
		}
		isTTFT := (ttftDeadline.scope != ttftTimeoutNone) &&
			(ttftDeadline.expired(openCtx, err) || (openCtx.Err() == context.DeadlineExceeded && (parentCtx == nil || parentCtx.Err() == nil)))
		tx.persistOpenTrace(openSpanCtx, c, aipapi.Call{}, openStart, true)
		return tx.failOpen(ctx, c, cPlan, openFailure{
			err:          err,
			ttftExpired:  isTTFT,
			ttftScope:    ttftDeadline.scope,
			openErr:      openCtxErr,
			wire:         true,
			postOpen:     openReturned,
			nilStreamErr: nilStreamErr,
		}, openSpan)
	}

	if stopWatcher != nil {
		stopWatcher()
	}
	if stopArmWatcher != nil {
		stopArmWatcher()
	}

	if tx.boundary != nil {
		stream = &wireBoundaryEventStream{ManagedEventStream: stream, boundary: tx.boundary}
	}
	tx.stream = &wireBodyClosingStream{
		ManagedEventStream: stream,
		closer:             bodyReader,
		cancel:             legCancel,
	}
	tx.accounting = newAttemptAccountingTracker(openStart)
	tx.recordAttemptLoggedFn = e.recordAttemptLogged

	// Trace then evidence binding: see commitSuccessfulOpen for the ordering
	// rule. tx.stream already owns the stream and body reader for cleanup.
	if stop, ferr := tx.commitSuccessfulOpen(ctx, successfulOpen{
		cand:      c,
		plan:      cPlan,
		traceCall: aipapi.Call{},
		traceCtx:  openSpanCtx,
		stream:    tx.stream,
		requestID: wp.requestID,
		openStart: openStart,
		openCtx:   legCtx,
		wire:      true,
		openSpan:  openSpan,
	}); stop {
		return ferr
	}

	if c.MarkedFirst && !cPlan.parallel {
		if err := e.Store.SetWeightedFirstConsumed(ctx, tx.reqFacts.aLegID, true); err != nil {
			return fmt.Errorf("executor: set weighted first consumed: %w", err)
		}
	}
	openedOK = true
	return nil
}
